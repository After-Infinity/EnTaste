package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	db     *sql.DB
	billMu sync.Mutex

	ErrActive = errors.New("finish the current bill first")
	ErrEmpty  = errors.New("tick at least one item")
)

type Product struct {
	ID       int64
	Name     string
	PerKg    int64
}

type Discount struct {
	ID    int64
	Name  string
	Kind  string // percent | flat
	Value int64  // percent: hundredths of a percent (10% = 1000); flat: paise
}

func (d Discount) Label() string {
	if d.Kind == "percent" {
		return fmt.Sprintf("%s%% off", strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", float64(d.Value)/100), "0"), "."))
	}
	return money(d.Value) + " off"
}

type Bill struct {
	ID           int64
	Code         string
	Subtotal     int64
	DiscountName string
	Discount     int64
	Total        int64
	Status       string // pending | sent | paid | cancelled
	CustJID      string
	CustName     string
	CustPhone    string
	CreatedAt    int64
	PaidAt       int64
}

type Line struct {
	Name  string
	Grams int
	Qty   int
	Unit  int64
	Total int64
}

func openDB() error {
	var err error
	db, err = sql.Open(sqlDriver, "file:bills.db"+pragmas)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1) // simple and safe for SQLite
	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS products (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		per_kg INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS discounts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		kind TEXT NOT NULL,
		value INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS bills (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		code TEXT UNIQUE NOT NULL,
		subtotal INTEGER NOT NULL,
		discount_name TEXT NOT NULL DEFAULT '',
		discount INTEGER NOT NULL DEFAULT 0,
		total INTEGER NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		cust_jid TEXT NOT NULL DEFAULT '',
		cust_name TEXT NOT NULL DEFAULT '',
		cust_phone TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		paid_at INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS bill_items (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		bill_id INTEGER NOT NULL REFERENCES bills(id),
		name TEXT NOT NULL,
		grams INTEGER NOT NULL,
		qty INTEGER NOT NULL,
		unit INTEGER NOT NULL,
		line INTEGER NOT NULL
	);`)
	if err != nil {
		return err
	}
	seedProducts()
	return nil
}

// First run only: import products.json if the table is empty.
func seedProducts() {
	var n int
	db.QueryRow("SELECT COUNT(*) FROM products").Scan(&n)
	if n > 0 {
		return
	}
	raw, err := os.ReadFile("products.json")
	if err != nil {
		return
	}
	var list []struct {
		Name  string  `json:"name"`
		Price float64 `json:"price_per_kg"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return
	}
	for _, p := range list {
		db.Exec("INSERT INTO products (name, per_kg) VALUES (?,?)", p.Name, int64(p.Price*100+0.5))
	}
}

// ---------- products ----------

func listProducts() []Product {
	rows, err := db.Query("SELECT id, name, per_kg FROM products ORDER BY name COLLATE NOCASE")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Product
	for rows.Next() {
		var p Product
		rows.Scan(&p.ID, &p.Name, &p.PerKg)
		out = append(out, p)
	}
	return out
}

func saveProduct(id int64, name string, perKg int64) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 60 {
		return errors.New("item name is required")
	}
	if id == 0 {
		_, err := db.Exec("INSERT INTO products (name, per_kg) VALUES (?,?)", name, perKg)
		return err
	}
	_, err := db.Exec("UPDATE products SET name=?, per_kg=? WHERE id=?", name, perKg, id)
	return err
}

func deleteProduct(id int64) { db.Exec("DELETE FROM products WHERE id=?", id) }

// ---------- discounts ----------

func listDiscounts() []Discount {
	rows, err := db.Query("SELECT id, name, kind, value FROM discounts ORDER BY name COLLATE NOCASE")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Discount
	for rows.Next() {
		var d Discount
		rows.Scan(&d.ID, &d.Name, &d.Kind, &d.Value)
		out = append(out, d)
	}
	return out
}

func getDiscount(id int64) (Discount, bool) {
	var d Discount
	err := db.QueryRow("SELECT id, name, kind, value FROM discounts WHERE id=?", id).
		Scan(&d.ID, &d.Name, &d.Kind, &d.Value)
	return d, err == nil
}

func saveDiscount(id int64, name, kind string, value int64) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 40 {
		return errors.New("discount name is required")
	}
	if kind != "percent" && kind != "flat" {
		return errors.New("bad discount type")
	}
	if kind == "percent" && (value <= 0 || value > 10000) {
		return errors.New("percent must be between 0 and 100")
	}
	if value <= 0 {
		return errors.New("value must be more than 0")
	}
	if id == 0 {
		_, err := db.Exec("INSERT INTO discounts (name, kind, value) VALUES (?,?,?)", name, kind, value)
		return err
	}
	_, err := db.Exec("UPDATE discounts SET name=?, kind=?, value=? WHERE id=?", name, kind, value, id)
	return err
}

func deleteDiscount(id int64) { db.Exec("DELETE FROM discounts WHERE id=?", id) }

// ---------- bills ----------

type Pick struct {
	ProductID int64
	Grams     int
	Qty       int
}

func activeBill() *Bill {
	return scanBill(db.QueryRow(billSelect + " WHERE status IN ('pending','sent') ORDER BY id DESC LIMIT 1"))
}

func latestBill() *Bill {
	return scanBill(db.QueryRow(billSelect + " ORDER BY id DESC LIMIT 1"))
}

func billByCode(code string) *Bill {
	return scanBill(db.QueryRow(billSelect+" WHERE code=?", code))
}

const billSelect = `SELECT id, code, subtotal, discount_name, discount, total, status,
	cust_jid, cust_name, cust_phone, created_at, paid_at FROM bills`

type scanner interface{ Scan(...any) error }

func scanBill(r scanner) *Bill {
	var b Bill
	err := r.Scan(&b.ID, &b.Code, &b.Subtotal, &b.DiscountName, &b.Discount, &b.Total,
		&b.Status, &b.CustJID, &b.CustName, &b.CustPhone, &b.CreatedAt, &b.PaidAt)
	if err != nil {
		return nil
	}
	return &b
}

func billLines(id int64) []Line {
	rows, err := db.Query("SELECT name, grams, qty, unit, line FROM bill_items WHERE bill_id=? ORDER BY id", id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Line
	for rows.Next() {
		var l Line
		rows.Scan(&l.Name, &l.Grams, &l.Qty, &l.Unit, &l.Total)
		out = append(out, l)
	}
	return out
}

func listBills(limit int) []Bill {
	rows, err := db.Query(billSelect+" ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Bill
	for rows.Next() {
		if b := scanBill(rows); b != nil {
			out = append(out, *b)
		}
	}
	return out
}

func discountAmount(d Discount, subtotal int64) int64 {
	var amt int64
	if d.Kind == "percent" {
		amt = (subtotal*d.Value + 5000) / 10000
	} else {
		amt = d.Value
	}
	if amt > subtotal {
		amt = subtotal
	}
	return amt
}

// createBill refuses to run while another bill is still open.
// Prices always come from the database, never from the browser.
func createBill(picks []Pick, discountID int64) (*Bill, error) {
	billMu.Lock()
	defer billMu.Unlock()

	if activeBill() != nil {
		return nil, ErrActive
	}
	prices := map[int64]Product{}
	for _, p := range listProducts() {
		prices[p.ID] = p
	}
	var lines []Line
	var subtotal int64
	for _, pk := range picks {
		p, ok := prices[pk.ProductID]
		valid := pk.Grams == 1000 || pk.Grams == 500 || pk.Grams == 250
		if !ok || !valid || pk.Qty < 1 || pk.Qty > 99 {
			continue
		}
		unit := packPrice(p.PerKg, pk.Grams)
		l := Line{Name: p.Name, Grams: pk.Grams, Qty: pk.Qty, Unit: unit, Total: unit * int64(pk.Qty)}
		lines = append(lines, l)
		subtotal += l.Total
	}
	if len(lines) == 0 {
		return nil, ErrEmpty
	}
	var dName string
	var dAmt int64
	if discountID != 0 {
		if d, ok := getDiscount(discountID); ok {
			dName = d.Name + " (" + d.Label() + ")"
			dAmt = discountAmount(d, subtotal)
		}
	}
	total := subtotal - dAmt

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	code := ""
	for {
		code = fmt.Sprintf("%06d", 100000+rand.IntN(900000))
		var x int
		if tx.QueryRow("SELECT 1 FROM bills WHERE code=?", code).Scan(&x) == sql.ErrNoRows {
			break
		}
	}
	res, err := tx.Exec(`INSERT INTO bills (code, subtotal, discount_name, discount, total, created_at)
		VALUES (?,?,?,?,?,?)`, code, subtotal, dName, dAmt, total, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	for _, l := range lines {
		if _, err := tx.Exec(`INSERT INTO bill_items (bill_id, name, grams, qty, unit, line)
			VALUES (?,?,?,?,?,?)`, id, l.Name, l.Grams, l.Qty, l.Unit, l.Total); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return billByCode(code), nil
}

func markSent(id int64, jid, name, phone string) {
	db.Exec("UPDATE bills SET status='sent', cust_jid=?, cust_name=?, cust_phone=? WHERE id=? AND status IN ('pending','sent')",
		jid, name, phone, id)
}

func markPaid(id int64) {
	db.Exec("UPDATE bills SET status='paid', paid_at=? WHERE id=? AND status IN ('pending','sent')", time.Now().Unix(), id)
}

func cancelBill(id int64) {
	db.Exec("UPDATE bills SET status='cancelled' WHERE id=? AND status IN ('pending','sent')", id)
}

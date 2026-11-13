package main

import (
	"context"
	"fmt"
	"log"
	"math/rand/v2"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

type WA struct {
	mu        sync.Mutex
	client    *whatsmeow.Client
	qr        string
	lastReply map[string]time.Time
}

var wa = &WA{lastReply: map[string]time.Time{}}

var codeRe = regexp.MustCompile(`\b(\d{6})\b`)

const cooldown = 15 * time.Second

func startWhatsApp() {
	ctx := context.Background()
	container, err := sqlstore.New(ctx, sqlDriver, "file:whatsapp.db"+pragmas, waLog.Stdout("DB", "WARN", true))
	if err != nil {
		log.Println("WhatsApp store error:", err)
		return
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		log.Println("WhatsApp device error:", err)
		return
	}
	wa.client = whatsmeow.NewClient(device, waLog.Stdout("WA", "WARN", true))
	wa.client.AddEventHandler(wa.handleEvent)
	wa.connectLoop()
}

func (w *WA) connectLoop() {
	ctx := context.Background()
	if w.client.Store.ID != nil {
		for {
			err := w.client.Connect()
			if err == nil {
				return // whatsmeow reconnects by itself after this
			}
			log.Println("WhatsApp connect error (retrying in 10s):", err)
			time.Sleep(10 * time.Second)
		}
	}
	log.Println("WhatsApp not linked yet: open /link in the browser and scan the QR")
	for w.client.Store.ID == nil {
		qrChan, err := w.client.GetQRChannel(ctx)
		if err != nil {
			log.Println("QR error:", err)
			time.Sleep(5 * time.Second)
			continue
		}
		if err := w.client.Connect(); err != nil {
			log.Println("WhatsApp connect error (retrying in 10s):", err)
			time.Sleep(10 * time.Second)
			continue
		}
		for item := range qrChan {
			if item.Event == whatsmeow.QRChannelEventCode {
				w.setQR(item.Code)
			} else {
				w.setQR("")
				log.Println("WhatsApp pairing:", item.Event)
			}
		}
		if w.client.Store.ID == nil {
			w.client.Disconnect()
			time.Sleep(time.Second)
		}
	}
	w.setQR("")
}

func (w *WA) setQR(s string) { w.mu.Lock(); w.qr = s; w.mu.Unlock() }
func (w *WA) getQR() string  { w.mu.Lock(); defer w.mu.Unlock(); return w.qr }

// status for the /link page
func (w *WA) status() (linked, online bool, number string) {
	if w.client == nil {
		return false, false, ""
	}
	if w.client.Store.ID != nil {
		return true, w.client.IsConnected(), w.client.Store.ID.User
	}
	return false, false, ""
}

func (w *WA) handleEvent(evt any) {
	switch v := evt.(type) {
	case *events.Connected:
		log.Println("WhatsApp connected")
	case *events.LoggedOut:
		log.Println("WhatsApp logged out: delete whatsapp.db and restart to link again")
	case *events.Message:
		go w.onMessage(v)
	}
}

func messageText(m *waE2E.Message) string {
	if m == nil {
		return ""
	}
	if t := m.GetConversation(); t != "" {
		return t
	}
	if e := m.GetExtendedTextMessage(); e != nil {
		return e.GetText()
	}
	return ""
}

func (w *WA) isSelfChat(chat types.JID) bool {
	s := w.client.Store
	if s.ID != nil && chat.User == s.ID.User {
		return true
	}
	return !s.LID.IsEmpty() && chat.User == s.LID.User
}

func phoneOf(info types.MessageInfo) string {
	for _, j := range []types.JID{info.Sender, info.SenderAlt, info.Chat} {
		if j.Server == types.DefaultUserServer && j.User != "" {
			return j.User
		}
	}
	return ""
}

func (w *WA) onMessage(v *events.Message) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("message handler crashed:", r)
		}
	}()
	info := v.Info
	text := strings.TrimSpace(messageText(v.Message))
	if text == "" || info.IsGroup {
		return
	}
	if info.Chat.Server != types.DefaultUserServer && info.Chat.Server != types.HiddenUserServer {
		return // status updates, newsletters, broadcasts
	}
	// our own replies start with these; never react to them
	for _, p := range []string{"🧾", "💳", "❌"} {
		if strings.HasPrefix(text, p) {
			return
		}
	}
	if time.Since(info.Timestamp) > 5*time.Minute {
		log.Printf("ignored old message from %s", info.Chat.User)
		return
	}
	if info.IsFromMe && !(cfg().AllowSelfTest && w.isSelfChat(info.Chat)) {
		return
	}
	log.Printf("message from %s (%q): %q", info.Chat.User, info.PushName, text)

	m := codeRe.FindStringSubmatch(text)
	if m == nil {
		log.Println("  -> no 6-digit code in message, ignoring")
		return
	}
	code := m[1]
	chat := info.Chat
	key := chat.String()

	w.mu.Lock()
	last := w.lastReply[key]
	if time.Since(last) < cooldown {
		w.mu.Unlock()
		log.Println("  -> cooldown, ignoring")
		return
	}
	w.lastReply[key] = time.Now()
	w.mu.Unlock()

	bill := billByCode(code)
	if bill == nil || bill.Status == "paid" || bill.Status == "cancelled" {
		log.Printf("  -> code %s is not an open bill", code)
		if strings.Contains(strings.ToLower(text), "bill") {
			w.say(chat, "❌ That code is not valid any more. Please ask the shop for a new QR.")
		}
		return
	}
	if bill.Status == "sent" && bill.CustJID != key {
		log.Printf("  -> bill %s already belongs to another customer", code)
		return
	}

	name := strings.TrimSpace(info.PushName)
	phone := phoneOf(info)
	log.Printf("  -> bill %s matched to customer %q %s", code, name, phone)

	if err := w.say(chat, billText(bill, billLines(bill.ID))); err != nil {
		log.Println("  -> send failed:", err)
		return
	}
	markSent(bill.ID, key, name, phone)
	pay := upiLink(bill)
	if err := w.say(chat, "💳 Pay "+money(bill.Total)+" with GPay or any UPI app:\n"+pay); err != nil {
		log.Println("  -> pay link send failed:", err)
	}
}

func billText(b *Bill, lines []Line) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "🧾 Hi from %s here is your bill\nBill %s\n\n", cfg().ShopName, b.Code)
	for _, l := range lines {
		item := packLabel(l.Grams)
		if l.Qty > 1 {
			item += fmt.Sprintf(" × %d", l.Qty)
		}
		fmt.Fprintf(&sb, "• %s — %s — %s\n", l.Name, item, money(l.Total))
	}
	if b.Discount > 0 {
		fmt.Fprintf(&sb, "\nSubtotal: %s\nDiscount %s: -%s", money(b.Subtotal), b.DiscountName, money(b.Discount))
	}
	fmt.Fprintf(&sb, "\n*Total: %s*", money(b.Total))
	return sb.String()
}

// say shows "typing…", waits a human-like moment, then sends.
func (w *WA) say(chat types.JID, text string) error {
	ctx := context.Background()
	_ = w.client.SendChatPresence(ctx, chat, types.ChatPresenceComposing, types.ChatPresenceMediaText)
	time.Sleep(time.Duration(1500+rand.IntN(2000)) * time.Millisecond)
	_, err := w.client.SendMessage(ctx, chat, &waE2E.Message{Conversation: proto.String(text)})
	_ = w.client.SendChatPresence(ctx, chat, types.ChatPresencePaused, types.ChatPresenceMediaText)
	return err
}

// upiLink: upi://pay?pa=name%40bank&pn=My+Shop&am=250&cu=INR&tn=Shopping+Bill
func upiLink(b *Bill) string {
	am := strconv.FormatInt(b.Total/100, 10)
	if b.Total%100 != 0 {
		am = fmt.Sprintf("%d.%02d", b.Total/100, b.Total%100)
	}
	c := cfg()
	return "upi://pay?pa=" + url.QueryEscape(c.UPIID) + "&pn=" + url.QueryEscape(c.ShopName) +
		"&am=" + am + "&cu=INR&tn=" + url.QueryEscape(payNote)
}

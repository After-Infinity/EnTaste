package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

type Config struct {
	ShopName      string `json:"shop_name"`
	UPIID         string `json:"upi_id"`
	WhatsAppNum   string `json:"whatsapp_number"`
	PublicBaseURL string `json:"public_base_url"`
	PanelPassword string `json:"panel_password"`
	Listen        string `json:"listen"`
	AllowSelfTest bool   `json:"allow_self_test"`
}

// Re-read on every use, so edits to config.json apply without a restart.
func cfg() Config {
	c := Config{ShopName: "Shop", Listen: ":8000"}
	raw, err := os.ReadFile("config.json")
	if err == nil {
		json.Unmarshal(raw, &c)
	}
	return c
}

func main() {
	c := cfg()
	if c.PanelPassword == "" {
		log.Fatal("set panel_password in config.json")
	}
	if c.PanelPassword == "change-this-password" {
		log.Println("WARNING: change panel_password in config.json")
	}
	if err := openDB(); err != nil {
		log.Fatal("database: ", err)
	}
	go startWhatsApp()

	srv := &http.Server{
		Addr:              c.Listen,
		Handler:           routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Println("────────────────────────────────────────")
	fmt.Println(" Shop is running")
	fmt.Println(" Counter:   http://localhost" + c.Listen)
	fmt.Println(" QR screen: http://localhost" + c.Listen + "/qr")
	fmt.Println(" Link WhatsApp (first time): http://localhost" + c.Listen + "/link")
	fmt.Println(" Login: keeper / (panel_password)")
	fmt.Println("────────────────────────────────────────")
	log.Fatal(srv.ListenAndServe())
}

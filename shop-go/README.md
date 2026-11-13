# Shop billing (Go)

Run (needs Go 1.26+): double-click `run.bat` (Windows) or `./run.sh`.
First run downloads packages, so it needs internet.

1. Edit `config.json`: upi_id, whatsapp_number (with country code), public_base_url, panel_password.
2. Open http://localhost:8000 (user `keeper`, your panel_password).
3. First time only: open the "WhatsApp" tab and scan the QR (WhatsApp > Linked devices).
4. Items tab: add your items (price per 1 kg). Discounts tab: add your offers.

Pages: Counter (make bill) / QR screen (/qr, always the same URL) / Items / Discounts / History / WhatsApp.

public_base_url must be a public https address that reaches this app, e.g.
`cloudflared tunnel --url http://localhost:8000`, then paste the https URL into config.json.

To unlink WhatsApp: stop the app, delete whatsapp.db, start again.
Data: bills.db (bills and items), whatsapp.db (WhatsApp login). Do not share whatsapp.db.

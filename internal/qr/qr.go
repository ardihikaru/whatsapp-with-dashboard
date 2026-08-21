// Package qr renders whatsmeow pairing payloads as PNG QR images.
package qr

import qrcode "github.com/skip2/go-qrcode"

// PNG encodes the raw pairing payload. Medium error correction matches what
// WhatsApp Web uses; 512px scans reliably from a laptop screen.
func PNG(payload string, size int) ([]byte, error) {
	if size <= 0 {
		size = 512
	}
	return qrcode.Encode(payload, qrcode.Medium, size)
}

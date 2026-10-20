package sip

import (
	"crypto/rand"
	"encoding/base64"
)

// NewSRTPMasterKey genera el valor inline: de un a=crypto para la suite
// AES_CM_128_HMAC_SHA1_80: 16 bytes de master key + 14 de master salt,
// en base64 estándar. Cada llamada usa una clave nueva.
func NewSRTPMasterKey() (string, error) {
	raw := make([]byte, 30)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

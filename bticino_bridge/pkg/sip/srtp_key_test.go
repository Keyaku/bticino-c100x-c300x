package sip

import (
	"encoding/base64"
	"testing"
)

func TestNewSRTPMasterKeyLength(t *testing.T) {
	k, err := NewSRTPMasterKey()
	if err != nil {
		t.Fatalf("NewSRTPMasterKey: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(k)
	if err != nil {
		t.Fatalf("no es base64 válido: %v", err)
	}
	if len(raw) != 30 {
		t.Fatalf("esperaba 30 bytes (16 key + 14 salt), got %d", len(raw))
	}
}

func TestNewSRTPMasterKeyIsRandom(t *testing.T) {
	a, _ := NewSRTPMasterKey()
	b, _ := NewSRTPMasterKey()
	if a == b {
		t.Fatal("dos claves consecutivas no deberían coincidir")
	}
}

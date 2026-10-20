package sip

import (
	"strings"
	"testing"
)

func TestBuildForwardArgs(t *testing.T) {
	ans := AudioAnswer{Port: 5000, PayloadType: 110, CryptoSuite: "AES_CM_128_HMAC_SHA1_80", CryptoKey: "QUJDRA=="}
	args := buildForwardArgs("/tmp/in.sdp", "127.0.0.1:10000", ans)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-f rtp") {
		t.Fatalf("salida no es RTP: %s", joined)
	}
	if !strings.Contains(joined, "rtp://127.0.0.1:10000") {
		t.Fatalf("destino RTP incorrecto: %s", joined)
	}
	if strings.Contains(joined, "-f wav") || strings.Contains(joined, ".wav") {
		t.Fatalf("no debe escribir WAV: %s", joined)
	}
	if !strings.Contains(joined, "srtp") {
		t.Fatalf("whitelist debe permitir srtp (descifrado entrante): %s", joined)
	}
}

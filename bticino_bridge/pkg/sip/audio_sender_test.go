package sip

import (
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestAudioSenderBuildArgs(t *testing.T) {
	s := NewAudioSender("/usr/local/bin/ffmpeg", logrus.New())
	ans := AudioAnswer{Port: 7078, PayloadType: 98, CryptoSuite: "AES_CM_128_HMAC_SHA1_80", CryptoKey: "KEY123"}
	args := s.buildArgs("/tmp/hola.wav", "127.0.0.1", ans)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-i /tmp/hola.wav",
		"-c:a libspeex",
		"-ar 8000",
		"-payload_type 98",
		"srtp://127.0.0.1:7078",
		"srtp_out_suite=AES_CM_128_HMAC_SHA1_80",
		"srtp_out_params=KEY123",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args no contienen %q. args:\n%s", want, joined)
		}
	}
}

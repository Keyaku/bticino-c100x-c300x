package sip

import (
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestBuildInboundSDP(t *testing.T) {
	ans := AudioAnswer{Port: 7078, PayloadType: 98, CryptoSuite: "AES_CM_128_HMAC_SHA1_80", CryptoKey: "KEY123"}
	sdp := BuildInboundSDP("127.0.0.1", 40100, ans)
	for _, want := range []string{
		"c=IN IP4 127.0.0.1",
		"m=audio 40100 RTP/SAVP 98", // puerto LOCAL, no el del portero
		"a=rtpmap:98 speex/8000",
		"a=crypto:2 AES_CM_128_HMAC_SHA1_80 inline:KEY123",
	} {
		if !strings.Contains(sdp, want) {
			t.Fatalf("SDP inbound sin %q:\n%s", want, sdp)
		}
	}
}

func TestAudioReceiverBuildArgs(t *testing.T) {
	r := NewAudioReceiver("/usr/local/bin/ffmpeg", logrus.New())
	args := r.buildArgs("/tmp/in.sdp", "/tmp/out.wav")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-protocol_whitelist", "file,crypto,rtp,udp,srtp",
		"-i /tmp/in.sdp",
		"-c:a pcm_s16le",
		"-ar 8000",
		"-ac 1",
		"-f wav",
		"/tmp/out.wav",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args sin %q:\n%s", want, joined)
		}
	}
}

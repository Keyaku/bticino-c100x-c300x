package sip

import (
	"strings"
	"testing"
)

func TestBuildAudioOfferSDP(t *testing.T) {
	sdp := BuildAudioOfferSDP("127.0.0.1", 40100, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	for _, want := range []string{
		"m=audio 40100 RTP/SAVP 98",
		"a=rtpmap:98 speex/8000",
		"a=crypto:2 AES_CM_128_HMAC_SHA1_80 inline:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"a=sendrecv",
		"c=IN IP4 127.0.0.1",
	} {
		if !strings.Contains(sdp, want) {
			t.Fatalf("SDP no contiene %q. SDP:\n%s", want, sdp)
		}
	}
	// El offer de audio NO debe negociar suites GCM (ffmpeg no las hace).
	if strings.Contains(sdp, "GCM") {
		t.Fatal("el offer no debe ofrecer suites GCM")
	}
}

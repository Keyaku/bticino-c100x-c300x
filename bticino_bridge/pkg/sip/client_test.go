package sip

import (
	"io"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"

	"bticino_bridge/pkg/events"
)

// TestBuildInviteRequestWithSDP verifica que el INVITE resultante incorpora
// el SDP recibido como parámetro (y no el buildSDP() dummy), y que
// buildInviteRequest (self-INVITE de vídeo) sigue usando el dummy intacto.
func TestBuildInviteRequestWithSDP(t *testing.T) {
	cfg := &SIPConfig{
		ServerAddr: "127.0.0.1:5060",
		Domain:     "example.com",
		Username:   "webrtc",
		LocalIP:    "127.0.0.1",
		LocalPort:  5090,
		DevAddr:    "20",
		Transport:  "udp",
	}
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	c := NewBTicinoSIPClient(cfg, events.NewEventBus(1, logger), logger)
	c.webrtcID = &sipIdentity{username: cfg.Username, fromTag: "fromtag123", cseq: 1}
	c.callID = "call123@127.0.0.1"

	customSDP := "v=0\r\no=webrtc 1 1 IN IP4 127.0.0.1\r\ns=talkback\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 40100 RTP/SAVP 98\r\na=rtpmap:98 speex/8000\r\na=crypto:2 AES_CM_128_HMAC_SHA1_80 inline:SOMEKEY\r\n"

	invite := c.buildInviteRequestWithSDP("c300x", customSDP)

	if !strings.Contains(invite, customSDP) {
		t.Fatalf("el INVITE no contiene el SDP inyectado:\n%s", invite)
	}
	if !strings.HasPrefix(invite, "INVITE sip:c300x@example.com SIP/2.0") {
		t.Fatalf("línea de petición inesperada:\n%s", invite)
	}

	// El self-INVITE de vídeo (buildInviteRequest) debe seguir usando el SDP
	// dummy de buildSDP(), no el customSDP anterior.
	c.callID = "call456@127.0.0.1"
	videoInvite := c.buildInviteRequest("c300x")
	if strings.Contains(videoInvite, "SOMEKEY") {
		t.Fatal("buildInviteRequest (self-INVITE de vídeo) no debe verse afectado por buildInviteRequestWithSDP")
	}
	if !strings.Contains(videoInvite, "dummykey") {
		t.Fatalf("buildInviteRequest debe seguir usando el SDP dummy:\n%s", videoInvite)
	}
}

func TestExtractSIPUser(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`"Doorbell" <sip:c300x@YOUR_GATEWAY_ID.bs.iotleg.com>;tag=abc123`, "c300x"},
		{`<sip:webrtc@example.com>`, "webrtc"},
		{`sip:alice@10.0.0.1;transport=tcp`, "alice"},
		{`sip:noatsign`, "noatsign"},
		{`Bob <sip:bob@host>`, "bob"},
		{`plain text no uri`, "plain text no uri"},
	}
	for _, c := range cases {
		if got := extractSIPUser(c.in); got != c.want {
			t.Errorf("extractSIPUser(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

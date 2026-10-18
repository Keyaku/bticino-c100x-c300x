package sip

import (
	"strconv"
	"strings"
	"testing"
)

func sampleDialog() AnswererDialog {
	return AnswererDialog{
		CallID:      "abc123",
		FromHdr:     "<sip:c300x@YOUR_GATEWAY_ID.bs.iotleg.com>;tag=aa",
		ToHdr:       "<sip:alluser@YOUR_GATEWAY_ID.bs.iotleg.com>",
		CSeq:        "20 INVITE",
		Via:         []string{"SIP/2.0/TCP 127.0.0.1:5060;branch=z9hG4bKaaa", "SIP/2.0/TCP 127.0.0.1:41440;branch=z9hG4bKbbb"},
		RecordRoute: []string{"<sip:127.0.0.1:5060;transport=tcp;lr>"},
		ToTag:       "bridgetag01",
	}
}

func TestBuildAnswererRegister(t *testing.T) {
	reg := BuildAnswererRegister("bridge", "YOUR_GATEWAY_ID.bs.iotleg.com", "127.0.0.1", 41000, 3600, "z9hG4bKr1", "callreg1", "ftag1", 1)
	for _, want := range []string{
		"REGISTER sip:YOUR_GATEWAY_ID.bs.iotleg.com SIP/2.0\r\n",
		"Via: SIP/2.0/TCP 127.0.0.1:41000;alias;branch=z9hG4bKr1;rport\r\n",
		"Contact: <sip:bridge@127.0.0.1:41000;transport=tcp>",
		"Expires: 3600\r\n",
		"CSeq: 1 REGISTER\r\n",
		"Content-Length: 0\r\n",
	} {
		if !strings.Contains(reg, want) {
			t.Errorf("REGISTER no contiene %q\n---\n%s", want, reg)
		}
	}
}

func TestBuildAnswererResponse_180SinCuerpo(t *testing.T) {
	d := sampleDialog()
	r := BuildAnswererResponse("180", "Ringing", d, "", "")
	for _, want := range []string{
		"SIP/2.0 180 Ringing\r\n",
		"Via: SIP/2.0/TCP 127.0.0.1:5060;branch=z9hG4bKaaa\r\n",
		"Via: SIP/2.0/TCP 127.0.0.1:41440;branch=z9hG4bKbbb\r\n",
		"Record-Route: <sip:127.0.0.1:5060;transport=tcp;lr>\r\n",
		"To: <sip:alluser@YOUR_GATEWAY_ID.bs.iotleg.com>;tag=bridgetag01\r\n",
		"CSeq: 20 INVITE\r\n",
		"Content-Length: 0\r\n",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("180 no contiene %q\n---\n%s", want, r)
		}
	}
	if strings.Contains(r, "Content-Type: application/sdp") {
		t.Error("180 no debe llevar SDP")
	}
}

func TestBuildAnswererResponse_200ConSDP(t *testing.T) {
	d := sampleDialog()
	sdp := BuildAnswerSDP("127.0.0.1", 10100, 10102, "KEY==")
	contact := "<sip:bridge@127.0.0.1:41000;transport=tcp>"
	r := BuildAnswererResponse("200", "OK", d, sdp, contact)
	for _, want := range []string{
		"SIP/2.0 200 OK\r\n",
		"To: <sip:alluser@YOUR_GATEWAY_ID.bs.iotleg.com>;tag=bridgetag01\r\n",
		"Contact: <sip:bridge@127.0.0.1", // Contact propio presente
		"Content-Type: application/sdp\r\n",
		"m=audio 10100 RTP/SAVP 96",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("200 no contiene %q\n---\n%s", want, r)
		}
	}
	// Content-Length debe casar con el cuerpo.
	if !strings.Contains(r, "Content-Length: "+strconv.Itoa(len(sdp))+"\r\n") {
		t.Errorf("Content-Length no casa con len(sdp)=%d\n%s", len(sdp), r)
	}
}

func TestBuildAnswererResponse_487SinContactNiSDP(t *testing.T) {
	d := sampleDialog()
	r := BuildAnswererResponse("487", "Request Terminated", d, "", "")
	if strings.Contains(r, "Contact:") {
		t.Errorf("487 no debe llevar Contact\n---\n%s", r)
	}
	if strings.Contains(r, "Content-Type: application/sdp") {
		t.Error("487 no debe llevar SDP")
	}
	if !strings.Contains(r, "Content-Length: 0\r\n") {
		t.Errorf("487 debe tener Content-Length: 0\n---\n%s", r)
	}
}

func TestBuildAnswererBYE(t *testing.T) {
	d := sampleDialog()
	b := BuildAnswererBYE(d, "bridge", "YOUR_GATEWAY_ID.bs.iotleg.com", "127.0.0.1", 41000, 2, "z9hG4bKbye1")
	for _, want := range []string{
		"BYE sip:c300x@YOUR_GATEWAY_ID.bs.iotleg.com SIP/2.0\r\n",
		"Via: SIP/2.0/TCP 127.0.0.1:41000;alias;branch=z9hG4bKbye1;rport\r\n",
		"Route: <sip:127.0.0.1:5060;transport=tcp;lr>\r\n",
		"From: <sip:alluser@YOUR_GATEWAY_ID.bs.iotleg.com>;tag=bridgetag01\r\n",
		"To: <sip:c300x@YOUR_GATEWAY_ID.bs.iotleg.com>;tag=aa\r\n",
		"Call-ID: abc123\r\n",
		"CSeq: 2 BYE\r\n",
		"Content-Length: 0\r\n",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("BYE no contiene %q\n---\n%s", want, b)
		}
	}
}

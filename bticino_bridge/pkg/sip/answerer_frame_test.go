package sip

import (
	"bufio"
	"io"
	"strconv"
	"strings"
	"testing"
)

func TestReadSIPFrame_INVITEConCuerpo(t *testing.T) {
	body := "v=0\r\no=c300x 1 1 IN IP4 127.0.0.1\r\nm=audio 44590 RTP/SAVP 96\r\n"
	msg := "INVITE sip:alluser@YOUR_GATEWAY_ID.bs.iotleg.com SIP/2.0\r\n" +
		"Via: SIP/2.0/TCP 127.0.0.1:5060;branch=z9hG4bKaaa\r\n" +
		"Via: SIP/2.0/TCP 127.0.0.1:41440;branch=z9hG4bKbbb\r\n" +
		"Record-Route: <sip:127.0.0.1:5060;transport=tcp;lr>\r\n" +
		"From: <sip:c300x@YOUR_GATEWAY_ID.bs.iotleg.com>;tag=aa\r\n" +
		"To: <sip:alluser@YOUR_GATEWAY_ID.bs.iotleg.com>\r\n" +
		"Call-ID: abc123\r\nCSeq: 20 INVITE\r\n" +
		"Content-Type: application/sdp\r\n" +
		"Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
	// Dos mensajes concatenados: framing debe leer solo el primero.
	r := bufio.NewReader(strings.NewReader(msg + "SIP/2.0 200 OK\r\nCSeq: 20 INVITE\r\nContent-Length: 0\r\n\r\n"))
	m, err := ReadSIPFrame(r)
	if err != nil {
		t.Fatalf("ReadSIPFrame: %v", err)
	}
	if m.Method != "INVITE" {
		t.Errorf("Method=%q", m.Method)
	}
	if len(m.ViaHeaders) != 2 {
		t.Errorf("esperaba 2 Via, got %d (%v)", len(m.ViaHeaders), m.ViaHeaders)
	}
	if len(m.RecordRoute) != 1 {
		t.Errorf("esperaba 1 Record-Route, got %d", len(m.RecordRoute))
	}
	if m.Headers["Call-ID"] != "abc123" {
		t.Errorf("Call-ID=%q", m.Headers["Call-ID"])
	}
	if !strings.Contains(m.Body, "m=audio 44590") {
		t.Errorf("body incompleto: %q", m.Body)
	}
	// El segundo mensaje sigue disponible en el reader:
	m2, err := ReadSIPFrame(r)
	if err != nil {
		t.Fatalf("segundo ReadSIPFrame: %v", err)
	}
	if m2.StatusCode != "200" {
		t.Errorf("segundo mensaje StatusCode=%q", m2.StatusCode)
	}
}

func TestReadSIPFrame_EOF(t *testing.T) {
	r := bufio.NewReader(strings.NewReader(""))
	_, err := ReadSIPFrame(r)
	if err != io.EOF {
		t.Fatalf("esperaba io.EOF, got %v", err)
	}
}

func TestParseSIPFrame_Respuesta(t *testing.T) {
	raw := "SIP/2.0 200 Registration successful\r\nCSeq: 1 REGISTER\r\nContent-Length: 0\r\n\r\n"
	m := ParseSIPFrame(raw)
	if m.StatusCode != "200" {
		t.Fatalf("StatusCode=%q", m.StatusCode)
	}
}

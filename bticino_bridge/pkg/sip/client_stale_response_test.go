package sip

import (
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"bticino_bridge/pkg/events"
)

// sipHeader extrae el valor de una cabecera de un mensaje SIP en crudo.
func sipHeader(raw, name string) string {
	for _, ln := range strings.Split(raw, "\r\n") {
		if strings.HasPrefix(strings.ToLower(ln), strings.ToLower(name)+":") {
			return strings.TrimSpace(ln[len(name)+1:])
		}
	}
	return ""
}

func sipResponse(status, callID, cseq, body string) string {
	ct := ""
	if body != "" {
		ct = "Content-Type: application/sdp\r\n"
	}
	return fmt.Sprintf("SIP/2.0 %s\r\nVia: SIP/2.0/UDP 127.0.0.1:5090;branch=z9hG4bKx\r\n"+
		"From: <sip:webrtc@example.com>;tag=fromtag123\r\nTo: <sip:c300x@example.com>;tag=remote1\r\n"+
		"Call-ID: %s\r\nCSeq: %s\r\n%sContent-Length: %d\r\n\r\n%s", status, callID, cseq, ct, len(body), body)
}

func sipBye(callID string) string {
	return fmt.Sprintf("BYE sip:webrtc@127.0.0.1:5090 SIP/2.0\r\nVia: SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bKbye\r\n"+
		"From: <sip:c300x@example.com>;tag=remote0\r\nTo: <sip:webrtc@example.com>;tag=fromtag123\r\n"+
		"Call-ID: %s\r\nCSeq: 20 BYE\r\nContent-Length: 0\r\n\r\n", callID)
}

// TestInviteWithSDPIgnoraRespuestasAjenas reproduce el bug de teardown del ojo
// (2026-10-13): en el socket webrtc quedaban respuestas de diálogos previos (el
// 200 del BYE anterior, 200 retransmitidos de un INVITE sin ACK, BYE del nativo).
// InviteWithSDP tomaba el PRIMER "200" como respuesta propia → SDP vacío/ajeno.
// Debe aceptar solo la respuesta con SU Call-ID y CSeq de INVITE, y contestar
// 200 a los BYE que el nativo envía por ese socket.
func TestInviteWithSDPIgnoraRespuestasAjenas(t *testing.T) {
	cfg := &SIPConfig{ServerAddr: "127.0.0.1:5060", Domain: "example.com", Username: "webrtc",
		LocalIP: "127.0.0.1", LocalPort: 5090, DevAddr: "20", Transport: "udp"}
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	c := NewBTicinoSIPClient(cfg, events.NewEventBus(10, logger), logger)
	// UDP real en loopback (como en producción): los datagramas se encolan y
	// un Write no bloquea esperando lectura (net.Pipe produciría deadlock).
	peer, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	local, err := net.Dial("udp", peer.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	_ = peer.SetDeadline(time.Now().Add(5 * time.Second))
	c.webrtcID = &sipIdentity{username: cfg.Username, fromTag: "fromtag123", cseq: 1, conn: local}
	c.callState = Registered

	realSDP := "v=0\r\nm=audio 9078 RTP/SAVP 98\r\na=rtpmap:98 speex/8000\r\n"
	staleSDP := "v=0\r\nm=audio 1111 RTP/SAVP 98\r\na=rtpmap:98 speex/8000\r\n"

	byeReply := make(chan string, 1)
	ack := make(chan string, 1)
	go func() {
		buf := make([]byte, 16384)
		n, from, err := peer.ReadFrom(buf)
		if err != nil {
			return
		}
		send := func(m string) { _, _ = peer.WriteTo([]byte(m), from) }
		cid := sipHeader(string(buf[:n]), "Call-ID")
		// 1) 200 del BYE del ojo anterior (sin SDP).
		send(sipResponse("200 OK", "old1@127.0.0.1", "538 BYE", ""))
		// 2) 200 retransmitido de un INVITE previo nunca ACKeado (otro Call-ID).
		send(sipResponse("200 OK", "old2@127.0.0.1", "537 INVITE", staleSDP))
		// 3) BYE del nativo de un diálogo previo: hay que contestarle 200.
		send(sipBye("old2@127.0.0.1"))
		n, _, err = peer.ReadFrom(buf)
		if err != nil {
			return
		}
		byeReply <- string(buf[:n])
		// 4) La respuesta buena.
		send(sipResponse("100 Trying", cid, "1 INVITE", ""))
		send(sipResponse("200 OK", cid, "1 INVITE", realSDP))
		n, _, err = peer.ReadFrom(buf)
		if err != nil {
			return
		}
		ack <- string(buf[:n])
	}()

	got, err := c.InviteWithSDP("c300x", "v=0\r\n")
	if err != nil {
		t.Fatalf("InviteWithSDP: %v", err)
	}
	if got != realSDP {
		t.Fatalf("SDP equivocado (tomó una respuesta ajena):\n%q", got)
	}
	select {
	case r := <-byeReply:
		if !strings.HasPrefix(r, "SIP/2.0 200") || sipHeader(r, "Call-ID") != "old2@127.0.0.1" {
			t.Fatalf("respuesta al BYE del nativo inesperada:\n%s", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no se contestó 200 al BYE del nativo")
	}
	select {
	case a := <-ack:
		if !strings.HasPrefix(a, "ACK ") {
			t.Fatalf("esperaba ACK, llegó:\n%s", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no se envió ACK a la respuesta buena")
	}
}

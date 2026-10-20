package sip

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"bticino_bridge/pkg/events"
)

// La rama del fork de Flexisip que llega a NUESTRO listener c300x con un INVITE
// que originamos nosotros (el ojo) debe recibir una respuesta FINAL. Si se ignora,
// Flexisip espera esa rama y no reenvía la respuesta final del bt_vct (p.ej. su
// 486): el ojo acababa en timeout a los 30 s (gate en vivo 2026-10-16).
func TestListenerContestaFinalAlInvitePropio(t *testing.T) {
	cfg := &SIPConfig{ServerAddr: "127.0.0.1:5060", Domain: "example.com", Username: "webrtc",
		LocalIP: "127.0.0.1", LocalPort: 5090, DevAddr: "20", Transport: "udp"}
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	c := NewBTicinoSIPClient(cfg, events.NewEventBus(10, logger), logger)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	local, err := net.Dial("udp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	c.c300xID = &sipIdentity{username: "c300x", fromTag: "c3tag", cseq: 1, conn: local}
	c.callState = Registered
	const cid = "own-eye@127.0.0.1"
	c.originatedCalls[cid] = time.Now()
	c.nativeCalls[cid] = time.Now() // INVITE del ojo (InviteWithSDP)

	raw := "INVITE sip:c300x@127.0.0.1:5090 SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bKproxy\r\n" +
		"Via: SIP/2.0/UDP 127.0.0.1:41796;branch=z9hG4bKorig;rport=41796\r\n" +
		"From: <sip:webrtc@example.com>;tag=fromtag123\r\nTo: <sip:c300x@example.com>\r\n" +
		"Call-ID: " + cid + "\r\nCSeq: 5 INVITE\r\nContent-Length: 0\r\n\r\n"
	inv := c.parseSingleSIPMessage(raw)
	c.handleC300xMessage(inv)

	_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 8192)
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatal("el listener no contestó al INVITE propio (la rama queda colgada en Flexisip)")
	}
	r := string(buf[:n])
	if !strings.HasPrefix(r, "SIP/2.0 480") {
		t.Fatalf("esperaba 480 final:\n%s", r)
	}
	if strings.Count(r, "\r\nVia: ") != 2 || sipHeader(r, "Call-ID") != cid || sipHeader(r, "CSeq") != "5 INVITE" {
		t.Fatalf("respuesta mal formada (Via/Call-ID/CSeq):\n%s", r)
	}
	if !strings.Contains(sipHeader(r, "To"), ";tag=") {
		t.Fatalf("una respuesta final necesita To-tag:\n%s", r)
	}

	// Flexisip contesta con un ACK hop-by-hop a nuestro 480: NO es una sesión.
	ack := c.parseSingleSIPMessage("ACK sip:c300x@127.0.0.1:5090 SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bKproxy\r\n" +
		"From: <sip:webrtc@example.com>;tag=fromtag123\r\nTo: <sip:c300x@example.com>;tag=x\r\n" +
		"Call-ID: " + cid + "\r\nCSeq: 5 ACK\r\nContent-Length: 0\r\n\r\n")
	c.handleC300xMessage(ack)
	if c.GetCallState() == Connected {
		t.Fatal("el ACK de nuestro 480 no debe marcar la sesión como conectada")
	}
}

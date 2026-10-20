package sip

import (
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"bticino_bridge/pkg/events"
)

// fakeUDPPeer simula Flexisip/bt_vct al otro lado del socket webrtc (UDP loopback).
type fakeUDPPeer struct {
	pc   net.PacketConn
	from net.Addr
	buf  []byte
}

func (p *fakeUDPPeer) read(t *testing.T) string {
	t.Helper()
	_ = p.pc.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, from, err := p.pc.ReadFrom(p.buf)
	if err != nil {
		t.Fatalf("peer: no llegó mensaje: %v", err)
	}
	p.from = from
	return string(p.buf[:n])
}

func (p *fakeUDPPeer) send(m string) { _, _ = p.pc.WriteTo([]byte(m), p.from) }

func newWatchedClient(t *testing.T) (*BTicinoSIPClient, *fakeUDPPeer, events.EventBus) {
	t.Helper()
	cfg := &SIPConfig{ServerAddr: "127.0.0.1:5060", Domain: "example.com", Username: "webrtc",
		LocalIP: "127.0.0.1", LocalPort: 5090, DevAddr: "20", Transport: "udp"}
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	bus := events.NewEventBus(10, logger)
	c := NewBTicinoSIPClient(cfg, bus, logger)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	local, err := net.Dial("udp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.stopDialogWatch(); local.Close(); pc.Close() })
	c.webrtcID = &sipIdentity{username: cfg.Username, fromTag: "fromtag123", cseq: 1, conn: local}
	c.callState = Registered
	return c, &fakeUDPPeer{pc: pc, buf: make([]byte, 16384)}, bus
}

// connectEye completa un INVITE→200→ACK y devuelve el Call-ID del diálogo.
func connectEye(t *testing.T, c *BTicinoSIPClient, peer *fakeUDPPeer) string {
	t.Helper()
	done := make(chan error, 1)
	go func() { _, err := c.InviteWithSDP("c300x", "v=0\r\n"); done <- err }()
	cid := sipHeader(peer.read(t), "Call-ID")
	peer.send(sipResponse("200 OK", cid, "1 INVITE", "v=0\r\nm=audio 9078 RTP/SAVP 98\r\n"))
	if ack := peer.read(t); !strings.HasPrefix(ack, "ACK ") {
		t.Fatalf("esperaba ACK:\n%s", ack)
	}
	if err := <-done; err != nil {
		t.Fatalf("InviteWithSDP: %v", err)
	}
	return cid
}

// El nativo cuelga el ojo: el vigilante contesta 200 al BYE y publica sip.dialog.ended
// con el Call-ID, sin que el bridge mande su propio BYE.
func TestDialogWatchBYERemoto(t *testing.T) {
	c, peer, bus := newWatchedClient(t)
	ended := subscribeOnce(bus, "sip.dialog.ended")
	cid := connectEye(t, c, peer)

	peer.send(sipBye(cid))
	r := peer.read(t)
	if !strings.HasPrefix(r, "SIP/2.0 200") || sipHeader(r, "Call-ID") != cid {
		t.Fatalf("respuesta al BYE remoto inesperada:\n%s", r)
	}
	select {
	case e := <-ended:
		d := e.Data.(map[string]interface{})
		if d["call_id"] != cid {
			t.Fatalf("call_id=%v, esperaba %s", d["call_id"], cid)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no se publicó sip.dialog.ended")
	}
	if c.GetCallState() == Connected {
		t.Fatal("tras el BYE remoto la sesión no debe seguir Connected")
	}
	if err := c.Hangup(); err != nil { // ya cerrada: no-op, sin BYE propio
		t.Fatalf("Hangup: %v", err)
	}
	_ = peer.pc.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, _, err := peer.pc.ReadFrom(peer.buf); err == nil {
		t.Fatalf("no debía mandarse BYE propio tras un BYE remoto, llegó:\n%s", string(peer.buf[:n]))
	}
}

// Nunca dos lectores en el socket: tras Hangup (que para el vigilante) un nuevo ojo
// lee su 200 sin que el vigilante del anterior se lo robe.
func TestDialogWatchSeParaAntesDelSiguienteInvite(t *testing.T) {
	c, peer, _ := newWatchedClient(t)
	connectEye(t, c, peer)
	if err := c.Hangup(); err != nil {
		t.Fatalf("Hangup: %v", err)
	}
	if bye := peer.read(t); !strings.HasPrefix(bye, "BYE ") {
		t.Fatalf("esperaba BYE:\n%s", bye)
	}
	for i := 0; i < 3; i++ { // varios ojos seguidos
		connectEye(t, c, peer)
		if err := c.Hangup(); err != nil {
			t.Fatalf("Hangup %d: %v", i, err)
		}
		peer.read(t) // BYE
	}
}

// sipOKConRuta es el 200 del bt_vct tal como llega por Flexisip (log real del
// 2026-10-16): Contact del nativo + Record-Route del proxy.
func sipOKConRuta(callID, body string) string {
	return "SIP/2.0 200 Ok\r\nVia: SIP/2.0/UDP 127.0.0.1:5090;branch=z9hG4bKx\r\n" +
		"Record-Route: <sip:127.0.0.1:5060;transport=tcp;lr>\r\n" +
		"Record-Route: <sip:127.0.0.1:5060;lr>\r\n" +
		"From: <sip:webrtc@example.com>;tag=fromtag123\r\nTo: <sip:c300x@example.com>;tag=NAhcCga\r\n" +
		"Call-ID: " + callID + "\r\nCSeq: 1 INVITE\r\n" +
		"Contact: <sip:c300x@127.0.0.1:57102;transport=tcp>;+sip.instance=\"<urn:uuid:x>\"\r\n" +
		"Content-Type: application/sdp\r\n" +
		"Content-Length: " + itoa(len(body)) + "\r\n\r\n" + body
}

func itoa(n int) string { return strconv.Itoa(n) }

// El BYE (y el ACK) deben ir al Contact del 200 con Route = Record-Route invertido
// (RFC 3261 §12.1.2). Antes iban a sip:c300x@dominio sin Route: Flexisip no los
// entregaba al bt_vct, que seguía en llamada ~30 s y contestaba 486 al siguiente ojo.
func TestByeYAckVanAlContactConRoute(t *testing.T) {
	c, peer, _ := newWatchedClient(t)
	done := make(chan error, 1)
	go func() { _, err := c.InviteWithSDP("c300x", "v=0\r\n"); done <- err }()
	cid := sipHeader(peer.read(t), "Call-ID")
	peer.send(sipOKConRuta(cid, "v=0\r\nm=audio 9078 RTP/SAVP 98\r\n"))
	ack := peer.read(t)
	if err := <-done; err != nil {
		t.Fatalf("InviteWithSDP: %v", err)
	}
	const target = "sip:c300x@127.0.0.1:57102;transport=tcp"
	wantRoutes := "Route: <sip:127.0.0.1:5060;lr>\r\nRoute: <sip:127.0.0.1:5060;transport=tcp;lr>\r\n"
	for name, m := range map[string]string{"ACK": ack} {
		if !strings.HasPrefix(m, name+" "+target+" SIP/2.0") {
			t.Fatalf("%s con Request-URI equivocada:\n%s", name, m)
		}
		if !strings.Contains(m, wantRoutes) {
			t.Fatalf("%s sin Route (Record-Route invertido):\n%s", name, m)
		}
	}
	if err := c.Hangup(); err != nil {
		t.Fatalf("Hangup: %v", err)
	}
	bye := peer.read(t)
	if !strings.HasPrefix(bye, "BYE "+target+" SIP/2.0") {
		t.Fatalf("BYE con Request-URI equivocada:\n%s", bye)
	}
	if !strings.Contains(bye, wantRoutes) {
		t.Fatalf("BYE sin Route:\n%s", bye)
	}
	if sipHeader(bye, "Call-ID") != cid || !strings.Contains(sipHeader(bye, "To"), "tag=NAhcCga") {
		t.Fatalf("BYE fuera de diálogo (Call-ID/To-tag):\n%s", bye)
	}
}

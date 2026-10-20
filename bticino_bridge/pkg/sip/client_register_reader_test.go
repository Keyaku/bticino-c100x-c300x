package sip

import (
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"bticino_bridge/pkg/events"
)

func sipRegisterOK(callID, cseq string) string {
	return "SIP/2.0 200 Registration successful\r\nVia: SIP/2.0/UDP 127.0.0.1:5090;branch=z9hG4bKr\r\n" +
		"From: <sip:x@example.com>;tag=a\r\nTo: <sip:x@example.com>;tag=b\r\n" +
		"Call-ID: " + callID + "\r\nCSeq: " + cseq + "\r\nContent-Length: 0\r\n\r\n"
}

// webrtc: mientras el vigilante del ojo posee el socket, el re-REGISTER NO lee
// (antes robaba/perdía respuestas: "Failed to refresh webrtc registration").
// Devuelve errSocketBusy al momento y se reintenta; con el socket libre funciona,
// contestando de paso un BYE rezagado que llegue antes del 200 del REGISTER.
func TestReRegisterWebrtcNoCompiteConElVigilante(t *testing.T) {
	c, peer, _ := newWatchedClient(t)
	connectEye(t, c, peer)

	start := time.Now()
	err := c.reRegisterIdentity(c.webrtcID)
	if !errors.Is(err, errSocketBusy) {
		t.Fatalf("con el vigilante activo esperaba errSocketBusy, got %v", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("el re-REGISTER no debe bloquearse esperando el socket")
	}
	if err := c.Hangup(); err != nil {
		t.Fatal(err)
	}
	peer.read(t) // BYE

	done := make(chan error, 1)
	go func() { done <- c.reRegisterIdentity(c.webrtcID) }()
	reg := peer.read(t)
	if !strings.HasPrefix(reg, "REGISTER ") {
		t.Fatalf("esperaba REGISTER:\n%s", reg)
	}
	peer.send(sipBye("viejo@127.0.0.1"))
	if r := peer.read(t); !strings.HasPrefix(r, "SIP/2.0 200") {
		t.Fatalf("esperaba 200 al BYE rezagado:\n%s", r)
	}
	peer.send(sipRegisterOK(sipHeader(reg, "Call-ID"), sipHeader(reg, "CSeq")))
	if err := <-done; err != nil {
		t.Fatalf("re-REGISTER con socket libre: %v", err)
	}
}

// c300x: el listener es el ÚNICO lector de su socket; el re-REGISTER recibe su
// respuesta a través de él (antes el listener se la comía: "Failed to refresh
// c300x registration ... i/o timeout" cada ~4-5 min).
func TestReRegisterC300xRecibeRespuestaViaListener(t *testing.T) {
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
	peer := &fakeUDPPeer{pc: pc, buf: make([]byte, 16384)}
	c.c300xID = &sipIdentity{username: "c300x", fromTag: "c3", callID: "reg-c300x@127.0.0.1", cseq: 1, conn: local}
	c.startC300xListener()
	defer close(c.stopCh)

	done := make(chan error, 1)
	go func() { done <- c.reRegisterIdentity(c.c300xID) }()
	reg := peer.read(t)
	if !strings.HasPrefix(reg, "REGISTER ") {
		t.Fatalf("esperaba REGISTER:\n%s", reg)
	}
	peer.send(sipRegisterOK(sipHeader(reg, "Call-ID"), sipHeader(reg, "CSeq")))
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("re-REGISTER c300x: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("el re-REGISTER c300x no recibió su 200 (se lo comió el listener)")
	}
}

// I1 (revisión): el vigilante recibe el lector TRASPASADO (nunca lo espera), así que
// pararlo no puede bloquearse; antes, un vigilante esperando el lector que otro
// retenía + ese otro pidiendo pararlo = interbloqueo permanente.
func TestStopDialogWatchNoSeBloqueaSiOtroRetieneElLector(t *testing.T) {
	c, _, _ := newWatchedClient(t)
	c.webrtcID.readMu.Lock()          // el bucle del INVITE posee el lector…
	c.startDialogWatch("x@127.0.0.1") // …y se lo traspasa al vigilante
	done := make(chan struct{})
	go func() { c.stopDialogWatch(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stopDialogWatch se bloquea")
	}
	if !c.webrtcID.readMu.TryLock() {
		t.Fatal("al parar, el vigilante debe soltar el lector traspasado")
	}
	c.webrtcID.readMu.Unlock()
}

// M1 (revisión): una respuesta REGISTER tardía de un refresco anterior (otro número
// de CSeq) que quedó en el canal NO debe tomarse como la del refresco actual.
func TestReRegisterC300xIgnoraRespuestaRezagada(t *testing.T) {
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
	peer := &fakeUDPPeer{pc: pc, buf: make([]byte, 16384)}
	c.c300xID = &sipIdentity{username: "c300x", fromTag: "c3", callID: "reg-c300x@127.0.0.1", cseq: 7, conn: local}
	c.startC300xListener()
	defer close(c.stopCh)
	// 401 rezagado de un refresco anterior, ya en el canal.
	c.c300xID.respCh <- c.parseSingleSIPMessage("SIP/2.0 401 Unauthorized\r\nCall-ID: reg-c300x@127.0.0.1\r\nCSeq: 3 REGISTER\r\nContent-Length: 0\r\n\r\n")

	done := make(chan error, 1)
	go func() { done <- c.reRegisterIdentity(c.c300xID) }()
	reg := peer.read(t)
	peer.send(sipRegisterOK(sipHeader(reg, "Call-ID"), sipHeader(reg, "CSeq")))
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tomó la respuesta rezagada: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("re-REGISTER sin respuesta")
	}
}

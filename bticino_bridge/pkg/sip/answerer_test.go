package sip

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"bticino_bridge/pkg/config"
	"bticino_bridge/pkg/events"
)

// fakeTransport implementa AnswererTransport con canales scriptables.
type fakeTransport struct {
	mu     sync.Mutex
	sent   []string
	in     chan *SIPMessage
	closed bool
}

func newFakeTransport() *fakeTransport { return &fakeTransport{in: make(chan *SIPMessage, 16)} }
func (f *fakeTransport) Send(raw string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, raw)
	return nil
}
func (f *fakeTransport) Recv() (*SIPMessage, error) {
	m, ok := <-f.in
	if !ok {
		return nil, errTransportClosed
	}
	return m, nil
}
func (f *fakeTransport) LocalHost() string { return "127.0.0.1" }
func (f *fakeTransport) LocalPort() int    { return 41000 }
func (f *fakeTransport) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		close(f.in)
		f.closed = true
	}
	return nil
}
func (f *fakeTransport) lastSentContaining(s string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.sent) - 1; i >= 0; i-- {
		if strings.Contains(f.sent[i], s) {
			return f.sent[i]
		}
	}
	return ""
}
func (f *fakeTransport) sentMatching(pred func(string) bool) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.sent) - 1; i >= 0; i-- {
		if pred(f.sent[i]) {
			return f.sent[i]
		}
	}
	return ""
}
func (f *fakeTransport) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}
func (f *fakeTransport) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return ""
	}
	return f.sent[len(f.sent)-1]
}

func inviteMsg(callID, cseq string) *SIPMessage {
	body := "v=0\r\nm=audio 44590 RTP/SAVP 96\r\nm=video 13876 RTP/SAVP 96\r\n"
	return &SIPMessage{
		Method: "INVITE", RequestURI: "sip:alluser@YOUR_GATEWAY_ID.bs.iotleg.com",
		Headers: map[string]string{"Call-ID": callID, "CSeq": cseq + " INVITE",
			"From": "<sip:c300x@YOUR_GATEWAY_ID.bs.iotleg.com>;tag=aa", "To": "<sip:alluser@YOUR_GATEWAY_ID.bs.iotleg.com>"},
		ViaHeaders:  []string{"SIP/2.0/TCP 127.0.0.1:5060;branch=z9hG4bKaaa"},
		RecordRoute: []string{"<sip:127.0.0.1:5060;transport=tcp;lr>"},
		Body:        body,
	}
}
func msg(method string, hdr map[string]string) *SIPMessage {
	return &SIPMessage{Method: method, Headers: hdr}
}

func newTestAnswerer(f *fakeTransport, bus events.EventBus) *Answerer {
	cfg := config.AnswererConfig{Enabled: true, User: "bridge", AudioPort: 10100, VideoPort: 10102, MaxCallSeconds: 120}
	a := NewAnswerer(cfg, "YOUR_GATEWAY_ID.bs.iotleg.com", f, bus, logrus.New())
	return a
}

func TestAnswerer_TimbrazoSinContestar_CANCEL(t *testing.T) {
	f := newFakeTransport()
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	a := newTestAnswerer(f, bus)
	a.Start()
	defer a.Stop()
	f.in <- inviteMsg("c1", "20")
	waitFor(t, func() bool { return f.lastSentContaining("180 Ringing") != "" }, "180 Ringing")
	if a.StateString() != "ringing" {
		t.Fatalf("estado=%q, esperaba ringing", a.StateString())
	}
	// CANCEL con Via propia distinta a la del INVITE y su CSeq "20 CANCEL".
	cancel := &SIPMessage{Method: "CANCEL",
		Headers:    map[string]string{"Call-ID": "c1", "CSeq": "20 CANCEL"},
		ViaHeaders: []string{"SIP/2.0/TCP 127.0.0.1:5060;branch=z9hG4bKcancel"}}
	f.in <- cancel
	waitFor(t, func() bool { return f.lastSentContaining("487 Request Terminated") != "" }, "487")
	waitFor(t, func() bool { return a.StateString() == "idle" }, "vuelta a idle")
	// El 200 al CANCEL debe llevar el CSeq y la Via del propio CANCEL, no los del INVITE.
	ok200 := f.sentMatching(func(s string) bool {
		return strings.Contains(s, "SIP/2.0 200 OK") && strings.Contains(s, "CSeq: 20 CANCEL")
	})
	if ok200 == "" {
		t.Fatal("no se envió 200 OK al CANCEL con CSeq: 20 CANCEL")
	}
	if !strings.Contains(ok200, "z9hG4bKcancel") {
		t.Fatalf("el 200-a-CANCEL no refleja la Via del CANCEL:\n%s", ok200)
	}
	// El 487 (respuesta al INVITE) SÍ usa el CSeq del INVITE.
	if !strings.Contains(f.lastSentContaining("487 Request Terminated"), "CSeq: 20 INVITE") {
		t.Fatal("el 487 debe conservar CSeq: 20 INVITE")
	}
}

func TestAnswerer_Contestar_200_ACK_IN_CALL_luego_BYE(t *testing.T) {
	f := newFakeTransport()
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	a := newTestAnswerer(f, bus)
	a.Start()
	defer a.Stop()
	f.in <- inviteMsg("c2", "21")
	waitFor(t, func() bool { return f.lastSentContaining("180 Ringing") != "" }, "180")
	if err := a.Answer(); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	got := f.lastSentContaining("200 OK")
	if got == "" || !strings.Contains(got, "m=audio 10100") {
		t.Fatalf("200 OK sin SDP propio:\n%s", got)
	}
	f.in <- msg("ACK", map[string]string{"Call-ID": "c2", "CSeq": "21 ACK"})
	waitFor(t, func() bool { return a.StateString() == "in_call" }, "in_call")
	if err := a.Hangup(); err != nil {
		t.Fatalf("Hangup: %v", err)
	}
	waitFor(t, func() bool { return f.lastSentContaining("BYE ") != "" }, "BYE saliente")
	waitFor(t, func() bool { return a.StateString() == "idle" }, "idle")
}

func TestAnswerer_DoorcallAnsweredLlevaParamsDeMedia(t *testing.T) {
	f := newFakeTransport()
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	// Capturar el payload de doorcall.answered.
	got := make(chan map[string]interface{}, 1)
	bus.Subscribe("doorcall.answered", func(e *events.Event) {
		if m, ok := e.Data.(map[string]interface{}); ok {
			got <- m
		}
	})
	a := newTestAnswerer(f, bus)
	a.Start()
	defer a.Stop()
	f.in <- inviteMsg("cm", "50") // inviteMsg trae m=audio 44590 en el offer
	waitFor(t, func() bool { return f.lastSentContaining("180 Ringing") != "" }, "180")
	if err := a.Answer(); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	select {
	case m := <-got:
		if m["media_host"] != "127.0.0.1" {
			t.Errorf("media_host=%v", m["media_host"])
		}
		if mp, _ := m["media_port"].(int); mp != 44590 {
			t.Errorf("media_port=%v, esperaba 44590", m["media_port"])
		}
		if k, _ := m["srtp_key"].(string); k == "" {
			t.Error("srtp_key vacío")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no llegó doorcall.answered")
	}
}

// doorcall.incoming debe llevar call_type clasificado a partir del SDP del INVITE
// (inviteMsg trae m=audio + m=video => placa).
func TestAnswerer_DoorcallIncomingLlevaCallType(t *testing.T) {
	f := newFakeTransport()
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	got := make(chan map[string]interface{}, 1)
	bus.Subscribe("doorcall.incoming", func(e *events.Event) {
		if m, ok := e.Data.(map[string]interface{}); ok {
			got <- m
		}
	})
	a := newTestAnswerer(f, bus)
	a.Start()
	defer a.Stop()
	f.in <- inviteMsg("ct", "70")
	select {
	case m := <-got:
		if m["call_type"] != CallTypePlaca {
			t.Errorf("call_type=%v, esperaba %q", m["call_type"], CallTypePlaca)
		}
		if m["call_id"] != "ct" {
			t.Errorf("call_id=%v, esperaba ct", m["call_id"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no llegó doorcall.incoming")
	}
}

func TestAnswerer_AnswerTrasCancel_FallaLimpio(t *testing.T) {
	f := newFakeTransport()
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	a := newTestAnswerer(f, bus)
	a.Start()
	defer a.Stop()
	f.in <- inviteMsg("c3", "22")
	waitFor(t, func() bool { return f.lastSentContaining("180 Ringing") != "" }, "180")
	f.in <- msg("CANCEL", map[string]string{"Call-ID": "c3", "CSeq": "22 CANCEL"})
	waitFor(t, func() bool { return a.StateString() == "idle" }, "idle")
	if err := a.Answer(); err == nil {
		t.Fatal("Answer tras CANCEL debe fallar (ErrNotRinging)")
	}
}

func TestAnswerer_SegundoInviteMientrasOcupado_486(t *testing.T) {
	f := newFakeTransport()
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	a := newTestAnswerer(f, bus)
	a.Start()
	defer a.Stop()
	f.in <- inviteMsg("c4", "23")
	waitFor(t, func() bool { return f.lastSentContaining("180 Ringing") != "" }, "180")
	f.in <- inviteMsg("c5", "1") // otro Call-ID
	waitFor(t, func() bool { return f.lastSentContaining("486 Busy Here") != "" }, "486")
	if a.StateString() != "ringing" {
		t.Fatalf("el 2º INVITE no debe cambiar el estado, got %q", a.StateString())
	}
}

func TestAnswerer_BYEEntrante_200_y_idle(t *testing.T) {
	f := newFakeTransport()
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	a := newTestAnswerer(f, bus)
	a.Start()
	defer a.Stop()
	f.in <- inviteMsg("c6", "24")
	waitFor(t, func() bool { return f.lastSentContaining("180 Ringing") != "" }, "180")
	a.Answer()
	f.in <- msg("ACK", map[string]string{"Call-ID": "c6", "CSeq": "24 ACK"})
	waitFor(t, func() bool { return a.StateString() == "in_call" }, "in_call")
	before := f.count()
	f.in <- msg("BYE", map[string]string{"Call-ID": "c6", "CSeq": "25 BYE",
		"From": "<sip:c300x@YOUR_GATEWAY_ID.bs.iotleg.com>;tag=aa", "To": "<sip:alluser@YOUR_GATEWAY_ID.bs.iotleg.com>;tag=x"})
	waitFor(t, func() bool { return a.StateString() == "idle" }, "idle tras BYE entrante")
	// El BYE entrante debe recibir su propio 200 (no confundir con el 200 del Answer previo):
	// el último enviado tras el BYE debe llevar su Call-ID + CSeq y el 200.
	last := f.last()
	if f.count() <= before {
		t.Fatal("el BYE entrante no generó ninguna respuesta")
	}
	if !strings.Contains(last, "200") || !strings.Contains(last, "Call-ID: c6") || !strings.Contains(last, "CSeq: 25 BYE") {
		t.Fatalf("el 200 al BYE entrante no coincide (Call-ID c6 / CSeq 25 BYE):\n%s", last)
	}
}

func TestAnswerer_DedupeRetransmisionINVITE(t *testing.T) {
	f := newFakeTransport()
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	a := newTestAnswerer(f, bus)
	a.Start()
	defer a.Stop()
	f.in <- inviteMsg("c7", "26")
	waitFor(t, func() bool { return f.lastSentContaining("180 Ringing") != "" }, "180")
	f.in <- inviteMsg("c7", "26") // retransmisión: mismo Call-ID+CSeq
	time.Sleep(50 * time.Millisecond)
	// Sigue en ringing, no 486 (no lo trata como llamada nueva).
	if a.StateString() != "ringing" {
		t.Fatalf("retransmisión rompió el estado: %q", a.StateString())
	}
	if f.lastSentContaining("486 Busy Here") != "" {
		t.Fatal("retransmisión no debe dar 486")
	}
}

// TestAnswerer_RingGuard_SinCancel_480YVuelveAIdle: si nadie contesta desde HA y el CANCEL de
// la placa se pierde, el contestador no puede quedarse en RINGING para siempre (respondería 486
// a todos los timbrazos siguientes). A los ring_timeout_seconds cierra el INVITE con 480 y vuelve
// a idle; el siguiente timbrazo recibe 180 de nuevo.
func TestAnswerer_RingGuard_SinCancel_480YVuelveAIdle(t *testing.T) {
	f := newFakeTransport()
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	cfg := config.AnswererConfig{Enabled: true, User: "bridge", AudioPort: 10100, VideoPort: 10102,
		MaxCallSeconds: 120, RingTimeoutSeconds: 1}
	a := NewAnswerer(cfg, "YOUR_GATEWAY_ID.bs.iotleg.com", f, bus, logrus.New())
	a.Start()
	defer a.Stop()
	f.in <- inviteMsg("cr", "40")
	waitFor(t, func() bool { return f.lastSentContaining("180 Ringing") != "" }, "180")
	waitFor(t, func() bool { return f.lastSentContaining("480 Temporarily Unavailable") != "" }, "480 por ring guard")
	if !strings.Contains(f.lastSentContaining("480 Temporarily Unavailable"), "CSeq: 40 INVITE") {
		t.Fatal("el 480 debe responder al INVITE (CSeq: 40 INVITE)")
	}
	waitFor(t, func() bool { return a.StateString() == "idle" }, "vuelta a idle")
	// Un timbrazo nuevo debe volver a sonar (180), no 486 Busy.
	f.in <- inviteMsg("cr2", "41")
	waitFor(t, func() bool {
		return strings.Contains(f.last(), "180 Ringing") && strings.Contains(f.last(), "cr2")
	}, "180 al segundo timbrazo")
}

// TestAnswerer_RingGuard_NoSaltaTrasContestar: contestar desarma la guarda de timbre; la llamada
// en curso solo la limita max_call_seconds.
func TestAnswerer_RingGuard_NoSaltaTrasContestar(t *testing.T) {
	f := newFakeTransport()
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	cfg := config.AnswererConfig{Enabled: true, User: "bridge", AudioPort: 10100, VideoPort: 10102,
		MaxCallSeconds: 120, RingTimeoutSeconds: 1}
	a := NewAnswerer(cfg, "YOUR_GATEWAY_ID.bs.iotleg.com", f, bus, logrus.New())
	a.Start()
	defer a.Stop()
	f.in <- inviteMsg("ca", "50")
	waitFor(t, func() bool { return f.lastSentContaining("180 Ringing") != "" }, "180")
	if err := a.Answer(); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	f.in <- msg("ACK", map[string]string{"Call-ID": "ca", "CSeq": "50 ACK"})
	waitFor(t, func() bool { return a.StateString() == "in_call" }, "in_call")
	time.Sleep(1500 * time.Millisecond)
	if a.StateString() != "in_call" || f.lastSentContaining("480") != "" {
		t.Fatalf("la guarda de timbre saltó tras contestar: estado=%s", a.StateString())
	}
}

// TestAnswerer_StopConTimbrePendiente_NoPanic: si el bridge se detiene mientras suena, la guarda
// de timbre no puede saltar después del Stop (publicaría sobre un bus ya cerrado → panic).
func TestAnswerer_StopConTimbrePendiente_NoPanic(t *testing.T) {
	f := newFakeTransport()
	bus := events.NewEventBus(2, logrus.New())
	cfg := config.AnswererConfig{Enabled: true, User: "bridge", AudioPort: 10100, VideoPort: 10102,
		MaxCallSeconds: 120, RingTimeoutSeconds: 1}
	a := NewAnswerer(cfg, "YOUR_GATEWAY_ID.bs.iotleg.com", f, bus, logrus.New())
	a.Start()
	f.in <- inviteMsg("cs", "60")
	waitFor(t, func() bool { return f.lastSentContaining("180 Ringing") != "" }, "180")
	a.Stop()
	bus.Close()
	time.Sleep(1500 * time.Millisecond) // más que RingTimeoutSeconds: la guarda NO debe disparar
}

func TestAnswerer_GuardTimeoutCuelga(t *testing.T) {
	f := newFakeTransport()
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	cfg := config.AnswererConfig{Enabled: true, User: "bridge", AudioPort: 10100, VideoPort: 10102, MaxCallSeconds: 1}
	a := NewAnswerer(cfg, "YOUR_GATEWAY_ID.bs.iotleg.com", f, bus, logrus.New())
	a.Start()
	defer a.Stop()
	f.in <- inviteMsg("cg", "30")
	waitFor(t, func() bool { return f.lastSentContaining("180 Ringing") != "" }, "180")
	a.Answer()
	f.in <- msg("ACK", map[string]string{"Call-ID": "cg", "CSeq": "30 ACK"})
	waitFor(t, func() bool { return a.StateString() == "in_call" }, "in_call")
	// MaxCallSeconds=1 → a los ~1s debe salir BYE por guard y volver a idle.
	waitFor(t, func() bool { return f.lastSentContaining("BYE ") != "" && a.StateString() == "idle" }, "guard BYE")
}

// TestAnswerer_RespuestaREGISTER_NoBloqueaElReadLoop reproduce el bug crítico de la ronda 1 de
// revisión: dispatch() ya tiene a.mu tomado, así que el handler del 200-al-REGISTER debe fijar
// a.registered directamente (NO llamar a un helper que vuelva a hacer a.mu.Lock(), porque
// sync.Mutex no es reentrante y el read-loop se quedaría bloqueado para siempre). El test primero
// comprueba el flip de IsRegistered() y LUEGO, con el mismo Answerer/goroutine, inyecta un INVITE
// y comprueba que todavía se responde 180 Ringing — si el read-loop se hubiera quedado colgado en
// el REGISTER anterior, este segundo waitFor haría timeout.
func TestAnswerer_RespuestaREGISTER_NoBloqueaElReadLoop(t *testing.T) {
	f := newFakeTransport()
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	a := newTestAnswerer(f, bus)
	a.Start()
	defer a.Stop()

	f.in <- &SIPMessage{StatusCode: "200", Headers: map[string]string{"CSeq": "1 REGISTER"}}
	waitFor(t, func() bool { return a.IsRegistered() }, "registrado")

	// El read-loop debe seguir vivo: un INVITE posterior tiene que despacharse con normalidad.
	f.in <- inviteMsg("creg", "40")
	waitFor(t, func() bool { return f.lastSentContaining("180 Ringing") != "" }, "180 Ringing tras REGISTER 200")
}

// waitFor sondea cond hasta 5s. El margen holgado evita flakes de timing bajo carga /
// instrumentación de -race (el guard de 1s y el ida-vuelta INVITE→200→BYE tardan más con -race).
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout esperando: %s", what)
}

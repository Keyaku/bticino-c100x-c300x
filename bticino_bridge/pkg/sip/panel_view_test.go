package sip

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bticino_bridge/pkg/config"
	"bticino_bridge/pkg/events"

	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

// --- Fakes inyectados (contadores atómicos: los lee waitFor desde otra goroutine) ---

type fakeEyeOrig struct {
	answer    string
	inviteErr error
	invites   atomic.Int64
	hangups   atomic.Int64
	onInvite  func() // se invoca DENTRO de InviteWithSDP (simula un cede a mitad del round-trip)
}

func (f *fakeEyeOrig) InviteWithSDP(t, s string) (string, error) {
	f.invites.Add(1)
	if f.onInvite != nil {
		f.onInvite()
	}
	return f.answer, f.inviteErr
}
func (f *fakeEyeOrig) Hangup() error  { f.hangups.Add(1); return nil }
func (f *fakeEyeOrig) CallID() string { return "eye-call-1" }

type fakeFwd struct {
	started atomic.Int64
	stopped atomic.Int64
	dst     atomic.Value // destino del último CaptureForward
}

func (f *fakeFwd) CaptureForward(h string, p int, d string, a AudioAnswer) error {
	f.dst.Store(d)
	f.started.Add(1)
	return nil
}
func (f *fakeFwd) Stop() { f.stopped.Add(1) }

type fakeMarker struct{ active bool }

func (f *fakeMarker) IsCallActive() bool { return f.active }

// Answer SDP realista del ojo: DOS secciones m= (vídeo recvonly + audio sendrecv).
// Comprueba que ParseAudioAnswer selecciona el leg de AUDIO (5000) y no el de vídeo (7000).
const fakeEyeAnswer = "v=0\r\n" +
	"m=video 7000 RTP/SAVP 96\r\n" +
	"a=rtpmap:96 H264/90000\r\n" +
	"a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:VIDEOvideoVIDEOvideoVIDEOvideoVIDEOvideo\r\n" +
	"m=audio 5000 RTP/SAVP 98\r\n" +
	"a=rtpmap:98 speex/8000\r\n" +
	"a=crypto:2 AES_CM_128_HMAC_SHA1_80 inline:QUJDRA==\r\n"

func logrusTestLogger() *logrus.Logger {
	l := logrus.New()
	l.SetLevel(logrus.PanicLevel) // silencioso en los tests
	return l
}

// subscribeOnce captura el primer Event publicado con ese nombre en un canal cap 1.
func subscribeOnce(bus events.EventBus, name string) chan *events.Event {
	ch := make(chan *events.Event, 1)
	bus.Subscribe(name, func(e *events.Event) {
		select {
		case ch <- e:
		default:
		}
	})
	return ch
}

func newPV(orig *fakeEyeOrig, fwd *fakeFwd, m *fakeMarker, bus events.EventBus, cfg config.PanelViewConfig) *PanelView {
	return NewPanelView(cfg, orig, fwd, nil, m, bus, "127.0.0.1", 10102, 10104, logrusTestLogger())
}

func cfgEnabled() config.PanelViewConfig {
	return config.PanelViewConfig{Enabled: true, DEVADDR: "11", TimeoutSeconds: 30, CallerUser: "webrtc"}
}

func TestPanelViewHappyPathEmitsEvents(t *testing.T) {
	bus := events.NewEventBus(2, logrusTestLogger())
	gotConnected := subscribeOnce(bus, "panelview.connected")
	gotSipConn := subscribeOnce(bus, "sip.call.connected")
	orig := &fakeEyeOrig{answer: fakeEyeAnswer}
	fwd := &fakeFwd{}
	pv := newPV(orig, fwd, &fakeMarker{active: false}, bus, cfgEnabled())
	_ = pv.Start()
	defer func() { _ = pv.Hangup() }() // parar el timer de auto-off (30 s) al salir

	if err := pv.Show(); err != nil {
		t.Fatalf("Show: %v", err)
	}
	if n := orig.invites.Load(); n != 1 {
		t.Fatalf("invites=%d", n)
	}
	ev := <-gotConnected
	data := ev.Data.(map[string]interface{})
	if data["call_id"] != "eye-call-1" {
		t.Fatalf("call_id inesperado: %+v", data)
	}
	if data["srtp_key"] == "" {
		t.Fatalf("srtp_key vacío: %+v", data)
	}
	port, ok := data["media_port"].(int)
	if !ok || port != 5000 {
		t.Fatalf("media_port debía ser el leg de AUDIO (5000), got %+v", data["media_port"])
	}
	<-gotSipConn // VER: arma los taps pasivos
	waitFor(t, func() bool { return fwd.started.Load() == 1 }, "OÍR forwarder debía arrancar")
}

func TestPanelViewRejectsWhenCallActive(t *testing.T) {
	bus := events.NewEventBus(2, logrusTestLogger())
	orig := &fakeEyeOrig{answer: fakeEyeAnswer}
	pv := newPV(orig, &fakeFwd{}, &fakeMarker{active: true}, bus, cfgEnabled())
	_ = pv.Start()
	if err := pv.Show(); err == nil {
		t.Fatal("debía rechazar: llamada nativa activa")
	}
	if n := orig.invites.Load(); n != 0 {
		t.Fatalf("no debía originar INVITE con callActive (invites=%d)", n)
	}
}

func TestPanelViewRejectsCleanlyOnInviteError(t *testing.T) {
	bus := events.NewEventBus(2, logrusTestLogger())
	orig := &fakeEyeOrig{inviteErr: errors.New("486 Busy")}
	fwd := &fakeFwd{}
	pv := newPV(orig, fwd, &fakeMarker{}, bus, cfgEnabled())
	_ = pv.Start()
	if err := pv.Show(); err == nil {
		t.Fatal("debía propagar el error de INVITE")
	}
	if pv.Active() {
		t.Fatal("no debe quedar activo tras fallo de INVITE")
	}
	if n := fwd.started.Load(); n != 0 {
		t.Fatalf("no debía armar media sin conexión (started=%d)", n)
	}
}

func TestPanelViewAutoOffTimerHangsUp(t *testing.T) {
	bus := events.NewEventBus(2, logrusTestLogger())
	cfg := cfgEnabled()
	cfg.TimeoutSeconds = 0 // sobreescrito abajo
	orig := &fakeEyeOrig{answer: fakeEyeAnswer}
	fwd := &fakeFwd{}
	pv := newPV(orig, fwd, &fakeMarker{}, bus, cfg)
	pv.setTimeoutForTest(20 * time.Millisecond)
	_ = pv.Start()
	if err := pv.Show(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return orig.hangups.Load() == 1 && fwd.stopped.Load() == 1 }, "auto-off debía colgar y parar media")
	if pv.Active() {
		t.Fatal("debió volver a IDLE tras el auto-off")
	}
}

func TestPanelViewRejectsWhenDisabledOrNoDevaddr(t *testing.T) {
	bus := events.NewEventBus(2, logrusTestLogger())
	for _, cfg := range []config.PanelViewConfig{
		{Enabled: false, DEVADDR: "11"},
		{Enabled: true, DEVADDR: ""},
	} {
		orig := &fakeEyeOrig{answer: fakeEyeAnswer}
		pv := newPV(orig, &fakeFwd{}, &fakeMarker{}, bus, cfg)
		_ = pv.Start()
		if err := pv.Show(); err == nil {
			t.Fatalf("debía rechazar con cfg=%+v", cfg)
		}
		if n := orig.invites.Load(); n != 0 {
			t.Fatalf("no debía originar INVITE (cfg=%+v)", cfg)
		}
	}
}

func TestPanelViewCedesToIncomingDoorbell(t *testing.T) {
	bus := events.NewEventBus(2, logrusTestLogger())
	orig := &fakeEyeOrig{answer: fakeEyeAnswer}
	fwd := &fakeFwd{}
	pv := newPV(orig, fwd, &fakeMarker{}, bus, cfgEnabled())
	_ = pv.Start()
	if err := pv.Show(); err != nil {
		t.Fatal(err)
	}
	bus.Publish("doorcall.incoming", map[string]interface{}{"call_id": "ring1"})
	waitFor(t, func() bool { return orig.hangups.Load() == 1 }, "el ojo debía ceder al timbre real")
}

// Important #2: si un cede (doorcall.incoming / botón Colgar) gana el mutex
// mientras el INVITE bloqueante está en vuelo, Show NO debe armar media ni
// timer; debe cerrar el diálogo recién abierto y volver a IDLE.
func TestPanelViewCedesDuringBlockingInvite(t *testing.T) {
	bus := events.NewEventBus(2, logrusTestLogger())
	orig := &fakeEyeOrig{answer: fakeEyeAnswer}
	fwd := &fakeFwd{}
	pv := newPV(orig, fwd, &fakeMarker{}, bus, cfgEnabled())
	_ = pv.Start()
	// El originator cuelga justo antes de devolver el answer (como si
	// doorcall.incoming colara durante el round-trip del INVITE).
	orig.onInvite = func() { _ = pv.Hangup() }
	_ = pv.Show()
	if pv.Active() {
		t.Fatal("no debe quedar activo tras ceder durante el INVITE")
	}
	if n := fwd.started.Load(); n != 0 {
		t.Fatalf("no debía armar el forwarder al ceder (started=%d)", n)
	}
	if n := orig.hangups.Load(); n < 1 {
		t.Fatalf("debía colgar el diálogo SIP recién abierto (hangups=%d)", n)
	}
}

// Important #1: panelview.ended debe llevar el call_id real (no vacío), para que
// la guarda anti-ffmpeg-huérfano de TalkSender.onEnded funcione.
func TestPanelViewEndedCarriesCallID(t *testing.T) {
	bus := events.NewEventBus(2, logrusTestLogger())
	gotEnded := subscribeOnce(bus, "panelview.ended")
	orig := &fakeEyeOrig{answer: fakeEyeAnswer}
	pv := newPV(orig, &fakeFwd{}, &fakeMarker{}, bus, cfgEnabled())
	_ = pv.Start()
	if err := pv.Show(); err != nil {
		t.Fatal(err)
	}
	_ = pv.Hangup()
	ev := <-gotEnded
	data := ev.Data.(map[string]interface{})
	if data["call_id"] != "eye-call-1" {
		t.Fatalf("panelview.ended debía llevar el call_id real del ojo, got %+v", data["call_id"])
	}
}

func TestPanelViewHangsUpOnDoorcallHangupRequest(t *testing.T) {
	bus := events.NewEventBus(2, logrusTestLogger())
	orig := &fakeEyeOrig{answer: fakeEyeAnswer}
	fwd := &fakeFwd{}
	pv := newPV(orig, fwd, &fakeMarker{}, bus, cfgEnabled())
	_ = pv.Start()
	if err := pv.Show(); err != nil {
		t.Fatal(err)
	}
	bus.Publish("doorcall.hangup.request", nil) // botón Colgar existente
	waitFor(t, func() bool { return orig.hangups.Load() == 1 && fwd.stopped.Load() == 1 }, "el botón Colgar debía cerrar el ojo")
}

// Un answer inválido (p.ej. el 200 SIN SDP del BYE anterior que se colaba en
// 2026-10-13) debe dejar rastro a nivel Warn: Show() se invoca desde el bus con
// el error descartado, así que sin este log el BYE inmediato era invisible.
func TestPanelViewAnswerInvalidoSeLoguea(t *testing.T) {
	bus := events.NewEventBus(2, logrusTestLogger())
	orig := &fakeEyeOrig{answer: "v=0\r\n"}
	logger, hook := logrustest.NewNullLogger()
	pv := NewPanelView(cfgEnabled(), orig, &fakeFwd{}, nil, &fakeMarker{}, bus, "127.0.0.1", 10102, 10104, logger)

	if err := pv.Show(); err == nil {
		t.Fatal("esperaba error de parse")
	}
	if orig.hangups.Load() != 1 {
		t.Fatalf("hangups=%d, esperaba 1 (no dejar diálogo huérfano)", orig.hangups.Load())
	}
	var found bool
	for _, e := range hook.AllEntries() {
		if e.Level <= logrus.WarnLevel && strings.Contains(e.Message, "answer") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no hay log Warn del answer inválido; entradas: %d", len(hook.AllEntries()))
	}
	if pv.Active() {
		t.Fatal("el ojo debe quedar liberado tras el fallo")
	}
}

// Si el nativo cuelga el ojo (sip.dialog.ended con SU call_id), PanelView desmonta:
// para el forwarder y publica panelview.ended (el sensor HA vuelve a idle).
// Un dialog.ended de OTRO diálogo no toca el ojo.
func TestPanelViewDesmontaConBYERemoto(t *testing.T) {
	bus := events.NewEventBus(2, logrusTestLogger())
	gotEnded := subscribeOnce(bus, "panelview.ended")
	orig := &fakeEyeOrig{answer: fakeEyeAnswer}
	fwd := &fakeFwd{}
	pv := newPV(orig, fwd, &fakeMarker{}, bus, cfgEnabled())
	_ = pv.Start()
	defer func() { _ = pv.Hangup() }()
	if err := pv.Show(); err != nil {
		t.Fatalf("Show: %v", err)
	}

	bus.Publish("sip.dialog.ended", map[string]interface{}{"call_id": "otro-dialogo", "reason": "remote_bye"})
	time.Sleep(100 * time.Millisecond)
	if !pv.Active() {
		t.Fatal("un dialog.ended ajeno no debe cerrar el ojo")
	}

	bus.Publish("sip.dialog.ended", map[string]interface{}{"call_id": "eye-call-1", "reason": "remote_bye"})
	select {
	case e := <-gotEnded:
		if d := e.Data.(map[string]interface{}); d["reason"] != "remote_bye" {
			t.Fatalf("reason=%v, esperaba remote_bye", d["reason"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no se publicó panelview.ended tras el BYE remoto")
	}
	if pv.Active() {
		t.Fatal("el ojo debe quedar cerrado")
	}
	if fwd.stopped.Load() < 1 {
		t.Fatal("el forwarder de OÍR debe pararse")
	}
}

// El OÍR del ojo va a la entrada PRIORITARIA del relay de audio (no a la 10000 que
// comparte con el tap mudo de lo:5000): así es lo que se oye en HA.
func TestPanelViewOIRVaALaEntradaPrioritaria(t *testing.T) {
	bus := events.NewEventBus(2, logrusTestLogger())
	fwd := &fakeFwd{}
	pv := newPV(&fakeEyeOrig{answer: fakeEyeAnswer}, fwd, &fakeMarker{}, bus, cfgEnabled())
	defer func() { _ = pv.Hangup() }()
	if err := pv.Show(); err != nil {
		t.Fatalf("Show: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for fwd.started.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got, want := fwd.dst.Load(), fmt.Sprintf("127.0.0.1:%d", EyeAudioRelayPort); got != want {
		t.Fatalf("OÍR reenvía a %v, esperaba %s", got, want)
	}
}

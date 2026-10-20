package sip

import (
	"errors"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"

	"bticino_bridge/pkg/events"
)

// Say debe colgar al terminar: si no, el diálogo queda vivo hasta que el nativo
// lo corta y el vigilante retiene el socket webrtc (re-REGISTER aplazado).
func TestAudioCallSayCuelgaAlTerminar(t *testing.T) {
	orig := &fakeOriginator{answer: goodAnswer}
	ac := NewAudioCall(orig, &fakeOWN{}, &fakePlayer{}, &fakeCapturer{}, "127.0.0.1", 40100, logrus.New())
	if err := ac.Say("/tmp/hola.wav"); err != nil {
		t.Fatalf("Say: %v", err)
	}
	if !orig.hungUp {
		t.Fatal("Say debe colgar (BYE) al terminar de reproducir")
	}
}

// Con un answer inválido el INVITE ya llegó a 200: hay que colgar (no dejar
// un diálogo huérfano en el device).
func TestAudioCallSayCuelgaSiAnswerInvalido(t *testing.T) {
	orig := &fakeOriginator{answer: "m=audio 7078 RTP/SAVP 98\r\na=rtpmap:98 speex/8000\r\n"}
	ac := NewAudioCall(orig, &fakeOWN{}, &fakePlayer{}, &fakeCapturer{}, "127.0.0.1", 40100, logrus.New())
	if err := ac.Say("/tmp/hola.wav"); err == nil {
		t.Fatal("esperaba error")
	}
	if !orig.hungUp {
		t.Fatal("debe colgar el diálogo si el answer es inválido")
	}
}

// Una sola pata de audio: Say con otra pata activa → ErrAudioLegBusy.
func TestAudioCallSayRespetaPataUnica(t *testing.T) {
	ac := NewAudioCall(&fakeOriginator{answer: goodAnswer}, &fakeOWN{}, &fakePlayer{}, &fakeCapturer{}, "127.0.0.1", 40100, logrus.New())
	ac.mu.Lock()
	ac.active = true
	ac.mu.Unlock()
	if err := ac.Say("/tmp/hola.wav"); !errors.Is(err, ErrAudioLegBusy) {
		t.Fatalf("esperaba ErrAudioLegBusy, got %v", err)
	}
}

// Solo los INVITE de InviteWithSDP (ojo, AudioCall) van al nativo; el self-INVITE
// de vídeo de MakeCall (video_on_demand) lo debe auto-contestar NUESTRO listener.
func TestShouldAutoAnswerSoloDesviaLasLlamadasAlNativo(t *testing.T) {
	c := &BTicinoSIPClient{originatedCalls: map[string]time.Time{}, nativeCalls: map[string]time.Time{}}
	c.originatedCalls["selfvideo@127.0.0.1"] = time.Now() // MakeCall
	c.originatedCalls["ojo@127.0.0.1"] = time.Now()       // InviteWithSDP
	c.nativeCalls["ojo@127.0.0.1"] = time.Now()
	if !c.shouldAutoAnswer("selfvideo@127.0.0.1") {
		t.Fatal("el self-INVITE de vídeo (MakeCall) debe auto-contestarse en loopback")
	}
	if c.shouldAutoAnswer("ojo@127.0.0.1") {
		t.Fatal("el INVITE del ojo debe ir al nativo, no auto-contestarse")
	}
}

// Pulsar "Ver placa" con un ojo YA abierto → ErrEyeBusy con log claro, y SIN
// publicar panelview.rejected (pondría el sensor HA en idle con el ojo abierto).
func TestPanelViewPulsarConOjoActivo(t *testing.T) {
	bus := events.NewEventBus(2, logrusTestLogger())
	rejected := subscribeOnce(bus, "panelview.rejected")
	logger, hook := logrustest.NewNullLogger()
	marker := &fakeMarker{}
	pv := NewPanelView(cfgEnabled(), &fakeEyeOrig{answer: fakeEyeAnswer}, &fakeFwd{}, nil, marker, bus, "127.0.0.1", 10102, 10104, logger)
	defer func() { _ = pv.Hangup() }()
	if err := pv.Show(); err != nil {
		t.Fatalf("Show: %v", err)
	}
	marker.active = true // el propio ojo enciende el canal nativo (callActive)
	if err := pv.Show(); !errors.Is(err, ErrEyeBusy) {
		t.Fatalf("esperaba ErrEyeBusy, got %v", err)
	}
	select {
	case <-rejected:
		t.Fatal("no debe publicarse panelview.rejected con el ojo abierto")
	case <-time.After(150 * time.Millisecond):
	}
	for _, e := range hook.AllEntries() {
		if e.Level <= logrus.WarnLevel {
			t.Fatalf("log engañoso a nivel %s: %q", e.Level, e.Message)
		}
	}
}

package sip

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

type fakeOriginator struct {
	gotTarget, gotSDP string
	answer            string
	err               error
	hungUp            bool

	// hungUpCh, si no es nil, se cierra (una sola vez) cuando Hangup() se
	// invoca. Igual que fakeCapturer.done: sincroniza con el test sin
	// carreras cuando Hangup() lo dispara una goroutine en segundo plano
	// (el cierre automático de la pata al fin natural del receiver).
	hungUpCh   chan struct{}
	hungUpOnce sync.Once
}

func (f *fakeOriginator) InviteWithSDP(target, sdp string) (string, error) {
	f.gotTarget, f.gotSDP = target, sdp
	return f.answer, f.err
}

func (f *fakeOriginator) Hangup() error {
	f.hungUp = true
	if f.hungUpCh != nil {
		f.hungUpOnce.Do(func() { close(f.hungUpCh) })
	}
	return nil
}

type fakeCapturer struct {
	captured  bool
	localPort int
	sinkPath  string
	ans       AudioAnswer
	stopped   bool

	// block, si no es nil, Capture() bloquea en él hasta que se cierre —
	// simula el proceso ffmpeg de captura siguiendo vivo (como en producción,
	// donde Capture() no vuelve hasta que ffmpeg termina). Stop() lo cierra
	// (una sola vez, vía stopOnce), igual que en la implementación real
	// Stop() mata el proceso y eso hace que la llamada bloqueante Capture()
	// retorne. Si es nil, Capture() retorna de inmediato tras registrar los
	// parámetros — simula el fin natural del receiver (timeout duro de 60s o
	// ffmpeg muerto por su cuenta), el escenario del Finding 1.
	block    chan struct{}
	stopOnce sync.Once

	// done, si no es nil, se cierra justo antes de que Capture() retorne.
	// OpenDuplex lanza Capture en una goroutine: el test debe esperar en
	// este canal (con timeout) ANTES de leer los campos de arriba, o el
	// race detector marca una lectura/escritura sin sincronizar entre la
	// goroutine y el test.
	done chan struct{}
}

func (f *fakeCapturer) Capture(localHost string, localPort int, sinkPath string, ans AudioAnswer) error {
	f.captured, f.localPort, f.sinkPath, f.ans = true, localPort, sinkPath, ans
	if f.block != nil {
		<-f.block
	}
	if f.done != nil {
		close(f.done)
	}
	return nil
}
func (f *fakeCapturer) Stop() {
	f.stopped = true
	if f.block != nil {
		f.stopOnce.Do(func() { close(f.block) })
	}
}

type fakeOWN struct{ cmds []string }

func (f *fakeOWN) SendOpenWebNetCommand(cmd string) error { f.cmds = append(f.cmds, cmd); return nil }

type fakePlayer struct {
	played bool
	host   string
	ans    AudioAnswer

	// done: mismo propósito que en fakeCapturer, para el sender de OpenDuplex.
	done chan struct{}
}

func (f *fakePlayer) Play(wav, host string, ans AudioAnswer) error {
	f.played, f.host, f.ans = true, host, ans
	if f.done != nil {
		close(f.done)
	}
	return nil
}
func (f *fakePlayer) Stop() {}

const goodAnswer = "m=audio 7078 RTP/SAVP 98\r\na=rtpmap:98 speex/8000\r\na=crypto:2 AES_CM_128_HMAC_SHA1_80 inline:KEYXYZ\r\n"

func TestAudioCallSayHappyPath(t *testing.T) {
	orig := &fakeOriginator{answer: goodAnswer}
	own := &fakeOWN{}
	pl := &fakePlayer{}
	ac := NewAudioCall(orig, own, pl, &fakeCapturer{}, "127.0.0.1", 40100, logrus.New())

	if err := ac.Say("/tmp/hola.wav"); err != nil {
		t.Fatalf("Say: %v", err)
	}
	// invitó a c300x con un SDP de audio real
	if orig.gotTarget != "c300x" {
		t.Fatalf("target: %q", orig.gotTarget)
	}
	if !contains(orig.gotSDP, "m=audio 40100 RTP/SAVP 98") || !contains(orig.gotSDP, "AES_CM_128_HMAC_SHA1_80") {
		t.Fatalf("SDP offer incorrecto:\n%s", orig.gotSDP)
	}
	// armó el audio con WHO=8
	if len(own.cmds) == 0 || own.cmds[0] != "*8*2#5#4*16##" {
		t.Fatalf("no armó WHO=8: %v", own.cmds)
	}
	// reprodujo al puerto+clave del portero
	if !pl.played || pl.ans.Port != 7078 || pl.ans.CryptoKey != "KEYXYZ" {
		t.Fatalf("player mal invocado: %+v", pl)
	}
}

func TestAudioCallSayInviteError(t *testing.T) {
	ac := NewAudioCall(&fakeOriginator{err: errors.New("403")}, &fakeOWN{}, &fakePlayer{}, &fakeCapturer{}, "127.0.0.1", 40100, logrus.New())
	if err := ac.Say("/tmp/hola.wav"); err == nil {
		t.Fatal("esperaba error si el INVITE falla")
	}
}

func TestAudioCallSayBadAnswer(t *testing.T) {
	// answer sin crypto soportada → error, y NO debe reproducir.
	pl := &fakePlayer{}
	ac := NewAudioCall(&fakeOriginator{answer: "m=audio 7078 RTP/SAVP 98\r\na=rtpmap:98 speex/8000\r\n"}, &fakeOWN{}, pl, &fakeCapturer{}, "127.0.0.1", 40100, logrus.New())
	if err := ac.Say("/tmp/hola.wav"); err == nil {
		t.Fatal("esperaba error sin crypto soportada")
	}
	if pl.played {
		t.Fatal("no debe reproducir si el answer es inválido")
	}
}

func TestAudioCallOpenDuplexHappyPath(t *testing.T) {
	orig := &fakeOriginator{answer: goodAnswer}
	own := &fakeOWN{}
	pl := &fakePlayer{done: make(chan struct{})}
	cap := &fakeCapturer{done: make(chan struct{})}
	ac := NewAudioCall(orig, own, pl, cap, "127.0.0.1", 40100, logrus.New())

	ans, err := ac.OpenDuplex("/tmp/door.wav", "/tmp/hola.wav")
	if err != nil {
		t.Fatalf("OpenDuplex: %v", err)
	}
	// OpenDuplex lanza receiver y sender en goroutines aparte sin esperarlas;
	// hay que sincronizar con su finalización ANTES de leer los campos de los
	// fakes (si no, hay lectura/escritura sin sincronizar → -race la marca).
	select {
	case <-cap.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout esperando a que el receiver (capturer) termine")
	}
	select {
	case <-pl.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout esperando a que el sender (player) termine")
	}
	if orig.gotTarget != "c300x" || !contains(orig.gotSDP, "m=audio 40100 RTP/SAVP 98") {
		t.Fatalf("offer mal: %q / %s", orig.gotTarget, orig.gotSDP)
	}
	if len(own.cmds) == 0 || own.cmds[0] != "*8*2#5#4*16##" {
		t.Fatalf("no armó WHO=8: %v", own.cmds)
	}
	if !cap.captured || cap.localPort != 40100 || cap.sinkPath != "/tmp/door.wav" || cap.ans.CryptoKey != "KEYXYZ" {
		t.Fatalf("capturer mal: %+v", cap)
	}
	if !pl.played || pl.ans.Port != 7078 {
		t.Fatalf("player mal: %+v", pl)
	}
	if ans.Port != 7078 {
		t.Fatalf("answer devuelto mal: %+v", ans)
	}
}

func TestAudioCallOpenDuplexBusy(t *testing.T) {
	// block: el receiver no debe autocerrar la pata durante el test (si
	// Capture() retornara de inmediato, el cierre automático del Finding 1
	// podría ganarle la carrera al segundo OpenDuplex y dejar de ver la pata
	// ocupada — falso negativo intermitente). Bloqueando, la pata se
	// mantiene activa de forma determinista mientras dura el test.
	cap := &fakeCapturer{block: make(chan struct{})}
	ac := NewAudioCall(&fakeOriginator{answer: goodAnswer}, &fakeOWN{}, &fakePlayer{}, cap, "127.0.0.1", 40100, logrus.New())
	t.Cleanup(func() { _ = ac.Close() })
	if _, err := ac.OpenDuplex("/tmp/a.wav", ""); err != nil {
		t.Fatalf("primera: %v", err)
	}
	if _, err := ac.OpenDuplex("/tmp/b.wav", ""); err != ErrAudioLegBusy {
		t.Fatalf("esperaba ErrAudioLegBusy, got %v", err)
	}
}

func TestAudioCallCloseHangsUp(t *testing.T) {
	orig := &fakeOriginator{answer: goodAnswer}
	// block: el receiver se mantiene "vivo" (como ffmpeg corriendo) hasta
	// que Close() llame a capturer.Stop() explícitamente — así el Close()
	// del test es siempre quien hace el trabajo de verdad (Hangup +
	// capturer.Stop) en su propia goroutine, sin carrera con el cierre
	// automático del receiver (que solo se dispara cuando block se cierra).
	cap := &fakeCapturer{block: make(chan struct{})}
	ac := NewAudioCall(orig, &fakeOWN{}, &fakePlayer{}, cap, "127.0.0.1", 40100, logrus.New())
	if _, err := ac.OpenDuplex("/tmp/a.wav", ""); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := ac.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !orig.hungUp || !cap.stopped {
		t.Fatalf("Close debe Hangup y parar capturer: hungUp=%v stopped=%v", orig.hungUp, cap.stopped)
	}
	if ac.IsActive() {
		t.Fatal("IsActive debe ser false tras Close")
	}
	// idempotente
	if err := ac.Close(); err != nil {
		t.Fatalf("Close idempotente: %v", err)
	}
}

// TestAudioCallReceiverNaturalExitTearsDownLeg cubre el Finding 1: si el
// receiver retorna por sí solo (timeout duro de 60s en Capture, o ffmpeg
// muerto por su cuenta) SIN pasar por /stop, la pata debe liberarse igual
// (active=false, BYE enviado) — si no, queda encallada para siempre y todo
// OpenDuplex posterior devuelve ErrAudioLegBusy.
func TestAudioCallReceiverNaturalExitTearsDownLeg(t *testing.T) {
	orig := &fakeOriginator{answer: goodAnswer, hungUpCh: make(chan struct{})}
	// cap sin block: Capture() retorna de inmediato tras registrar los
	// parámetros, simulando el fin natural del receiver (no hay /stop).
	cap := &fakeCapturer{}
	ac := NewAudioCall(orig, &fakeOWN{}, &fakePlayer{}, cap, "127.0.0.1", 40100, logrus.New())

	if _, err := ac.OpenDuplex("/tmp/a.wav", ""); err != nil {
		t.Fatalf("open: %v", err)
	}

	// Sincroniza con el cierre automático (Hangup es lo último que hace
	// Close() antes de retornar) en vez de leer los campos directamente,
	// para que -race no marque una lectura sin sincronizar con la goroutine
	// del receiver.
	select {
	case <-orig.hungUpCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout esperando el BYE automático tras el fin natural del receiver")
	}

	if !orig.hungUp {
		t.Fatal("el fin natural del receiver debe colgar la llamada (BYE)")
	}
	if !cap.stopped {
		t.Fatal("el fin natural del receiver debe parar también el player/capturer (vía Close)")
	}
	if ac.IsActive() {
		t.Fatal("la pata debe quedar inactiva tras el fin natural del receiver (si no, se encalla)")
	}
	// Una siguiente OpenDuplex NO debe devolver ErrAudioLegBusy: la pata se
	// liberó sola, sin necesidad de /stop.
	if _, err := ac.OpenDuplex("/tmp/b.wav", ""); err != nil {
		t.Fatalf("esperaba poder reabrir la pata tras el fin natural del receiver, got %v", err)
	}
}

func TestAudioCallOpenDuplexInviteError(t *testing.T) {
	cap := &fakeCapturer{}
	ac := NewAudioCall(&fakeOriginator{err: errors.New("403")}, &fakeOWN{}, &fakePlayer{}, cap, "127.0.0.1", 40100, logrus.New())
	if _, err := ac.OpenDuplex("/tmp/a.wav", ""); err == nil {
		t.Fatal("esperaba error si el INVITE falla")
	}
	if cap.captured {
		t.Fatal("no debe capturar si el INVITE falla")
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

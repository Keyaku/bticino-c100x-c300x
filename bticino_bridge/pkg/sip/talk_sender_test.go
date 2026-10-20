package sip

import (
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"bticino_bridge/pkg/config"
	"bticino_bridge/pkg/events"
)

// fakeProc y fakeRunner registran el lanzamiento y controlan Kill/Wait.
type fakeProc struct {
	mu     sync.Mutex
	killed bool
	waitCh chan struct{}
}

func newFakeProc() *fakeProc { return &fakeProc{waitCh: make(chan struct{})} }
func (p *fakeProc) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.killed {
		p.killed = true
		close(p.waitCh)
	}
	return nil
}
func (p *fakeProc) Wait() error { <-p.waitCh; return nil }
func (p *fakeProc) isKilled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killed
}

type fakeRunner struct {
	mu       sync.Mutex
	name     string
	args     []string
	procs    []*fakeProc
	starts   int
	failNext bool
}

func (r *fakeRunner) Start(name string, args []string) (Proc, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
	if r.failNext {
		r.failNext = false
		return nil, errTransportClosed // cualquier error
	}
	r.name = name
	r.args = append([]string{}, args...)
	p := newFakeProc()
	r.procs = append(r.procs, p)
	return p, nil
}
func (r *fakeRunner) lastArgs() []string { r.mu.Lock(); defer r.mu.Unlock(); return r.args }
func (r *fakeRunner) startCount() int    { r.mu.Lock(); defer r.mu.Unlock(); return r.starts }
func (r *fakeRunner) setFailNext(v bool) { r.mu.Lock(); defer r.mu.Unlock(); r.failNext = v }

func newTalkSenderTest(runner ProcRunner, bus events.EventBus, enabled bool) *TalkSender {
	cfg := config.AnswererConfig{Enabled: true, TalkEnabled: enabled, AudioPort: 10100}
	return NewTalkSender(cfg, runner, bus, logrus.New(), "ffmpeg", "127.0.0.1", 40004, 97)
}

func answeredEvent(bus events.EventBus, port int, key string) {
	bus.Publish("doorcall.answered", map[string]interface{}{
		"call_id": "c1", "media_host": "127.0.0.1", "media_port": port, "srtp_key": key,
	})
}

func TestTalkSender_LanzaFFmpegAlContestar(t *testing.T) {
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	r := &fakeRunner{}
	s := newTalkSenderTest(r, bus, true)
	s.Start()
	defer s.Stop()
	answeredEvent(bus, 7078, "KEYb64==")
	waitFor2(t, func() bool { return r.startCount() == 1 }, "ffmpeg lanzado")
	args := strings.Join(r.lastArgs(), " ")
	for _, want := range []string{
		"-payload_type 96",
		"-srtp_out_suite AES_CM_128_HMAC_SHA1_80",
		"-srtp_out_params KEYb64==",
		"srtp://127.0.0.1:7078",
		"-i", // seguido de la ruta del SDP temporal
	} {
		if !strings.Contains(args, want) {
			t.Errorf("args ffmpeg no contienen %q\n%s", want, args)
		}
	}
}

func TestTalkSender_MataFFmpegAlColgar(t *testing.T) {
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	r := &fakeRunner{}
	s := newTalkSenderTest(r, bus, true)
	s.Start()
	defer s.Stop()
	answeredEvent(bus, 7078, "K==")
	waitFor2(t, func() bool { return r.startCount() == 1 }, "lanzado")
	bus.Publish("doorcall.ended", map[string]interface{}{"reason": "hangup_local"})
	waitFor2(t, func() bool { return len(r.procs) == 1 && r.procs[0].isKilled() }, "matado al colgar")
}

// TestTalkSender_EndedAntesQueAnswered_MismoCallID_NoLanza reproduce la carrera de reordenamiento
// del EventBus: si el doorcall.ended de una llamada llega ANTES que su propio doorcall.answered
// (p.ej. el visitante cuelga justo tras contestar), no debe lanzarse un ffmpeg huérfano que
// ningún ended posterior mataría.
func TestTalkSender_EndedAntesQueAnswered_MismoCallID_NoLanza(t *testing.T) {
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	r := &fakeRunner{}
	s := newTalkSenderTest(r, bus, true)
	s.Start()
	defer s.Stop()

	bus.Publish("doorcall.ended", map[string]interface{}{"reason": "hangup_remote", "call_id": "c1"})
	// Holgura para que onEnded se procese (registre lastEndedCallID) ANTES del answered.
	time.Sleep(50 * time.Millisecond)
	bus.Publish("doorcall.answered", map[string]interface{}{
		"call_id": "c1", "media_host": "127.0.0.1", "media_port": 7078, "srtp_key": "K==",
	})
	time.Sleep(80 * time.Millisecond)
	if r.startCount() != 0 {
		t.Fatal("ended antes que answered del MISMO call_id: NO debe lanzar ffmpeg (huérfano)")
	}
}

// TestTalkSender_EndedCallIDDistinto_SiLanza confirma que la correlación por call_id no bloquea
// falsamente una llamada NUEVA (call_id distinto) que llega tras el ended de una anterior.
func TestTalkSender_EndedCallIDDistinto_SiLanza(t *testing.T) {
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	r := &fakeRunner{}
	s := newTalkSenderTest(r, bus, true)
	s.Start()
	defer s.Stop()

	bus.Publish("doorcall.ended", map[string]interface{}{"reason": "hangup_remote", "call_id": "c1"})
	time.Sleep(50 * time.Millisecond)
	bus.Publish("doorcall.answered", map[string]interface{}{
		"call_id": "c2", "media_host": "127.0.0.1", "media_port": 7078, "srtp_key": "K==",
	})
	waitFor2(t, func() bool { return r.startCount() == 1 }, "llamada nueva (call_id distinto) debe lanzar ffmpeg")
}

func TestTalkSender_DeshabilitadoNoLanza(t *testing.T) {
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	r := &fakeRunner{}
	s := newTalkSenderTest(r, bus, false /* talk_enabled=false */)
	s.Start()
	defer s.Stop()
	answeredEvent(bus, 7078, "K==")
	time.Sleep(80 * time.Millisecond)
	if r.startCount() != 0 {
		t.Fatal("con talk_enabled=false NO debe lanzar ffmpeg")
	}
}

func TestTalkSender_PuertoInvalidoNoLanza(t *testing.T) {
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	r := &fakeRunner{}
	s := newTalkSenderTest(r, bus, true)
	s.Start()
	defer s.Stop()
	answeredEvent(bus, 0, "K==") // media_port=0 (offer sin audio parseable)
	time.Sleep(80 * time.Millisecond)
	if r.startCount() != 0 {
		t.Fatal("con media_port=0 NO debe lanzar ffmpeg")
	}
}

func TestTalkSender_FalloDeLanzamientoNoTumba(t *testing.T) {
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	r := &fakeRunner{failNext: true}
	s := newTalkSenderTest(r, bus, true)
	s.Start()
	defer s.Stop()
	answeredEvent(bus, 7078, "K==") // el runner falla: no debe panicar ni bloquear
	time.Sleep(80 * time.Millisecond)
	// Sigue vivo y aceptando otro answered después.
	r.setFailNext(false)
	answeredEvent(bus, 7078, "K==")
	waitFor2(t, func() bool { return r.startCount() == 2 }, "reintento tras fallo")
}

// freeUDPPort devuelve un puerto UDP libre en loopback (para no chocar con 40004 real).
func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	p := c.LocalAddr().(*net.UDPAddr).Port
	c.Close()
	return p
}

// sdpDeArgs lee el SDP temporal que se pasó a ffmpeg tras "-i".
func sdpDeArgs(t *testing.T, args []string) string {
	t.Helper()
	for i, a := range args {
		if a == "-i" && i+1 < len(args) {
			b, err := os.ReadFile(args[i+1])
			if err != nil {
				t.Fatalf("no se pudo leer el SDP %s: %v", args[i+1], err)
			}
			return string(b)
		}
	}
	t.Fatal("args sin -i")
	return ""
}

func newTalkSenderKeepalive(runner ProcRunner, bus events.EventBus, keepalive bool, bcPort int) *TalkSender {
	cfg := config.AnswererConfig{Enabled: true, TalkEnabled: true, TalkKeepalive: keepalive, AudioPort: 10100}
	return NewTalkSender(cfg, runner, bus, logrus.New(), "ffmpeg", "127.0.0.1", bcPort, 97)
}

// Sin talk_keepalive: ffmpeg lee directamente el backchannel de go2rtc (comportamiento actual).
func TestTalkSender_SinKeepalive_SDPApuntaAlBackchannel(t *testing.T) {
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	r := &fakeRunner{}
	bc := freeUDPPort(t)
	s := newTalkSenderKeepalive(r, bus, false, bc)
	s.Start()
	defer s.Stop()
	answeredEvent(bus, 7078, "K==")
	waitFor2(t, func() bool { return r.startCount() == 1 }, "ffmpeg lanzado")
	if sdp := sdpDeArgs(t, r.lastArgs()); !strings.Contains(sdp, fmt.Sprintf("m=audio %d ", bc)) {
		t.Fatalf("sin keepalive el SDP debe apuntar al backchannel %d:\n%s", bc, sdp)
	}
}

// Con talk_keepalive: el relé escucha el backchannel y ffmpeg lee la salida del relé (bc+2).
func TestTalkSender_Keepalive_SDPApuntaAlRele(t *testing.T) {
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	r := &fakeRunner{}
	bc := freeUDPPort(t)
	s := newTalkSenderKeepalive(r, bus, true, bc)
	s.Start()
	defer s.Stop()
	answeredEvent(bus, 7078, "K==")
	waitFor2(t, func() bool { return r.startCount() == 1 }, "ffmpeg lanzado")
	if sdp := sdpDeArgs(t, r.lastArgs()); !strings.Contains(sdp, fmt.Sprintf("m=audio %d ", bc+2)) {
		t.Fatalf("con keepalive el SDP debe apuntar a la salida del relé %d:\n%s", bc+2, sdp)
	}
	// El relé ocupa el puerto del backchannel mientras dura la llamada.
	if c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: bc}); err == nil {
		c.Close()
		t.Fatalf("el relé debería estar escuchando en %d", bc)
	}
}

// Al colgar se para el relé y se libera el puerto del backchannel.
func TestTalkSender_Keepalive_ColgarParaRele(t *testing.T) {
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	r := &fakeRunner{}
	bc := freeUDPPort(t)
	s := newTalkSenderKeepalive(r, bus, true, bc)
	s.Start()
	defer s.Stop()
	answeredEvent(bus, 7078, "K==")
	waitFor2(t, func() bool { return r.startCount() == 1 }, "ffmpeg lanzado")
	bus.Publish("doorcall.ended", map[string]interface{}{"call_id": "c1", "reason": "hangup_local"})
	waitFor2(t, func() bool {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: bc})
		if err != nil {
			return false
		}
		c.Close()
		return true
	}, "puerto del backchannel liberado al colgar")
}

// waitFor2 sondea hasta 3s (holgura bajo -race).
func waitFor2(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout esperando: %s", what)
}

// TestTalkSender_ArmaConPanelViewConnected: en modo ojo (Ver placa) el HABLAR arma con
// panelview.connected y se desarma con panelview.ended (mismo payload que doorcall.*).
func TestTalkSender_ArmaConPanelViewConnected(t *testing.T) {
	bus := events.NewEventBus(2, logrus.New())
	defer bus.Close()
	r := &fakeRunner{}
	s := newTalkSenderTest(r, bus, true)
	s.Start()
	defer s.Stop()
	bus.Publish("panelview.connected", map[string]interface{}{
		"call_id": "eye1", "media_host": "127.0.0.1", "media_port": 5000, "srtp_key": "QUJDRA==",
	})
	waitFor2(t, func() bool { return r.startCount() == 1 }, "ffmpeg lanzado con panelview.connected")
	bus.Publish("panelview.ended", map[string]interface{}{"call_id": "eye1"})
	waitFor2(t, func() bool { return len(r.procs) == 1 && r.procs[0].isKilled() }, "matado con panelview.ended")
}

// TestTalkSender_PanelViewMediaPT: el PT negociado (media_pt) se enhebra a -payload_type;
// sin media_pt (doorcall) sigue siendo 96.
func TestTalkSender_PanelViewMediaPT(t *testing.T) {
	for _, tc := range []struct {
		name string
		data map[string]interface{}
		want string
	}{
		{"ojo98", map[string]interface{}{"call_id": "e", "media_port": 5000, "srtp_key": "QUJDRA==", "media_pt": 98}, "-payload_type 98"},
		{"doorcall96", map[string]interface{}{"call_id": "d", "media_port": 5000, "srtp_key": "QUJDRA=="}, "-payload_type 96"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bus := events.NewEventBus(2, logrus.New())
			defer bus.Close()
			r := &fakeRunner{}
			s := newTalkSenderTest(r, bus, true)
			s.Start()
			defer s.Stop()
			topic := "panelview.connected"
			if tc.name == "doorcall96" {
				topic = "doorcall.answered"
			}
			bus.Publish(topic, tc.data)
			waitFor2(t, func() bool { return r.startCount() == 1 }, "ffmpeg lanzado")
			if args := strings.Join(r.lastArgs(), " "); !strings.Contains(args, tc.want) {
				t.Errorf("args sin %q: %s", tc.want, args)
			}
		})
	}
}

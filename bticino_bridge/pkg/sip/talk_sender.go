package sip

import (
	"fmt"
	"os"
	"sync"

	"github.com/sirupsen/logrus"

	"bticino_bridge/pkg/config"
	"bticino_bridge/pkg/events"
)

// Proc abstrae un proceso externo en marcha (ffmpeg real o un fake en tests).
type Proc interface {
	Kill() error
	Wait() error
}

// ProcRunner abstrae el lanzamiento de un proceso externo, inyectable para tests offline.
type ProcRunner interface {
	Start(name string, args []string) (Proc, error)
}

// TalkSender lanza/mata un ffmpeg que envía el audio-mic de HA (backchannel local en
// bcHost:bcPort) hacia la placa como SRTP, dirigido por doorcall.answered/doorcall.ended.
// Un fallo del runner NUNCA debe tumbar la llamada: se loguea y se ignora.
type TalkSender struct {
	cfg        config.AnswererConfig
	runner     ProcRunner
	bus        events.EventBus
	logger     *logrus.Logger
	ffmpegPath string
	bcHost     string
	bcPort     int
	bcPT       int

	mu              sync.Mutex
	proc            Proc
	keepalive       *upstreamKeepalive // relé de subida continua (solo con cfg.TalkKeepalive)
	sdpPath         string
	stopped         bool
	lastEndedCallID string // último call_id visto en doorcall.ended; evita el ffmpeg huérfano si
	// el ended llega ANTES que el answered del mismo call-id (orden de goroutines del EventBus).
}

// NewTalkSender construye el sender. backchannelHost/Port/PT describen dónde ffmpeg debe
// escuchar el audio-mic local (el extremo de go2rtc), no el destino SRTP hacia la placa.
func NewTalkSender(cfg config.AnswererConfig, runner ProcRunner, bus events.EventBus, logger *logrus.Logger, ffmpegPath, backchannelHost string, backchannelPort, backchannelPT int) *TalkSender {
	return &TalkSender{
		cfg:        cfg,
		runner:     runner,
		bus:        bus,
		logger:     logger,
		ffmpegPath: ffmpegPath,
		bcHost:     backchannelHost,
		bcPort:     backchannelPort,
		bcPT:       backchannelPT,
	}
}

// Start se suscribe a doorcall.answered (lanza ffmpeg) y doorcall.ended (lo mata).
func (s *TalkSender) Start() error {
	s.bus.Subscribe("doorcall.answered", func(e *events.Event) { s.onAnswered(e) })
	s.bus.Subscribe("doorcall.ended", func(e *events.Event) { s.onEnded(e) })
	// Modo ojo (Ver placa): mismo payload que doorcall.*, se reusan los handlers.
	s.bus.Subscribe("panelview.connected", func(e *events.Event) { s.onAnswered(e) })
	s.bus.Subscribe("panelview.ended", func(e *events.Event) { s.onEnded(e) })
	return nil
}

// onAnswered arranca el ffmpeg de talk-back si TalkEnabled y el payload trae media_port/srtp_key
// válidos. Cualquier fallo (temporal, runner) se loguea y se ignora: la llamada sigue sin hablar.
func (s *TalkSender) onAnswered(e *events.Event) {
	if !s.cfg.TalkEnabled {
		return
	}
	data, _ := e.Data.(map[string]interface{})
	port, _ := data["media_port"].(int)
	key, _ := data["srtp_key"].(string)
	host, _ := data["media_host"].(string)
	callID, _ := data["call_id"].(string)
	// media_pt es opcional: solo lo envía el ojo (PanelView). Doorcall no lo trae → 96.
	pt, _ := data["media_pt"].(int)
	if pt <= 0 {
		pt = 96
	}
	if host == "" {
		host = "127.0.0.1"
	}
	if port <= 0 || key == "" {
		s.logger.Warnf("[TALK] doorcall.answered sin media_port/srtp_key válidos: no se habla")
		return
	}

	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	// Carrera de reordenamiento del EventBus: si el doorcall.ended de ESTA misma llamada ya llegó
	// (goroutine de onEnded ganó la carrera al mutex antes que esta), no lanzar un ffmpeg huérfano
	// que ningún ended posterior matará. Una llamada NUEVA tiene otro call-id y no se ve afectada.
	if callID != "" && callID == s.lastEndedCallID {
		s.mu.Unlock()
		s.logger.Infof("[TALK] la llamada %s ya había terminado; no se lanza talk-back", callID)
		return
	}
	if s.proc != nil { // por si acaso: matar un sender previo (no debería pasar, una llamada a la vez)
		s.killLocked()
	}

	// Con talk_keepalive, un relé escucha el backchannel de go2rtc y emite un flujo continuo
	// (voz o silencio) a bcPort+2, que es lo que lee ffmpeg. Si el relé no arranca, se cae al
	// comportamiento clásico (ffmpeg lee el backchannel directo): mejor hablar sin keepalive
	// que no hablar.
	sdpPort := s.bcPort
	if s.cfg.TalkKeepalive {
		k, kerr := startUpstreamKeepalive(s.bcHost, s.bcPort, s.bcPort+2, s.bcPT, s.logger)
		if kerr != nil {
			s.logger.Warnf("[TALK] keepalive no disponible (%v): talk-back sin subida continua", kerr)
		} else {
			s.keepalive = k
			sdpPort = s.bcPort + 2
		}
	}

	// Escribir el SDP de backchannel (dónde ffmpeg lee el audio-mic local) a un temporal.
	sdp := BuildBackchannelSDP(sdpPort, s.bcPT)
	f, err := os.CreateTemp("", "bt-talk-*.sdp")
	if err != nil {
		s.killLocked() // para el relé si llegó a arrancar
		s.mu.Unlock()
		s.logger.Warnf("[TALK] no se pudo crear el SDP temporal: %v — no se habla", err)
		return
	}
	if _, werr := f.WriteString(sdp); werr != nil {
		f.Close()
		os.Remove(f.Name())
		s.killLocked() // para el relé si llegó a arrancar
		s.mu.Unlock()
		s.logger.Warnf("[TALK] no se pudo escribir el SDP temporal: %v — no se habla", werr)
		return
	}
	f.Close()

	args := buildFFmpegArgs(f.Name(), key, host, port, pt)
	proc, err := s.runner.Start(s.ffmpegPath, args)
	if err != nil {
		os.Remove(f.Name())
		s.killLocked() // para el relé si llegó a arrancar
		s.mu.Unlock()
		s.logger.Warnf("[TALK] no se pudo lanzar ffmpeg (%v) — la llamada sigue sin talk-back", err)
		return
	}
	s.proc = proc
	s.sdpPath = f.Name()
	s.mu.Unlock()

	s.logger.Infof("[TALK] ffmpeg de talk-back lanzado → srtp://%s:%d", host, port)
	// Wait() bloquea hasta que el proceso termine: NUNCA con s.mu tomado.
	go func() {
		werr := proc.Wait()
		s.logger.Infof("[TALK] ffmpeg de talk-back terminó: %v", werr)
	}()
}

// onEnded mata el ffmpeg en curso (si lo hay) y limpia el temporal. Registra el call_id ANTES de
// matar/limpiar, para que un doorcall.answered del MISMO call-id que llegue después (reordenado
// por el EventBus) sepa que esa llamada ya terminó y no lance un ffmpeg huérfano.
func (s *TalkSender) onEnded(e *events.Event) {
	data, _ := e.Data.(map[string]interface{})
	cid, _ := data["call_id"].(string)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastEndedCallID = cid
	s.killLocked()
}

// killLocked mata el proceso y borra el temporal. Asume s.mu tomado. Matar dos veces es un no-op.
func (s *TalkSender) killLocked() {
	if s.proc != nil {
		s.proc.Kill()
		s.proc = nil
	}
	if s.keepalive != nil {
		s.keepalive.Stop()
		s.keepalive = nil
	}
	if s.sdpPath != "" {
		os.Remove(s.sdpPath)
		s.sdpPath = ""
	}
}

// Stop mata el ffmpeg si sigue vivo y marca el sender como detenido; idempotente.
func (s *TalkSender) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	s.killLocked()
}

// buildFFmpegArgs devuelve los argumentos (sin el binario) para el sender de talk-back.
func buildFFmpegArgs(sdpPath, srtpKeyB64, dstHost string, dstPort, outPT int) []string {
	return []string{
		"-hide_banner", "-loglevel", "warning",
		"-protocol_whitelist", "file,udp,rtp",
		"-fflags", "nobuffer",
		"-i", sdpPath,
		"-acodec", "copy",
		"-payload_type", fmt.Sprintf("%d", outPT), // 96 (el PT speex de nuestro answer)
		"-srtp_out_suite", "AES_CM_128_HMAC_SHA1_80",
		"-srtp_out_params", srtpKeyB64,
		"-f", "rtp",
		fmt.Sprintf("srtp://%s:%d", dstHost, dstPort),
	}
}

package sip

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// defaultAudioSenderTimeout es el tope de ejecución de ffmpeg. Evita que un
// WAV que no termina (p.ej. /dev/zero, un pipe) cuelgue Play/el handler HTTP
// indefinidamente (anti-DoS). Un WAV de talk-back normal dura pocos segundos.
const defaultAudioSenderTimeout = 30 * time.Second

// AudioSender envía un WAV como SRTP speex/8000 al puerto de audio del portero,
// delegando la capa SRTP en ffmpeg (protocolo srtp + libspeex). SSRC propio
// (ffmpeg genera uno aleatorio), sesión legítima → el portero lo renderiza.
type AudioSender struct {
	ffmpeg  string
	timeout time.Duration
	logger  *logrus.Logger
	mu      sync.Mutex
	cancel  context.CancelFunc
	cmd     *exec.Cmd
}

func NewAudioSender(ffmpegPath string, logger *logrus.Logger) *AudioSender {
	if logger == nil {
		logger = logrus.New()
	}
	return &AudioSender{ffmpeg: ffmpegPath, timeout: defaultAudioSenderTimeout, logger: logger}
}

// buildArgs construye los argumentos de ffmpeg (helper testeable sin ejecutar).
func (s *AudioSender) buildArgs(wavPath, host string, ans AudioAnswer) []string {
	url := fmt.Sprintf("srtp://%s:%d?srtp_out_suite=%s&srtp_out_params=%s",
		host, ans.Port, ans.CryptoSuite, ans.CryptoKey)
	return []string{
		"-hide_banner", "-loglevel", "warning", "-re",
		"-i", wavPath,
		"-c:a", "libspeex", "-ar", "8000", "-ac", "1",
		"-payload_type", fmt.Sprintf("%d", ans.PayloadType),
		"-f", "rtp", url,
	}
}

// Play lanza ffmpeg (con timeout duro) y bloquea hasta que termina el WAV o
// se alcanza el tope. Errores no fatales. El timeout garantiza que Play retorna
// aunque la fuente no termine nunca (anti-DoS del handler que lo invoca).
func (s *AudioSender) Play(wavPath, host string, ans AudioAnswer) error {
	timeout := s.timeout
	if timeout <= 0 {
		timeout = defaultAudioSenderTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	s.mu.Lock()
	cmd := exec.CommandContext(ctx, s.ffmpeg, s.buildArgs(wavPath, host, ans)...)
	s.cmd = cmd
	s.cancel = cancel
	s.mu.Unlock()

	s.logger.Infof("audio-sender: enviando %s → srtp %s:%d (speex PT%d, tope %s)", wavPath, host, ans.Port, ans.PayloadType, timeout)
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.logger.WithError(err).Warnf("audio-sender: ffmpeg terminó con error: %s", string(out))
	}
	return err
}

// Stop mata el ffmpeg en curso si lo hay (cancela su contexto).
func (s *AudioSender) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

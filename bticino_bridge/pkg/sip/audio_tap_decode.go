package sip

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

const defaultAudioTapTimeout = 60 * time.Second

// BuildTapDecodeSDP genera el SDP que ffmpeg lee para decodificar el RTP Speex
// EN CLARO reenviado por el tap a listenHost:listenPort. Sin a=crypto (el leg
// interno 5000 no va cifrado, a diferencia del leg SRTP hacia la app).
func BuildTapDecodeSDP(listenHost string, listenPort int, payloadType int) string {
	return fmt.Sprintf(
		"v=0\r\no=- 0 0 IN IP4 %s\r\ns=Tap\r\nc=IN IP4 %s\r\nt=0 0\r\n"+
			"m=audio %d RTP/AVP %d\r\na=rtpmap:%d speex/8000\r\na=recvonly\r\n",
		listenHost, listenHost, listenPort, payloadType, payloadType,
	)
}

// AudioTapDecoder decodifica el RTP Speex del tap a un WAV vía ffmpeg.
type AudioTapDecoder struct {
	ffmpeg  string
	timeout time.Duration
	logger  *logrus.Logger
	mu      sync.Mutex
	cancel  context.CancelFunc
	cmd     *exec.Cmd
}

func NewAudioTapDecoder(ffmpegPath string, logger *logrus.Logger) *AudioTapDecoder {
	if logger == nil {
		logger = logrus.New()
	}
	return &AudioTapDecoder{ffmpeg: ffmpegPath, timeout: defaultAudioTapTimeout, logger: logger}
}

// SetTimeout alarga (o acorta) el tope duro de ffmpeg. Por defecto
// defaultAudioTapTimeout (60s, pensado como protección anti-DoS genérica
// para invocaciones puntuales); llamadores que orquestan sesiones más largas
// (p.ej. AudioListenOnCall durante una llamada real) lo alargan para no
// cortar el audio a mitad. Solo afecta a ESTA instancia.
func (d *AudioTapDecoder) SetTimeout(dur time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.timeout = dur
}

func (d *AudioTapDecoder) buildArgs(sdpPath, sinkPath string) []string {
	return []string{
		"-hide_banner", "-loglevel", "warning",
		"-protocol_whitelist", "file,rtp,udp",
		"-i", sdpPath,
		"-c:a", "pcm_s16le", "-ar", "8000", "-ac", "1",
		// ffmpeg bufferiza la salida (AVIO buffer); sin esto el WAV sale a 0 bytes
		// al parar el decoder con kill/cancel (en lugar de exit limpio).
		// Con -flush_packets 1, ffmpeg flushea después de cada paquete, por lo que
		// el WAV crece continuamente y sobrevive al teardown.
		"-flush_packets", "1",
		"-f", "wav", "-y", sinkPath,
	}
}

// Decode escribe el SDP a un temporal, lanza ffmpeg (timeout duro) y bloquea
// hasta fin/Stop. El tap debe estar reenviando a listenPort ANTES o durante.
func (d *AudioTapDecoder) Decode(listenHost string, listenPort, payloadType int, sinkPath string) error {
	sdp := BuildTapDecodeSDP(listenHost, listenPort, payloadType)
	f, err := os.CreateTemp("", "tap-*.sdp")
	if err != nil {
		return fmt.Errorf("sdp temp: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(sdp); err != nil {
		f.Close()
		return fmt.Errorf("sdp write: %w", err)
	}
	f.Close()

	d.mu.Lock()
	timeout := d.timeout
	d.mu.Unlock()
	if timeout <= 0 {
		timeout = defaultAudioTapTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	d.mu.Lock()
	cmd := exec.CommandContext(ctx, d.ffmpeg, d.buildArgs(f.Name(), sinkPath)...)
	d.cmd = cmd
	d.cancel = cancel
	d.mu.Unlock()

	d.logger.Infof("audio-tap: decodificando rtp %s:%d → %s (speex PT%d, tope %s)", listenHost, listenPort, sinkPath, payloadType, timeout)
	out, err := cmd.CombinedOutput()
	// DeadlineExceeded (tope duro de fin de sesión) y Canceled (Stop()) son
	// terminaciones normales de fin de sesión, no fallos reales de ffmpeg —
	// no se loguean como error (hallazgo 3, revisión final de rama
	// feat/audio-tap-5000). Un fallo genuino de ffmpeg (ctx aún no vencido/
	// cancelado) sí se loguea.
	ctxErr := ctx.Err()
	if err != nil && ctxErr != context.Canceled && ctxErr != context.DeadlineExceeded {
		d.logger.WithError(err).Warnf("audio-tap: ffmpeg terminó con error: %s", string(out))
	}
	return err
}

func (d *AudioTapDecoder) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancel != nil {
		d.cancel()
	}
	if d.cmd != nil && d.cmd.Process != nil {
		_ = d.cmd.Process.Kill()
	}
}

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

const defaultAudioReceiverTimeout = 60 * time.Second

// AudioCapturer recibe el audio SRTP entrante del portero y lo escribe a un sink.
// localHost/localPort = el endpoint LOCAL del bridge (el anunciado en la oferta),
// donde el portero envía; ans aporta la suite y la clave SRTP a descifrar.
type AudioCapturer interface {
	Capture(localHost string, localPort int, sinkPath string, ans AudioAnswer) error
	Stop()
}

// BuildInboundSDP genera el SDP que ffmpeg lee para decodificar el RTP entrante
// (speex/8000) en el puerto LOCAL del bridge, descifrando con la clave del answer.
func BuildInboundSDP(localHost string, localPort int, ans AudioAnswer) string {
	return fmt.Sprintf(
		"v=0\r\no=- 0 0 IN IP4 %s\r\ns=Listen\r\nc=IN IP4 %s\r\nt=0 0\r\n"+
			"m=audio %d RTP/SAVP %d\r\na=rtpmap:%d speex/8000\r\n"+
			"a=crypto:2 %s inline:%s\r\na=recvonly\r\n",
		localHost, localHost, localPort, ans.PayloadType, ans.PayloadType, ans.CryptoSuite, ans.CryptoKey,
	)
}

type AudioReceiver struct {
	ffmpeg  string
	timeout time.Duration
	logger  *logrus.Logger
	mu      sync.Mutex
	cancel  context.CancelFunc
	cmd     *exec.Cmd
}

func NewAudioReceiver(ffmpegPath string, logger *logrus.Logger) *AudioReceiver {
	if logger == nil {
		logger = logrus.New()
	}
	return &AudioReceiver{ffmpeg: ffmpegPath, timeout: defaultAudioReceiverTimeout, logger: logger}
}

func (r *AudioReceiver) buildArgs(sdpPath, sinkPath string) []string {
	return []string{
		"-hide_banner", "-loglevel", "warning",
		"-protocol_whitelist", "file,crypto,rtp,udp,srtp",
		"-i", sdpPath,
		"-c:a", "pcm_s16le", "-ar", "8000", "-ac", "1",
		"-f", "wav", "-y", sinkPath,
	}
}

// Capture escribe el SDP inbound a un temporal, lanza ffmpeg (con timeout duro) y
// bloquea hasta que ffmpeg termina o Stop() lo corta. Errores no fatales.
func (r *AudioReceiver) Capture(localHost string, localPort int, sinkPath string, ans AudioAnswer) error {
	sdp := BuildInboundSDP(localHost, localPort, ans)
	f, err := os.CreateTemp("", "inbound-*.sdp")
	if err != nil {
		return fmt.Errorf("sdp temp: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(sdp); err != nil {
		f.Close()
		return fmt.Errorf("sdp write: %w", err)
	}
	f.Close()

	r.logger.Infof("audio-receiver: escuchando srtp %s:%d → %s (speex PT%d)", localHost, localPort, sinkPath, ans.PayloadType)
	return r.runFFmpeg(r.buildArgs(f.Name(), sinkPath))
}

// buildForwardArgs: como buildArgs pero la salida es RTP a dstHostPort (speex copy),
// para alimentar el relay de audio (go2rtc /doorbell) en vez de escribir WAV.
func buildForwardArgs(sdpPath, dstHostPort string, ans AudioAnswer) []string {
	return []string{
		"-hide_banner", "-loglevel", "warning",
		"-protocol_whitelist", "file,crypto,rtp,udp,srtp",
		"-fflags", "nobuffer",
		"-i", sdpPath,
		"-c:a", "copy",
		"-f", "rtp",
		fmt.Sprintf("rtp://%s", dstHostPort),
	}
}

// CaptureForward descifra el SRTP entrante (clave de la placa en ans) y lo reenvía
// como RTP a dstHostPort. Bloquea hasta Stop() o fin del proceso. Mismo ciclo de
// vida que Capture (ctx con timeout + Stop()).
func (r *AudioReceiver) CaptureForward(localHost string, localPort int, dstHostPort string, ans AudioAnswer) error {
	f, err := os.CreateTemp("", "bt-eye-in-*.sdp")
	if err != nil {
		return fmt.Errorf("sdp temp: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(BuildInboundSDP(localHost, localPort, ans)); err != nil {
		f.Close()
		return fmt.Errorf("sdp write: %w", err)
	}
	f.Close()

	r.logger.Infof("audio-receiver: reenviando srtp %s:%d → rtp://%s (speex PT%d)", localHost, localPort, dstHostPort, ans.PayloadType)
	return r.runFFmpeg(buildForwardArgs(f.Name(), dstHostPort, ans))
}

// runFFmpeg lanza ffmpeg con timeout duro y bloquea hasta que termina o Stop() lo
// corta. Errores no fatales (se registran).
func (r *AudioReceiver) runFFmpeg(args []string) error {
	timeout := r.timeout
	if timeout <= 0 {
		timeout = defaultAudioReceiverTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	r.mu.Lock()
	cmd := exec.CommandContext(ctx, r.ffmpeg, args...)
	r.cmd = cmd
	r.cancel = cancel
	r.mu.Unlock()

	out, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() != context.Canceled {
		r.logger.WithError(err).Warnf("audio-receiver: ffmpeg terminó con error: %s", string(out))
	}
	return err
}

func (r *AudioReceiver) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
	if r.cmd != nil && r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
	}
}

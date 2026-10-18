package sip

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

func TestBuildTapDecodeSDP(t *testing.T) {
	sdp := BuildTapDecodeSDP("127.0.0.1", 11000, 110)
	for _, want := range []string{
		"c=IN IP4 127.0.0.1",
		"m=audio 11000 RTP/AVP 110", // RTP EN CLARO (AVP, no SAVP), puerto de escucha local
		"a=rtpmap:110 speex/8000",
	} {
		if !strings.Contains(sdp, want) {
			t.Fatalf("SDP tap sin %q:\n%s", want, sdp)
		}
	}
	if strings.Contains(sdp, "a=crypto") {
		t.Fatalf("el tap es RTP en claro: no debe llevar a=crypto:\n%s", sdp)
	}
}

func TestAudioTapDecoderBuildArgs(t *testing.T) {
	d := NewAudioTapDecoder("/usr/local/bin/ffmpeg", logrus.New())
	args := d.buildArgs("/tmp/tap.sdp", "/tmp/out.wav")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-protocol_whitelist", "file,rtp,udp",
		"-i /tmp/tap.sdp",
		"-c:a pcm_s16le", "-ar 8000", "-ac 1",
		"-flush_packets", "1",
		"-f wav", "/tmp/out.wav",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args sin %q:\n%s", want, joined)
		}
	}
}

func TestNativeAudioRTPPortIs5000(t *testing.T) {
	if NativeAudioRTPPort != 5000 {
		t.Fatalf("NativeAudioRTPPort debe ser 5000, got %d", NativeAudioRTPPort)
	}
}

func TestAudioTapDecoderDefaultTimeout(t *testing.T) {
	d := NewAudioTapDecoder("/usr/local/bin/ffmpeg", logrus.New())
	if d.timeout != defaultAudioTapTimeout {
		t.Fatalf("esperaba timeout por defecto %s, got %s", defaultAudioTapTimeout, d.timeout)
	}
}

func TestAudioTapDecoderSetTimeout(t *testing.T) {
	d := NewAudioTapDecoder("/usr/local/bin/ffmpeg", logrus.New())
	d.SetTimeout(10 * time.Minute)
	if d.timeout != 10*time.Minute {
		t.Fatalf("esperaba timeout alargado a 10m, got %s", d.timeout)
	}
	// No debe afectar a otras instancias (solo a ESTA, per doc de SetTimeout).
	other := NewAudioTapDecoder("/usr/local/bin/ffmpeg", logrus.New())
	if other.timeout != defaultAudioTapTimeout {
		t.Fatalf("SetTimeout no debe afectar a otras instancias, got %s", other.timeout)
	}
}

// newFakeFfmpegScript crea un ejecutable de mentira que ignora sus argumentos
// y duerme más allá de cualquier timeout de test, para forzar de forma
// determinista (sin depender de ffmpeg real ni de tráfico RTP) que Decode()
// termine por el tope duro del contexto (DeadlineExceeded), NO por un fallo
// genuino del proceso. Usa `exec` para que sleep SUSTITUYA el proceso sh (en
// vez de un hijo aparte): así el kill del contexto termina el PID exacto que
// sostiene los pipes de stdout/stderr, y CombinedOutput() vuelve al instante
// del kill en vez de esperar 5s a que un "sleep" huérfano cierre esos pipes
// por su cuenta.
func newFakeFfmpegScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ffmpeg.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatalf("no se pudo escribir el script de prueba: %v", err)
	}
	return path
}

// TestAudioTapDecoderSuppressesDeadlineExceededLog cubre el hallazgo 3 de la
// revisión final de rama feat/audio-tap-5000: el fin de sesión normal por el
// tope duro (ctx.Err()==DeadlineExceeded) no debe loguearse como si fuera un
// fallo real de ffmpeg.
func TestAudioTapDecoderSuppressesDeadlineExceededLog(t *testing.T) {
	logger, hook := logrustest.NewNullLogger()
	d := NewAudioTapDecoder(newFakeFfmpegScript(t), logger)
	d.SetTimeout(150 * time.Millisecond)

	sink := filepath.Join(t.TempDir(), "out.wav")
	if err := d.Decode("127.0.0.1", 15099, 110, sink); err == nil {
		t.Fatal("esperaba error: el proceso se mata al vencer el tope duro")
	}

	for _, entry := range hook.AllEntries() {
		if entry.Level <= logrus.WarnLevel {
			t.Fatalf("un DeadlineExceeded normal no debe loguearse como warning/error: nivel=%s msg=%q", entry.Level, entry.Message)
		}
	}
}

// TestAudioTapDecoderLogsGenuineFfmpegFailure verifica que un fallo REAL del
// proceso (no causado por el tope duro ni por Stop()) SIGUE logueándose: el
// hallazgo 3 solo pide suprimir las terminaciones normales de fin de sesión.
func TestAudioTapDecoderLogsGenuineFfmpegFailure(t *testing.T) {
	logger, hook := logrustest.NewNullLogger()
	d := NewAudioTapDecoder(filepath.Join(t.TempDir(), "no-existe-este-binario"), logger)
	d.SetTimeout(5 * time.Second) // de sobra: el fallo debe llegar antes por binario inexistente

	sink := filepath.Join(t.TempDir(), "out.wav")
	if err := d.Decode("127.0.0.1", 15098, 110, sink); err == nil {
		t.Fatal("esperaba error: binario ffmpeg inexistente")
	}

	found := false
	for _, entry := range hook.AllEntries() {
		if entry.Level == logrus.WarnLevel {
			found = true
		}
	}
	if !found {
		t.Fatal("esperaba un log Warn para un fallo genuino de ffmpeg (binario inexistente, no deadline/cancel)")
	}
}

package sip

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// TestAudioSenderPlayHonorsTimeout: un proceso que no termina debe ser matado
// por el timeout, de modo que Play RETORNA (no cuelga el handler HTTP → anti-DoS).
func TestAudioSenderPlayHonorsTimeout(t *testing.T) {
	// Script falso que ignora sus args y duerme mucho (simula ffmpeg colgado).
	dir := t.TempDir()
	fake := filepath.Join(dir, "fakeffmpeg.sh")
	// exec: el sleep REEMPLAZA al shell (hijo directo, como ffmpeg real), para
	// que CommandContext lo mate al vencer el timeout sin nietos que dejen los
	// pipes abiertos.
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := NewAudioSender(fake, logrus.New())
	s.timeout = 200 * time.Millisecond // tope corto para el test

	ans := AudioAnswer{Port: 7078, PayloadType: 98, CryptoSuite: "AES_CM_128_HMAC_SHA1_80", CryptoKey: "K"}

	start := time.Now()
	err := s.Play(filepath.Join(dir, "x.wav"), "127.0.0.1", ans)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Fatalf("Play se colgó (%v): el timeout no mató el proceso", elapsed)
	}
	if err == nil {
		t.Fatal("esperaba error al matar el proceso por timeout")
	}
}

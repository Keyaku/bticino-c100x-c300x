package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAudioDuplexDefaults(t *testing.T) {
	// Sin bloque audio: fail-closed y sink por defecto.
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("sip:\n  enabled: true\n"), 0644); err != nil {
		t.Fatalf("no se pudo escribir config temporal: %v", err)
	}

	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Audio.DuplexEnabled {
		t.Fatal("DuplexEnabled debe ser false si se omite el bloque (fail-closed)")
	}
	if cfg.Audio.DuplexSinkPath == "" {
		t.Fatal("DuplexSinkPath debe tener default no vacío")
	}
	if cfg.Audio.ListenSinkPath == "" {
		t.Fatal("ListenSinkPath debe tener default no vacío")
	}
	if cfg.Audio.ListenSinkPath == cfg.Audio.DuplexSinkPath {
		t.Fatal("ListenSinkPath debe ser DISTINTO de DuplexSinkPath por defecto (sink dedicado, hallazgo 2)")
	}
}

func TestAudioDuplexValuesRespected(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	body := "audio:\n  duplex_enabled: true\n  duplex_sink_path: /tmp/x.wav\n"
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatalf("no se pudo escribir config temporal: %v", err)
	}

	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Audio.DuplexEnabled || cfg.Audio.DuplexSinkPath != "/tmp/x.wav" {
		t.Fatalf("valores no respetados: %+v", cfg.Audio)
	}
}

func TestAudioListenTapDefaultFailClosed(t *testing.T) {
	// Sin bloque audio: ListenTapEnabled fail-closed (igual que DuplexEnabled).
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("sip:\n  enabled: true\n"), 0644); err != nil {
		t.Fatalf("no se pudo escribir config temporal: %v", err)
	}

	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Audio.ListenTapEnabled {
		t.Fatal("ListenTapEnabled debe ser false si se omite el bloque (fail-closed)")
	}
}

func TestAudioListenTapValueRespected(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	body := "audio:\n  listen_tap_enabled: true\n  duplex_sink_path: /tmp/x.wav\n  listen_sink_path: /tmp/listen.wav\n"
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatalf("no se pudo escribir config temporal: %v", err)
	}

	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Audio.ListenTapEnabled {
		t.Fatal("ListenTapEnabled debe respetar el valor true del YAML")
	}
	if cfg.Audio.DuplexSinkPath != "/tmp/x.wav" {
		t.Fatalf("DuplexSinkPath no respetado: %+v", cfg.Audio)
	}
	// Sink dedicado del oído pasivo (hallazgo 2, revisión final de rama
	// feat/audio-tap-5000): NO comparte archivo con DuplexSinkPath.
	if cfg.Audio.ListenSinkPath != "/tmp/listen.wav" {
		t.Fatalf("ListenSinkPath no respetado: %+v", cfg.Audio)
	}
}

func TestAudioListenSinkPathDefault(t *testing.T) {
	// Sin listen_sink_path explícito: auto-default (no fail-closed, como
	// DuplexSinkPath) a un WAV distinto del de la pata duplex.
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	body := "audio:\n  listen_tap_enabled: true\n"
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatalf("no se pudo escribir config temporal: %v", err)
	}

	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	const want = "/home/bticino/cfg/extra/recordings/door_audio_listen.wav"
	if cfg.Audio.ListenSinkPath != want {
		t.Fatalf("esperaba default ListenSinkPath=%q, got %q", want, cfg.Audio.ListenSinkPath)
	}
}

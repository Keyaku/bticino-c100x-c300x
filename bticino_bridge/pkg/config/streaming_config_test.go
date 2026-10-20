package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestStreamingLiveAudioOnCallDefaultFailClosed cubre el knob nuevo
// live_audio_on_call: a diferencia de live_on_call (vídeo, que sí se
// distribuye a true en configs/config.yaml), es una feature nueva y debe
// quedar OFF si se omite el campo — mismo patrón fail-closed que
// Audio.DuplexEnabled / Audio.ListenTapEnabled.
func TestStreamingLiveAudioOnCallDefaultFailClosed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("sip:\n  enabled: true\n"), 0644); err != nil {
		t.Fatalf("no se pudo escribir config temporal: %v", err)
	}

	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Streaming.LiveAudioOnCall {
		t.Fatal("LiveAudioOnCall debe ser false si se omite el bloque streaming (fail-closed)")
	}
}

func TestStreamingLiveAudioOnCallValueRespected(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	body := "streaming:\n  live_audio_on_call: true\n"
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatalf("no se pudo escribir config temporal: %v", err)
	}

	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Streaming.LiveAudioOnCall {
		t.Fatal("LiveAudioOnCall debe respetar el valor true del YAML")
	}
}

// TestGetStreamingConfigDoesNotDefaultLiveAudioOnCall documenta que, a
// diferencia de LiveOnCall (vídeo), GetStreamingConfig() NO debe defaultear
// LiveAudioOnCall a true: es una feature nueva, fail-closed por diseño.
func TestGetStreamingConfigDoesNotDefaultLiveAudioOnCall(t *testing.T) {
	sc := GetStreamingConfig()
	if sc.LiveAudioOnCall {
		t.Fatal("GetStreamingConfig() no debe defaultear LiveAudioOnCall a true (fail-closed)")
	}
}

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnswererConfigDefaultsFailClosed(t *testing.T) {
	// Un config sin bloque answerer => Enabled=false (fail-closed).
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	os.WriteFile(p, []byte("sip:\n  enabled: true\n  domain: \"YOUR_GATEWAY_ID.bs.iotleg.com\"\n"), 0o644)
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SIP.Answerer.Enabled {
		t.Fatal("answerer debe ser fail-closed (Enabled=false) por defecto")
	}
}

func TestAnswererConfigParsed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	os.WriteFile(p, []byte(
		"sip:\n"+
			"  enabled: true\n"+
			"  domain: \"YOUR_GATEWAY_ID.bs.iotleg.com\"\n"+
			"  answerer:\n"+
			"    enabled: true\n"+
			"    user: \"bridge\"\n"+
			"    audio_port: 10100\n"+
			"    video_port: 10102\n"+
			"    max_call_seconds: 120\n"+
			"    talk_enabled: false\n"), 0o644)
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	a := cfg.SIP.Answerer
	if !a.Enabled || a.User != "bridge" || a.AudioPort != 10100 || a.VideoPort != 10102 || a.MaxCallSeconds != 120 {
		t.Fatalf("answerer mal parseado: %+v", a)
	}
}

func TestAnswererTalkKeepalive(t *testing.T) {
	// Fail-closed: sin la clave => false (comportamiento actual). Con talk_keepalive: true => true.
	dir := t.TempDir()
	p := filepath.Join(dir, "off.yaml")
	os.WriteFile(p, []byte("sip:\n  enabled: true\n  domain: \"d\"\n  answerer:\n    enabled: true\n"), 0o644)
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SIP.Answerer.TalkKeepalive {
		t.Fatal("talk_keepalive debe ser false por defecto (fail-closed)")
	}
	p2 := filepath.Join(dir, "on.yaml")
	os.WriteFile(p2, []byte("sip:\n  enabled: true\n  domain: \"d\"\n  answerer:\n    enabled: true\n    talk_keepalive: true\n"), 0o644)
	cfg2, err := LoadConfig(p2)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg2.SIP.Answerer.TalkKeepalive {
		t.Fatal("talk_keepalive: true no se parseó")
	}
}

func TestAnswererRingTimeoutDefault70(t *testing.T) {
	// Sin ring_timeout_seconds en el YAML => 70 s (mismo valor que usa la app oficial 1.17.3
	// para la llamada entrante). Con default en código, el device no necesita cambiar su config.
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	os.WriteFile(p, []byte("sip:\n  enabled: true\n  domain: \"d\"\n  answerer:\n    enabled: true\n"), 0o644)
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.SIP.Answerer.RingTimeoutSeconds; got != 70 {
		t.Fatalf("RingTimeoutSeconds=%d, esperaba 70 por defecto", got)
	}
}

package config

import "testing"

func TestPanelViewDefaults(t *testing.T) {
	// Un YAML mínimo con panelview habilitado pero sin timeout/caller: LoadConfig
	// debe aplicar los defaults (30s, "webrtc") y respetar enabled/devaddr.
	yaml := "streaming:\n  panelview:\n    enabled: true\n    devaddr: \"11\"\n"
	path := writeTempConfig(t, yaml)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	pv := cfg.Streaming.PanelView
	if !pv.Enabled || pv.DEVADDR != "11" {
		t.Fatalf("enabled/devaddr no respetados: %+v", pv)
	}
	if pv.TimeoutSeconds != 30 {
		t.Fatalf("TimeoutSeconds default = %d, quería 30", pv.TimeoutSeconds)
	}
	if pv.CallerUser != "webrtc" {
		t.Fatalf("CallerUser default = %q, quería webrtc", pv.CallerUser)
	}
}

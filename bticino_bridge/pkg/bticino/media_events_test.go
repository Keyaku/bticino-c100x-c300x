package bticino

import "testing"

func TestIsMediaChannelUp(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		// Verdaderos: canal-up de audio y vídeo (capturados en vivo 2026-08-22)
		{"audio 5000 up", "*7*300#127#0#0#1#5000#2*##", true},
		{"video 5007 up", "*7*300#127#0#0#1#5007#0*##", true},
		{"audio 5000 otro subvalor", "*7*300#127#0#0#1#5000#1*##", true},
		// Falsos: otros WHO=7 que NO son canal-up de media
		{"video mode display", "*7*220#0*##", false},
		{"brillo pantalla", "*7*73#0#0*##", false},
		{"otro 300 sin puerto media", "*7*300#127#0#0#1#5002#1*##", false},
		// Falsos: no es WHO=7
		{"hangup WHO=8", "*8*3#5#4*416##", false},
		{"intercom activo WHO=8", "*8*2#5#4*16##", false},
		// Falsos: basura / vacío
		{"vacío", "", false},
		{"5000 sin prefijo 300", "*7*301#127#0#0#1#5000#2*##", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsMediaChannelUp(c.raw); got != c.want {
				t.Fatalf("IsMediaChannelUp(%q) = %v, want %v", c.raw, got, c.want)
			}
		})
	}
}

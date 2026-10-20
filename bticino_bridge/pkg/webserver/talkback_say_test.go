package webserver

import "testing"

func TestValidateAudioFilePath(t *testing.T) {
	ok := []string{
		"/tmp/hola.wav",
		"/tmp/sub/dir/audio.wav",
		"/home/bticino/cfg/extra/prompt.wav",
	}
	for _, p := range ok {
		if err := validateAudioFilePath(p); err != nil {
			t.Errorf("esperaba válido %q, error: %v", p, err)
		}
	}

	bad := []string{
		"",                      // vacío
		"relativo.wav",          // no absoluto
		"../../etc/passwd",      // relativo con traversal
		"/tmp/../etc/passwd",    // traversal que escapa a /etc
		"/etc/passwd",           // fuera de los directorios permitidos
		"/home/bticino/.ssh/id", // fuera del subdir permitido
	}
	for _, p := range bad {
		if err := validateAudioFilePath(p); err == nil {
			t.Errorf("esperaba rechazo de %q, pero pasó", p)
		}
	}
}

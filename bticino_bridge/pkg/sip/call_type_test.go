package sip

import "testing"

// La app oficial 1.17.3 no clasifica las llamadas entrantes; lo único que mira es si el remoto
// trae vídeo. Seguimos esa misma señal: con vídeo => placa (la única con evidencia real), solo
// audio => intercom (conjetura: monitor interior), nada legible => desconocido.
func TestClassifyCallType(t *testing.T) {
	casos := []struct {
		nombre string
		sdp    string
		want   string
	}{
		{"placa: audio+vídeo (SDP real de la placa)",
			"v=0\r\no=c300x 1 1 IN IP4 127.0.0.1\r\ns=Talk\r\nm=audio 7078 RTP/SAVP 96 18 101\r\nm=video 9078 RTP/SAVP 96 97\r\n",
			CallTypePlaca},
		{"intercom: solo audio",
			"v=0\r\ns=Talk\r\nm=audio 7078 RTP/SAVP 96\r\n",
			CallTypeIntercom},
		{"vídeo rechazado (puerto 0) cuenta como solo audio",
			"v=0\r\nm=audio 7078 RTP/SAVP 96\r\nm=video 0 RTP/SAVP 96\r\n",
			CallTypeIntercom},
		{"SDP vacío", "", CallTypeDesconocido},
		{"sin líneas m=", "v=0\r\ns=Talk\r\n", CallTypeDesconocido},
		{"solo LF (sin CR) también vale",
			"v=0\nm=audio 7078 RTP/SAVP 96\nm=video 9078 RTP/SAVP 96\n",
			CallTypePlaca},
	}
	for _, c := range casos {
		if got := ClassifyCallType(c.sdp); got != c.want {
			t.Errorf("%s: ClassifyCallType=%q, esperaba %q", c.nombre, got, c.want)
		}
	}
}

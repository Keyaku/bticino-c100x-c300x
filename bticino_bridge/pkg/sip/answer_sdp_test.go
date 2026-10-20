package sip

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGenerateSRTPKey(t *testing.T) {
	k, err := GenerateSRTPKey()
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(k)
	if err != nil {
		t.Fatalf("no es base64 válido: %v", err)
	}
	if len(raw) != 30 {
		t.Fatalf("esperaba 30 bytes (16 key + 14 salt), got %d", len(raw))
	}
	k2, _ := GenerateSRTPKey()
	if k == k2 {
		t.Fatal("dos claves iguales — no es aleatorio")
	}
}

func TestBuildAnswerSDP_AudioYVideo(t *testing.T) {
	sdp := BuildAnswerSDP("127.0.0.1", 10100, 10102, "TESTKEYb64==")
	for _, want := range []string{
		"c=IN IP4 127.0.0.1\r\n",
		"m=audio 10100 RTP/SAVP 96\r\n",
		"a=rtpmap:96 speex/8000\r\n",
		"a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:TESTKEYb64==\r\n",
		"m=video 10102 RTP/SAVP 96\r\n",
		"a=rtpmap:96 H264/90000\r\n",
	} {
		if !strings.Contains(sdp, want) {
			t.Errorf("SDP no contiene %q\n---\n%s", want, sdp)
		}
	}
}

func TestBuildAnswerSDP_SoloAudio(t *testing.T) {
	sdp := BuildAnswerSDP("127.0.0.1", 10100, 0, "K==")
	if strings.Contains(sdp, "m=video") {
		t.Errorf("videoPort=0 no debe emitir m=video\n%s", sdp)
	}
	// Asegurar que tiene m=audio con CRLF
	if !strings.Contains(sdp, "m=audio 10100 RTP/SAVP 96\r\n") {
		t.Errorf("SDP debe contener m=audio con CRLF\n%s", sdp)
	}
}

func TestBuildAnswerSDP_VideoRecvOnly(t *testing.T) {
	// El bridge solo RECIBE vídeo (no tiene cámara): la sección m=video debe declarar
	// a=recvonly, como hace la app oficial 1.17.3 (acceptWithParams con VideoDirection
	// RecvOnly). El audio sigue sendrecv (F2 habla hacia la placa).
	sdp := BuildAnswerSDP("127.0.0.1", 10100, 10102, "K==")
	i := strings.Index(sdp, "m=video")
	if i < 0 {
		t.Fatalf("sin m=video:\n%s", sdp)
	}
	audio, video := sdp[:i], sdp[i:]
	if !strings.Contains(video, "a=recvonly\r\n") || strings.Contains(video, "a=sendrecv") {
		t.Errorf("vídeo debe ser recvonly:\n%s", video)
	}
	if !strings.Contains(audio, "a=sendrecv\r\n") {
		t.Errorf("audio debe seguir sendrecv:\n%s", audio)
	}
}

func TestParseRemoteAudioPort(t *testing.T) {
	tests := []struct {
		name    string
		offer   string
		want    int
		wantErr bool
	}{
		{
			name: "offer válido con puerto 7078",
			offer: "v=0\r\no=c300x 3703 703 IN IP4 127.0.0.1\r\ns=Talk\r\n" +
				"c=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
				"m=audio 7078 RTP/SAVP 96 18 101\r\na=rtpmap:96 speex/8000\r\n" +
				"m=video 9078 RTP/SAVP 96 97\r\na=rtpmap:96 H264/90000\r\n",
			want:    7078,
			wantErr: false,
		},
		{
			name:    "offer sin m=audio",
			offer:   "v=0\r\no=c300x 3703 703 IN IP4 127.0.0.1\r\ns=Talk\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n",
			want:    0,
			wantErr: true,
		},
		{
			name:    "m=audio con puerto no numérico",
			offer:   "v=0\r\nm=audio abc RTP/SAVP 96\r\n",
			want:    0,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := ParseRemoteAudioPort(tt.offer)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseRemoteAudioPort() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if p != tt.want {
				t.Errorf("ParseRemoteAudioPort() = %d, want %d", p, tt.want)
			}
		})
	}
}

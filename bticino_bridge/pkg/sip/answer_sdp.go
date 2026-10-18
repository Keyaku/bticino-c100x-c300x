package sip

import (
	"fmt"
	"strconv"
	"strings"
)

// GenerateSRTPKey genera una clave SDES para AES_CM_128_HMAC_SHA1_80 (30 bytes,
// base64). Alias de NewSRTPMasterKey para mantener una única fuente de clave.
func GenerateSRTPKey() (string, error) {
	return NewSRTPMasterKey()
}

// BuildAnswerSDP construye la SDP answer del contestador. Audio speex PT96 sobre SRTP
// (AES_CM_128_HMAC_SHA1_80). Si videoPort>0, añade m=video H264 42801F. Determinista.
func BuildAnswerSDP(localIP string, audioPort, videoPort int, srtpKeyB64 string) string {
	var b strings.Builder
	b.WriteString("v=0\r\n")
	b.WriteString(fmt.Sprintf("o=bridge 0 0 IN IP4 %s\r\n", localIP))
	b.WriteString("s=Talk\r\n")
	b.WriteString(fmt.Sprintf("c=IN IP4 %s\r\n", localIP))
	b.WriteString("t=0 0\r\n")
	// audio
	b.WriteString(fmt.Sprintf("m=audio %d RTP/SAVP 96\r\n", audioPort))
	b.WriteString("a=rtpmap:96 speex/8000\r\n")
	b.WriteString("a=fmtp:96 vbr=on\r\n")
	b.WriteString(fmt.Sprintf("a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:%s\r\n", srtpKeyB64))
	b.WriteString("a=sendrecv\r\n")
	if videoPort > 0 {
		b.WriteString(fmt.Sprintf("m=video %d RTP/SAVP 96\r\n", videoPort))
		b.WriteString("a=rtpmap:96 H264/90000\r\n")
		b.WriteString("a=fmtp:96 profile-level-id=42801F\r\n")
		b.WriteString(fmt.Sprintf("a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:%s\r\n", srtpKeyB64))
		// recvonly: el bridge solo RECIBE el vídeo de la placa (no tiene cámara que enviar).
		// Es lo que declara la app oficial 1.17.3 al contestar. No afecta al vídeo hacia HA.
		b.WriteString("a=recvonly\r\n")
	}
	return b.String()
}

// ParseRemoteAudioPort extrae el puerto de la primera línea m=audio del offer.
func ParseRemoteAudioPort(offer string) (int, error) {
	for _, line := range strings.Split(offer, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "m=audio ") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0, fmt.Errorf("m=audio malformada: %q", line)
			}
			return strconv.Atoi(fields[1])
		}
	}
	return 0, fmt.Errorf("no hay m=audio en el offer")
}

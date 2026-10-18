package sip

import (
	"strconv"
	"strings"
)

// Tipos de llamada entrante que el contestador expone en HA.
const (
	CallTypePlaca       = "placa"       // timbre de la placa de calle (trae vídeo). Evidencia real.
	CallTypeIntercom    = "intercom"    // solo audio: presumiblemente otro monitor interior. Conjetura.
	CallTypeDesconocido = "desconocido" // SDP vacío o sin líneas m= legibles.
)

// ClassifyCallType decide el tipo de una llamada entrante a partir del SDP del INVITE.
// Misma señal que usa la app oficial 1.17.3 (isVideoEnabled del remoto): con un m=video de
// puerto distinto de 0 es la placa; si solo hay audio, intercom; si no hay m= legibles,
// desconocido. La app NO clasifica las entrantes más allá de esto, y no hay capturas que
// respalden distinguir el contestador como llamante, así que no se intenta.
func ClassifyCallType(sdp string) string {
	audio, video := false, false
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		var media string
		switch {
		case strings.HasPrefix(line, "m=audio "):
			media = "audio"
		case strings.HasPrefix(line, "m=video "):
			media = "video"
		default:
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		port, err := strconv.Atoi(fields[1])
		if err != nil || port == 0 { // puerto 0 = medio rechazado/inactivo
			continue
		}
		if media == "video" {
			video = true
		} else {
			audio = true
		}
	}
	switch {
	case video:
		return CallTypePlaca
	case audio:
		return CallTypeIntercom
	default:
		return CallTypeDesconocido
	}
}

package bticino

import "strings"

// IsMediaChannelUp indica si una trama OWN del bus (raw, tal cual llega del
// monitor en puerto 20000) es un anuncio de subida de canal de media nativo:
// audio (lo:5000) o vídeo (lo:5007). Forma real capturada 2026-08-22:
//
//	*7*300#127#0#0#1#5000#2*##   (audio)
//	*7*300#127#0#0#1#5007#0*##   (vídeo)
//
// Es la señal fiable de "hay media en ese puerto" en la auto-activación nativa
// de la app (que NO genera INVITE SIP). Función pura, solo clasifica; no envía
// nada al bus.
func IsMediaChannelUp(raw string) bool {
	// WHO=7, WHAT con prefijo de canal de media 300#.
	if !strings.HasPrefix(raw, "*7*300#") {
		return false
	}
	// Token de puerto de media (delimitado por # a ambos lados para no casar
	// 5002 u otros puertos con substring de 5000/5007).
	return strings.Contains(raw, "#5000#") || strings.Contains(raw, "#5007#")
}

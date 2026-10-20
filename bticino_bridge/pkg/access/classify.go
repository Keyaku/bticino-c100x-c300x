// Package access clasifica aperturas del portal (telefonillo vs llave/RFID)
// correlacionando con timbres/llamadas previos. Es un suscriptor puro del
// EventBus: no abre sockets ni envía comandos al bus.
package access

import "time"

// Etiquetas de acceso.
const (
	TypeTelefonillo = "telefonillo" // apertura correlacionada con timbre/llamada previos
	TypeLlave       = "llave"       // apertura sin timbre/llamada previos (RFID/residente)
	TypeDesconocido = "desconocido" // señales en conflicto
)

// Niveles de confianza (acuerdo entre correlación y duración).
const (
	ConfAlta  = "alta"
	ConfBaja  = "baja"
	ConfMedia = "media" // sin dato de duración para desempatar
)

// ClassifyInput son las señales ya resueltas en el momento de decidir.
type ClassifyInput struct {
	HadCall       bool          // hubo timbre/llamada dentro de la ventana, antes de abrir
	Duration      time.Duration // tiempo con la cerradura energizada (19→20)
	DurationKnown bool          // false si no llegó el cierre a tiempo
	VisitorMax    time.Duration // umbral de "duración de visita"
}

// ClassifyResult es la etiqueta y su confianza.
type ClassifyResult struct {
	Type       string
	Confidence string
}

// Classify es determinista y pura. La correlación fija la etiqueta; la duración
// fija la confianza; el único conflicto real (sin llamada pero pulso corto)
// produce "desconocido". Ver la tabla de decisión en el spec.
func Classify(in ClassifyInput) ClassifyResult {
	if in.HadCall {
		if !in.DurationKnown {
			return ClassifyResult{TypeTelefonillo, ConfMedia}
		}
		if in.Duration <= in.VisitorMax {
			return ClassifyResult{TypeTelefonillo, ConfAlta}
		}
		return ClassifyResult{TypeTelefonillo, ConfBaja}
	}
	if !in.DurationKnown {
		return ClassifyResult{TypeLlave, ConfMedia}
	}
	if in.Duration > in.VisitorMax {
		return ClassifyResult{TypeLlave, ConfAlta}
	}
	return ClassifyResult{TypeDesconocido, ConfBaja}
}

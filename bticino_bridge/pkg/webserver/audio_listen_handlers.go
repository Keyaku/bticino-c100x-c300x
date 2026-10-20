package webserver

import (
	"encoding/json"
	"net/http"
)

// AudioListenController es la interfaz mínima que necesitan los endpoints
// /api/audio/listen/* (Task 4, sdd 2026-08-21-audio-tap-5000) para forzar el
// armado/desarmado del "oído" pasivo del portero (Task 3,
// pkg/sip/audio_listen_on_call.go: AudioListenOnCall). Se define aquí, en vez
// de acoplar el handler directamente al tipo concreto *sip.AudioListenOnCall,
// para poder inyectar un fake en los tests del webserver sin depender de
// RTP/ffmpeg real. Coincide exactamente con los métodos exportados de
// AudioListenOnCall — a diferencia de DuplexController, Start no devuelve
// error: el armado es idempotente y puramente pasivo (tap + decoder), sin un
// estado "ocupado" que rechazar.
type AudioListenController interface {
	Start(reason string)
	Stop()
	IsActive() bool
}

// SetAudioListenController inyecta el controlador del oído pasivo (tap
// lo:5000) usado por POST/GET /api/audio/listen/*.
func (ws *WebServer) SetAudioListenController(alc AudioListenController) {
	ws.audioListenController = alc
}

// Endpoints de validación local del audio-listen (espejo pasivo del leg de
// audio nativo, WHO=6/16 vídeo aparte — ver pkg/sip/audio_listen_on_call.go).
// Gateados por audio.listen_tap_enabled: si está desactivado en config, los
// tres devuelven 404 (fail-closed — ni siquiera revelan que el endpoint
// existe), en vez de 503 "no disponible".
//
// Estos endpoints son para VALIDACIÓN en el device (forzar el armado durante
// pruebas de comunicación de la app); la vía PRIMARIA de armado es por
// eventos del bus (doorbell.pressed, sip.call.*), cableada en cmd/main.go —
// exactamente igual que LiveVideoOnCall.
//
//	POST /api/audio/listen/start
//	POST /api/audio/listen/stop
//	GET  /api/audio/listen/status

func (ws *WebServer) handleAudioListenStart(w http.ResponseWriter, r *http.Request) {
	if !ws.config.Audio.ListenTapEnabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if ws.audioListenController == nil {
		ws.writeAudioListenJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"success": false, "error": "audio-listen no disponible (SIP no inicializado)",
		})
		return
	}

	ws.audioListenController.Start("api")

	// 202: el armado dispara el decoder (goroutine) y el tap; la captura
	// sigue en marcha en segundo plano. Sin datos sensibles en el body.
	ws.writeAudioListenJSON(w, http.StatusAccepted, map[string]interface{}{
		"success": true,
		"active":  ws.audioListenController.IsActive(),
	})
}

func (ws *WebServer) handleAudioListenStop(w http.ResponseWriter, r *http.Request) {
	if !ws.config.Audio.ListenTapEnabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if ws.audioListenController == nil {
		ws.writeAudioListenJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"success": false, "error": "audio-listen no disponible (SIP no inicializado)",
		})
		return
	}

	ws.audioListenController.Stop()
	ws.writeAudioListenJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (ws *WebServer) handleAudioListenStatus(w http.ResponseWriter, r *http.Request) {
	if !ws.config.Audio.ListenTapEnabled {
		http.NotFound(w, r)
		return
	}
	// active es autoritativo (controller.IsActive()), no un flag espejo: el
	// desarme puede llegar por el guard automático o por el fin natural del
	// decoder, sin pasar por /stop.
	active := ws.audioListenController != nil && ws.audioListenController.IsActive()

	ws.writeAudioListenJSON(w, http.StatusOK, map[string]interface{}{
		"active":    active,
		"sink_path": ws.config.Audio.ListenSinkPath,
	})
}

func (ws *WebServer) writeAudioListenJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

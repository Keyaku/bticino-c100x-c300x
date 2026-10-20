package webserver

import (
	"encoding/json"
	"errors"
	"net/http"

	"bticino_bridge/pkg/sip"
)

// DuplexController es la interfaz mínima que necesitan los endpoints
// /api/audio/duplex/* (Task 4, sdd 2026-08-21-duplex-door-audio) para
// abrir/cerrar la pata full-duplex de validación local (Task 3,
// pkg/sip/audio_call.go: AudioCall.OpenDuplex/Close). Se define aquí, en
// vez de acoplar el handler directamente al tipo concreto *sip.AudioCall,
// para poder inyectar un fake en los tests del webserver sin depender de
// SIP/RTP real.
type DuplexController interface {
	OpenDuplex(sinkPath, talkWav string) (sip.AudioAnswer, error)
	Close() error
	// IsActive es la fuente de verdad para /status: la pata puede
	// autocerrarse sola (fin natural del receiver, ver
	// pkg/sip/audio_call.go OpenDuplex) sin pasar por /stop, así que un
	// flag espejo en el webserver quedaría obsoleto tras ese cierre.
	IsActive() bool
}

// SetDuplexController inyecta el controlador de la pata full-duplex de
// validación local usada por POST/GET /api/audio/duplex/*.
func (ws *WebServer) SetDuplexController(dc DuplexController) {
	ws.duplexController = dc
}

// Endpoints de validación local del audio full-duplex de la pata propia del
// bridge (WHO=8, ver pkg/sip/audio_call.go). Gateados por
// audio.duplex_enabled: si está desactivado en config, los tres devuelven
// 404 (fail-closed — ni siquiera revelan que el endpoint existe), en vez de
// 503 "no disponible".
//
//	POST /api/audio/duplex/start?talk=<wav opcional>
//	POST /api/audio/duplex/stop
//	GET  /api/audio/duplex/status

func (ws *WebServer) handleAudioDuplexStart(w http.ResponseWriter, r *http.Request) {
	if !ws.config.Audio.DuplexEnabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if ws.duplexController == nil {
		ws.writeAudioDuplexJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"success": false, "error": "audio duplex no disponible (SIP no inicializado)",
		})
		return
	}

	talk := r.URL.Query().Get("talk")
	answer, err := ws.duplexController.OpenDuplex(ws.config.Audio.DuplexSinkPath, talk)
	if err != nil {
		if errors.Is(err, sip.ErrAudioLegBusy) {
			ws.writeAudioDuplexJSON(w, http.StatusConflict, map[string]interface{}{
				"success": false, "error": err.Error(),
			})
			return
		}
		ws.logger.WithError(err).Warn("audio/duplex/start: fallo al abrir la pata full-duplex")
		ws.writeAudioDuplexJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"success": false, "error": err.Error(),
		})
		return
	}

	// 202: la pata ya quedó abierta (OpenDuplex vuelve tras el setup —
	// INVITE/answer/armado), pero la captura/reproducción de audio siguen en
	// marcha en segundo plano. Nunca se incluye crypto_key: es la clave SRTP
	// de la sesión y no debe salir por HTTP.
	ws.writeAudioDuplexJSON(w, http.StatusAccepted, map[string]interface{}{
		"success":      true,
		"port":         answer.Port,
		"payload_type": answer.PayloadType,
		"crypto_suite": answer.CryptoSuite,
	})
}

func (ws *WebServer) handleAudioDuplexStop(w http.ResponseWriter, r *http.Request) {
	if !ws.config.Audio.DuplexEnabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if ws.duplexController == nil {
		ws.writeAudioDuplexJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"success": false, "error": "audio duplex no disponible (SIP no inicializado)",
		})
		return
	}

	err := ws.duplexController.Close()

	if err != nil {
		ws.logger.WithError(err).Warn("audio/duplex/stop: fallo al cerrar la pata full-duplex")
		ws.writeAudioDuplexJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"success": false, "error": err.Error(),
		})
		return
	}
	ws.writeAudioDuplexJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (ws *WebServer) handleAudioDuplexStatus(w http.ResponseWriter, r *http.Request) {
	if !ws.config.Audio.DuplexEnabled {
		http.NotFound(w, r)
		return
	}
	// active es autoritativo (controller.IsActive()), no un flag espejo: la
	// pata puede autocerrarse (fin natural del receiver) sin pasar por
	// /stop, y un flag local aquí quedaría desactualizado en ese caso.
	active := ws.duplexController != nil && ws.duplexController.IsActive()

	ws.writeAudioDuplexJSON(w, http.StatusOK, map[string]interface{}{
		"active":    active,
		"sink_path": ws.config.Audio.DuplexSinkPath,
	})
}

func (ws *WebServer) writeAudioDuplexJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

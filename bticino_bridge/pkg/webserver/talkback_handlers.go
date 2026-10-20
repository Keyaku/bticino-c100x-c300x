package webserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"bticino_bridge/pkg/bticino"
)

// audioAllowedDirs son los directorios bajo los cuales se permite un WAV para
// /api/talkback/say. Evita lectura de fichero arbitrario (aunque la API ya va
// tras Basic Auth): la ruta debe ser absoluta, sin `..`, y quedar dentro de
// uno de estos árboles.
var audioAllowedDirs = []string{"/tmp", "/home/bticino/cfg/extra"}

// validateAudioFilePath valida que file sea una ruta absoluta, limpia (sin
// traversal) y contenida en uno de los directorios permitidos.
func validateAudioFilePath(file string) error {
	if file == "" {
		return fmt.Errorf("ruta vacía")
	}
	if !filepath.IsAbs(file) {
		return fmt.Errorf("debe ser una ruta absoluta")
	}
	clean := filepath.Clean(file)
	for _, dir := range audioAllowedDirs {
		d := filepath.Clean(dir)
		if clean == d || strings.HasPrefix(clean, d+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("fuera de los directorios permitidos (%s)", strings.Join(audioAllowedDirs, ", "))
}

// Talk-back (audio ascendente app/bridge → portero). EXPERIMENTAL.
//
// Mecanismo (ver _investigacion/docs/TALKBACK_AUDIO_ASCENDENTE_2026-07-09.md):
// el media de la llamada es RTP plano (el c300x usa crypto "dummy" en el SDP).
// El talk-back = (1) armar el audio bidireccional con el comando OWN
// *8*2#5#4*16## (bticino.CmdAnswerPickup), y (2) enviar RTP Speex/8000 (pt=110)
// al puerto de recepción de audio del portero (SDP-negociado por sesión → se
// pasa como parámetro para poder probar el puerto observado en una llamada viva).
//
// Endpoints:
//   POST /api/talkback/start   {host?, port, source, loop?, arm?}
//   POST /api/talkback/stop
//   GET  /api/talkback/status
//   POST /api/talkback/say?file=<ruta_wav>   (talk-back v2, ver audioCall/handleTalkbackSay)

func (ws *WebServer) handleTalkbackStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Host   string `json:"host"`   // destino RTP (default 127.0.0.1)
		Port   int    `json:"port"`   // puerto de recepción de audio del portero (obligatorio)
		PT     int    `json:"pt"`     // RTP payload type Speex/8000 (default 98, el del c300x)
		Source string `json:"source"` // ruta a un fichero de audio (WAV, etc.) — obligatorio
		Loop   bool   `json:"loop"`   // repetir el fichero de forma continua
		Arm    *bool  `json:"arm"`    // enviar el comando de armado (default true)
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		ws.writeTalkbackJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false, "error": "cuerpo JSON inválido",
		})
		return
	}
	if body.Port <= 0 || body.Source == "" {
		ws.writeTalkbackJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false, "error": "se requieren 'port' (>0) y 'source' (ruta a WAV)",
		})
		return
	}

	arm := true
	if body.Arm != nil {
		arm = *body.Arm
	}

	result := map[string]interface{}{}

	// (1) Armar el audio bidireccional (OWN *8*2#5#4*16##)
	if arm {
		if err := ws.bridge.SendOpenWebNetCommand(bticino.CmdAnswerPickup); err != nil {
			ws.logger.WithError(err).Warn("talkback: fallo al enviar el comando de armado")
			result["arm_error"] = err.Error()
		} else {
			result["armed"] = bticino.CmdAnswerPickup
		}
	}

	// (2) Lanzar el pipeline de subida (RTP Speex → puerto del portero)
	if err := ws.talkback.Start(body.Host, body.Port, body.PT, body.Source, body.Loop); err != nil {
		ws.writeTalkbackJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"success": false, "error": err.Error(), "arm": result,
		})
		return
	}

	result["success"] = true
	result["target"] = ws.talkback.Target()
	result["message"] = "talk-back iniciado (experimental)"
	ws.writeTalkbackJSON(w, http.StatusOK, result)
}

func (ws *WebServer) handleTalkbackStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ws.talkback.Stop()
	ws.writeTalkbackJSON(w, http.StatusOK, map[string]interface{}{
		"success": true, "message": "talk-back detenido",
	})
}

func (ws *WebServer) handleTalkbackStatus(w http.ResponseWriter, r *http.Request) {
	ws.writeTalkbackJSON(w, http.StatusOK, map[string]interface{}{
		"active": ws.talkback.IsActive(),
		"target": ws.talkback.Target(),
	})
}

// handleTalkbackSay implementa el talk-back v2: el bridge origina su propia
// llamada de audio local a c300x (SDP real, no el dummy del self-INVITE de
// vídeo) y reproduce el WAV indicado por el altavoz de la puerta. A
// diferencia de /api/talkback/start (que exige conocer de antemano el
// puerto/clave SRTP de una sesión ya viva), aquí AudioCall.Say negocia su
// propia sesión de principio a fin (ver pkg/sip/audio_call.go).
//
//	POST /api/talkback/say?file=/ruta/al/audio.wav
func (ws *WebServer) handleTalkbackSay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if ws.audioCall == nil {
		ws.writeTalkbackJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"success": false, "error": "talk-back v2 no disponible (SIP/OpenWebNet no inicializados)",
		})
		return
	}

	file := r.URL.Query().Get("file")
	if file == "" {
		ws.writeTalkbackJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false, "error": "se requiere el parámetro 'file' (ruta al WAV)",
		})
		return
	}
	if err := validateAudioFilePath(file); err != nil {
		ws.writeTalkbackJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false, "error": "ruta de audio no permitida: " + err.Error(),
		})
		return
	}

	if err := ws.audioCall.Say(file); err != nil {
		ws.logger.WithError(err).Warn("talkback/say: fallo al originar la llamada de audio o reproducir")
		ws.writeTalkbackJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"success": false, "error": err.Error(),
		})
		return
	}

	ws.writeTalkbackJSON(w, http.StatusOK, map[string]interface{}{
		"success": true, "file": file, "message": "talk-back v2: WAV enviado",
	})
}

func (ws *WebServer) writeTalkbackJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

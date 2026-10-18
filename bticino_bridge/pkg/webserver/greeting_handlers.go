package webserver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"bticino_bridge/pkg/bticino"
)

// Talk-back v1 (saludo del contestador → altavoz de la PUERTA).
//
// Vía cooperativa VALIDADA en vivo (2026-08-20): el contestador nativo
// (bt_answering_machine, un cliente SIP liblinphone) reproduce su mensaje de
// bienvenida hacia el bus/puerta (estado DSP EN_AUDIO_PLAY_LOC_SCS) cuando
// auto-responde una llamada de puerta sin contestar. Si sustituimos el fichero
// del saludo por el nuestro y el contestador está activo, el visitante en la
// placa oye NUESTRO audio.
//
// A diferencia de talk-back v2 (/api/talkback/say, self-INVITE que solo llega
// al altavoz INTERIOR por la topología del DSP ZL380), esta vía sí alcanza la
// puerta — a cambio de ser un saludo one-shot al saltar el contestador, no
// conversación interactiva.
//
// Endpoints:
//
//	POST   /api/talkback/greeting   (cuerpo = audio; ?activate=true|false)
//	GET    /api/talkback/greeting
//	DELETE /api/talkback/greeting   (?deactivate=true|false)

const (
	// greetingPath es el fichero de saludo que reproduce el contestador
	// (referenciado por bt_av_media). greetingAltPath es una variante que
	// algunas builds del firmware leen; escribimos ambos por seguridad.
	greetingPath    = "/home/bticino/cfg/extra/7/messaggio.wav"
	greetingAltPath = "/home/bticino/cfg/extra/7/audio.wav"
	// greetingSentinel marca que el saludo actual lo puso el bridge, para
	// poder restaurar (borrar) sin tocar un saludo legítimo del usuario.
	greetingSentinel = "/home/bticino/cfg/extra/7/.bridge_greeting"
	// greetingFFmpeg es el ffmpeg del device (con libspeex/pcm), usado para
	// normalizar cualquier audio de entrada al formato del pipeline del
	// portero: PCM s16le, mono, 8000 Hz (filesrc!wavparse!audioconvert, sin
	// resample → la frecuencia debe ser exactamente 8000).
	greetingFFmpeg   = "/usr/local/bin/ffmpeg"
	greetingMaxBytes = 8 << 20 // 8 MiB de audio de entrada como tope
)

// handleGreeting despacha GET/POST/DELETE sobre /api/talkback/greeting.
func (ws *WebServer) handleGreeting(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		ws.handleGreetingSet(w, r)
	case http.MethodGet:
		ws.handleGreetingStatus(w, r)
	case http.MethodDelete:
		ws.handleGreetingClear(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGreetingSet recibe un audio en el cuerpo, lo normaliza a PCM s16le
// mono 8000 y lo instala como saludo del contestador. Salvo ?activate=false,
// activa además el contestador (*8*91##) para que el saludo llegue a sonar.
func (ws *WebServer) handleGreetingSet(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, greetingMaxBytes+1))
	if err != nil {
		ws.writeTalkbackJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false, "error": "no se pudo leer el cuerpo: " + err.Error(),
		})
		return
	}
	if len(body) == 0 {
		ws.writeTalkbackJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false, "error": "cuerpo vacío: envía el audio (WAV/MP3/…) en el cuerpo de la petición",
		})
		return
	}
	if len(body) > greetingMaxBytes {
		ws.writeTalkbackJSON(w, http.StatusRequestEntityTooLarge, map[string]interface{}{
			"success": false, "error": fmt.Sprintf("audio demasiado grande (máximo %d bytes)", greetingMaxBytes),
		})
		return
	}

	// Escribir la entrada a un temporal y transcodificar con ffmpeg al formato
	// exacto del pipeline del portero.
	tmpIn, err := os.CreateTemp("/tmp", "greeting-in-*")
	if err != nil {
		ws.writeTalkbackJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"success": false, "error": "no se pudo crear temporal: " + err.Error(),
		})
		return
	}
	defer os.Remove(tmpIn.Name())
	if _, err := tmpIn.Write(body); err != nil {
		tmpIn.Close()
		ws.writeTalkbackJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"success": false, "error": "no se pudo escribir temporal: " + err.Error(),
		})
		return
	}
	tmpIn.Close()

	if err := ws.transcodeGreeting(tmpIn.Name(), greetingPath); err != nil {
		ws.logger.WithError(err).Warn("greeting: fallo al transcodificar el audio")
		ws.writeTalkbackJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false, "error": "audio no válido o no transcodificable: " + err.Error(),
		})
		return
	}
	// Copiar también a la variante audio.wav (best-effort).
	if data, err := os.ReadFile(greetingPath); err == nil {
		_ = os.WriteFile(greetingAltPath, data, 0644)
	}
	// Marcar que el saludo lo puso el bridge.
	_ = os.WriteFile(greetingSentinel, []byte(time.Now().Format(time.RFC3339)+"\n"), 0644)

	result := map[string]interface{}{
		"success": true,
		"path":    greetingPath,
		"message": "saludo del contestador actualizado; sonará en la placa cuando salte el contestador",
	}

	// Activar el contestador salvo que se pida explícitamente lo contrario.
	if r.URL.Query().Get("activate") != "false" {
		if err := ws.bridge.SendOpenWebNetCommand(bticino.CmdVoicemailOn); err != nil {
			ws.logger.WithError(err).Warn("greeting: fallo al activar el contestador")
			result["activate_error"] = err.Error()
		} else {
			result["answering_machine"] = "activado"
		}
	}

	ws.logger.Info("Talk-back v1: saludo del contestador actualizado vía web dashboard")
	ws.writeTalkbackJSON(w, http.StatusOK, result)
}

// handleGreetingStatus informa si hay un saludo puesto por el bridge y su tamaño.
func (ws *WebServer) handleGreetingStatus(w http.ResponseWriter, r *http.Request) {
	res := map[string]interface{}{
		"bridge_greeting": fileExists(greetingSentinel),
		"greeting_exists": fileExists(greetingPath),
	}
	if fi, err := os.Stat(greetingPath); err == nil {
		res["size"] = fi.Size()
		res["modified"] = fi.ModTime().Format(time.RFC3339)
	}
	ws.writeTalkbackJSON(w, http.StatusOK, res)
}

// handleGreetingClear elimina el saludo puesto por el bridge (restaura el
// saludo por defecto del firmware). Con ?deactivate=true desactiva además el
// contestador. Solo borra si el saludo lo puso el bridge (sentinel presente).
func (ws *WebServer) handleGreetingClear(w http.ResponseWriter, r *http.Request) {
	if !fileExists(greetingSentinel) {
		ws.writeTalkbackJSON(w, http.StatusOK, map[string]interface{}{
			"success": true, "message": "no hay saludo puesto por el bridge; nada que restaurar",
		})
		return
	}
	_ = os.Remove(greetingPath)
	_ = os.Remove(greetingAltPath)
	_ = os.Remove(greetingSentinel)

	result := map[string]interface{}{
		"success": true, "message": "saludo del bridge eliminado; restaurado el saludo por defecto",
	}
	if r.URL.Query().Get("deactivate") == "true" {
		if err := ws.bridge.SendOpenWebNetCommand(bticino.CmdVoicemailOff); err != nil {
			ws.logger.WithError(err).Warn("greeting: fallo al desactivar el contestador")
			result["deactivate_error"] = err.Error()
		} else {
			result["answering_machine"] = "desactivado"
		}
	}
	ws.logger.Info("Talk-back v1: saludo del contestador restaurado vía web dashboard")
	ws.writeTalkbackJSON(w, http.StatusOK, result)
}

// transcodeGreeting normaliza src al formato del pipeline del portero
// (PCM s16le, mono, 8000 Hz) escribiendo dst. Escribe a un temporal y hace
// rename atómico para no dejar un saludo a medias si ffmpeg falla.
func (ws *WebServer) transcodeGreeting(src, dst string) error {
	tmpOut := dst + ".tmp"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, greetingFFmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", src,
		"-ar", "8000", "-ac", "1", "-c:a", "pcm_s16le",
		"-f", "wav", tmpOut,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(tmpOut)
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("ffmpeg agotó el tiempo (30s)")
		}
		msg := string(out)
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		return fmt.Errorf("ffmpeg: %v: %s", err, msg)
	}
	if err := os.Rename(tmpOut, dst); err != nil {
		os.Remove(tmpOut)
		return fmt.Errorf("no se pudo instalar el saludo: %w", err)
	}
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(filepath.Clean(p))
	return err == nil
}

package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bticino_bridge/pkg/config"
	"bticino_bridge/pkg/sip"
	"github.com/sirupsen/logrus"
)

// fakeDuplex implementa DuplexController para los tests, sin tocar
// SIP/RTP real (Task 3, pkg/sip/audio_call.go).
type fakeDuplex struct {
	opened, closed bool
	busy           bool
	active         bool // simula el estado autoritativo de AudioCall.active
}

func (f *fakeDuplex) OpenDuplex(sink, talk string) (sip.AudioAnswer, error) {
	if f.busy {
		return sip.AudioAnswer{}, sip.ErrAudioLegBusy
	}
	f.opened = true
	f.active = true
	return sip.AudioAnswer{
		Port:        7078,
		PayloadType: 98,
		CryptoSuite: "AES_CM_128_HMAC_SHA1_80",
		CryptoKey:   "SECRET",
	}, nil
}

func (f *fakeDuplex) Close() error {
	f.closed = true
	f.active = false
	return nil
}

func (f *fakeDuplex) IsActive() bool { return f.active }

func newTestDuplexWS(enabled bool, fd *fakeDuplex) *WebServer {
	return &WebServer{
		logger: logrus.New(),
		config: &config.Config{
			Audio: config.AudioConfig{
				DuplexEnabled:  enabled,
				DuplexSinkPath: "/tmp/door_audio.wav",
			},
		},
		duplexController: fd,
	}
}

func TestAudioDuplexHandlers(t *testing.T) {
	t.Run("start OK: 202 y sin clave SRTP en el body", func(t *testing.T) {
		fd := &fakeDuplex{}
		ws := newTestDuplexWS(true, fd)
		rec := httptest.NewRecorder()
		ws.handleAudioDuplexStart(rec, httptest.NewRequest(http.MethodPost, "/api/audio/duplex/start", nil))

		if rec.Code != http.StatusAccepted {
			t.Fatalf("esperaba 202, got %d (body=%s)", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "SECRET") {
			t.Fatalf("la clave SRTP NO debe aparecer en el body: %s", rec.Body.String())
		}
		var body map[string]interface{}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("body no es JSON válido: %v", err)
		}
		if body["port"] != float64(7078) {
			t.Errorf("esperaba port=7078, got %v", body["port"])
		}
		if body["payload_type"] != float64(98) {
			t.Errorf("esperaba payload_type=98, got %v", body["payload_type"])
		}
		if body["crypto_suite"] != "AES_CM_128_HMAC_SHA1_80" {
			t.Errorf("esperaba crypto_suite, got %v", body["crypto_suite"])
		}
		if _, ok := body["crypto_key"]; ok {
			t.Error("el body no debe contener la clave 'crypto_key'")
		}
		if !fd.opened {
			t.Error("esperaba que OpenDuplex fuese invocado")
		}
	})

	t.Run("disabled: 404", func(t *testing.T) {
		fd := &fakeDuplex{}
		ws := newTestDuplexWS(false, fd)
		rec := httptest.NewRecorder()
		ws.handleAudioDuplexStart(rec, httptest.NewRequest(http.MethodPost, "/api/audio/duplex/start", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("esperaba 404 con duplex_enabled=false, got %d", rec.Code)
		}
		if fd.opened {
			t.Error("OpenDuplex no debe invocarse si duplex_enabled=false")
		}
	})

	t.Run("busy: 409", func(t *testing.T) {
		fd := &fakeDuplex{busy: true}
		ws := newTestDuplexWS(true, fd)
		rec := httptest.NewRecorder()
		ws.handleAudioDuplexStart(rec, httptest.NewRequest(http.MethodPost, "/api/audio/duplex/start", nil))
		if rec.Code != http.StatusConflict {
			t.Fatalf("esperaba 409 con pata ocupada, got %d", rec.Code)
		}
	})

	t.Run("start rechaza GET", func(t *testing.T) {
		fd := &fakeDuplex{}
		ws := newTestDuplexWS(true, fd)
		rec := httptest.NewRecorder()
		ws.handleAudioDuplexStart(rec, httptest.NewRequest(http.MethodGet, "/api/audio/duplex/start", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("esperaba 405 en GET, got %d", rec.Code)
		}
	})

	t.Run("stop llama a Close", func(t *testing.T) {
		fd := &fakeDuplex{}
		ws := newTestDuplexWS(true, fd)
		rec := httptest.NewRecorder()
		ws.handleAudioDuplexStop(rec, httptest.NewRequest(http.MethodPost, "/api/audio/duplex/stop", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("esperaba 200 en stop, got %d", rec.Code)
		}
		if !fd.closed {
			t.Error("esperaba que Close fuese invocado")
		}
	})

	t.Run("stop disabled: 404", func(t *testing.T) {
		fd := &fakeDuplex{}
		ws := newTestDuplexWS(false, fd)
		rec := httptest.NewRecorder()
		ws.handleAudioDuplexStop(rec, httptest.NewRequest(http.MethodPost, "/api/audio/duplex/stop", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("esperaba 404, got %d", rec.Code)
		}
		if fd.closed {
			t.Error("Close no debe invocarse si duplex_enabled=false")
		}
	})

	t.Run("status disabled: 404", func(t *testing.T) {
		fd := &fakeDuplex{}
		ws := newTestDuplexWS(false, fd)
		rec := httptest.NewRecorder()
		ws.handleAudioDuplexStatus(rec, httptest.NewRequest(http.MethodGet, "/api/audio/duplex/status", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("esperaba 404, got %d", rec.Code)
		}
	})

	t.Run("status enabled: 200 con sink_path", func(t *testing.T) {
		fd := &fakeDuplex{}
		ws := newTestDuplexWS(true, fd)
		rec := httptest.NewRecorder()
		ws.handleAudioDuplexStatus(rec, httptest.NewRequest(http.MethodGet, "/api/audio/duplex/status", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("esperaba 200, got %d", rec.Code)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("body no es JSON válido: %v", err)
		}
		if body["sink_path"] != "/tmp/door_audio.wav" {
			t.Errorf("esperaba sink_path, got %v", body["sink_path"])
		}
		if body["active"] != false {
			t.Errorf("esperaba active=false antes de start, got %v", body["active"])
		}
	})

	t.Run("status refleja active tras start", func(t *testing.T) {
		fd := &fakeDuplex{}
		ws := newTestDuplexWS(true, fd)
		ws.handleAudioDuplexStart(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/audio/duplex/start", nil))

		rec := httptest.NewRecorder()
		ws.handleAudioDuplexStatus(rec, httptest.NewRequest(http.MethodGet, "/api/audio/duplex/status", nil))
		var body map[string]interface{}
		_ = json.NewDecoder(rec.Body).Decode(&body)
		if body["active"] != true {
			t.Errorf("esperaba active=true tras start, got %v", body["active"])
		}
	})

	t.Run("status es autoritativo: refleja el autocierre de la pata sin pasar por /stop", func(t *testing.T) {
		// Finding 4: /status debe consultar controller.IsActive() en vez de
		// un flag local — si la pata se autocierra (Finding 1: fin natural
		// del receiver tras el timeout de 60s) sin que nadie llame a
		// /stop, un flag espejo en el webserver quedaría desactualizado en
		// active=true para siempre.
		fd := &fakeDuplex{}
		ws := newTestDuplexWS(true, fd)
		ws.handleAudioDuplexStart(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/audio/duplex/start", nil))

		// Simula el autocierre: la pata se pone inactiva sola (como hace
		// AudioCall tras el fin natural del receiver), SIN pasar por
		// handleAudioDuplexStop.
		fd.active = false

		rec := httptest.NewRecorder()
		ws.handleAudioDuplexStatus(rec, httptest.NewRequest(http.MethodGet, "/api/audio/duplex/status", nil))
		var body map[string]interface{}
		_ = json.NewDecoder(rec.Body).Decode(&body)
		if body["active"] != false {
			t.Errorf("esperaba active=false tras el autocierre (sin /stop), got %v", body["active"])
		}
	})

	t.Run("status con controller nil: active=false sin panic", func(t *testing.T) {
		// Construcción directa (NO vía newTestDuplexWS(true, nil)): pasar un
		// *fakeDuplex nil por ese helper produciría una interfaz
		// DuplexController NO nil con puntero subyacente nil (el clásico
		// "nil interface gotcha" de Go), que sí panicaría al llamar
		// IsActive(). Aquí se deja duplexController con su cero-valor real
		// (interfaz nil), que es el caso que /status debe tolerar.
		ws := &WebServer{
			logger: logrus.New(),
			config: &config.Config{
				Audio: config.AudioConfig{DuplexEnabled: true, DuplexSinkPath: "/tmp/door_audio.wav"},
			},
		}
		rec := httptest.NewRecorder()
		ws.handleAudioDuplexStatus(rec, httptest.NewRequest(http.MethodGet, "/api/audio/duplex/status", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("esperaba 200, got %d", rec.Code)
		}
		var body map[string]interface{}
		_ = json.NewDecoder(rec.Body).Decode(&body)
		if body["active"] != false {
			t.Errorf("esperaba active=false con controller nil, got %v", body["active"])
		}
	})
}

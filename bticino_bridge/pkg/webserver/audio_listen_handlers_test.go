package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"bticino_bridge/pkg/config"
	"github.com/sirupsen/logrus"
)

// fakeAudioListen implementa AudioListenController para los tests, sin tocar
// RTP/ffmpeg real (Task 3, pkg/sip/audio_listen_on_call.go).
type fakeAudioListen struct {
	startCalls int
	stopCalls  int
	lastReason string
	active     bool // simula el estado autoritativo de AudioListenOnCall.active
}

func (f *fakeAudioListen) Start(reason string) {
	f.startCalls++
	f.lastReason = reason
	f.active = true
}

func (f *fakeAudioListen) Stop() {
	f.stopCalls++
	f.active = false
}

func (f *fakeAudioListen) IsActive() bool { return f.active }

func newTestAudioListenWS(enabled bool, fal *fakeAudioListen) *WebServer {
	return &WebServer{
		logger: logrus.New(),
		config: &config.Config{
			Audio: config.AudioConfig{
				ListenTapEnabled: enabled,
				// Sink DEDICADO del oído pasivo, distinto de DuplexSinkPath
				// (hallazgo 2, revisión final de rama feat/audio-tap-5000).
				ListenSinkPath: "/tmp/door_audio_listen.wav",
				DuplexSinkPath: "/tmp/door_audio.wav",
			},
		},
		audioListenController: fal,
	}
}

func TestAudioListenHandlers(t *testing.T) {
	t.Run("start OK: 202, invoca Start(\"api\") y refleja active", func(t *testing.T) {
		fal := &fakeAudioListen{}
		ws := newTestAudioListenWS(true, fal)
		rec := httptest.NewRecorder()
		ws.handleAudioListenStart(rec, httptest.NewRequest(http.MethodPost, "/api/audio/listen/start", nil))

		if rec.Code != http.StatusAccepted {
			t.Fatalf("esperaba 202, got %d (body=%s)", rec.Code, rec.Body.String())
		}
		if fal.startCalls != 1 {
			t.Errorf("esperaba Start invocado una vez, got %d", fal.startCalls)
		}
		if fal.lastReason != "api" {
			t.Errorf("esperaba reason=api, got %q", fal.lastReason)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("body no es JSON válido: %v", err)
		}
		if body["success"] != true {
			t.Errorf("esperaba success=true, got %v", body["success"])
		}
		if body["active"] != true {
			t.Errorf("esperaba active=true tras start, got %v", body["active"])
		}
	})

	t.Run("start es idempotente (Start no debe fallar en doble start)", func(t *testing.T) {
		fal := &fakeAudioListen{}
		ws := newTestAudioListenWS(true, fal)
		ws.handleAudioListenStart(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/audio/listen/start", nil))
		rec := httptest.NewRecorder()
		ws.handleAudioListenStart(rec, httptest.NewRequest(http.MethodPost, "/api/audio/listen/start", nil))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("esperaba 202 en el segundo start, got %d", rec.Code)
		}
		if fal.startCalls != 2 {
			t.Errorf("esperaba 2 invocaciones a Start (el handler no filtra dobles), got %d", fal.startCalls)
		}
	})

	t.Run("disabled: 404 y Start no se invoca", func(t *testing.T) {
		fal := &fakeAudioListen{}
		ws := newTestAudioListenWS(false, fal)
		rec := httptest.NewRecorder()
		ws.handleAudioListenStart(rec, httptest.NewRequest(http.MethodPost, "/api/audio/listen/start", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("esperaba 404 con listen_tap_enabled=false, got %d", rec.Code)
		}
		if fal.startCalls != 0 {
			t.Error("Start no debe invocarse si listen_tap_enabled=false")
		}
	})

	t.Run("start rechaza GET", func(t *testing.T) {
		fal := &fakeAudioListen{}
		ws := newTestAudioListenWS(true, fal)
		rec := httptest.NewRecorder()
		ws.handleAudioListenStart(rec, httptest.NewRequest(http.MethodGet, "/api/audio/listen/start", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("esperaba 405 en GET, got %d", rec.Code)
		}
	})

	t.Run("start con controller nil: 503", func(t *testing.T) {
		ws := &WebServer{
			logger: logrus.New(),
			config: &config.Config{
				Audio: config.AudioConfig{ListenTapEnabled: true},
			},
		}
		rec := httptest.NewRecorder()
		ws.handleAudioListenStart(rec, httptest.NewRequest(http.MethodPost, "/api/audio/listen/start", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("esperaba 503 con controller nil, got %d", rec.Code)
		}
	})

	t.Run("stop llama a Stop", func(t *testing.T) {
		fal := &fakeAudioListen{}
		ws := newTestAudioListenWS(true, fal)
		rec := httptest.NewRecorder()
		ws.handleAudioListenStop(rec, httptest.NewRequest(http.MethodPost, "/api/audio/listen/stop", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("esperaba 200 en stop, got %d", rec.Code)
		}
		if fal.stopCalls != 1 {
			t.Errorf("esperaba Stop invocado una vez, got %d", fal.stopCalls)
		}
	})

	t.Run("stop disabled: 404", func(t *testing.T) {
		fal := &fakeAudioListen{}
		ws := newTestAudioListenWS(false, fal)
		rec := httptest.NewRecorder()
		ws.handleAudioListenStop(rec, httptest.NewRequest(http.MethodPost, "/api/audio/listen/stop", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("esperaba 404, got %d", rec.Code)
		}
		if fal.stopCalls != 0 {
			t.Error("Stop no debe invocarse si listen_tap_enabled=false")
		}
	})

	t.Run("stop rechaza GET", func(t *testing.T) {
		fal := &fakeAudioListen{}
		ws := newTestAudioListenWS(true, fal)
		rec := httptest.NewRecorder()
		ws.handleAudioListenStop(rec, httptest.NewRequest(http.MethodGet, "/api/audio/listen/stop", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("esperaba 405 en GET, got %d", rec.Code)
		}
	})

	t.Run("status disabled: 404", func(t *testing.T) {
		fal := &fakeAudioListen{}
		ws := newTestAudioListenWS(false, fal)
		rec := httptest.NewRecorder()
		ws.handleAudioListenStatus(rec, httptest.NewRequest(http.MethodGet, "/api/audio/listen/status", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("esperaba 404, got %d", rec.Code)
		}
	})

	t.Run("status enabled: 200 con sink_path y active=false antes de start", func(t *testing.T) {
		fal := &fakeAudioListen{}
		ws := newTestAudioListenWS(true, fal)
		rec := httptest.NewRecorder()
		ws.handleAudioListenStatus(rec, httptest.NewRequest(http.MethodGet, "/api/audio/listen/status", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("esperaba 200, got %d", rec.Code)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("body no es JSON válido: %v", err)
		}
		if body["sink_path"] != "/tmp/door_audio_listen.wav" {
			t.Errorf("esperaba sink_path (ListenSinkPath, dedicado), got %v", body["sink_path"])
		}
		if body["active"] != false {
			t.Errorf("esperaba active=false antes de start, got %v", body["active"])
		}
	})

	t.Run("status refleja active tras start y tras stop", func(t *testing.T) {
		fal := &fakeAudioListen{}
		ws := newTestAudioListenWS(true, fal)
		ws.handleAudioListenStart(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/audio/listen/start", nil))

		rec := httptest.NewRecorder()
		ws.handleAudioListenStatus(rec, httptest.NewRequest(http.MethodGet, "/api/audio/listen/status", nil))
		var body map[string]interface{}
		_ = json.NewDecoder(rec.Body).Decode(&body)
		if body["active"] != true {
			t.Errorf("esperaba active=true tras start, got %v", body["active"])
		}

		ws.handleAudioListenStop(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/audio/listen/stop", nil))
		rec2 := httptest.NewRecorder()
		ws.handleAudioListenStatus(rec2, httptest.NewRequest(http.MethodGet, "/api/audio/listen/status", nil))
		var body2 map[string]interface{}
		_ = json.NewDecoder(rec2.Body).Decode(&body2)
		if body2["active"] != false {
			t.Errorf("esperaba active=false tras stop, got %v", body2["active"])
		}
	})

	t.Run("status es autoritativo: refleja el autodesarme sin pasar por /stop", func(t *testing.T) {
		// El guard interno (SetGuardTimeout) o el fin natural del decoder
		// pueden desarmar la sesión sin pasar por /stop; /status debe
		// consultar IsActive() en vez de un flag espejo del webserver.
		fal := &fakeAudioListen{}
		ws := newTestAudioListenWS(true, fal)
		ws.handleAudioListenStart(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/audio/listen/start", nil))

		fal.active = false // simula el autodesarme

		rec := httptest.NewRecorder()
		ws.handleAudioListenStatus(rec, httptest.NewRequest(http.MethodGet, "/api/audio/listen/status", nil))
		var body map[string]interface{}
		_ = json.NewDecoder(rec.Body).Decode(&body)
		if body["active"] != false {
			t.Errorf("esperaba active=false tras el autodesarme (sin /stop), got %v", body["active"])
		}
	})

	t.Run("status con controller nil: active=false sin panic", func(t *testing.T) {
		// Construcción directa (NO vía newTestAudioListenWS(true, nil)): pasar
		// un *fakeAudioListen nil por ese helper produciría una interfaz
		// AudioListenController NO nil con puntero subyacente nil (nil
		// interface gotcha), que sí panicaría al llamar IsActive(). Aquí se
		// deja audioListenController con su cero-valor real (interfaz nil).
		ws := &WebServer{
			logger: logrus.New(),
			config: &config.Config{
				Audio: config.AudioConfig{ListenTapEnabled: true, ListenSinkPath: "/tmp/door_audio_listen.wav"},
			},
		}
		rec := httptest.NewRecorder()
		ws.handleAudioListenStatus(rec, httptest.NewRequest(http.MethodGet, "/api/audio/listen/status", nil))
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

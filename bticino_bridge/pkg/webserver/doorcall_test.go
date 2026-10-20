package webserver

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirupsen/logrus"

	"bticino_bridge/pkg/config"
)

// fakeAnswerer es un doble de pruebas de *sip.Answerer para los handlers del
// webserver (Task 9 — no importa pkg/sip, la interfaz Answerer es estructural).
type fakeAnswerer struct {
	state                string
	answerErr, hangupErr error
}

func (f *fakeAnswerer) Answer() error       { return f.answerErr }
func (f *fakeAnswerer) Hangup() error       { return f.hangupErr }
func (f *fakeAnswerer) StateString() string { return f.state }
func (f *fakeAnswerer) IsRegistered() bool  { return true }

func newWS(t *testing.T) *WebServer {
	return NewWebServer(&config.Config{}, nil, logrus.New())
}

func TestDoorcallAnswer_503SiNoHayAnswerer(t *testing.T) {
	ws := newWS(t)
	rec := httptest.NewRecorder()
	ws.handleDoorcallAnswer(rec, httptest.NewRequest(http.MethodPost, "/api/doorcall/answer", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("esperaba 503, got %d", rec.Code)
	}
}

func TestDoorcallAnswer_409SiNoRinging(t *testing.T) {
	ws := newWS(t)
	ws.SetAnswerer(&fakeAnswerer{state: "idle", answerErr: errors.New("no hay llamada en RINGING")})
	rec := httptest.NewRecorder()
	ws.handleDoorcallAnswer(rec, httptest.NewRequest(http.MethodPost, "/api/doorcall/answer", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("esperaba 409, got %d", rec.Code)
	}
}

func TestDoorcallAnswer_OK(t *testing.T) {
	ws := newWS(t)
	ws.SetAnswerer(&fakeAnswerer{state: "ringing"})
	rec := httptest.NewRecorder()
	ws.handleDoorcallAnswer(rec, httptest.NewRequest(http.MethodPost, "/api/doorcall/answer", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("esperaba 200, got %d", rec.Code)
	}
}

func TestDoorcallHangup_503SiNoHayAnswerer(t *testing.T) {
	ws := newWS(t)
	rec := httptest.NewRecorder()
	ws.handleDoorcallHangup(rec, httptest.NewRequest(http.MethodPost, "/api/doorcall/hangup", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("esperaba 503, got %d", rec.Code)
	}
}

func TestDoorcallHangup_409SiNoEnLlamada(t *testing.T) {
	ws := newWS(t)
	ws.SetAnswerer(&fakeAnswerer{state: "idle", hangupErr: errors.New("no hay llamada activa")})
	rec := httptest.NewRecorder()
	ws.handleDoorcallHangup(rec, httptest.NewRequest(http.MethodPost, "/api/doorcall/hangup", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("esperaba 409, got %d", rec.Code)
	}
}

func TestDoorcallHangup_OK(t *testing.T) {
	ws := newWS(t)
	ws.SetAnswerer(&fakeAnswerer{state: "in_call"})
	rec := httptest.NewRecorder()
	ws.handleDoorcallHangup(rec, httptest.NewRequest(http.MethodPost, "/api/doorcall/hangup", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("esperaba 200, got %d", rec.Code)
	}
}

func TestDoorcallStatus_503SiNoHayAnswerer(t *testing.T) {
	ws := newWS(t)
	rec := httptest.NewRecorder()
	ws.handleDoorcallStatus(rec, httptest.NewRequest(http.MethodGet, "/api/doorcall/status", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("esperaba 503, got %d", rec.Code)
	}
}

func TestDoorcallStatus_OK(t *testing.T) {
	ws := newWS(t)
	ws.SetAnswerer(&fakeAnswerer{state: "in_call"})
	rec := httptest.NewRecorder()
	ws.handleDoorcallStatus(rec, httptest.NewRequest(http.MethodGet, "/api/doorcall/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("esperaba 200, got %d", rec.Code)
	}
}

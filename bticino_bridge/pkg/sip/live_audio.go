package sip

import (
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// LiveAudioOnCall arma el audio en vivo de la puerta durante una llamada mediante
// un espejo PASIVO: tap del leg de audio nativo en lo:5000 (Speex PT110 en claro)
// → relay de audio del RTSP (10000). CERO decode/transcode (el códec ya casa) y
// CERO comandos al bus. Espejo de LiveVideoOnCall, sin el CallActiveMarker (el
// audio no dispara self-INVITE). go2rtc publica /doorbell (audio=pcma) a HA.
type LiveAudioOnCall struct {
	tap    VideoTap
	logger *logrus.Logger

	mu     sync.Mutex
	active bool
	guard  time.Duration
	timer  *time.Timer
}

func NewLiveAudioOnCall(tap VideoTap, logger *logrus.Logger) *LiveAudioOnCall {
	if logger == nil {
		logger = logrus.New()
	}
	return &LiveAudioOnCall{tap: tap, logger: logger}
}

// SetGuardTimeout activa un desarme de seguridad si no llega el fin de llamada.
// 0 lo desactiva. Llamar antes de Start.
func (l *LiveAudioOnCall) SetGuardTimeout(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.guard = d
}

// Start arma el espejo de audio. Idempotente. Leak-safe: si un Stop gana la
// carrera mientras tap.Start() está en curso, se re-chequea y se para el tap.
func (l *LiveAudioOnCall) Start(reason string) {
	l.mu.Lock()
	if l.active {
		l.mu.Unlock()
		return
	}
	l.active = true
	if l.guard > 0 {
		l.timer = time.AfterFunc(l.guard, func() {
			l.logger.Warn("live-audio: guard vencido sin fin de llamada — desarmando")
			l.Stop()
		})
	}
	l.mu.Unlock()

	l.logger.Infof("live-audio: armando espejo pasivo lo:5000 → relay (motivo=%s)", reason)
	if l.tap != nil {
		if err := l.tap.Start(); err != nil {
			l.logger.WithError(err).Warn("live-audio: tap no disponible — degradando sin audio")
		}
	}
	// Fix del leak: si un Stop concurrente volteó active mientras tap.Start()
	// estaba en curso, el tap habría arrancado con active=false → pararlo.
	l.mu.Lock()
	orphaned := !l.active
	l.mu.Unlock()
	if orphaned && l.tap != nil {
		l.logger.Warn("live-audio: Stop ganó la carrera durante Start — parando el tap huérfano")
		l.tap.Stop()
	}
}

// Stop desarma (para el tap). Idempotente.
func (l *LiveAudioOnCall) Stop() {
	l.mu.Lock()
	if !l.active {
		l.mu.Unlock()
		return
	}
	l.active = false
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
	l.mu.Unlock()

	if l.tap != nil {
		l.tap.Stop()
	}
	l.logger.Info("live-audio: desarmado")
}

func (l *LiveAudioOnCall) IsActive() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active
}

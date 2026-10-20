package sip

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"bticino_bridge/pkg/bticino"
	"bticino_bridge/pkg/config"
	"bticino_bridge/pkg/events"

	"github.com/sirupsen/logrus"
)

// ErrEyeBusy indica que ya hay un ojo ("Ver placa") en curso: Show es idempotente.
var ErrEyeBusy = errors.New("panelview: ya hay un ojo activo")

// EyeOriginator origina el diálogo SIP del ojo contra el Flexisip local.
// Lo satisface *BTicinoSIPClient.
type EyeOriginator interface {
	InviteWithSDP(target, sdp string) (string, error)
	Hangup() error
	CallID() string
}

// AudioForwarder reenvía el SRTP entrante de la placa al relay de audio (OÍR).
// Lo satisface *AudioReceiver.
type AudioForwarder interface {
	CaptureForward(localHost string, localPort int, dstHostPort string, ans AudioAnswer) error
	Stop()
}

// CallActiveChecker consulta si hay una llamada NATIVA/contestador en curso, para
// no pelear con el nativo (no-interferencia). OJO: es distinto de CallActiveMarker
// (ese es el SETTER SetCallActive que usa LiveVideoOnCall); aquí hace falta LEER.
// Lo satisface *EnhancedRTSPServer vía IsCallActive().
type CallActiveChecker interface {
	IsCallActive() bool
}

// PanelView es la FSM de "Ver placa": origina el ojo (autoaccensione a demanda),
// arma las 3 pistas de media (VER por taps, OÍR por forwarder, HABLAR vía
// TalkSender que escucha panelview.connected) y se autoapaga por timeout.
// Una sola pata: check-and-set bajo mutex, igual que AudioCall/Answerer.
type PanelView struct {
	cfg       config.PanelViewConfig
	orig      EyeOriginator
	fwd       AudioForwarder
	own       OWNSender
	marker    CallActiveChecker
	bus       events.EventBus
	localIP   string
	audioPort int
	videoPort int
	logger    *logrus.Logger

	timeout time.Duration

	mu     sync.Mutex
	active bool
	timer  *time.Timer
}

// NewPanelView construye la FSM. own y marker pueden ser nil.
func NewPanelView(cfg config.PanelViewConfig, orig EyeOriginator, fwd AudioForwarder, own OWNSender, marker CallActiveChecker, bus events.EventBus, localIP string, audioPort, videoPort int, logger *logrus.Logger) *PanelView {
	if logger == nil {
		logger = logrus.New()
	}
	return &PanelView{
		cfg: cfg, orig: orig, fwd: fwd, own: own, marker: marker, bus: bus,
		localIP: localIP, audioPort: audioPort, videoPort: videoPort, logger: logger,
		timeout: time.Duration(cfg.TimeoutSeconds) * time.Second,
	}
}

// setTimeoutForTest ajusta el auto-off (solo tests).
func (p *PanelView) setTimeoutForTest(d time.Duration) { p.timeout = d }

// Start suscribe la FSM a los eventos del bus.
func (p *PanelView) Start() error {
	p.bus.Subscribe("panelview.request", func(*events.Event) { _ = p.Show() })
	p.bus.Subscribe("doorcall.hangup.request", func(*events.Event) { _ = p.Hangup() }) // botón Colgar existente
	p.bus.Subscribe("doorcall.incoming", func(*events.Event) { _ = p.Hangup() })       // cede al timbre real
	// El nativo colgó el ojo (BYE remoto, lo detecta el vigilante del cliente SIP):
	// desmontar sin mandar BYE propio (el cliente ya marcó la sesión cerrada).
	p.bus.Subscribe("sip.dialog.ended", func(e *events.Event) {
		d, _ := e.Data.(map[string]interface{})
		if cid, _ := d["call_id"].(string); cid != "" && p.orig != nil && cid == p.orig.CallID() {
			_ = p.teardown("remote_bye")
		}
	})
	return nil
}

// Active indica si hay un ojo en curso.
func (p *PanelView) Active() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.active }

func (p *PanelView) reject(reason string) error {
	p.logger.Warnf("panelview: rechazado (%s)", reason)
	p.bus.Publish("panelview.rejected", map[string]interface{}{"reason": reason})
	return fmt.Errorf("panelview: %s", reason)
}

// Show origina el ojo. Idempotente: ErrEyeBusy si ya hay uno activo.
func (p *PanelView) Show() error {
	// Guardas fail-closed.
	if !p.cfg.Enabled || p.cfg.DEVADDR == "" {
		return p.reject("deshabilitado o sin devaddr")
	}
	p.mu.Lock()
	if p.active {
		// Primero el ojo propio: con el ojo abierto el canal nativo está arriba
		// (callActive), y antes se rechazaba como "llamada nativa activa" y se
		// publicaba panelview.rejected (sensor HA a idle con el ojo abierto).
		p.mu.Unlock()
		p.logger.Info("panelview: ya hay un ojo activo; se ignora la pulsación")
		return ErrEyeBusy
	}
	if p.marker != nil && p.marker.IsCallActive() {
		p.mu.Unlock()
		return p.reject("llamada nativa/contestador activa")
	}
	p.active = true
	p.mu.Unlock()

	key, err := NewSRTPMasterKey()
	if err != nil {
		p.release()
		return fmt.Errorf("clave SRTP: %w", err)
	}
	offer := BuildEyeOfferSDP(p.localIP, p.audioPort, p.videoPort, key, p.cfg.DEVADDR)
	answerSDP, err := p.orig.InviteWithSDP("c300x", offer)
	if err != nil {
		p.release()
		// Show() se llama desde el bus con el error descartado: dejar rastro aquí.
		p.logger.WithError(err).Warn("panelview: INVITE del ojo falló")
		return fmt.Errorf("INVITE ojo: %w", err)
	}
	ans, err := ParseAudioAnswer(answerSDP)
	if err != nil {
		_ = p.orig.Hangup() // no dejar diálogo huérfano
		p.release()
		p.logger.WithError(err).Warn("panelview: answer SDP del ojo inválido; colgado")
		return fmt.Errorf("parse answer: %w", err)
	}
	if p.cfg.SendWho8Arm && p.own != nil {
		if err := p.own.SendOpenWebNetCommand(bticino.CmdAnswerPickup); err != nil {
			p.logger.WithError(err).Debug("panelview: arma WHO=8 sin ACK (continuo)")
		}
	}
	callID := p.orig.CallID()

	// Sección crítica: el INVITE corrió FUERA del mutex; un Hangup (p.ej. un
	// doorcall.incoming que cede, o el botón Colgar) pudo colarse y poner
	// active=false mientras esperábamos el answer. Re-comprobamos active y
	// armamos media + timer bajo el MISMO lock, de forma atómica: así "el ojo
	// cede" nunca deja media/timer huérfanos armados con active==false.
	p.mu.Lock()
	if !p.active {
		p.mu.Unlock()
		_ = p.orig.Hangup() // cerrar el diálogo SIP recién abierto (no dejarlo huérfano)
		p.logger.Info("panelview: ojo cedido durante el INVITE; cerrado sin armar media")
		return nil
	}
	p.bus.Publish("panelview.started", map[string]interface{}{"call_id": callID})
	// VER: armar los taps pasivos (lo:5007→10002 vídeo, lo:5000→10000 audio aunque mudo).
	p.bus.Publish("sip.call.connected", map[string]interface{}{"reason": "panelview"})
	// OÍR: reenviar el SRTP entrante (clave de la placa en ans) a la entrada PRIORITARIA
	// del relay de audio: desplaza al tap mudo de lo:5000 y sale como PT110 del SDP.
	go func() {
		if err := p.fwd.CaptureForward(p.localIP, p.audioPort, fmt.Sprintf("127.0.0.1:%d", EyeAudioRelayPort), ans); err != nil {
			p.logger.WithError(err).Debug("panelview: forwarder OÍR terminó")
		}
	}()
	// HABLAR: TalkSender escucha panelview.connected (media_port DEBE ser int).
	p.bus.Publish("panelview.connected", map[string]interface{}{
		"call_id": callID, "media_host": "127.0.0.1", "media_port": ans.Port, "srtp_key": key,
		"media_pt": ans.PayloadType, // PT negociado (speex del ojo); HABLAR lo usa en -payload_type
	})
	// Auto-off (dentro de la misma sección crítica).
	p.timer = time.AfterFunc(p.timeout, func() { _ = p.Hangup() })
	p.mu.Unlock()
	p.logger.Infof("panelview: ojo abierto hacia placa %s (auto-off %s)", p.cfg.DEVADDR, p.timeout)
	return nil
}

func (p *PanelView) release() { p.mu.Lock(); p.active = false; p.mu.Unlock() }

// Hangup cierra el ojo. Teardown idempotente (no-op si ya está cerrado).
func (p *PanelView) Hangup() error { return p.teardown("panelview") }

// teardown desmonta el ojo; reason viaja en panelview.ended. Idempotente.
func (p *PanelView) teardown(reason string) error {
	p.mu.Lock()
	if !p.active {
		p.mu.Unlock()
		return nil
	}
	p.active = false
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.mu.Unlock()

	// Captura el call_id ANTES del BYE: TalkSender.onEnded lo guarda como
	// lastEndedCallID para romper la guarda anti-ffmpeg-huérfano si el EventBus
	// reordena un panelview.ended delante del panelview.connected del mismo ojo.
	// Con call_id vacío esa guarda nunca casaría y dejaría talk-back huérfano.
	cid := ""
	if p.orig != nil {
		cid = p.orig.CallID()
	}
	if p.fwd != nil {
		p.fwd.Stop()
	}
	if p.orig != nil {
		if err := p.orig.Hangup(); err != nil {
			p.logger.WithError(err).Debug("panelview: BYE con error")
		}
	}
	p.bus.Publish("sip.call.ended", map[string]interface{}{"reason": "panelview"})
	p.bus.Publish("panelview.ended", map[string]interface{}{"reason": reason, "call_id": cid})
	p.logger.Infof("panelview: ojo cerrado (%s)", reason)
	return nil
}

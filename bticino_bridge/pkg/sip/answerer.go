package sip

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"bticino_bridge/pkg/config"
	"bticino_bridge/pkg/events"
)

var (
	// ErrNotRinging se devuelve al contestar cuando no hay una llamada en RINGING.
	ErrNotRinging = errors.New("no hay llamada en RINGING")
	// ErrNotInCall se devuelve al colgar cuando no hay llamada en curso.
	ErrNotInCall = errors.New("no hay llamada en curso")
	// errTransportClosed marca el cierre del transporte (equivalente a io.EOF del socket).
	errTransportClosed = errors.New("transporte cerrado")
)

type answererState int

const (
	stIdle answererState = iota
	stRinging
	stAnswering
	stInCall
)

// AnswererTransport abstrae el socket SIP (permite un fake en tests). Recv bloquea y devuelve
// un mensaje o error (errTransportClosed al cerrarse).
type AnswererTransport interface {
	Send(raw string) error
	Recv() (*SIPMessage, error)
	LocalHost() string
	LocalPort() int
	Close() error
}

// Answerer es la máquina de estados del contestador SIP: registra el AOR contra el Flexisip
// local, atiende el INVITE del fork del timbre (100/180), y a la orden del usuario contesta
// (200 OK con SDP propio) o cuelga (BYE). Un único read-loop (run) despacha los mensajes
// entrantes; Answer/Hangup mutan el estado bajo el mismo mutex.
type Answerer struct {
	cfg    config.AnswererConfig
	domain string
	tr     AnswererTransport
	bus    events.EventBus
	logger *logrus.Logger
	now    func() time.Time // inyectable para test del guard/branch

	mu         sync.Mutex
	state      answererState
	dialog     AnswererDialog // la llamada actual (RINGING+)
	offer      string         // SDP offer del INVITE en curso
	lastResp   string         // última respuesta enviada al INVITE (para dedupe de retransmisiones)
	registered bool
	byeCSeq    int
	regCSeq    int
	guardTimer *time.Timer
	ringTimer  *time.Timer // guarda de timbre: cierra con 480 un RINGING sin CANCEL (ring_timeout_seconds)
	drain      *mediaDrain // (T6) drenaje UDP de la media SRTP directa (best-effort, ver Answer())
	stopCh     chan struct{}
	stopped    bool
	branchSeq  int
	tagSeq     int
	regWG      sync.WaitGroup // sincroniza el cierre de la goroutine de re-REGISTER en Stop()
}

// answererRegisterExpires es el Expires anunciado en cada REGISTER (segundos). El re-REGISTER se
// dispara a la mitad de este intervalo para refrescar el binding antes de que el registrar lo
// expire.
const answererRegisterExpires = 3600

// NewAnswerer construye el contestador. El transporte concreto (TCP contra Flexisip) lo aporta T5;
// en tests se inyecta un fake.
func NewAnswerer(cfg config.AnswererConfig, domain string, transport AnswererTransport, bus events.EventBus, logger *logrus.Logger) *Answerer {
	if logger == nil {
		logger = logrus.New()
	}
	return &Answerer{
		cfg: cfg, domain: domain, tr: transport, bus: bus, logger: logger,
		now: time.Now, state: stIdle, stopCh: make(chan struct{}),
	}
}

// Start arranca el read-loop, envía el REGISTER inicial y suscribe las órdenes del bus.
func (a *Answerer) Start() error {
	if err := a.registerOnce(); err != nil {
		a.logger.Warnf("[ANSWERER] registro inicial falló: %v (se reintenta en el próximo tick de re-REGISTER)", err)
	}
	if a.bus != nil {
		a.bus.Subscribe("doorcall.answer.request", func(*events.Event) { _ = a.Answer() })
		a.bus.Subscribe("doorcall.hangup.request", func(*events.Event) { _ = a.Hangup() })
	}
	go a.run()
	a.regWG.Add(1)
	go a.reregisterLoop()
	return nil
}

// reregisterLoop refresca periódicamente el registro del AOR (cada expires/2) para que el
// binding no caduque en el registrar. Se detiene limpiamente cuando se cierra stopCh (Stop()).
func (a *Answerer) reregisterLoop() {
	defer a.regWG.Done()
	interval := (answererRegisterExpires / 2) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-a.stopCh:
			return
		case <-ticker.C:
			if err := a.registerOnce(); err != nil {
				a.logger.Warnf("[ANSWERER] re-REGISTER falló: %v", err)
			}
		}
	}
}

// run lee mensajes del transporte y los despacha hasta que el transporte se cierra.
func (a *Answerer) run() {
	for {
		select {
		case <-a.stopCh:
			return
		default:
		}
		m, err := a.tr.Recv()
		if err != nil {
			if a.isStopped() {
				return
			}
			a.logger.Warnf("[ANSWERER] Recv error: %v", err)
			// F1 no reconecta automáticamente: la conexión TCP inicial es la única del ciclo de
			// vida del Answerer. Si Flexisip local cae/reinicia, el registro se pierde y hay que
			// reiniciar el bridge para volver a registrar (mejora futura, fuera de alcance F1).
			return
		}
		a.dispatch(m)
	}
}

// dispatch procesa un mensaje entrante bajo el mutex.
func (a *Answerer) dispatch(m *SIPMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch strings.ToUpper(m.Method) {
	case "INVITE":
		a.onInvite(m)
	case "CANCEL":
		a.onCancel(m)
	case "ACK":
		if a.state == stAnswering && a.sameDialog(m) {
			a.toInCall()
		}
	case "BYE":
		if a.sameDialog(m) && (a.state == stInCall || a.state == stAnswering) {
			// El 200 a un BYE debe reflejar el CSeq del propio BYE, no el del INVITE.
			byeDlg := a.dialog
			if c := m.Headers["CSeq"]; c != "" {
				byeDlg.CSeq = c
			}
			if err := a.tr.Send(BuildAnswererResponse("200", "OK", byeDlg, "", "")); err != nil {
				a.logger.Warnf("[ANSWERER] Send 200-a-BYE falló: %v", err)
			}
			a.endCall("hangup_remote")
		}
	case "":
		// Respuesta (a REGISTER/BYE/...) o keepalive (bare-CRLF, sin CSeq). Solo nos interesa
		// aquí la respuesta al REGISTER: confirma/retira el registro del AOR. El resto
		// (200 al BYE, etc.) no requiere acción en el read-loop.
		// OJO: dispatch ya tiene a.mu tomado (defer arriba) — set directo del campo, NUNCA
		// a.setRegistered() (esa toma el lock ella misma: con sync.Mutex no reentrante,
		// llamarla desde aquí bloquearía este goroutine para siempre en cada REGISTER 200).
		if cseq := m.Headers["CSeq"]; strings.Contains(cseq, "REGISTER") {
			a.registered = strings.HasPrefix(m.StatusCode, "2")
		}
	}
}

// onInvite atiende un INVITE entrante. Asume el mutex tomado.
func (a *Answerer) onInvite(m *SIPMessage) {
	callID := m.Headers["Call-ID"]
	cseq := m.Headers["CSeq"]
	if a.state != stIdle {
		if callID == a.dialog.CallID && cseq == a.dialog.CSeq {
			// Retransmisión (mismo Call-ID+CSeq): reenviar la última respuesta, sin cambiar de estado.
			if a.lastResp != "" {
				if err := a.tr.Send(a.lastResp); err != nil {
					a.logger.Warnf("[ANSWERER] Send retransmisión (reenvío última respuesta) falló: %v", err)
				}
			}
			return
		}
		// Otra llamada mientras estamos ocupados → 486 Busy Here (no cambia estado).
		busy := a.dialogFromInvite(m)
		if err := a.tr.Send(BuildAnswererResponse("486", "Busy Here", busy, "", "")); err != nil {
			a.logger.Warnf("[ANSWERER] Send 486 falló: %v", err)
		}
		return
	}
	// Nueva llamada: capturamos el diálogo y respondemos 100/180.
	a.dialog = a.dialogFromInvite(m)
	a.offer = m.Body
	if err := a.tr.Send(BuildAnswererResponse("100", "Trying", a.dialog, "", "")); err != nil {
		a.logger.Warnf("[ANSWERER] Send 100 Trying falló: %v", err)
	}
	ringing := BuildAnswererResponse("180", "Ringing", a.dialog, "", "")
	if err := a.tr.Send(ringing); err != nil {
		a.logger.Warnf("[ANSWERER] Send 180 Ringing falló: %v", err)
	}
	a.lastResp = ringing
	a.state = stRinging
	// Guarda de timbre: si nadie contesta y el CANCEL de la placa se pierde, no podemos
	// quedarnos en RINGING (contestaríamos 486 a todos los timbrazos siguientes).
	if secs := a.cfg.RingTimeoutSeconds; secs > 0 {
		a.ringTimer = time.AfterFunc(time.Duration(secs)*time.Second, a.ringTimeout)
	}
	a.publish("doorcall.incoming", map[string]interface{}{
		"call_id":   callID,
		"call_type": ClassifyCallType(a.offer), // placa / intercom / desconocido
	})
}

// onCancel atiende un CANCEL entrante. Asume el mutex tomado.
func (a *Answerer) onCancel(m *SIPMessage) {
	if a.state == stRinging && a.sameDialog(m) {
		// El 200 a un CANCEL debe reflejar el CSeq ("20 CANCEL") y las Via del propio CANCEL,
		// no los del INVITE guardado en el diálogo.
		cancelDlg := a.dialog
		cancelDlg.Via = m.ViaHeaders
		if c := m.Headers["CSeq"]; c != "" {
			cancelDlg.CSeq = c
		}
		if err := a.tr.Send(BuildAnswererResponse("200", "OK", cancelDlg, "", "")); err != nil { // 200 al CANCEL
			a.logger.Warnf("[ANSWERER] Send 200-a-CANCEL falló: %v", err)
		}
		// El 487 SIGUE usando a.dialog (es la respuesta al INVITE original, correcto).
		if err := a.tr.Send(BuildAnswererResponse("487", "Request Terminated", a.dialog, "", "")); err != nil { // 487 al INVITE
			a.logger.Warnf("[ANSWERER] Send 487 falló: %v", err)
		}
		a.endCall("cancelled")
	}
}

// Answer contesta la llamada en RINGING enviando 200 OK con SDP propio. El ACK llega por el loop.
func (a *Answerer) Answer() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != stRinging {
		return ErrNotRinging
	}
	key, err := GenerateSRTPKey()
	if err != nil {
		return err
	}
	vp := a.cfg.VideoPort
	if vp < 0 {
		vp = 0 // -1 = audio-only: no anunciamos m=video
	}
	sdp := BuildAnswerSDP("127.0.0.1", a.cfg.AudioPort, vp, key)
	contact := fmt.Sprintf("<sip:%s@%s:%d;transport=tcp>", a.cfg.User, a.tr.LocalHost(), a.tr.LocalPort())
	resp := BuildAnswererResponse("200", "OK", a.dialog, sdp, contact)
	// El 200 va al hilo ANTES de transitar: si el Send falla, seguimos en RINGING para poder
	// reintentar/colgar, y devolvemos el error en vez de anunciar éxito falso.
	if err := a.tr.Send(resp); err != nil {
		a.logger.Warnf("[ANSWERER] Send 200 OK falló: %v", err)
		return err
	}
	a.lastResp = resp
	a.state = stAnswering
	a.stopRingTimer() // contestada: ya no aplica la guarda de timbre
	// Guard anti-placa-ocupada/no-ACK: se arma en Answer (no en toInCall) para que un
	// stAnswering colgado (200 enviado, ACK que nunca llega) también se auto-cure a los
	// max_call_seconds. guardHangup ya cubre stAnswering y stInCall.
	if secs := a.cfg.MaxCallSeconds; secs > 0 {
		a.guardTimer = time.AfterFunc(time.Duration(secs)*time.Second, a.guardHangup)
	}
	// Drenaje de media best-effort: si el puerto ya está ocupado (p. ej. entre tests, o en
	// producción si algo lo retiene), NO fallamos la respuesta ya enviada — solo avisamos.
	if d, derr := startMediaDrain(a.cfg.AudioPort, vp, a.logger); derr != nil {
		a.logger.Warnf("[ANSWERER] drenaje no disponible: %v", derr)
	} else {
		a.drain = d
	}
	remotePort := 0
	if rp, perr := ParseRemoteAudioPort(a.offer); perr == nil {
		remotePort = rp
	} else {
		a.logger.Warnf("[ANSWERER] no se pudo parsear el puerto de audio del offer (F2 no podrá hablar): %v", perr)
	}
	a.publish("doorcall.answered", map[string]interface{}{
		"call_id":    a.dialog.CallID,
		"media_host": "127.0.0.1",
		"media_port": remotePort,
		"srtp_key":   key, // la clave que anunciamos en el 200 OK (cifra lo que ENVIAMOS)
	})
	return nil
}

// toInCall confirma la llamada tras el ACK. El guard ya se armó en Answer (cubre el caso
// no-ACK), así que aquí solo transitamos. Asume el mutex tomado.
func (a *Answerer) toInCall() {
	a.state = stInCall
}

// Hangup cuelga la llamada en curso enviando BYE hacia la placa.
func (a *Answerer) Hangup() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != stInCall && a.state != stAnswering {
		return ErrNotInCall
	}
	a.sendBYE()
	a.endCall("hangup_local")
	return nil
}

// guardHangup lo dispara el guardTimer en su propia goroutine: toma el mutex y cuelga si aún hay llamada.
func (a *Answerer) guardHangup() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != stInCall && a.state != stAnswering {
		return // ya se colgó por otra vía: no doble-BYE
	}
	a.sendBYE()
	a.endCall("guard_timeout")
}

// ringTimeout lo dispara el ringTimer en su propia goroutine. Si la llamada sigue en RINGING
// (nadie contestó y no llegó CANCEL), cierra el INVITE con 480 y vuelve a idle.
func (a *Answerer) ringTimeout() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped || a.state != stRinging {
		return // contestador parado, o llamada ya contestada/cancelada/colgada por otra vía
	}
	if err := a.tr.Send(BuildAnswererResponse("480", "Temporarily Unavailable", a.dialog, "", "")); err != nil {
		a.logger.Warnf("[ANSWERER] Send 480 (ring timeout) falló: %v", err)
	}
	a.endCall("ring_timeout")
}

// stopRingTimer desarma la guarda de timbre si está armada. Asume el mutex tomado.
func (a *Answerer) stopRingTimer() {
	if a.ringTimer != nil {
		a.ringTimer.Stop()
		a.ringTimer = nil
	}
}

// sendBYE construye y envía el BYE in-dialog. Asume el mutex tomado.
func (a *Answerer) sendBYE() {
	a.byeCSeq++
	branch := a.nextBranch()
	bye := BuildAnswererBYE(a.dialog, a.cfg.User, a.domain, a.tr.LocalHost(), a.tr.LocalPort(), a.byeCSeq, branch)
	if err := a.tr.Send(bye); err != nil {
		a.logger.Warnf("[ANSWERER] Send BYE falló: %v", err)
	}
}

// endCall limpia el estado tras cerrarse la llamada y publica doorcall.ended. Asume el mutex tomado.
func (a *Answerer) endCall(reason string) {
	callID := a.dialog.CallID // capturar ANTES de limpiar el diálogo, para correlar con doorcall.answered
	if a.guardTimer != nil {
		a.guardTimer.Stop()
		a.guardTimer = nil
	}
	a.stopRingTimer()
	if a.drain != nil {
		a.drain.Stop()
		a.drain = nil
	}
	a.state = stIdle
	a.dialog = AnswererDialog{}
	a.offer = ""
	a.lastResp = ""
	a.publish("doorcall.ended", map[string]interface{}{"reason": reason, "call_id": callID})
}

// dialogFromInvite construye un AnswererDialog a partir de un INVITE entrante, con to-tag propio.
func (a *Answerer) dialogFromInvite(m *SIPMessage) AnswererDialog {
	return AnswererDialog{
		CallID:      m.Headers["Call-ID"],
		FromHdr:     m.Headers["From"],
		ToHdr:       m.Headers["To"],
		CSeq:        m.Headers["CSeq"],
		Via:         m.ViaHeaders,
		RecordRoute: m.RecordRoute,
		ToTag:       a.newTag(),
	}
}

// sameDialog indica si el mensaje pertenece al diálogo en curso (por Call-ID).
func (a *Answerer) sameDialog(m *SIPMessage) bool {
	id := m.Headers["Call-ID"]
	return id != "" && id == a.dialog.CallID
}

// registerOnce envía un REGISTER del AOR del contestador. El 200 lo confirma el read-loop
// (dispatch, case "") marcando a.registered directamente bajo el mutex ya tomado.
func (a *Answerer) registerOnce() error {
	a.mu.Lock()
	a.regCSeq++
	cseq := a.regCSeq
	branch := a.nextBranch()
	callID := fmt.Sprintf("reg-%d@%s", a.now().UnixNano(), a.tr.LocalHost())
	fromTag := fmt.Sprintf("t%d", a.now().UnixNano())
	a.mu.Unlock()
	reg := BuildAnswererRegister(a.cfg.User, a.domain, a.tr.LocalHost(), a.tr.LocalPort(), answererRegisterExpires, branch, callID, fromTag, cseq)
	return a.tr.Send(reg)
}

// Stop cuelga si hay llamada, marca la parada y cierra el transporte.
func (a *Answerer) Stop() error {
	a.mu.Lock()
	if a.state == stInCall || a.state == stAnswering {
		a.sendBYE()
		a.endCall("hangup_local")
	}
	a.stopRingTimer() // una guarda de timbre pendiente no puede dispararse tras el Stop
	if !a.stopped {
		a.stopped = true
		close(a.stopCh)
	}
	a.mu.Unlock()
	a.regWG.Wait() // espera a que reregisterLoop salga limpio antes de cerrar el transporte
	return a.tr.Close()
}

// StateString devuelve el estado público del contestador.
func (a *Answerer) StateString() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch a.state {
	case stRinging:
		return "ringing"
	case stAnswering, stInCall:
		return "in_call"
	default:
		return "idle"
	}
}

// IsRegistered indica si el AOR está registrado (lo marca dispatch al ver el 200 del REGISTER).
func (a *Answerer) IsRegistered() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.registered
}

func (a *Answerer) isStopped() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopped
}

// nextBranch genera un branch RFC-3261 único. Asume el mutex tomado.
func (a *Answerer) nextBranch() string {
	a.branchSeq++
	return fmt.Sprintf("z9hG4bK%d%d", a.now().UnixNano(), a.branchSeq)
}

// newTag genera un tag único para el To propio. Asume el mutex tomado.
func (a *Answerer) newTag() string {
	a.tagSeq++
	return fmt.Sprintf("bt%d%d", a.now().UnixNano(), a.tagSeq)
}

// publish emite un evento al bus si hay bus.
func (a *Answerer) publish(name string, data interface{}) {
	if a.bus != nil {
		a.bus.Publish(name, data)
	}
}

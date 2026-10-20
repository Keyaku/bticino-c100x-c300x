package sip

import (
	"errors"
	"fmt"
	"sync"

	"bticino_bridge/pkg/bticino"

	"github.com/sirupsen/logrus"
)

// CallOriginator origina un INVITE con SDP y puede cerrar el diálogo (Hangup ya
// existe en *BTicinoSIPClient: envía BYE con el Call-ID/tag del diálogo actual).
type CallOriginator interface {
	InviteWithSDP(target, sdp string) (answerSDP string, err error)
	Hangup() error
}

type OWNSender interface{ SendOpenWebNetCommand(cmd string) error }

type AudioPlayer interface {
	Play(wavPath, host string, ans AudioAnswer) error
	Stop()
}

// ErrAudioLegBusy: ya hay una pata de audio activa (coexistencia: una sola pata).
var ErrAudioLegBusy = errors.New("audio: ya hay una pata de audio activa")

type AudioCall struct {
	orig      CallOriginator
	own       OWNSender
	player    AudioPlayer
	capturer  AudioCapturer
	localIP   string
	audioPort int
	logger    *logrus.Logger

	mu     sync.Mutex
	active bool
}

func NewAudioCall(orig CallOriginator, own OWNSender, player AudioPlayer, capturer AudioCapturer, localIP string, audioPort int, logger *logrus.Logger) *AudioCall {
	if logger == nil {
		logger = logrus.New()
	}
	return &AudioCall{orig: orig, own: own, player: player, capturer: capturer, localIP: localIP, audioPort: audioPort, logger: logger}
}

// Say (talk-back v2, compat): pata de solo-envío que reproduce un WAV a la puerta.
// Ocupa la pata única (ErrAudioLegBusy) y cuelga SIEMPRE al terminar: antes el
// diálogo quedaba vivo hasta que el nativo lo cortaba, y el vigilante del cliente
// SIP retenía el socket webrtc (re-REGISTER aplazado) todo ese tiempo.
func (a *AudioCall) Say(wavPath string) error {
	a.mu.Lock()
	if a.active {
		a.mu.Unlock()
		return ErrAudioLegBusy
	}
	a.active = true
	a.mu.Unlock()
	defer a.releaseActive()

	key, err := NewSRTPMasterKey()
	if err != nil {
		return fmt.Errorf("clave SRTP: %w", err)
	}
	offer := BuildAudioOfferSDP(a.localIP, a.audioPort, key)
	answerSDP, err := a.orig.InviteWithSDP("c300x", offer)
	if err != nil {
		return fmt.Errorf("INVITE c300x: %w", err)
	}
	// Hay diálogo vivo (200 OK): colgar al salir, pase lo que pase.
	defer func() {
		if herr := a.orig.Hangup(); herr != nil {
			a.logger.WithError(herr).Debug("audio-call: BYE al terminar Say")
		}
	}()
	ans, err := ParseAudioAnswer(answerSDP)
	if err != nil {
		return fmt.Errorf("parse answer SDP: %w", err)
	}
	if a.own != nil {
		if err := a.own.SendOpenWebNetCommand(bticino.CmdAnswerPickup); err != nil {
			a.logger.WithError(err).Debug("audio-call: WHO=8 arm sin ACK (continuo)")
		}
	}
	return a.player.Play(wavPath, "127.0.0.1", ans)
}

// OpenDuplex abre la pata full-duplex: INVITE c300x → answer → arm WHO=8 →
// RECEIVER (siempre, → sinkPath) y SENDER (si talkWav != "") en paralelo.
// Devuelve la AudioAnswer negociada. Una sola pata activa (ErrAudioLegBusy).
func (a *AudioCall) OpenDuplex(sinkPath, talkWav string) (AudioAnswer, error) {
	// Reserva la pata en la MISMA sección crítica que el check (evita TOCTOU:
	// dos llamadas concurrentes no deben pasar ambas, y un Close() concurrente
	// no debe ver active=false mientras el INVITE está en vuelo).
	a.mu.Lock()
	if a.active {
		a.mu.Unlock()
		return AudioAnswer{}, ErrAudioLegBusy
	}
	a.active = true
	a.mu.Unlock()

	key, err := NewSRTPMasterKey()
	if err != nil {
		a.releaseActive()
		return AudioAnswer{}, fmt.Errorf("clave SRTP: %w", err)
	}
	offer := BuildAudioOfferSDP(a.localIP, a.audioPort, key)
	answerSDP, err := a.orig.InviteWithSDP("c300x", offer)
	if err != nil {
		a.releaseActive()
		return AudioAnswer{}, fmt.Errorf("INVITE c300x: %w", err)
	}
	ans, err := ParseAudioAnswer(answerSDP)
	if err != nil {
		// El INVITE llegó a 200 OK (hay diálogo SIP vivo) pero no pudimos
		// parsear el answer: colgar para no dejar un diálogo huérfano en el
		// dispositivo (restricción de no-interferencia).
		if a.orig != nil {
			if herr := a.orig.Hangup(); herr != nil {
				a.logger.WithError(herr).Debug("audio-call: BYE tras fallo de parse de answer")
			}
		}
		a.releaseActive()
		return AudioAnswer{}, fmt.Errorf("parse answer SDP: %w", err)
	}
	if a.own != nil {
		if err := a.own.SendOpenWebNetCommand(bticino.CmdAnswerPickup); err != nil {
			a.logger.WithError(err).Debug("audio-call: WHO=8 arm sin ACK (continuo)")
		}
	}

	// Receiver siempre; bloquea → goroutine. Lo corta Close(), pero también
	// puede retornar por sí solo (timeout duro de 60s en Capture, o ffmpeg
	// muerto por su cuenta). En ese caso nadie más va a liberar la pata: hay
	// que cerrar aquí también, si no queda encallada (active=true para
	// siempre, diálogo SIP huérfano, y todo OpenDuplex posterior devuelve
	// ErrAudioLegBusy hasta el próximo /stop manual). Close() es idempotente
	// (guardado por el flag active bajo a.mu), así que:
	//   - /stop normal: Close() ya puso active=false y llamó a
	//     capturer.Stop() (que mata ffmpeg) ANTES de que este Capture()
	//     retorne; cuando retorna, este Close() ve active=false y es un
	//     no-op.
	//   - fin natural (timeout/ffmpeg muerto): Capture() retorna solo, este
	//     Close() es el primero en verlo y hace active=false + player.Stop()
	//     + Hangup() (BYE).
	// Solo se hace para el receiver (la dirección continua/"listen"): el
	// sender (Say/Play) es un one-shot que no debe tumbar la pata al acabar.
	go func() {
		if err := a.capturer.Capture(a.localIP, a.audioPort, sinkPath, ans); err != nil {
			a.logger.WithError(err).Debug("audio-call: receiver terminó")
		}
		if err := a.Close(); err != nil {
			a.logger.WithError(err).Debug("audio-call: Close tras fin del receiver")
		}
	}()
	// Sender opcional.
	if talkWav != "" {
		go func() {
			if err := a.player.Play(talkWav, "127.0.0.1", ans); err != nil {
				a.logger.WithError(err).Debug("audio-call: sender terminó")
			}
		}()
	}
	a.logger.Info("audio-call: pata full-duplex abierta")
	return ans, nil
}

// IsActive indica si la pata full-duplex sigue activa. Es la fuente de
// verdad para /api/audio/duplex/status: la pata puede autocerrarse (fin
// natural del receiver, ver OpenDuplex) sin pasar por /stop, así que un flag
// espejo en el webserver quedaría obsoleto tras ese cierre.
func (a *AudioCall) IsActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active
}

// releaseActive libera la reserva de pata activa (rollback ante error tras el
// check-and-set de OpenDuplex).
func (a *AudioCall) releaseActive() {
	a.mu.Lock()
	a.active = false
	a.mu.Unlock()
}

// Close para receiver+sender, envía BYE y libera. Idempotente.
func (a *AudioCall) Close() error {
	a.mu.Lock()
	if !a.active {
		a.mu.Unlock()
		return nil
	}
	a.active = false
	a.mu.Unlock()

	if a.capturer != nil {
		a.capturer.Stop()
	}
	if a.player != nil {
		a.player.Stop()
	}
	if a.orig != nil {
		if err := a.orig.Hangup(); err != nil {
			a.logger.WithError(err).Debug("audio-call: BYE con error")
		}
	}
	a.logger.Info("audio-call: pata full-duplex cerrada")
	return nil
}

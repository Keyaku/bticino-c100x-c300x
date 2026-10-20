package sip

import (
	"net"
	"time"
)

// dialogWatchPoll es el plazo de cada lectura del vigilante: acota lo que tarda
// stopDialogWatch en recuperar el socket para otro lector.
const dialogWatchPoll = 200 * time.Millisecond

// dialogWatch es el vigilante de un diálogo saliente ya conectado (el ojo).
// Tras el ACK nadie leía el socket webrtc: un BYE del nativo se quedaba sin 200
// (el bt_vct lo retransmitía) y el bridge no se enteraba de que el ojo había
// terminado. La app oficial reacciona al BYE remoto al instante.
type dialogWatch struct {
	stop chan struct{}
	done chan struct{}
}

// startDialogWatch arranca el vigilante del diálogo callID. PRECONDICIÓN: el
// llamante posee webrtcID.readMu (el bucle del INVITE) y se lo TRASPASA: el
// vigilante lo suelta al terminar. Así nunca espera el lector (no hay
// interbloqueo con stopDialogWatch) y nadie se cuela entre el ACK y la vigilancia.
func (c *BTicinoSIPClient) startDialogWatch(callID string) {
	w := &dialogWatch{stop: make(chan struct{}), done: make(chan struct{})}
	c.watchMu.Lock()
	c.watch = w
	c.watchMu.Unlock()
	go c.runDialogWatch(w, c.webrtcID, callID)
}

// stopDialogWatch para el vigilante y ESPERA a que suelte el socket. Idempotente.
// No debe llamarse con c.mutex tomado: el vigilante lo necesita al procesar un BYE.
func (c *BTicinoSIPClient) stopDialogWatch() {
	c.watchMu.Lock()
	w := c.watch
	c.watch = nil
	c.watchMu.Unlock()
	if w == nil {
		return
	}
	close(w.stop)
	<-w.done
}

func (c *BTicinoSIPClient) runDialogWatch(w *dialogWatch, id *sipIdentity, callID string) {
	defer close(w.done)
	defer id.readMu.Unlock() // lector traspasado por startDialogWatch
	conn := id.conn
	for {
		select {
		case <-w.stop:
			return
		default:
		}
		msgs, err := c.readSIPMessagesFrom(conn, dialogWatchPoll)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			c.logger.WithError(err).Debug("SIP: vigilante del diálogo termina (socket)")
			return
		}
		for _, m := range msgs {
			if m.StatusCode != "" || m.Method != "BYE" {
				c.logger.Debugf("SIP: vigilante ignora %s%s", m.Method, m.StatusCode)
				continue
			}
			c.webrtcID.mutex.Lock()
			c.sendBYEResponse(c.webrtcID, m)
			c.webrtcID.mutex.Unlock()
			if sipCallID(m) != callID {
				continue // BYE rezagado de otro diálogo: basta con el 200
			}
			c.mutex.Lock()
			if c.callID == callID {
				c.sessionActive = false
				c.callState = Registered
			}
			c.mutex.Unlock()
			c.logger.Infof("SIP: el remoto colgó el diálogo %s (BYE contestado)", callID)
			c.eventBus.PublishWithSource("sip.dialog.ended", map[string]interface{}{
				"call_id": callID,
				"reason":  "remote_bye",
			}, "sip")
			return
		}
	}
}

// sipCallID devuelve el Call-ID de un mensaje (tolerante a la capitalización).
func sipCallID(m *SIPMessage) string {
	for _, k := range []string{"Call-ID", "Call-Id", "call-id"} {
		if v := m.Headers[k]; v != "" {
			return v
		}
	}
	return ""
}

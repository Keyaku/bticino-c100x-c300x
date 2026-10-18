package sip

import (
	"encoding/binary"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// RTSPAudioPT es el PT de la pista de audio anunciada en el SDP del RTSP (speex/8000).
	RTSPAudioPT = 110
	// EyeAudioRelayPort es la entrada PRIORITARIA del relay de audio: el OÍR del ojo
	// (PanelView) reenvía aquí, y desplaza al tap de lo:5000 (mudo en modo ojo).
	EyeAudioRelayPort = 10012

	// fillPrioHold: mientras la entrada prioritaria haya enviado hace menos de esto,
	// la fuente principal se descarta (no se mezclan dos flujos en la pista).
	fillPrioHold = 200 * time.Millisecond
	// fillIdle: sin audio de ninguna fuente durante esto => se emite silencio.
	fillIdle = 60 * time.Millisecond
)

// relayFill es el estado del modo "pista de audio siempre viva" de un RTPRelay.
//
// Por qué: el stream `doorbell` de go2rtc es un ffmpeg que transcodifica el audio
// (-c:a pcm_alaw) y NO emite nada —ni vídeo— hasta recibir el primer paquete de audio
// válido (reproducido 2026-10-16). Si la pista de audio del RTSP va vacía o con un PT
// distinto del anunciado en el SDP, go2rtc mata el exec por timeout y la tarjeta HA
// se queda en negro aunque el vídeo esté llegando.
//
// En modo relleno todo lo que sale va reescrito con UN solo SSRC, secuencia continua
// y el PT del SDP; si no llega audio se inyecta silencio speex cada 20 ms. Una entrada
// prioritaria (el OÍR del ojo) desplaza a la principal (el tap de lo:5000, mudo en el ojo).
type relayFill struct {
	pt       byte
	prioPort int
	prioConn *net.UDPConn
	ssrc     uint32
	start    time.Time

	mu       sync.Mutex
	seq      uint16
	lastOut  time.Time
	lastIn   time.Time
	lastPrio time.Time
}

// EnableSilenceFill activa el modo relleno con el PT de salida pt y una entrada
// prioritaria en prioPort (0 = sin entrada prioritaria). Llamar antes de Start.
func (r *RTPRelay) EnableSilenceFill(pt byte, prioPort int) {
	r.fill = &relayFill{pt: pt, prioPort: prioPort, ssrc: rand.Uint32()}
}

// startFill abre la entrada prioritaria y arranca el reloj de silencio.
func (r *RTPRelay) startFill() error {
	f := r.fill
	f.start = time.Now()
	if f.prioPort > 0 {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: f.prioPort})
		if err != nil {
			return err
		}
		f.prioConn = c
		go r.prioLoop(c)
	}
	go r.fillTickLoop()
	return nil
}

func (r *RTPRelay) stopFill() {
	if r.fill != nil && r.fill.prioConn != nil {
		r.fill.prioConn.Close()
	}
}

// fillMain procesa un paquete de la entrada principal en modo relleno.
func (r *RTPRelay) fillMain(packet []byte) {
	f := r.fill
	payload := rtpPayload(packet)
	if payload == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if time.Since(f.lastPrio) < fillPrioHold {
		return // manda la entrada prioritaria
	}
	f.lastIn = time.Now()
	r.emitLocked(payload)
}

// prioLoop lee la entrada prioritaria (OÍR del ojo).
func (r *RTPRelay) prioLoop(c *net.UDPConn) {
	buf := make([]byte, 2048)
	for {
		n, _, err := c.ReadFromUDP(buf)
		if err != nil {
			return // cerrada por Stop
		}
		payload := rtpPayload(buf[:n])
		if payload == nil {
			continue
		}
		atomic.AddUint64(&r.packetsReceived, 1)
		f := r.fill
		f.mu.Lock()
		now := time.Now()
		f.lastPrio, f.lastIn = now, now
		r.emitLocked(payload)
		f.mu.Unlock()
	}
}

// fillTickLoop inyecta silencio cuando ninguna fuente envía audio.
func (r *RTPRelay) fillTickLoop() {
	t := time.NewTicker(keepaliveTick)
	defer t.Stop()
	for {
		select {
		case <-r.stopCh:
			return
		case <-t.C:
			if r.ConsumerCount() == 0 {
				continue
			}
			f := r.fill
			f.mu.Lock()
			now := time.Now()
			if now.Sub(f.lastIn) >= fillIdle && now.Sub(f.lastOut) >= keepaliveFrame {
				r.emitLocked(speexSilenceFrame)
			}
			f.mu.Unlock()
		}
	}
}

// emitLocked construye el RTP de salida (PT/SSRC/secuencia propios, timestamp del
// reloj real para una línea de tiempo continua) y lo reparte. Asume fill.mu tomado.
func (r *RTPRelay) emitLocked(payload []byte) {
	f := r.fill
	now := time.Now()
	pkt := make([]byte, 12+len(payload))
	pkt[0], pkt[1] = 0x80, f.pt
	binary.BigEndian.PutUint16(pkt[2:4], f.seq)
	binary.BigEndian.PutUint32(pkt[4:8], uint32(now.Sub(f.start).Nanoseconds()*keepaliveRate/int64(time.Second)))
	binary.BigEndian.PutUint32(pkt[8:12], f.ssrc)
	copy(pkt[12:], payload)
	f.seq++
	f.lastOut = now
	r.fanOut(pkt)
}

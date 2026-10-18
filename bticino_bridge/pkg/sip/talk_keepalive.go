package sip

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// speexSilenceFrame es una trama speex NB (8 kHz, 20 ms) de silencio, 38 bytes. Se obtuvo
// codificando silencio con ffmpeg/libspeex y tomando el payload RTP en régimen estable
// (el 1er paquete del encoder difiere ligeramente; del 2º en adelante es este):
//
//	ffmpeg -f lavfi -i anullsrc=r=8000:cl=mono -acodec libspeex -ar 8000 -ac 1 \
//	       -payload_type 97 -f rtp rtp://127.0.0.1:<puerto>
var speexSilenceFrame = mustHex("2e9d1b9a2002017fffffffffff8100bfffffffffffc0805fffffffffffe0402ffffffffffff7")

const (
	keepaliveFrame   = 20 * time.Millisecond // duración de una trama speex
	keepaliveMicIdle = 60 * time.Millisecond // sin voz del micro durante esto => se inyecta silencio
	keepaliveTick    = 5 * time.Millisecond  // resolución del reloj de inyección
	keepaliveRate    = 8000                  // reloj RTP de speex NB
)

// upstreamKeepalive es un relé RTP que mantiene viva la subida de audio hacia la placa.
// Escucha el micrófono de HA (RTP speex que manda go2rtc) y emite hacia ffmpeg un flujo
// CONTINUO: reenvía la voz en cuanto llega y, si el micro calla, una trama de silencio cada
// 20 ms. Así ffmpeg (que solo copia y cifra en SRTP) nunca se queda sin paquetes, y la placa
// recibe audio ascendente desde que se contesta (hipótesis RTP simétrico: no manda el audio
// de bajada hasta recibir el de subida). Mezclar en ffmpeg con amix NO sirve: amix se bloquea
// si la entrada del micro no trae paquetes (comprobado).
type upstreamKeepalive struct {
	in     *net.UDPConn
	out    *net.UDPConn
	pt     byte
	ssrc   uint32
	logger *logrus.Logger
	start  time.Time

	mu       sync.Mutex
	seq      uint16
	lastSent time.Time
	lastMic  time.Time

	stopOnce sync.Once
	done     chan struct{}
	wg       sync.WaitGroup
}

// startUpstreamKeepalive escucha en inHost:inPort (0 = efímero) y emite a 127.0.0.1:outPort
// con payload type pt. Empieza a emitir silencio inmediatamente.
func startUpstreamKeepalive(inHost string, inPort, outPort, pt int, logger *logrus.Logger) (*upstreamKeepalive, error) {
	in, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(inHost), Port: inPort})
	if err != nil {
		return nil, fmt.Errorf("keepalive: escuchar %s:%d: %w", inHost, inPort, err)
	}
	out, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: outPort})
	if err != nil {
		in.Close()
		return nil, fmt.Errorf("keepalive: salida 127.0.0.1:%d: %w", outPort, err)
	}
	k := &upstreamKeepalive{
		in:     in,
		out:    out,
		pt:     byte(pt) & 0x7F,
		ssrc:   rand.Uint32(),
		seq:    uint16(rand.Uint32()),
		logger: logger,
		start:  time.Now(),
		done:   make(chan struct{}),
	}
	k.wg.Add(2)
	go k.readLoop()
	go k.tickLoop()
	return k, nil
}

// InPort devuelve el puerto real donde escucha el micro (útil con inPort=0).
func (k *upstreamKeepalive) InPort() int {
	return k.in.LocalAddr().(*net.UDPAddr).Port
}

// Stop para el relé y libera los sockets. Idempotente.
func (k *upstreamKeepalive) Stop() {
	k.stopOnce.Do(func() {
		close(k.done)
		k.in.Close()
		k.out.Close()
		k.wg.Wait()
	})
}

// readLoop reenvía al instante cada paquete del micro (con nuestra secuencia y SSRC).
func (k *upstreamKeepalive) readLoop() {
	defer k.wg.Done()
	buf := make([]byte, 2048)
	for {
		n, _, err := k.in.ReadFromUDP(buf)
		if err != nil {
			return // socket cerrado por Stop
		}
		payload := rtpPayload(buf[:n])
		if payload == nil {
			continue
		}
		k.mu.Lock()
		k.lastMic = time.Now()
		k.sendLocked(payload)
		k.mu.Unlock()
	}
}

// tickLoop inyecta silencio cada 20 ms mientras el micro esté callado.
func (k *upstreamKeepalive) tickLoop() {
	defer k.wg.Done()
	t := time.NewTicker(keepaliveTick)
	defer t.Stop()
	for {
		select {
		case <-k.done:
			return
		case <-t.C:
			k.mu.Lock()
			now := time.Now()
			if now.Sub(k.lastMic) >= keepaliveMicIdle && now.Sub(k.lastSent) >= keepaliveFrame {
				k.sendLocked(speexSilenceFrame)
			}
			k.mu.Unlock()
		}
	}
}

// sendLocked emite un paquete RTP con el payload dado. Timestamp derivado del reloj real para
// que voz y silencio compartan una línea de tiempo continua. Asume k.mu tomado.
func (k *upstreamKeepalive) sendLocked(payload []byte) {
	now := time.Now()
	pkt := make([]byte, 12+len(payload))
	pkt[0] = 0x80 // versión 2, sin padding/extensión/CSRC
	pkt[1] = k.pt
	binary.BigEndian.PutUint16(pkt[2:4], k.seq)
	binary.BigEndian.PutUint32(pkt[4:8], uint32(now.Sub(k.start).Nanoseconds()*keepaliveRate/int64(time.Second)))
	binary.BigEndian.PutUint32(pkt[8:12], k.ssrc)
	copy(pkt[12:], payload)
	if _, err := k.out.Write(pkt); err != nil {
		select {
		case <-k.done: // cerrando: error esperado
		default:
			k.logger.Debugf("[TALK] keepalive: envío falló: %v", err)
		}
		return
	}
	k.seq++
	k.lastSent = now
}

// rtpPayload extrae el payload de un paquete RTP v2 (salta CSRC, extensión y padding).
// Devuelve nil si el paquete no es RTP válido o no trae payload.
func rtpPayload(b []byte) []byte {
	if len(b) < 12 || b[0]>>6 != 2 {
		return nil
	}
	hdr := 12 + 4*int(b[0]&0x0F)
	if b[0]&0x10 != 0 { // extensión
		if len(b) < hdr+4 {
			return nil
		}
		hdr += 4 + 4*int(binary.BigEndian.Uint16(b[hdr+2:hdr+4]))
	}
	end := len(b)
	if b[0]&0x20 != 0 { // padding: el último byte dice cuántos
		end -= int(b[len(b)-1])
	}
	if hdr >= end {
		return nil
	}
	return b[hdr:end]
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

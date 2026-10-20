package sip

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
)

// mediaDrainBufSize es el tamaño de buffer de lectura por paquete. Un paquete SRTP típico
// (audio o vídeo H.264 empaquetado) cabe holgado en 2048 bytes; si llegase algo mayor, ReadFrom
// simplemente lo trunca (no nos interesa el contenido, solo drenarlo).
const mediaDrainBufSize = 2048

// mediaDrainLogInterval es la cadencia del resumen de paquetes/bytes drenados.
const mediaDrainLogInterval = 2 * time.Second

// mediaDrain drena (lee y descarta) la media SRTP que bt_vct envía directamente a los puertos
// negociados en el SDP del contestador. El bridge no descifra ni usa este tráfico — lo útil llega
// en claro por los taps v0.24 en lo:5000/5007 — este drenaje existe solo para que el kernel no
// responda ICMP port-unreachable (lo que podría cortar la llamada RTP/SRTP del lado de la placa).
type mediaDrain struct {
	audioConn *net.UDPConn
	videoConn *net.UDPConn
	logger    *logrus.Logger

	// Contadores atómicos. Se usa atomic.Int64 (no int64 crudo + atomic.AddInt64)
	// porque en ARM de 32 bits (GOARCH=arm, nuestro target) un int64 solo queda
	// alineado a 4 bytes si no es el primer campo del struct, y atomic.AddInt64
	// sobre una dirección no alineada a 8 provoca "panic: unaligned 64-bit atomic
	// operation" (crash del proceso al armar el drain en una llamada real).
	// atomic.Int64 lo alinea a 8 bytes en todas las plataformas.
	packetsAudio atomic.Int64
	bytesAudio   atomic.Int64
	packetsVideo atomic.Int64
	bytesVideo   atomic.Int64

	ticker   *time.Ticker
	done     chan struct{}
	stopOnce sync.Once
}

// startMediaDrain abre sockets UDP en loopback (127.0.0.1) para audioPort y, si videoPort>0,
// también para videoPort, y arranca una goroutine de lectura por cada uno más una goroutine de
// log periódico. Si audioPort==0 (o videoPort==0 junto a audioPort==0, ver nota) se usa un puerto
// efímero elegido por el SO — pensado para tests. En producción se pasan los puertos concretos
// del AnswererConfig (10100/10102 por defecto).
func startMediaDrain(audioPort, videoPort int, logger *logrus.Logger) (*mediaDrain, error) {
	if logger == nil {
		logger = logrus.New()
	}
	audioConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: audioPort})
	if err != nil {
		return nil, err
	}
	d := &mediaDrain{
		audioConn: audioConn,
		logger:    logger,
		ticker:    time.NewTicker(mediaDrainLogInterval),
		done:      make(chan struct{}),
	}
	if videoPort > 0 {
		videoConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: videoPort})
		if err != nil {
			d.ticker.Stop()
			audioConn.Close()
			return nil, err
		}
		d.videoConn = videoConn
	}
	go d.readLoop(d.audioConn, &d.packetsAudio, &d.bytesAudio)
	if d.videoConn != nil {
		go d.readLoop(d.videoConn, &d.packetsVideo, &d.bytesVideo)
	}
	// nota: readLoop recibe punteros a atomic.Int64 (no se copian: atomic.Int64
	// contiene noCopy) — se pasa &d.campo directamente.
	go d.logLoop()
	return d, nil
}

// readLoop lee y descarta paquetes de conn hasta que Stop() cierra el socket (ReadFrom devuelve
// error y la goroutine sale).
func (d *mediaDrain) readLoop(conn *net.UDPConn, packets, bytes *atomic.Int64) {
	buf := make([]byte, mediaDrainBufSize)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return // socket cerrado por Stop(): fin normal de la goroutine
		}
		packets.Add(1)
		bytes.Add(int64(n))
	}
}

// logLoop resume cada ~2s cuánto se ha drenado, o sale al cerrarse done (Stop()).
func (d *mediaDrain) logLoop() {
	for {
		select {
		case <-d.done:
			return
		case <-d.ticker.C:
			d.logger.Debugf(
				"[ANSWERER] drenaje media: audio %d paquetes/%d bytes, vídeo %d paquetes/%d bytes",
				d.packetsAudio.Load(), d.bytesAudio.Load(),
				d.packetsVideo.Load(), d.bytesVideo.Load(),
			)
		}
	}
}

// Stop cierra los sockets (desbloquea ReadFrom → las goroutines de lectura salen) y para el
// ticker de log. Idempotente: llamar dos veces no entra en pánico.
func (d *mediaDrain) Stop() {
	d.stopOnce.Do(func() {
		close(d.done)
		d.ticker.Stop()
		if d.audioConn != nil {
			d.audioConn.Close()
		}
		if d.videoConn != nil {
			d.videoConn.Close()
		}
	})
}

// AudioAddr devuelve la dirección local del socket de audio (útil en tests con puerto efímero).
func (d *mediaDrain) AudioAddr() string {
	if d.audioConn == nil {
		return ""
	}
	return d.audioConn.LocalAddr().String()
}

// VideoAddr devuelve la dirección local del socket de vídeo, o "" si no se abrió (videoPort<=0).
func (d *mediaDrain) VideoAddr() string {
	if d.videoConn == nil {
		return ""
	}
	return d.videoConn.LocalAddr().String()
}

// PacketsAudio devuelve el número de paquetes de audio drenados hasta ahora.
func (d *mediaDrain) PacketsAudio() int64 {
	return d.packetsAudio.Load()
}

// PacketsVideo devuelve el número de paquetes de vídeo drenados hasta ahora.
func (d *mediaDrain) PacketsVideo() int64 {
	return d.packetsVideo.Load()
}

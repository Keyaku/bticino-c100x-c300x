package sip

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func mkRTP(pt byte, seq uint16, ssrc uint32, payload []byte) []byte {
	b := make([]byte, 12+len(payload))
	b[0], b[1] = 0x80, pt
	binary.BigEndian.PutUint16(b[2:4], seq)
	binary.BigEndian.PutUint32(b[8:12], ssrc)
	copy(b[12:], payload)
	return b
}

// newFillRelay arranca un relay de audio con relleno de silencio + entrada prioritaria
// y un consumidor UDP de prueba. Devuelve el relay, el consumidor y los puertos.
func newFillRelay(t *testing.T) (*RTPRelay, *net.UDPConn, int, int) {
	t.Helper()
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	mainPort, prioPort := freeUDPPort(t), freeUDPPort(t)
	r := NewRTPRelay("audio", mainPort, logger)
	r.EnableSilenceFill(110, prioPort)
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Stop)
	cons, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cons.Close() })
	if err := r.AddUDPConsumer("c1", "127.0.0.1", cons.LocalAddr().(*net.UDPAddr).Port); err != nil {
		t.Fatal(err)
	}
	return r, cons, mainPort, prioPort
}

func readRTP(t *testing.T, c *net.UDPConn, d time.Duration) (pt byte, seq uint16, ssrc uint32, payload []byte) {
	t.Helper()
	buf := make([]byte, 2048)
	_ = c.SetReadDeadline(time.Now().Add(d))
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("el consumidor no recibió RTP: %v", err)
	}
	return buf[1] & 0x7f, binary.BigEndian.Uint16(buf[2:4]), binary.BigEndian.Uint32(buf[8:12]), append([]byte(nil), buf[12:n]...)
}

func sendTo(t *testing.T, port int, pkt []byte) {
	t.Helper()
	c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write(pkt)
}

// Sin ninguna fuente de audio, la pista sigue viva con silencio speex PT110: el ffmpeg
// de go2rtc (que transcodifica audio) no emite NADA hasta el primer paquete de audio, y
// sin esto la tarjeta HA se quedaba en negro aunque el vídeo llegara (2026-10-16).
func TestRelayAudioRellenaSilencioSinFuente(t *testing.T) {
	_, cons, _, _ := newFillRelay(t)
	pt, seq1, ssrc1, payload := readRTP(t, cons, 500*time.Millisecond)
	if pt != 110 || !bytes.Equal(payload, speexSilenceFrame) {
		t.Fatalf("esperaba silencio speex PT110, got PT%d payload %x", pt, payload)
	}
	_, seq2, ssrc2, _ := readRTP(t, cons, 500*time.Millisecond)
	if seq2 != seq1+1 || ssrc2 != ssrc1 {
		t.Fatalf("flujo no continuo: seq %d→%d ssrc %x→%x", seq1, seq2, ssrc1, ssrc2)
	}
}

// El audio real (aunque llegue con otro PT, p.ej. 98 del SRTP de la placa) sale con
// PT110, nuestro SSRC y secuencia continua, y desplaza al silencio.
func TestRelayAudioReescribeFuenteAPT110(t *testing.T) {
	_, cons, mainPort, _ := newFillRelay(t)
	readRTP(t, cons, 500*time.Millisecond) // silencio inicial
	voice := []byte("VOZ-REAL")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		sendTo(t, mainPort, mkRTP(98, 1, 0xdeadbeef, voice))
		pt, _, ssrc, p := readRTP(t, cons, 200*time.Millisecond)
		if bytes.Equal(p, voice) {
			if pt != 110 || ssrc == 0xdeadbeef {
				t.Fatalf("la voz debe salir reescrita (PT110, SSRC propio): PT%d ssrc %x", pt, ssrc)
			}
			return
		}
	}
	t.Fatal("la voz de la fuente principal no llegó al consumidor")
}

// Con la entrada prioritaria activa (OÍR del ojo), el tap mudo de la fuente principal
// se descarta: nunca se mezclan dos flujos en la misma pista.
func TestRelayAudioPrioridadDescartaPrincipal(t *testing.T) {
	_, cons, mainPort, prioPort := newFillRelay(t)
	prio, mainP := []byte("PRIORIDAD"), []byte("TAP-MUDO")
	stop := make(chan struct{})
	go func() { // ambas fuentes emitiendo a la vez cada 20 ms
		tk := time.NewTicker(20 * time.Millisecond)
		defer tk.Stop()
		for i := uint16(0); ; i++ {
			select {
			case <-stop:
				return
			case <-tk.C:
				sendTo(t, prioPort, mkRTP(98, i, 1, prio))
				sendTo(t, mainPort, mkRTP(110, i, 2, mainP))
			}
		}
	}()
	time.Sleep(150 * time.Millisecond) // que arranque la prioridad
	for i := 0; i < 20; i++ {
		_, _, _, p := readRTP(t, cons, 300*time.Millisecond)
		if bytes.Equal(p, mainP) {
			close(stop)
			t.Fatal("con la prioridad activa no debe pasar el audio de la fuente principal")
		}
	}
	close(stop)
}

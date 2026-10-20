package sip

import (
	"bytes"
	"encoding/binary"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// rtpPkt es lo que el test lee de la salida del relé.
type rtpPkt struct {
	pt      byte
	seq     uint16
	ts      uint32
	payload []byte
}

func listenOut(t *testing.T) (*net.UDPConn, int) {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen salida: %v", err)
	}
	return c, c.LocalAddr().(*net.UDPAddr).Port
}

func readPkt(t *testing.T, c *net.UDPConn, timeout time.Duration) (rtpPkt, bool) {
	t.Helper()
	buf := make([]byte, 2048)
	c.SetReadDeadline(time.Now().Add(timeout))
	n, _, err := c.ReadFromUDP(buf)
	if err != nil || n < 12 {
		return rtpPkt{}, false
	}
	return rtpPkt{
		pt:      buf[1] & 0x7F,
		seq:     binary.BigEndian.Uint16(buf[2:4]),
		ts:      binary.BigEndian.Uint32(buf[4:8]),
		payload: append([]byte{}, buf[12:n]...),
	}, true
}

// micPacket construye un RTP mínimo (cabecera de 12 bytes) como el que manda go2rtc.
func micPacket(seq uint16, payload []byte) []byte {
	h := make([]byte, 12)
	h[0] = 0x80
	h[1] = 97
	binary.BigEndian.PutUint16(h[2:4], seq)
	binary.BigEndian.PutUint32(h[8:12], 0xCAFEBABE)
	return append(h, payload...)
}

func sendMic(t *testing.T, port int, pkt []byte) {
	t.Helper()
	c, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatalf("dial mic: %v", err)
	}
	defer c.Close()
	c.Write(pkt)
}

// Sin micrófono el relé emite silencio speex continuo, con su propio PT y secuencia consecutiva:
// es lo que mantiene "viva" la subida para que la placa abra el audio de bajada.
func TestKeepalive_SinMic_SilencioContinuo(t *testing.T) {
	out, outPort := listenOut(t)
	defer out.Close()
	k, err := startUpstreamKeepalive("127.0.0.1", 0, outPort, 97, logrus.New())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer k.Stop()
	var prev *rtpPkt
	for i := 0; i < 6; i++ {
		p, ok := readPkt(t, out, time.Second)
		if !ok {
			t.Fatalf("paquete %d: no llegó (el relé debe emitir sin entrada)", i)
		}
		if p.pt != 97 {
			t.Fatalf("PT=%d, esperaba 97", p.pt)
		}
		if !bytes.Equal(p.payload, speexSilenceFrame) {
			t.Fatalf("payload no es la trama de silencio: %x", p.payload)
		}
		if prev != nil && p.seq != prev.seq+1 {
			t.Fatalf("secuencia no consecutiva: %d tras %d", p.seq, prev.seq)
		}
		pp := p
		prev = &pp
	}
}

// Cuando llega voz del micrófono de HA, se reenvía su payload tal cual, sin huecos de secuencia.
func TestKeepalive_ReenviaPayloadDelMic(t *testing.T) {
	out, outPort := listenOut(t)
	defer out.Close()
	k, err := startUpstreamKeepalive("127.0.0.1", 0, outPort, 97, logrus.New())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer k.Stop()
	if _, ok := readPkt(t, out, time.Second); !ok { // ya está emitiendo silencio
		t.Fatal("el relé no arrancó a emitir")
	}
	voz := []byte("VOZ-DE-HA-0001")
	sendMic(t, k.InPort(), micPacket(500, voz))
	var prevSeq uint16
	have := false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p, ok := readPkt(t, out, 300*time.Millisecond)
		if !ok {
			continue
		}
		if have && p.seq != prevSeq+1 {
			t.Fatalf("hueco de secuencia: %d tras %d", p.seq, prevSeq)
		}
		prevSeq, have = p.seq, true
		if bytes.Equal(p.payload, voz) {
			return // OK
		}
	}
	t.Fatal("el payload del micrófono no se reenvió")
}

// Tras callar el micrófono, el relé vuelve a meter silencio.
func TestKeepalive_VuelveASilencioTrasMic(t *testing.T) {
	out, outPort := listenOut(t)
	defer out.Close()
	k, err := startUpstreamKeepalive("127.0.0.1", 0, outPort, 97, logrus.New())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer k.Stop()
	voz := []byte("VOZ-DE-HA-0002")
	sendMic(t, k.InPort(), micPacket(1, voz))
	sawVoz := false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p, ok := readPkt(t, out, 300*time.Millisecond)
		if !ok {
			continue
		}
		if bytes.Equal(p.payload, voz) {
			sawVoz = true
			continue
		}
		if sawVoz && bytes.Equal(p.payload, speexSilenceFrame) {
			return // OK: silencio de nuevo tras la voz
		}
	}
	t.Fatalf("no volvió el silencio tras la voz (vio voz=%v)", sawVoz)
}

// Integración con un ffmpeg REAL (se salta si no hay ffmpeg en el PATH): con el micro callado,
// ffmpeg leyendo del relé (mismos argumentos que en producción) debe sacar SRTP continuo.
// Es justo el caso que rompía el diseño ingenuo con amix (ffmpeg se colgaba sin paquetes de micro).
func TestKeepalive_IntegracionFFmpegSacaSRTPSinMic(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg no disponible")
	}
	srtpOut, srtpPort := listenOut(t) // hace de "placa"
	defer srtpOut.Close()
	ffIn := freeUDPPort(t) // donde escucha ffmpeg (salida del relé)
	sdpFile := filepath.Join(t.TempDir(), "talk.sdp")
	if err := os.WriteFile(sdpFile, []byte(BuildBackchannelSDP(ffIn, 97)), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := GenerateSRTPKey()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ff, buildFFmpegArgs(sdpFile, key, "127.0.0.1", srtpPort, 96)...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("ffmpeg: %v", err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	k, err := startUpstreamKeepalive("127.0.0.1", 0, ffIn, 97, logrus.New())
	if err != nil {
		t.Fatalf("relé: %v", err)
	}
	defer k.Stop()
	got := 0
	deadline := time.Now().Add(6 * time.Second)
	for got < 25 && time.Now().Before(deadline) {
		if _, ok := readPkt(t, srtpOut, 500*time.Millisecond); ok {
			got++
		}
	}
	if got < 25 {
		t.Fatalf("ffmpeg solo sacó %d paquetes SRTP sin micro (esperaba flujo continuo)", got)
	}
}

// Stop libera el puerto de entrada (el del micrófono) y es idempotente.
func TestKeepalive_StopLiberaPuerto(t *testing.T) {
	out, outPort := listenOut(t)
	defer out.Close()
	k, err := startUpstreamKeepalive("127.0.0.1", 0, outPort, 97, logrus.New())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	in := k.InPort()
	k.Stop()
	k.Stop()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: in})
	if err != nil {
		t.Fatalf("el puerto %d sigue ocupado tras Stop: %v", in, err)
	}
	c.Close()
}

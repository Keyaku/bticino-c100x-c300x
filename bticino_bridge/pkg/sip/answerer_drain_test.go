package sip

import (
	"net"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// TestMediaDrain_ConsumeUDPsinError verifica que el drenaje lee y descarta paquetes UDP sin
// error, incrementando el contador de paquetes de audio. Usa puertos efímeros (0,0) para no
// depender de puertos fijos durante el test.
func TestMediaDrain_ConsumeUDPsinError(t *testing.T) {
	d, err := startMediaDrain(0, 0, logrus.New()) // 0,0 => puertos efímeros elegidos por el SO
	if err != nil {
		t.Fatalf("startMediaDrain: %v", err)
	}
	// Descubre el puerto real de audio (expón d.AudioAddr() para el test).
	addr := d.AudioAddr()
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	for i := 0; i < 10; i++ {
		conn.Write([]byte("paquete-srtp-simulado"))
	}
	time.Sleep(50 * time.Millisecond)
	d.Stop()
	if got := d.PacketsAudio(); got < 1 {
		t.Fatalf("esperaba >=1 paquete drenado, got %d", got)
	}
}

// TestMediaDrain_VideoPortCero verifica que si videoPort==0 no se abre socket de vídeo (no falla,
// simplemente no hay drenaje de vídeo).
func TestMediaDrain_VideoPortCero(t *testing.T) {
	d, err := startMediaDrain(0, 0, logrus.New())
	if err != nil {
		t.Fatalf("startMediaDrain: %v", err)
	}
	defer d.Stop()
	if d.VideoAddr() != "" {
		t.Fatalf("esperaba VideoAddr vacío con videoPort=0, got %q", d.VideoAddr())
	}
}

// TestMediaDrain_StopEsIdempotente verifica que llamar Stop() dos veces no entra en pánico.
func TestMediaDrain_StopEsIdempotente(t *testing.T) {
	d, err := startMediaDrain(0, 0, logrus.New())
	if err != nil {
		t.Fatalf("startMediaDrain: %v", err)
	}
	d.Stop()
	d.Stop()
}

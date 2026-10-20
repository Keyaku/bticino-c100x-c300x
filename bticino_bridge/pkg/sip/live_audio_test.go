package sip

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// fakeAudioTap es el doble de test para VideoTap en los tests de LiveAudioOnCall.
// Cuando entered/release no son nil, Start() bloquea: cierra entered al entrar
// y espera a que el test cierre release, para poder ejercer la ventana de
// carrera con un Stop concurrente (test del leak).
type fakeAudioTap struct {
	mu            sync.Mutex
	starts, stops int
	err           error
	entered       chan struct{}
	release       chan struct{}
}

func (f *fakeAudioTap) Start() error {
	f.mu.Lock()
	f.starts++
	err := f.err
	f.mu.Unlock()
	if f.entered != nil {
		close(f.entered)
		<-f.release
	}
	return err
}

func (f *fakeAudioTap) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
}

func (f *fakeAudioTap) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.stops
}

func TestLiveAudioStartArmsTap(t *testing.T) {
	tap := &fakeAudioTap{}
	l := NewLiveAudioOnCall(tap, logrus.New())

	l.Start("test")

	if !l.IsActive() {
		t.Fatal("esperaba IsActive()=true tras Start")
	}
	if starts, _ := tap.counts(); starts != 1 {
		t.Fatalf("esperaba 1 arranque del tap, hubo %d", starts)
	}
}

func TestLiveAudioStartIdempotent(t *testing.T) {
	tap := &fakeAudioTap{}
	l := NewLiveAudioOnCall(tap, logrus.New())
	l.Start("a")
	l.Start("b")
	if starts, _ := tap.counts(); starts != 1 {
		t.Fatalf("esperaba 1 arranque (idempotente), hubo %d", starts)
	}
}

func TestLiveAudioStopDisarmsAndAllowsRestart(t *testing.T) {
	tap := &fakeAudioTap{}
	l := NewLiveAudioOnCall(tap, logrus.New())
	l.Start("a")
	l.Stop()
	if l.IsActive() {
		t.Fatal("esperaba IsActive()=false tras Stop")
	}
	if _, stops := tap.counts(); stops != 1 {
		t.Fatalf("esperaba 1 parada del tap, hubo %d", stops)
	}
	l.Start("b")
	if starts, _ := tap.counts(); starts != 2 {
		t.Fatalf("esperaba 2 arranques tras re-Start, hubo %d", starts)
	}
}

func TestLiveAudioStopWithoutStartIsNoop(t *testing.T) {
	tap := &fakeAudioTap{}
	l := NewLiveAudioOnCall(tap, logrus.New())
	l.Stop()
	if _, stops := tap.counts(); stops != 0 {
		t.Fatalf("Stop sin Start no debe parar el tap (hubo %d)", stops)
	}
}

func TestLiveAudioTapErrorIsNonFatal(t *testing.T) {
	tap := &fakeAudioTap{err: errors.New("operation not permitted")}
	l := NewLiveAudioOnCall(tap, logrus.New())
	l.Start("sin-permisos") // no debe entrar en pánico
	if !l.IsActive() {
		t.Fatal("esperaba IsActive()=true aunque el tap falle")
	}
}

func TestLiveAudioNilTapIsSafe(t *testing.T) {
	l := NewLiveAudioOnCall(nil, logrus.New())
	l.Start("a") // no debe entrar en pánico
	l.Stop()
}

func TestLiveAudioGuardAutoStopsWhenHangupIsMissed(t *testing.T) {
	tap := &fakeAudioTap{}
	l := NewLiveAudioOnCall(tap, logrus.New())
	l.SetGuardTimeout(30 * time.Millisecond)
	l.Start("a")

	deadline := time.Now().Add(2 * time.Second)
	for l.IsActive() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if l.IsActive() {
		t.Fatal("esperaba auto-stop por guard, sigue activo")
	}
	if _, stops := tap.counts(); stops != 1 {
		t.Fatalf("esperaba 1 parada del tap por guard, hubo %d", stops)
	}
}

func TestLiveAudioGuardCanceledByStop(t *testing.T) {
	tap := &fakeAudioTap{}
	l := NewLiveAudioOnCall(tap, logrus.New())
	l.SetGuardTimeout(30 * time.Millisecond)
	l.Start("a")
	l.Stop()
	time.Sleep(80 * time.Millisecond) // el guard ya habría vencido
	if _, stops := tap.counts(); stops != 1 {
		t.Fatalf("el guard cancelado no debe parar otra vez (stops=%d)", stops)
	}
	// un re-Start tras el guard vencido debe funcionar con guard nuevo
	l.Start("b")
	if !l.IsActive() {
		t.Fatal("esperaba activo tras re-Start")
	}
	l.Stop()
}

// TestLiveAudioStopDuringStartDoesNotLeakTap ejerce el fix del leak: si Stop()
// gana la carrera mientras tap.Start() está en curso (bloqueado), Start debe
// re-chequear el estado tras el retorno de tap.Start() y parar el tap huérfano
// en vez de dejarlo corriendo con active=false.
func TestLiveAudioStopDuringStartDoesNotLeakTap(t *testing.T) {
	tap := &fakeAudioTap{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	l := NewLiveAudioOnCall(tap, logrus.New())

	startDone := make(chan struct{})
	go func() {
		l.Start("race")
		close(startDone)
	}()

	// Esperar a que Start() esté bloqueado dentro de tap.Start().
	<-tap.entered

	// Stop concurrente durante la ventana bloqueante: como el tap aún no ha
	// devuelto, Stop() solo puede voltear active=false (el tap real seguirá
	// arrancando en segundo plano hasta que tap.Start() retorne).
	l.Stop()

	// Liberar tap.Start() para que retorne y Start() pueda re-chequear active.
	close(tap.release)
	<-startDone

	if l.IsActive() {
		t.Fatal("esperaba IsActive()=false tras Stop concurrente durante Start")
	}

	starts, stops := tap.counts()
	if starts != 1 {
		t.Fatalf("esperaba 1 arranque del tap, hubo %d", starts)
	}
	// Secuencia determinista dado el channel-sync: Stop() ve active=true (ya lo
	// puso Start antes de bloquear en tap.Start) y para el tap una vez (stops=1)
	// aunque tap.Start() siga en curso; al liberarse y retornar, el re-chequeo
	// del fix del leak ve active=false (orphaned) y para el tap otra vez
	// (stops=2). Sin el fix, stops se quedaría en 1 y el tap real (cuyo Start
	// termina de montarse DESPUÉS del primer Stop) quedaría fugado corriendo.
	if stops != 2 {
		t.Fatalf("tap fugado: esperaba 2 paradas (Stop directo + re-chequeo del fix), hubo %d (starts=%d)", stops, starts)
	}
}

package access

import (
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"bticino_bridge/pkg/config"
	"bticino_bridge/pkg/events"
	"bticino_bridge/pkg/multicast/handlers"
)

func testCfg() config.AccessConfig {
	return config.AccessConfig{
		Enabled:            true,
		CorrelationWindow:  30 * time.Second,
		MaxHoldWait:        8 * time.Second,
		DurationVisitorMax: 3500 * time.Millisecond,
	}
}

// collect suscribe access.classified y devuelve un canal con el evento.
func collect(t *testing.T, bus events.EventBus) <-chan AccessEvent {
	t.Helper()
	ch := make(chan AccessEvent, 4)
	bus.Subscribe("access.classified", func(e *events.Event) {
		if ev, ok := e.Data.(AccessEvent); ok {
			ch <- ev
		}
	})
	return ch
}

func waitEvent(t *testing.T, ch <-chan AccessEvent) AccessEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timeout esperando access.classified")
		return AccessEvent{}
	}
}

// newTestClassifier crea un Classifier con reloj controlado y afterFunc que
// captura TODAS las funciones de timeout armadas (una por cada onUnlocked),
// en orden, para poder dispararlas a mano — incluido el caso de supersesión,
// donde interesa poder disparar un timer "viejo" ya reemplazado y comprobar
// que queda inerte.
func newTestClassifier(bus events.EventBus) (*Classifier, *time.Time, *[]func()) {
	logger := logrus.New()
	logger.SetLevel(logrus.PanicLevel)
	c := NewClassifier(testCfg(), bus, logger)
	nowVal := time.Unix(1000, 0)
	fired := []func(){}
	c.now = func() time.Time { return nowVal }
	c.afterFunc = func(_ time.Duration, f func()) stopTimer {
		fired = append(fired, f)
		return noopTimer{}
	}
	return c, &nowVal, &fired
}

func TestClassifier_Telefonillo(t *testing.T) {
	bus := events.NewEventBus(4, nil)
	defer bus.Close()
	ch := collect(t, bus)
	c, now, _ := newTestClassifier(bus)

	c.recordRing("doorbell")
	*now = now.Add(2 * time.Second)
	c.onUnlocked("20")
	*now = now.Add(2500 * time.Millisecond)
	c.onLocked("20")

	ev := waitEvent(t, ch)
	if ev.Type != TypeTelefonillo || ev.Confidence != ConfAlta {
		t.Errorf("got {%s,%s}, quiero {telefonillo,alta}", ev.Type, ev.Confidence)
	}
	if !ev.HadCall || ev.RingSource != "doorbell" {
		t.Errorf("had_call/ring_source mal: %+v", ev)
	}
	if ev.DurationS < 2.4 || ev.DurationS > 2.6 {
		t.Errorf("duration_s = %v, quiero ~2.5", ev.DurationS)
	}
}

func TestClassifier_Llave(t *testing.T) {
	bus := events.NewEventBus(4, nil)
	defer bus.Close()
	ch := collect(t, bus)
	c, now, _ := newTestClassifier(bus)

	c.onUnlocked("20") // sin timbre previo
	*now = now.Add(4500 * time.Millisecond)
	c.onLocked("20")

	ev := waitEvent(t, ch)
	if ev.Type != TypeLlave || ev.Confidence != ConfAlta {
		t.Errorf("got {%s,%s}, quiero {llave,alta}", ev.Type, ev.Confidence)
	}
	if ev.HadCall {
		t.Errorf("had_call = true, quiero false")
	}
}

func TestClassifier_TimeoutSinCierre(t *testing.T) {
	bus := events.NewEventBus(4, nil)
	defer bus.Close()
	ch := collect(t, bus)
	c, _, fired := newTestClassifier(bus)

	c.onUnlocked("20") // sin timbre
	if len(*fired) == 0 {
		t.Fatal("no se armó el timer de max_hold_wait")
	}
	(*fired)[len(*fired)-1]() // simular vencimiento sin cierre

	ev := waitEvent(t, ch)
	if ev.Type != TypeLlave || ev.Confidence != ConfMedia {
		t.Errorf("got {%s,%s}, quiero {llave,media}", ev.Type, ev.Confidence)
	}
	if ev.DurationS != 0 {
		t.Errorf("duration_s = %v, quiero 0 (sin cierre)", ev.DurationS)
	}
}

func TestClassifier_VentanaExpirada(t *testing.T) {
	bus := events.NewEventBus(4, nil)
	defer bus.Close()
	ch := collect(t, bus)
	c, now, _ := newTestClassifier(bus)

	c.recordRing("doorbell")
	*now = now.Add(40 * time.Second) // fuera de la ventana de 30s
	c.onUnlocked("20")
	*now = now.Add(2 * time.Second)
	c.onLocked("20")

	ev := waitEvent(t, ch)
	if ev.HadCall {
		t.Errorf("had_call = true; el timbre está fuera de ventana")
	}
	if ev.Type != TypeDesconocido { // sin llamada + pulso corto = conflicto
		t.Errorf("type = %s, quiero desconocido", ev.Type)
	}
}

// TestClassifier_DobleAperturaMismaWhere cubre la supersesión: una segunda
// apertura del mismo WHERE llega antes de que cierre la primera (dos
// aperturas de RFID/llave, o un rebote del relé, back-to-back). No debe
// perderse el primer acceso: debe volcarse (flush) con duración desconocida
// antes de que arranque el segundo pendiente.
func TestClassifier_DobleAperturaMismaWhere(t *testing.T) {
	bus := events.NewEventBus(4, nil)
	defer bus.Close()
	ch := collect(t, bus)
	c, now, _ := newTestClassifier(bus)

	c.recordRing("doorbell")
	*now = now.Add(1 * time.Second)
	c.onUnlocked("20") // primera apertura, correlacionada con el timbre

	*now = now.Add(3 * time.Second)
	c.onUnlocked("20") // segunda apertura del mismo WHERE, sin cierre de la primera

	*now = now.Add(2 * time.Second)
	c.onLocked("20") // cierra la segunda

	first := waitEvent(t, ch)
	second := waitEvent(t, ch)

	// El bus reparte cada handler en su propia goroutine (varios workers) sin
	// garantía de orden entre eventos consecutivos — también entre dos
	// publicaciones del mismo nombre. Identificamos cada evento por su
	// contenido (duration_s), no por el orden de llegada al canal.
	flushed, closed := first, second
	if flushed.DurationS != 0 {
		flushed, closed = second, first
	}

	if flushed.DurationS != 0 {
		t.Errorf("evento de volcado por supersesión duration_s = %v, quiero 0", flushed.DurationS)
	}
	if !flushed.HadCall {
		t.Errorf("evento de volcado debería tener had_call=true (correlacionado con el timbre)")
	}
	if flushed.Confidence != ConfMedia {
		t.Errorf("evento de volcado confidence = %s, quiero media (sin duración conocida)", flushed.Confidence)
	}
	if closed.DurationS < 1.9 || closed.DurationS > 2.1 {
		t.Errorf("evento de cierre real duration_s = %v, quiero ~2", closed.DurationS)
	}
}

// TestClassifier_TimerViejoInerteTrasSupersesion confirma la parte 2 del
// fix: el timer de max_hold_wait de un pendiente superseído no debe producir
// un evento adicional ni interferir con el pendiente nuevo si se dispara
// (identidad de puntero en finalize).
func TestClassifier_TimerViejoInerteTrasSupersesion(t *testing.T) {
	bus := events.NewEventBus(4, nil)
	defer bus.Close()
	ch := collect(t, bus)
	c, now, fired := newTestClassifier(bus)

	c.onUnlocked("20") // primera apertura, sin timbre
	if len(*fired) != 1 {
		t.Fatalf("se esperaba 1 timer armado, hay %d", len(*fired))
	}
	oldTimer := (*fired)[0]

	*now = now.Add(1 * time.Second)
	c.onUnlocked("20") // segunda apertura del mismo WHERE: supersede a la primera

	if len(*fired) != 2 {
		t.Fatalf("se esperaba 2 timers armados, hay %d", len(*fired))
	}

	// La primera apertura ya se volcó (evento de supersesión).
	ev1 := waitEvent(t, ch)
	if ev1.DurationS != 0 {
		t.Errorf("evento de supersesión duration_s = %v, quiero 0", ev1.DurationS)
	}

	// Disparar el timer VIEJO a mano: no debe publicar nada ni tocar el
	// pendiente nuevo.
	oldTimer()

	select {
	case ev := <-ch:
		t.Fatalf("el timer viejo, tras supersesión, no debería publicar nada; llegó %+v", ev)
	case <-time.After(200 * time.Millisecond):
		// esperado: no llega nada
	}

	// El pendiente nuevo sigue vivo: cerrarlo produce su propio evento normal.
	*now = now.Add(2 * time.Second)
	c.onLocked("20")
	ev2 := waitEvent(t, ch)
	if ev2.DurationS < 1.9 || ev2.DurationS > 2.1 {
		t.Errorf("segundo evento (cierre real tras supersesión) duration_s = %v, quiero ~2", ev2.DurationS)
	}
}

func TestWhereOf(t *testing.T) {
	e := &events.Event{Name: "door.unlocked", Data: &handlers.OpenWebNetEvent{Where: "20"}}
	if got := whereOf(e); got != "20" {
		t.Errorf("whereOf = %q, quiero 20", got)
	}
	if got := whereOf(&events.Event{Data: "no-es-owe"}); got != "" {
		t.Errorf("whereOf con tipo raro = %q, quiero vacío", got)
	}
}

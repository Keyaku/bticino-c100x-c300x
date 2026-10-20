package access

import (
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"bticino_bridge/pkg/config"
	"bticino_bridge/pkg/events"
	"bticino_bridge/pkg/multicast/handlers"
)

// stopTimer abstrae *time.Timer para poder inyectar uno falso en tests.
type stopTimer interface{ Stop() bool }

// noopTimer es un stopTimer inerte (para tests).
type noopTimer struct{}

func (noopTimer) Stop() bool { return true }

// AccessEvent es el Data del evento "access.classified".
type AccessEvent struct {
	Type       string    `json:"type"`
	Confidence string    `json:"confidence"`
	Where      string    `json:"where"`
	HadCall    bool      `json:"had_call"`
	RingSource string    `json:"ring_source,omitempty"`
	DurationS  float64   `json:"duration_s"`
	Timestamp  time.Time `json:"timestamp"`
}

type ringInfo struct {
	at     time.Time
	source string
	set    bool
}

type pendingOpen struct {
	openedAt  time.Time
	hadCall   bool
	ringSrc   string
	timer     stopTimer
	finalized bool
}

// Classifier escucha el EventBus y publica "access.classified".
type Classifier struct {
	cfg    config.AccessConfig
	bus    events.EventBus
	logger *logrus.Logger

	mu       sync.Mutex
	lastRing ringInfo
	pending  map[string]*pendingOpen
	subs     []string

	// Inyectables para test:
	now       func() time.Time
	afterFunc func(time.Duration, func()) stopTimer
}

// NewClassifier crea el clasificador. now/afterFunc usan los reales por defecto.
func NewClassifier(cfg config.AccessConfig, bus events.EventBus, logger *logrus.Logger) *Classifier {
	if logger == nil {
		logger = logrus.New()
	}
	return &Classifier{
		cfg:       cfg,
		bus:       bus,
		logger:    logger,
		pending:   make(map[string]*pendingOpen),
		now:       time.Now,
		afterFunc: func(d time.Duration, f func()) stopTimer { return time.AfterFunc(d, f) },
	}
}

// Start registra las suscripciones al EventBus.
func (c *Classifier) Start() {
	c.subs = append(c.subs,
		c.bus.Subscribe("doorbell.pressed", func(*events.Event) { c.recordRing("doorbell") }),
		c.bus.Subscribe("doorbell.floor.pressed", func(*events.Event) { c.recordRing("doorbell_floor") }),
		c.bus.Subscribe("sip.call.incoming", func(*events.Event) { c.recordRing("sip") }),
		c.bus.Subscribe("door.unlocked", func(e *events.Event) { c.onUnlocked(whereOf(e)) }),
		c.bus.Subscribe("door.locked", func(e *events.Event) { c.onLocked(whereOf(e)) }),
	)
	c.logger.Info("Clasificador de accesos: suscripciones activas")
}

// Stop cancela las suscripciones (tapa timers pendientes vía el bus).
func (c *Classifier) Stop() {
	for _, id := range c.subs {
		c.bus.Unsubscribe(id)
	}
	c.subs = nil
}

// recordRing guarda el último timbre/llamada visto.
func (c *Classifier) recordRing(source string) {
	c.mu.Lock()
	c.lastRing = ringInfo{at: c.now(), source: source, set: true}
	c.mu.Unlock()
}

// onUnlocked abre un pendiente para WHERE y arma el timer de max_hold_wait.
//
// Si ya había un pendiente sin cerrar para el mismo WHERE (doble apertura
// del mismo relé sin que llegara el cierre intermedio: p. ej. dos toques de
// llave/RFID en <max_hold_wait), se le supersede: primero se vuelca (flush)
// como si hubiera vencido su timeout (duración desconocida), para no perder
// ese acceso, y luego se arma el nuevo pendiente. La decisión de qué hacer
// con el viejo (marcarlo finalizado, parar su timer) y el reemplazo por el
// nuevo ocurren bajo el MISMO lock, sin soltar el mutex entre medias, para
// que no quede una ventana en la que otro onUnlocked/onLocked/timer
// concurrente pueda ver el pendiente viejo "vivo" en el mapa a la vez que
// ya fue decidido su reemplazo. Solo la publicación (I/O) del volcado se
// hace fuera del lock.
func (c *Classifier) onUnlocked(where string) {
	c.mu.Lock()
	old, hadOld := c.pending[where]
	var oldHadCall bool
	var oldRingSrc string
	var oldTs time.Time
	if hadOld {
		old.finalized = true
		if old.timer != nil {
			old.timer.Stop()
		}
		oldHadCall, oldRingSrc = old.hadCall, old.ringSrc
		oldTs = c.now()
	}

	hadCall, ringSrc := false, ""
	if c.lastRing.set && c.now().Sub(c.lastRing.at) <= c.cfg.CorrelationWindow {
		hadCall = true
		ringSrc = c.lastRing.source
	}
	p := &pendingOpen{openedAt: c.now(), hadCall: hadCall, ringSrc: ringSrc}
	// El timer captura el puntero p (identidad), no el WHERE a secas: así,
	// si este pendiente es superseído más tarde, el propio timer viejo
	// queda inerte en finalize (cur != p) en vez de tocar el pendiente que
	// lo reemplazó.
	p.timer = c.afterFunc(c.cfg.MaxHoldWait, func() { c.finalize(where, p, 0, false) })
	c.pending[where] = p
	c.mu.Unlock()

	if hadOld {
		c.publishAccess(where, oldHadCall, oldRingSrc, 0, false, oldTs)
	}
}

// onLocked cierra el pendiente de WHERE midiendo la duración del pulso.
func (c *Classifier) onLocked(where string) {
	c.mu.Lock()
	p, ok := c.pending[where]
	if !ok {
		c.mu.Unlock()
		return
	}
	dur := c.now().Sub(p.openedAt)
	c.mu.Unlock()
	c.finalize(where, p, dur, true)
}

// finalize clasifica y publica el pendiente p de WHERE, solo si p sigue
// siendo el pendiente vigente en el mapa (comparación por identidad de
// puntero, no solo por clave WHERE) y no fue finalizado ya. Este guard es lo
// que hace segura la competencia entre el cierre real (onLocked), el timer
// de max_hold_wait de ESE pendiente concreto, y una eventual supersesión por
// una nueva apertura del mismo WHERE (que ya habrá marcado p.finalized=true
// y reemplazado la entrada del mapa antes de que este finalize se ejecute).
func (c *Classifier) finalize(where string, p *pendingOpen, dur time.Duration, durKnown bool) {
	c.mu.Lock()
	cur, ok := c.pending[where]
	if !ok || cur != p || p.finalized {
		c.mu.Unlock()
		return
	}
	p.finalized = true
	if p.timer != nil {
		p.timer.Stop()
	}
	delete(c.pending, where)
	hadCall, ringSrc := p.hadCall, p.ringSrc
	ts := c.now()
	c.mu.Unlock()

	c.publishAccess(where, hadCall, ringSrc, dur, durKnown, ts)
}

// publishAccess clasifica (Classify es puro) y publica "access.classified".
// Se llama siempre fuera de c.mu.
func (c *Classifier) publishAccess(where string, hadCall bool, ringSrc string, dur time.Duration, durKnown bool, ts time.Time) {
	res := Classify(ClassifyInput{
		HadCall:       hadCall,
		Duration:      dur,
		DurationKnown: durKnown,
		VisitorMax:    c.cfg.DurationVisitorMax,
	})
	ev := AccessEvent{
		Type:       res.Type,
		Confidence: res.Confidence,
		Where:      where,
		HadCall:    hadCall,
		RingSource: ringSrc,
		Timestamp:  ts,
	}
	if durKnown {
		ev.DurationS = dur.Seconds()
	}
	c.logger.WithFields(logrus.Fields{
		"type": ev.Type, "confidence": ev.Confidence, "where": where,
		"had_call": hadCall, "duration_s": ev.DurationS,
	}).Info("Acceso clasificado")
	c.bus.PublishWithSource("access.classified", ev, "access")
}

// whereOf extrae el WHERE de un evento door.* (defensivo: "" si no encaja).
func whereOf(e *events.Event) string {
	if e == nil {
		return ""
	}
	if owe, ok := e.Data.(*handlers.OpenWebNetEvent); ok {
		return owe.Where
	}
	return ""
}

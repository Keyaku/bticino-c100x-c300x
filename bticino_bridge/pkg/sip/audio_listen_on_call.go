package sip

import (
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// AudioDecoder abstrae la decodificación bloqueante del RTP reenviado por el
// tap (AudioTapDecoder en producción, Task 2). Decode BLOQUEA hasta que el
// proceso termina por sí solo (timeout duro interno) o Stop lo cancela.
type AudioDecoder interface {
	Decode(listenHost string, listenPort, payloadType int, sinkPath string) error
	Stop()
}

// audioDecoderTimeoutSetter es una interfaz OPCIONAL: si el AudioDecoder
// inyectado la implementa (como *AudioTapDecoder), AudioListenOnCall alarga
// su tope duro por defecto (60s en AudioTapDecoder, pensado como protección
// anti-DoS para invocaciones puntuales) para no cortar el audio a mitad de
// una llamada real más larga. Un decoder que no la implemente simplemente no
// se ve afectado.
type audioDecoderTimeoutSetter interface {
	SetTimeout(d time.Duration)
}

// defaultAudioListenDecoderTimeout es el tope aplicado al decoder cuando no
// hay guard configurado (SetGuardTimeout). Con guard configurado se usa ese
// valor, ya que es el límite real de la sesión.
const defaultAudioListenDecoderTimeout = 10 * time.Minute

// AudioListenOnCall arma el "oído" del portero durante una llamada real con
// parte de RED: un espejo pasivo del leg de audio nativo (lo:5000, RTP Speex
// en claro — ver NativeAudioRTPPort) reenviado a un puerto de relay local que
// un AudioDecoder decodifica a WAV. Mismo ciclo de vida que LiveVideoOnCall
// (armado/desarmado en sip.call.connected/ended, cableado en cmd/main.go),
// pero además orquesta un decodificador bloqueante en una goroutine propia.
// CERO comandos al bus: igual que el vídeo, es un espejo puramente pasivo.
type AudioListenOnCall struct {
	tap         VideoTap // reutiliza la interfaz mínima Start()/Stop() del vídeo
	decoder     AudioDecoder
	relayPort   int
	payloadType int
	sinkPath    string
	logger      *logrus.Logger

	mu         sync.Mutex
	active     bool
	generation uint64        // se incrementa en cada Start; identifica la sesión actual
	guard      time.Duration // 0 = sin guard
	timer      *time.Timer
}

// SetGuardTimeout activa un desarme automático de seguridad: si el fin de
// llamada no llega, Stop se invoca solo pasado d desde el Start. 0 desactiva
// el guard. Llamar antes de Start. Mismo comportamiento que LiveVideoOnCall.
func (a *AudioListenOnCall) SetGuardTimeout(d time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.guard = d
}

// NewAudioListenOnCall crea el orquestador. relayPort es el puerto local
// (127.0.0.1) donde el tap reenvía y el decoder escucha; payloadType es el PT
// RTP del audio Speex nativo (110 en el firmware).
func NewAudioListenOnCall(tap VideoTap, decoder AudioDecoder, relayPort, payloadType int, sinkPath string, logger *logrus.Logger) *AudioListenOnCall {
	if logger == nil {
		logger = logrus.New()
	}
	return &AudioListenOnCall{
		tap:         tap,
		decoder:     decoder,
		relayPort:   relayPort,
		payloadType: payloadType,
		sinkPath:    sinkPath,
		logger:      logger,
	}
}

// Start arma el oído. Idempotente: si ya está activo, no hace nada.
// ORDEN: primero el decodificador (en una goroutine — Decode bloquea
// escuchando en 127.0.0.1:relayPort), luego el tap (que reenvía
// lo:5000 → 127.0.0.1:relayPort), para que el decoder ya esté a la escucha
// cuando empiecen a llegar paquetes. Un fallo del tap degrada sin audio pero
// mantiene la sesión armada, igual que LiveVideoOnCall con el vídeo.
func (a *AudioListenOnCall) Start(reason string) {
	a.mu.Lock()
	if a.active {
		a.mu.Unlock()
		return
	}
	a.active = true
	a.generation++
	gen := a.generation
	guard := a.guard
	if guard > 0 {
		a.timer = time.AfterFunc(guard, func() {
			a.logger.Warn("audio-listen: guard vencido sin fin de llamada — desarmando")
			a.Stop()
		})
	}
	a.mu.Unlock()

	a.logger.Infof("audio-listen: armando oído (motivo=%s)", reason)

	if a.decoder != nil {
		if setter, ok := a.decoder.(audioDecoderTimeoutSetter); ok {
			timeout := defaultAudioListenDecoderTimeout
			if guard > 0 {
				timeout = guard
			}
			setter.SetTimeout(timeout)
		}
		go func() {
			if err := a.decoder.Decode("127.0.0.1", a.relayPort, a.payloadType, a.sinkPath); err != nil {
				a.logger.WithError(err).Debug("audio-listen: decoder terminó")
			}
			// El decoder puede volver por sí solo (su propio tope duro,
			// alargado arriba pero no infinito, u otro motivo) SIN que nadie
			// haya llamado a Stop(). Si no desarmamos aquí, IsActive() sigue
			// diciendo true mientras el tap reenvía RTP a un socket que ya
			// nadie lee — estado mentiroso y silencioso. selfStopIfCurrent
			// solo actúa si `gen` sigue siendo la sesión activa: evita que
			// esta goroutine, si se retrasa, desarme por error una sesión
			// posterior (un Stop()+Start() ya la habrían reemplazado).
			a.selfStopIfCurrent(gen)
		}()
	}
	if a.tap != nil {
		if err := a.tap.Start(); err != nil {
			a.logger.WithError(err).Warn("audio-listen: tap no disponible — degradando sin audio")
		} else {
			// tap.Start() se llama DELIBERADAMENTE fuera del lock (puede
			// bloquear: apertura de socket AF_PACKET). En esa ventana un
			// Stop() concurrente (guard, sip.call.ended) puede haber corrido
			// entero su beginTeardown/teardown ANTES de que este tap.Start()
			// vuelva. Su propio tap.Stop() en ese teardown no hizo nada útil
			// porque el tap todavía no había arrancado. Sin esta
			// comprobación, el tap quedaría arrancado con active=false y
			// ningún desarme posterior lo pararía: el decoder se autodesarma
			// solo en su tope duro (SetTimeout arriba), pero el tap (socket
			// AF_PACKET + goroutine) no tiene mecanismo de autodesarme propio
			// — quedaría filtrado (leaked) para siempre.
			//
			// OJO: la comprobación es SOLO `!a.active`, NUNCA `generation`.
			// `a.tap`/`a.decoder` son instancias ÚNICAS compartidas por TODAS
			// las generaciones (se construyen una vez en cmd/main.go), no una
			// por sesión. Si en la ventana se coló un Stop()+Start() completo
			// (sesión B, generación distinta de la nuestra) y B ya está
			// activa, `a.active` es true: el tap compartido pertenece
			// LEGÍTIMAMENTE a B (LoopbackRTPTap.Start es idempotente — el
			// tap.Start() de B ya lo dejó corriendo, y este tap.Start()
			// tardío de A no hace nada nuevo sobre el mismo tap). Pararlo
			// aquí cortaría el audio de una llamada EN VIVO (B) de forma
			// silenciosa, porque IsActive() seguiría devolviendo true (lo
			// puso B) — una regresión peor que la fuga original. Por eso NO
			// se comprueba `generation` aquí (a diferencia de
			// beginTeardown/selfStopIfCurrent, donde si corresponde: allí se
			// decide si TERMINAR la sesión activa, aquí solo si limpiar un
			// tap que quedó huérfano porque NADIE lo reclamó).
			//
			// Residual aceptado: en el caso de generación distinta, la
			// goroutine del decoder de la sesión A (ya lanzada en este mismo
			// Start()) queda huérfana — se autodesarma sola en su propio tope
			// duro (ctx timeout). El decoder compartido no es seguro para dos
			// Decode() realmente concurrentes, pero requiere la misma ventana
			// de descheduling improbable que el resto de esta carrera; fuera
			// de alcance aquí (solo se corrige la condición de Stop tap/decoder).
			//
			// NOTA: LiveVideoOnCall (live_video.go) tiene exactamente el
			// mismo patrón (active=true bajo lock, tap.Start() fuera del
			// lock) y por tanto la misma fuga latente. Queda fuera de alcance
			// de esta rama — se corrige solo aquí (revisión de rama
			// feat/audio-tap-5000, hallazgo 1; el vídeo se trata aparte).
			a.mu.Lock()
			orphaned := !a.active
			a.mu.Unlock()
			if orphaned {
				a.logger.Warn("audio-listen: Stop() ganó la carrera contra tap.Start() sin que ninguna sesión lo reclame — desarmando el tap recién iniciado para no filtrarlo")
				a.tap.Stop()
				if a.decoder != nil {
					a.decoder.Stop()
				}
			}
		}
	}
}

// beginTeardown decide, BAJO LOCK, si procede desarmar, y si procede fija
// active=false ATÓMICAMENTE con esa decisión (junto con la comprobación de
// generación, si checkGen). Esto cierra la ventana de carrera entre
// "comprobar" y "desarmar": separarlas (comprobar bajo un lock, desarmar bajo
// otro lock distinto más tarde) permite que un Stop()+Start() completo se
// cuele en el hueco, y que una goroutine atrasada que YA había pasado su
// comprobación acabe desarmando la sesión NUEVA (el mismo bug que
// `generation` pretendía evitar, solo que por el orden de intercalado
// contrario). Con la decisión y el flip en una única sección crítica: si el
// Start() concurrente aún no ha corrido, esta llamada gana la carrera y para
// la sesión VIEJA (correcto); si ya corrió y avanzó `generation`, la
// comprobación de generación falla aquí mismo, sin hueco donde colarse
// (correcto). Devuelve el timer a cancelar (fuera del lock) si procede.
func (a *AudioListenOnCall) beginTeardown(checkGen bool, gen uint64) (proceed bool, timer *time.Timer) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.active || (checkGen && a.generation != gen) {
		return false, nil
	}
	a.active = false
	t := a.timer
	a.timer = nil
	return true, t
}

// Stop desarma (para el tap y el decodificador). Idempotente. Orden inverso
// al Start: primero el tap (deja de reenviar), luego el decodificador (que
// desbloquea su Decode en curso).
func (a *AudioListenOnCall) Stop() {
	proceed, timer := a.beginTeardown(false, 0)
	if !proceed {
		return
	}
	a.teardown(timer)
}

// selfStopIfCurrent desarma la sesión SOLO si `gen` sigue siendo la
// generación activa (ver campo `generation`, incrementado en cada Start).
// La invoca la goroutine del decoder cuando Decode() vuelve por sí solo. Sin
// esta comprobación (y sin que la comprobación sea ATÓMICA con el flip de
// `active`, ver beginTeardown), una goroutine "atrasada" de una sesión ya
// parada externamente podría desarmar por error una sesión posterior
// arrancada mientras tanto (re-Start rápido tras Stop).
func (a *AudioListenOnCall) selfStopIfCurrent(gen uint64) {
	proceed, timer := a.beginTeardown(true, gen)
	if !proceed {
		return
	}
	a.teardown(timer)
}

// teardown para el timer/tap/decodificador SIN mantener el lock (evita
// bloquear a otros llamantes mientras tap.Stop()/decoder.Stop() — que
// pueden tardar — se ejecutan). Solo debe llamarse tras un beginTeardown que
// haya devuelto proceed=true (o sea, con active YA puesto a false).
func (a *AudioListenOnCall) teardown(timer *time.Timer) {
	if timer != nil {
		timer.Stop()
	}
	if a.tap != nil {
		a.tap.Stop()
	}
	if a.decoder != nil {
		a.decoder.Stop()
	}
	a.logger.Info("audio-listen: desarmado")
}

// IsActive indica si el oído está armado.
func (a *AudioListenOnCall) IsActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active
}

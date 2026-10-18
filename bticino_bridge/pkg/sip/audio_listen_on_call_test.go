package sip

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// decodeCall registra los argumentos de una invocación a Decode.
type decodeCall struct {
	host string
	port int
	pt   int
	sink string
}

// fakeDecoder simula AudioTapDecoder: Decode BLOQUEA (como el ffmpeg real)
// hasta que Stop() lo libere. invoked se señaliza (canal con buffer) en
// cuanto Decode entra, para que los tests esperen sin sleeps ni carreras.
// block se RECREA en cada Decode(): un fakeDecoder puede vivir varios ciclos
// Start/Stop (restart), y un canal cerrado de una sesión anterior no debe
// desbloquear instantáneamente la sesión siguiente.
type fakeDecoder struct {
	mu      sync.Mutex
	calls   []decodeCall
	stops   int
	invoked chan struct{}
	block   chan struct{} // nil entre sesiones; Decode() lo crea, Stop() lo cierra y lo vacía
	err     error
}

func newFakeDecoder() *fakeDecoder {
	return &fakeDecoder{invoked: make(chan struct{}, 8)}
}

func (f *fakeDecoder) Decode(host string, port, pt int, sink string) error {
	f.mu.Lock()
	f.calls = append(f.calls, decodeCall{host, port, pt, sink})
	if f.block == nil {
		f.block = make(chan struct{})
	}
	block := f.block
	f.mu.Unlock()
	f.invoked <- struct{}{}
	<-block
	return f.err
}

func (f *fakeDecoder) Stop() {
	f.mu.Lock()
	f.stops++
	block := f.block
	f.block = nil // el próximo Decode() de una futura sesión crea uno nuevo
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
			// ya cerrado (Decode ya había vuelto por su cuenta)
		default:
			close(block)
		}
	}
}

func (f *fakeDecoder) counts() (calls int, stops int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls), f.stops
}

func (f *fakeDecoder) lastCall() (decodeCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return decodeCall{}, false
	}
	return f.calls[len(f.calls)-1], true
}

// waitInvoked bloquea hasta que Decode haya sido invocado al menos una vez
// (sincronización goroutine↔aserción por canal, sin sleeps ni carreras).
func (f *fakeDecoder) waitInvoked(t *testing.T) {
	t.Helper()
	select {
	case <-f.invoked:
	case <-time.After(2 * time.Second):
		t.Fatal("decoder.Decode nunca fue invocado")
	}
}

// orderCheckTap es un tap de prueba cuyo Start() verifica que el decoder ya
// fue invocado (canal decoderInvoked) ANTES de marcarse a sí mismo arrancado.
// Si el productor invirtiera el orden, startedAfterDecoder quedaría en false.
type orderCheckTap struct {
	decoderInvoked <-chan struct{}

	mu                  sync.Mutex
	starts              int
	startedAfterDecoder bool
}

func (t *orderCheckTap) Start() error {
	after := false
	select {
	case <-t.decoderInvoked:
		after = true
	case <-time.After(2 * time.Second):
	}
	t.mu.Lock()
	t.starts++
	t.startedAfterDecoder = after
	t.mu.Unlock()
	return nil
}

func (t *orderCheckTap) Stop() {}

func (t *orderCheckTap) snapshot() (int, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.starts, t.startedAfterDecoder
}

// TestAudioListenStartArmsDecoderBeforeTap verifica el orden exigido por el
// diseño: el decoder debe estar ya invocado (escuchando) antes de que el tap
// arranque a reenviar. El decoder señaliza un canal EXCLUSIVO del test (vía
// signalingDecoder) para no competir con otras lecturas del canal interno de
// fakeDecoder.
func TestAudioListenStartArmsDecoderBeforeTap(t *testing.T) {
	decoderInvoked := make(chan struct{}, 1)
	dec := newFakeDecoder()
	// Wrapper: reenvía la señal de invocación a un canal propio del test,
	// además del canal interno que usa fakeDecoder.
	wrapped := &signalingDecoder{inner: dec, extra: decoderInvoked}

	tap := &orderCheckTap{decoderInvoked: decoderInvoked}
	l := NewAudioListenOnCall(tap, wrapped, 15000, 110, "/tmp/audio-tap-test.wav", logrus.New())

	l.Start("test")

	deadline := time.Now().Add(2 * time.Second)
	for {
		starts, afterDecoder := tap.snapshot()
		if starts > 0 {
			if !afterDecoder {
				t.Fatal("el tap arrancó ANTES de que el decoder fuera invocado (orden incorrecto)")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("el tap nunca arrancó")
		}
		time.Sleep(2 * time.Millisecond)
	}

	l.Stop()
}

// signalingDecoder envuelve fakeDecoder y además emite en un canal propio del
// test en el momento en que Decode es invocado (antes de bloquear).
type signalingDecoder struct {
	inner *fakeDecoder
	extra chan struct{}
}

func (s *signalingDecoder) Decode(host string, port, pt int, sink string) error {
	s.extra <- struct{}{}
	return s.inner.Decode(host, port, pt, sink)
}

func (s *signalingDecoder) Stop() { s.inner.Stop() }

func TestAudioListenStartCallsDecoderWithExpectedParams(t *testing.T) {
	dec := newFakeDecoder()
	tap := &mockTap{}
	l := NewAudioListenOnCall(tap, dec, 15000, 110, "/tmp/sink.wav", logrus.New())

	l.Start("test")
	dec.waitInvoked(t)

	call, ok := dec.lastCall()
	if !ok {
		t.Fatal("esperaba una llamada a Decode")
	}
	if call.host != "127.0.0.1" {
		t.Fatalf("esperaba host 127.0.0.1, got %q", call.host)
	}
	if call.port != 15000 {
		t.Fatalf("esperaba port 15000, got %d", call.port)
	}
	if call.pt != 110 {
		t.Fatalf("esperaba payloadType 110, got %d", call.pt)
	}
	if call.sink != "/tmp/sink.wav" {
		t.Fatalf("esperaba sink /tmp/sink.wav, got %q", call.sink)
	}

	l.Stop()
}

func TestAudioListenStartIsIdempotent(t *testing.T) {
	dec := newFakeDecoder()
	tap := &mockTap{}
	l := NewAudioListenOnCall(tap, dec, 15000, 110, "/tmp/sink.wav", logrus.New())

	l.Start("a")
	dec.waitInvoked(t)
	l.Start("b") // no debe arrancar nada de nuevo

	if starts, _ := tap.counts(); starts != 1 {
		t.Fatalf("esperaba 1 arranque del tap (idempotente), hubo %d", starts)
	}
	if calls, _ := dec.counts(); calls != 1 {
		t.Fatalf("esperaba 1 invocación del decoder (idempotente), hubo %d", calls)
	}
	if !l.IsActive() {
		t.Fatal("esperaba IsActive()=true")
	}

	l.Stop()
}

func TestAudioListenStopDisarmsBoth(t *testing.T) {
	dec := newFakeDecoder()
	tap := &mockTap{}
	l := NewAudioListenOnCall(tap, dec, 15000, 110, "/tmp/sink.wav", logrus.New())

	l.Start("a")
	dec.waitInvoked(t)
	l.Stop()

	if l.IsActive() {
		t.Fatal("esperaba IsActive()=false tras Stop")
	}
	if _, stops := tap.counts(); stops != 1 {
		t.Fatalf("esperaba 1 parada del tap, hubo %d", stops)
	}
	if _, stops := dec.counts(); stops != 1 {
		t.Fatalf("esperaba 1 parada del decoder, hubo %d", stops)
	}
}

func TestAudioListenStopWithoutStartIsNoop(t *testing.T) {
	dec := newFakeDecoder()
	tap := &mockTap{}
	l := NewAudioListenOnCall(tap, dec, 15000, 110, "/tmp/sink.wav", logrus.New())

	l.Stop()

	if _, stops := tap.counts(); stops != 0 {
		t.Fatalf("Stop sin Start no debe parar el tap (hubo %d)", stops)
	}
	if _, stops := dec.counts(); stops != 0 {
		t.Fatalf("Stop sin Start no debe parar el decoder (hubo %d)", stops)
	}
}

func TestAudioListenRestartAfterStop(t *testing.T) {
	dec := newFakeDecoder()
	tap := &mockTap{}
	l := NewAudioListenOnCall(tap, dec, 15000, 110, "/tmp/sink.wav", logrus.New())

	l.Start("a")
	dec.waitInvoked(t)
	l.Stop()

	l.Start("b")
	dec.waitInvoked(t)
	if !l.IsActive() {
		t.Fatal("esperaba IsActive()=true tras re-Start")
	}
	if starts, _ := tap.counts(); starts != 2 {
		t.Fatalf("esperaba 2 arranques del tap, hubo %d", starts)
	}
	if calls, _ := dec.counts(); calls != 2 {
		t.Fatalf("esperaba 2 invocaciones del decoder, hubo %d", calls)
	}

	l.Stop()
}

func TestAudioListenTapErrorIsNonFatal(t *testing.T) {
	dec := newFakeDecoder()
	tap := &mockTap{err: errors.New("operation not permitted")}
	l := NewAudioListenOnCall(tap, dec, 15000, 110, "/tmp/sink.wav", logrus.New())

	l.Start("sin-permisos") // no debe entrar en pánico
	dec.waitInvoked(t)

	if !l.IsActive() {
		t.Fatal("esperaba IsActive()=true aunque el tap falle")
	}

	l.Stop()
}

func TestAudioListenNilTapAndDecoderAreSafe(t *testing.T) {
	l := NewAudioListenOnCall(nil, nil, 15000, 110, "/tmp/sink.wav", logrus.New())
	l.Start("a") // no debe entrar en pánico
	if !l.IsActive() {
		t.Fatal("esperaba IsActive()=true incluso sin tap/decoder")
	}
	l.Stop()
	if l.IsActive() {
		t.Fatal("esperaba IsActive()=false tras Stop")
	}
}

func TestAudioListenGuardAutoStopsWhenHangupIsMissed(t *testing.T) {
	dec := newFakeDecoder()
	tap := &mockTap{}
	l := NewAudioListenOnCall(tap, dec, 15000, 110, "/tmp/sink.wav", logrus.New())
	l.SetGuardTimeout(30 * time.Millisecond)
	l.Start("a")
	dec.waitInvoked(t)

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
	if _, stops := dec.counts(); stops != 1 {
		t.Fatalf("esperaba 1 parada del decoder por guard, hubo %d", stops)
	}
}

// selfEndingDecoder simula un decoder cuyo Decode TERMINA POR SÍ SOLO (p.ej.
// su tope duro interno) sin que nadie haya llamado a Stop(). invoked
// señaliza la entrada; stops cuenta las llamadas a Stop (para comprobar que
// AudioListenOnCall igualmente llama a decoder.Stop() al autodesarmarse).
type selfEndingDecoder struct {
	mu      sync.Mutex
	calls   int
	stops   int
	invoked chan struct{}
}

func newSelfEndingDecoder() *selfEndingDecoder {
	return &selfEndingDecoder{invoked: make(chan struct{}, 1)}
}

func (d *selfEndingDecoder) Decode(host string, port, pt int, sink string) error {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	d.invoked <- struct{}{}
	return nil // retorno natural, no provocado por Stop()
}

func (d *selfEndingDecoder) Stop() {
	d.mu.Lock()
	d.stops++
	d.mu.Unlock()
}

func (d *selfEndingDecoder) counts() (calls, stops int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls, d.stops
}

// signalingTap es un tap de prueba cuyo Stop() señaliza un canal (con
// buffer), para sincronizar el test con el desarme sin sleeps.
type signalingTap struct {
	mu      sync.Mutex
	starts  int
	stops   int
	stopped chan struct{}
}

func newSignalingTap() *signalingTap {
	return &signalingTap{stopped: make(chan struct{}, 1)}
}

func (t *signalingTap) Start() error {
	t.mu.Lock()
	t.starts++
	t.mu.Unlock()
	return nil
}

func (t *signalingTap) Stop() {
	t.mu.Lock()
	t.stops++
	t.mu.Unlock()
	select {
	case t.stopped <- struct{}{}:
	default:
	}
}

func (t *signalingTap) counts() (starts, stops int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.starts, t.stops
}

// TestAudioListenSelfDisarmsWhenDecoderReturnsNaturally cubre el hallazgo de
// revisión: si Decode() vuelve por sí solo (p.ej. su tope duro interno, que
// AudioListenOnCall alarga pero no hace infinito) sin que nadie haya llamado
// a Stop(), la sesión debe autodesarmarse (parar el tap, marcar inactive) en
// vez de quedar en un estado mentiroso (IsActive()=true con el tap
// reenviando RTP a un socket que ya nadie lee).
func TestAudioListenSelfDisarmsWhenDecoderReturnsNaturally(t *testing.T) {
	dec := newSelfEndingDecoder()
	tap := newSignalingTap()
	l := NewAudioListenOnCall(tap, dec, 15000, 110, "/tmp/sink.wav", logrus.New())

	l.Start("a")

	select {
	case <-tap.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("esperaba que el tap se parara solo tras el retorno natural del decoder")
	}

	if l.IsActive() {
		t.Fatal("esperaba IsActive()=false tras el retorno natural del decoder")
	}
	if _, stops := tap.counts(); stops != 1 {
		t.Fatalf("esperaba 1 parada del tap, hubo %d", stops)
	}
	if _, stops := dec.counts(); stops != 1 {
		t.Fatalf("esperaba 1 parada del decoder (Stop llamado al autodesarmar), hubo %d", stops)
	}
}

// racingDecoder simula un Decode() cuyo retorno el TEST controla con
// precisión, LLAMADA A LLAMADA (un canal de liberación propio por invocación,
// no compartido). A diferencia de fakeDecoder, Stop() NO libera la llamada en
// curso: modela un decoder real cuyo proceso puede tardar en morir más allá
// de la señal de Stop() (p.ej. ffmpeg no ha recibido aún la señal del SO), lo
// que permite retrasar deliberadamente el retorno "natural" de una sesión ya
// desarmada hasta después de que otra sesión haya arrancado.
type racingDecoder struct {
	mu       sync.Mutex
	calls    int
	stops    int
	invoked  chan struct{}
	releases []chan struct{}
}

func newRacingDecoder() *racingDecoder {
	return &racingDecoder{invoked: make(chan struct{}, 8)}
}

func (d *racingDecoder) Decode(host string, port, pt int, sink string) error {
	d.mu.Lock()
	d.calls++
	release := make(chan struct{})
	d.releases = append(d.releases, release)
	d.mu.Unlock()
	d.invoked <- struct{}{}
	<-release
	return nil
}

func (d *racingDecoder) Stop() {
	d.mu.Lock()
	d.stops++
	d.mu.Unlock()
	// Deliberadamente no libera ninguna llamada en curso.
}

func (d *racingDecoder) counts() (calls, stops int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls, d.stops
}

// releaseCall libera la llamada nº idx (0-based) a Decode().
func (d *racingDecoder) releaseCall(t *testing.T, idx int) {
	t.Helper()
	d.mu.Lock()
	if idx >= len(d.releases) {
		d.mu.Unlock()
		t.Fatalf("releaseCall(%d): solo hay %d llamadas registradas", idx, len(d.releases))
	}
	ch := d.releases[idx]
	d.mu.Unlock()
	close(ch)
}

// TestAudioListenStaleSelfStopDoesNotTearDownNewerSession reproduce, de forma
// determinista, el escenario del hallazgo de re-revisión: el Decode() de una
// sesión YA parada (gen=1) vuelve por su cuenta DESPUÉS de que una sesión
// nueva (gen=2) ya esté armada. selfStopIfCurrent(1), al ejecutarse tarde, no
// debe desarmar la sesión 2. Verificado por AUSENCIA de efecto tras un margen
// acotado (una aserción negativa no puede esperarse via canal sin más: se usa
// un margen fijo, no un sleep como mecanismo de sincronización primario — la
// señalización de las invocaciones vía `invoked` sí lo es).
func TestAudioListenStaleSelfStopDoesNotTearDownNewerSession(t *testing.T) {
	dec := newRacingDecoder()
	tap := &mockTap{}
	l := NewAudioListenOnCall(tap, dec, 15000, 110, "/tmp/sink.wav", logrus.New())

	l.Start("a") // gen=1
	select {
	case <-dec.invoked:
	case <-time.After(2 * time.Second):
		t.Fatal("decoder de la sesión 1 nunca fue invocado")
	}

	l.Stop() // desarma la sesión 1: active=false, tap.Stop(), dec.Stop().
	// El Decode() de la sesión 1 SIGUE bloqueado deliberadamente (releases[0]
	// no se ha cerrado): su retorno "natural" queda pendiente a propósito.
	if _, stops := dec.counts(); stops != 1 {
		t.Fatalf("esperaba 1 parada del decoder tras Stop(), hubo %d", stops)
	}

	l.Start("b") // gen=2: sesión nueva y activa, mientras la 1 sigue "en vuelo"
	select {
	case <-dec.invoked:
	case <-time.After(2 * time.Second):
		t.Fatal("decoder de la sesión 2 nunca fue invocado")
	}
	if !l.IsActive() {
		t.Fatal("esperaba IsActive()=true tras Start de la sesión 2")
	}

	// Ahora dejamos volver, con retraso deliberado, al Decode() de la sesión 1
	// (gen=1): su selfStopIfCurrent(1) corre con la sesión 2 (gen=2) YA
	// activa. beginTeardown debe rechazarlo por generación.
	dec.releaseCall(t, 0)

	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if !l.IsActive() {
		t.Fatal("BUG: la sesión 2 quedó desarmada por el auto-stop tardío de la sesión 1")
	}
	if starts, stops := tap.counts(); starts != 2 || stops != 1 {
		t.Fatalf("esperaba tap start=2 stop=1 (solo la sesión 1 parada), got start=%d stop=%d", starts, stops)
	}
	if calls, stops := dec.counts(); calls != 2 || stops != 1 {
		t.Fatalf("esperaba decoder calls=2 stops=1 (solo la sesión 1 parada), got calls=%d stops=%d", calls, stops)
	}

	l.Stop() // limpieza: desarma de verdad la sesión 2
	dec.releaseCall(t, 1)
}

// decoderWithTimeout es un fake decoder que además registra el timeout que
// AudioListenOnCall le configura (implementa audioDecoderTimeoutSetter).
// Bloquea en Decode hasta Stop(), igual que fakeDecoder.
type decoderWithTimeout struct {
	*fakeDecoder // puntero: evita copiar el sync.Mutex embebido
	mu           sync.Mutex
	timeout      time.Duration
	set          chan struct{}
}

func newDecoderWithTimeout() *decoderWithTimeout {
	return &decoderWithTimeout{
		fakeDecoder: newFakeDecoder(),
		set:         make(chan struct{}, 1),
	}
}

func (d *decoderWithTimeout) SetTimeout(dur time.Duration) {
	d.mu.Lock()
	d.timeout = dur
	d.mu.Unlock()
	select {
	case d.set <- struct{}{}:
	default:
	}
}

func (d *decoderWithTimeout) getTimeout() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.timeout
}

func (d *decoderWithTimeout) waitSet(t *testing.T) {
	t.Helper()
	select {
	case <-d.set:
	case <-time.After(2 * time.Second):
		t.Fatal("SetTimeout nunca fue invocado")
	}
}

func TestAudioListenSetsDefaultDecoderTimeoutWithoutGuard(t *testing.T) {
	dec := newDecoderWithTimeout()
	tap := &mockTap{}
	l := NewAudioListenOnCall(tap, dec, 15000, 110, "/tmp/sink.wav", logrus.New())

	l.Start("a")
	dec.waitSet(t)

	if got := dec.getTimeout(); got != defaultAudioListenDecoderTimeout {
		t.Fatalf("esperaba timeout por defecto %s, got %s", defaultAudioListenDecoderTimeout, got)
	}

	l.Stop()
}

func TestAudioListenSetsDecoderTimeoutFromGuard(t *testing.T) {
	dec := newDecoderWithTimeout()
	tap := &mockTap{}
	l := NewAudioListenOnCall(tap, dec, 15000, 110, "/tmp/sink.wav", logrus.New())
	l.SetGuardTimeout(5 * time.Minute)

	l.Start("a")
	dec.waitSet(t)

	if got := dec.getTimeout(); got != 5*time.Minute {
		t.Fatalf("esperaba timeout = guard (5m), got %s", got)
	}

	l.Stop()
}

func TestAudioListenGuardCanceledByStop(t *testing.T) {
	dec := newFakeDecoder()
	tap := &mockTap{}
	l := NewAudioListenOnCall(tap, dec, 15000, 110, "/tmp/sink.wav", logrus.New())
	l.SetGuardTimeout(30 * time.Millisecond)
	l.Start("a")
	dec.waitInvoked(t)
	l.Stop()
	time.Sleep(80 * time.Millisecond) // el guard ya habría vencido

	if _, stops := tap.counts(); stops != 1 {
		t.Fatalf("el guard cancelado no debe parar otra vez (stops=%d)", stops)
	}

	l.Start("b")
	dec.waitInvoked(t)
	if !l.IsActive() {
		t.Fatal("esperaba activo tras re-Start")
	}
	l.Stop()
}

// blockingStartTap es un tap cuyo Start() BLOQUEA hasta que el test cierra
// release, para reproducir de forma determinista la ventana de carrera del
// hallazgo 1 (revisión final de rama feat/audio-tap-5000): Start() marca
// active=true, suelta el lock, y SOLO ENTONCES llama a tap.Start() (aquí
// bloqueado). entered se cierra en cuanto Start() entra (ANTES de bloquear),
// para que el test sepa que ya está en la ventana sin usar sleeps.
type blockingStartTap struct {
	mu      sync.Mutex
	starts  int
	stops   int
	entered chan struct{}
	release chan struct{}
}

func newBlockingStartTap() *blockingStartTap {
	return &blockingStartTap{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (t *blockingStartTap) Start() error {
	close(t.entered)
	<-t.release
	t.mu.Lock()
	t.starts++
	t.mu.Unlock()
	return nil
}

func (t *blockingStartTap) Stop() {
	t.mu.Lock()
	t.stops++
	t.mu.Unlock()
}

func (t *blockingStartTap) counts() (starts, stops int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.starts, t.stops
}

// TestAudioListenStartStopRaceDoesNotLeakTap cubre el hallazgo 1 de la
// revisión final de rama: si un Stop() concurrente corre entero (bajo lock,
// active=false, tap.Stop() incluido) MIENTRAS el tap.Start() de un Start()
// anterior sigue bloqueado (fuera del lock, a propósito — apertura de socket
// AF_PACKET), el tap.Stop() de ese Stop() no sirve de nada porque el tap
// todavía no había arrancado desde su punto de vista. Sin el re-chequeo tras
// tap.Start(), el tap quedaría arrancado para siempre con active=false: el
// decoder se autodesarma solo en su tope duro, pero el tap (socket AF_PACKET
// + goroutine) no tiene ningún mecanismo de autodesarme — fuga permanente.
// Sincronizado enteramente por canales (entered/release/done), sin sleeps
// como mecanismo de sincronización primario.
func TestAudioListenStartStopRaceDoesNotLeakTap(t *testing.T) {
	dec := newFakeDecoder()
	tap := newBlockingStartTap()
	l := NewAudioListenOnCall(tap, dec, 15000, 110, "/tmp/sink.wav", logrus.New())

	startDone := make(chan struct{})
	go func() {
		l.Start("a") // bloquea dentro de tap.Start() hasta que el test libere
		close(startDone)
	}()

	// Esperar a que tap.Start() haya entrado (y esté bloqueado): ahora
	// active=true pero el tap real, desde fuera, aún no ha "arrancado".
	select {
	case <-tap.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("tap.Start() nunca entró")
	}

	// Stop() concurrente gana la carrera: corre entero (beginTeardown +
	// teardown, incluido su propio tap.Stop() — que aquí no incrementa stops
	// porque Start() sigue bloqueado y starts sigue en 0) mientras tap.Start()
	// de la sesión sigue bloqueado.
	l.Stop()

	if l.IsActive() {
		t.Fatal("esperaba IsActive()=false tras el Stop() que ganó la carrera")
	}
	if starts, stops := tap.counts(); starts != 0 || stops != 1 {
		t.Fatalf("antes de liberar: esperaba tap start=0 stop=1 (el Stop de teardown, sin efecto útil), got start=%d stop=%d", starts, stops)
	}

	// Liberar tap.Start(): "arranca" tarde, después de que Stop() ya corrió
	// entero. El fix debe detectar que la sesión ya no está activa y parar el
	// tap inmediatamente, en vez de dejarlo filtrado.
	close(tap.release)

	select {
	case <-startDone:
	case <-time.After(2 * time.Second):
		t.Fatal("l.Start() nunca volvió tras liberar tap.Start()")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		starts, stops := tap.counts()
		if starts == 1 && stops == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("BUG: tap filtrado tras la carrera — esperaba start=1 stop=2 (el segundo stop del re-chequeo), got start=%d stop=%d", starts, stops)
		}
		time.Sleep(2 * time.Millisecond)
	}

	if l.IsActive() {
		t.Fatal("esperaba IsActive()=false tras la carrera y el re-chequeo")
	}
}

// staggeredTap es un tap cuya PRIMERA invocación a Start() BLOQUEA hasta que
// el test cierra release; las invocaciones SIGUIENTES vuelven al instante.
// Modela LoopbackRTPTap.Start (idempotente: si el tap ya está en marcha,
// arrancarlo de nuevo no bloquea) para poder reproducir, de forma
// determinista, una sesión B que se arma y termina de re-chequearse ANTES de
// que el tap.Start() tardío de una sesión A anterior por fin vuelva.
type staggeredTap struct {
	mu      sync.Mutex
	calls   int
	starts  int
	stops   int
	entered chan struct{} // se cierra en la PRIMERA entrada a Start()
	release chan struct{} // el test lo cierra para liberar esa primera llamada
}

func newStaggeredTap() *staggeredTap {
	return &staggeredTap{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (t *staggeredTap) Start() error {
	t.mu.Lock()
	t.calls++
	first := t.calls == 1
	t.mu.Unlock()
	if first {
		close(t.entered)
		<-t.release
	}
	t.mu.Lock()
	t.starts++
	t.mu.Unlock()
	return nil
}

func (t *staggeredTap) Stop() {
	t.mu.Lock()
	t.stops++
	t.mu.Unlock()
}

func (t *staggeredTap) counts() (starts, stops int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.starts, t.stops
}

// TestAudioListenGenerationMismatchDoesNotStopNewerSession cubre la
// regresión detectada en la re-revisión: el re-chequeo post-tap.Start() del
// hallazgo 1 debe fijarse SOLO en `active` (¿alguien reclama el tap
// compartido ahora mismo?), NUNCA en `generation`. a.tap/a.decoder son
// instancias ÚNICAS compartidas por TODAS las generaciones (construidas una
// vez en cmd/main.go, no una por sesión). Si mientras el tap.Start() de la
// sesión A sigue bloqueado se cuela un Stop()+Start() completo (sesión B,
// generación distinta), el tap compartido pasa a pertenecer LEGÍTIMAMENTE a
// B. Cuando el tap.Start() tardío de A por fin vuelve, NO debe pararlo: eso
// cortaría en silencio el audio de la llamada EN VIVO de B (IsActive()
// seguiría devolviendo true, porque lo puso B) — una regresión peor que la
// fuga que el hallazgo 1 corrigió. Sincronizado por canales
// (entered/release/waitInvoked); los únicos sleeps son márgenes acotados
// para aserciones NEGATIVAS (ausencia de un Stop indebido), sin usarse como
// mecanismo de sincronización primario.
func TestAudioListenGenerationMismatchDoesNotStopNewerSession(t *testing.T) {
	dec := newFakeDecoder()
	tap := newStaggeredTap()
	l := NewAudioListenOnCall(tap, dec, 15000, 110, "/tmp/sink.wav", logrus.New())

	startADone := make(chan struct{})
	go func() {
		l.Start("a") // sesión A (gen=1): su tap.Start() es la 1ª invocación, bloqueada
		close(startADone)
	}()
	dec.waitInvoked(t) // decode de la sesión A invocado

	select {
	case <-tap.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("tap.Start() de la sesión A nunca entró")
	}

	// Desarma A por completo (beginTeardown+teardown, incluido su propio
	// decoder.Stop() — libera el Decode() bloqueado de A) MIENTRAS su
	// tap.Start() sigue bloqueado.
	l.Stop()
	if l.IsActive() {
		t.Fatal("esperaba IsActive()=false tras Stop() de la sesión A")
	}

	// Arranca la sesión B (gen=2): su tap.Start() es la 2ª invocación del
	// tap compartido, que en este fake NO bloquea (modela idempotencia) —
	// l.Start("b") vuelve ya con el re-chequeo del hallazgo 1 incluido.
	l.Start("b")
	dec.waitInvoked(t) // decode de la sesión B invocado

	if !l.IsActive() {
		t.Fatal("esperaba IsActive()=true tras Start de la sesión B")
	}
	// stop=1 es el tap.Stop() del teardown de A (paso previo, sin efecto útil
	// porque el tap seguía bloqueado): NO debe haber un segundo stop aquí por
	// el armado de B.
	if starts, stops := tap.counts(); starts != 1 || stops != 1 {
		t.Fatalf("tras armar B (A aún bloqueado): esperaba tap start=1 (solo B) stop=1 (el de teardown de A), got start=%d stop=%d", starts, stops)
	}

	// Liberar el tap.Start() tardío de A: su propio re-chequeo debe ver
	// active=true (sesión B activa, aunque generation ya no sea la de A) y
	// NO debe tocar el tap compartido.
	close(tap.release)

	select {
	case <-startADone:
	case <-time.After(2 * time.Second):
		t.Fatal("l.Start(a) nunca volvió tras liberar su tap.Start()")
	}

	// Margen para dejar correr (si lo hubiera, incorrectamente) el
	// tap.Stop()/decoder.Stop() del re-chequeo tardío de A, y confirmar por
	// AUSENCIA que la sesión B no resultó afectada.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if !l.IsActive() {
		t.Fatal("BUG: la sesión B quedó desarmada por el tap.Start() tardío de A (regresión del re-chequeo por generación)")
	}
	// stop sigue en 1 (el mismo de antes, el del teardown de A): el
	// re-chequeo tardío de A NO debe añadir un segundo stop sobre el tap
	// compartido, que a estas alturas pertenece a la sesión B, activa.
	if starts, stops := tap.counts(); starts != 2 || stops != 1 {
		t.Fatalf("esperaba tap start=2 (A tardío + B) stop=1 (SIN un segundo stop indebido sobre el tap de B), got start=%d stop=%d", starts, stops)
	}

	l.Stop() // limpieza real de la sesión B
}

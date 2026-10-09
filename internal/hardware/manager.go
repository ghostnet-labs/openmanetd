package hardware

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/openmanet/openmanetd/internal/util/board"
	"github.com/rs/zerolog"
)

// Loop timing.
const (
	maxStepInterval = time.Second
	minStepInterval = 10 * time.Millisecond
	maxSubscribers  = 16
)

// Shutdowner powers the node off after the radios are down.
type Shutdowner interface {
	Poweroff(ctx context.Context) error
}

// ExecShutdowner runs the OpenWrt poweroff command.
type ExecShutdowner struct{}

// Poweroff implements Shutdowner.
func (ExecShutdowner) Poweroff(ctx context.Context) error {
	if err := exec.CommandContext(ctx, "/sbin/poweroff").Run(); err != nil {
		return fmt.Errorf("poweroff: %w", err)
	}

	return nil
}

// CommsGuard lets the recovery ladder coordinate a USB hub reset with the
// comms subsystem, whose OpenVLM audio device sits on the same hub.
type CommsGuard interface {
	// TransmitActive reports whether a PTT transmit is in progress.
	TransmitActive() bool
	// BeforeHubReset is called just before the hub reset pulse.
	BeforeHubReset(ctx context.Context)
}

// Options configures a Manager. Every dependency may be nil; the manager
// then reports the matching telemetry as unavailable and drives nothing
// that needs it.
type Options struct {
	Log        zerolog.Logger
	Clock      Clock
	Lines      LineOpener
	Bus        Bus
	Battery    MonitorReader
	Store      RecoveryStore
	Watchdogs  WatchdogOpener
	Shutdowner Shutdowner
	Comms      CommsGuard
	// BoardID is board.json model.id ("" when unknown).
	BoardID string
	// WatchdogIdentity selects the gpio-wdt device (DefaultWatchdogIdentity
	// when empty).
	WatchdogIdentity string
	// FaultInputs names the status inputs to request (see FaultInputNames).
	// Inputs whose source is not selected must stay out of this list so
	// they report unavailable rather than a floating level.
	FaultInputs []string
	// PackOhm is R_pack for the compensated voltage (DefaultPackOhm if 0).
	PackOhm float64
	// SeriesCells is the pack's series cell count (0 when unknown). The
	// low-battery policy runs only for the V1 3S pack.
	SeriesCells uint32
	// ActuationEnable allows driving GPIO and bus controls on a detected V1.
	ActuationEnable bool
	// WatchdogEnable allows opening and petting the supervisor watchdog.
	WatchdogEnable bool
	// BatteryShutdownEnable allows the low-battery graceful poweroff.
	BatteryShutdownEnable bool
}

// requests are operator requests recorded by the control API and consumed
// by the step loop.
type requests struct {
	powerCaller   [radioCount]string
	recoverCaller [radioCount]string
	rfCaller      string
	gnssCaller    string
	power         [radioCount]bool
	recover       [radioCount]bool
	rfDisable     bool
	rfChanged     bool
	gnssReset     bool
}

// inputState tracks one fault input.
type inputState struct {
	lastChange time.Time
	edges      uint64
	requested  bool
	fault      bool
}

// Manager is the hardware manager. Construct with New, drive with Run (or
// Step in tests), and read with Status, Events and Subscribe.
type Manager struct { //nolint:govet // Keep mu directly above the fields it guards.
	log        zerolog.Logger
	clock      Clock
	lineOpener LineOpener
	bus        Bus
	store      RecoveryStore
	wdOpener   WatchdogOpener
	shutdowner Shutdowner
	comms      CommsGuard
	done       chan struct{}
	// wake has capacity 1 so control requests never block: one pending
	// wake-up is enough because Step consumes every recorded request.
	wake      chan struct{}
	closeOnce sync.Once

	// stepMu serializes Step and Close. A step runs multi-line hardware
	// sequences (enable, settle, release) that must not interleave, so the
	// lock is deliberately held across that I/O; no other lock is taken
	// inside it except briefly mu.
	stepMu sync.Mutex
	// Loop-owned state, touched only with stepMu held.
	lines              Lines
	watchdog           Watchdog
	battery            *batteryTracker
	radios             [radioCount]*radioMachine
	inputs             [faultLineCount]inputState
	record             RecoveryRecord
	opts               Options
	powerOKSince       time.Time
	bothFailedSince    time.Time
	lastHubReset       time.Time
	lastPet            time.Time
	shutdownAt         time.Time
	wdDevice           string
	petsTotal          uint64
	nextWake           time.Duration
	controlMode        ControlMode
	controlReason      ControlModeReason
	lastLevel          BatteryLevel
	bootsLastHour      uint32
	boardDetected      bool
	storeHealthy       bool
	powerFault         bool
	hostResetRequested bool
	hostResetBlocked   bool
	shutdownPending    bool
	shutdownDone       bool
	shutdownSkipped    bool
	closed             bool

	mu        sync.Mutex // protects the fields below
	req       requests
	status    Status
	events    eventRing
	subs      map[int]chan struct{}
	nextSubID int
}

// New builds a manager: it detects the board, loads the boot-loop guard,
// picks the control mode, requests lines and opens the watchdog. It never
// fails; anything missing degrades to telemetry-only and is reported in
// the status.
func New(opts Options) *Manager {
	if opts.Clock == nil {
		opts.Clock = realClock{}
	}

	if opts.Store == nil {
		opts.Store = &memoryStore{}
	}

	if opts.WatchdogIdentity == "" {
		opts.WatchdogIdentity = DefaultWatchdogIdentity
	}

	m := &Manager{
		log:        opts.Log,
		clock:      opts.Clock,
		lineOpener: opts.Lines,
		bus:        opts.Bus,
		store:      opts.Store,
		wdOpener:   opts.Watchdogs,
		shutdowner: opts.Shutdowner,
		comms:      opts.Comms,
		opts:       opts,
		done:       make(chan struct{}),
		wake:       make(chan struct{}, 1),
		subs:       make(map[int]chan struct{}, maxSubscribers),
		nextWake:   maxStepInterval,
		lastLevel:  BatteryLevelUnknown,
	}

	now := m.clock.Now()
	m.boardDetected = opts.BoardID == board.GhostnetV1
	m.battery = newBatteryTracker(opts.Battery, opts.SeriesCells, opts.PackOhm, m.boardDetected)

	m.loadRecord(now)
	m.selectMode()
	m.openLines()
	m.openWatchdog()
	m.initRadios(now)

	m.log.Info().
		Bool("boardDetected", m.boardDetected).
		Str("controlMode", m.controlMode.String()).
		Str("reason", m.controlReason.String()).
		Bool("watchdogArmed", m.watchdog != nil).
		Uint32("recoveryBootsLastHour", m.bootsLastHour).
		Msg("hardware manager started")

	m.publish(now)

	return m
}

// loadRecord reads the boot-loop guard record. An unreadable record leaves
// the guard counting from zero but blocks software host-reset requests for
// this boot, because they could no longer be counted.
func (m *Manager) loadRecord(now time.Time) {
	rec, err := m.store.Load()
	if err != nil {
		m.log.Error().Err(err).Msg("hardware: recovery record unreadable; host reset requests disabled this boot")

		rec = RecoveryRecord{}
	} else {
		m.storeHealthy = true
	}

	rec.RecoveryResets = prune(rec.RecoveryResets, now)
	rec.HostResetRequests = prune(rec.HostResetRequests, now)
	m.record = rec
	m.bootsLastHour = uint32(countSince(rec.RecoveryResets, now.Add(-bootLoopWindow)))
}

func (m *Manager) selectMode() {
	m.controlMode = ControlModeTelemetryOnly

	switch {
	case !m.boardDetected:
		m.controlReason = ControlReasonBoardNotDetected
	case !m.opts.ActuationEnable:
		m.controlReason = ControlReasonActuationDisabled
	case m.bootsLastHour >= bootLoopLimit:
		m.controlReason = ControlReasonBootLoopGuard
		m.log.Error().Uint32("recoveryBootsLastHour", m.bootsLastHour).
			Msg("hardware: boot-loop guard tripped; telemetry-only for this boot")
	case m.lineOpener == nil:
		m.controlReason = ControlReasonLinesUnresolved
	default:
		m.controlMode = ControlModeActive
		m.controlReason = ControlReasonNone
	}
}

// openLines requests the outputs (active mode only) and the configured
// fault inputs (detected board only). Lines are never requested on any
// other board.
func (m *Manager) openLines() {
	if !m.boardDetected || m.lineOpener == nil {
		return
	}

	specs := make([]LineSpec, 0, len(outputSpecs())+faultLineCount)

	if m.controlMode == ControlModeActive {
		for _, s := range outputSpecs() {
			if !m.lineOpener.Resolve(s.Name) {
				m.log.Error().Str("line", s.Name).Msg("hardware: required GPIO line name not found; telemetry-only")
				m.controlMode, m.controlReason = ControlModeTelemetryOnly, ControlReasonLinesUnresolved
				specs = specs[:0]

				break
			}

			specs = append(specs, s)
		}
	}

	wanted := make(map[string]bool, len(m.opts.FaultInputs))
	for _, n := range m.opts.FaultInputs {
		wanted[n] = true
	}

	inputs := faultInputs()
	for i, in := range inputs {
		if !wanted[in.spec.Name] {
			continue
		}

		delete(wanted, in.spec.Name)

		if !m.lineOpener.Resolve(in.spec.Name) {
			m.log.Warn().Str("line", in.spec.Name).Msg("hardware: fault input not found; reported unavailable")

			continue
		}

		specs = append(specs, in.spec)
		m.inputs[i].requested = true
	}

	for n := range wanted {
		m.log.Warn().Str("line", n).Msg("hardware: unknown fault input name in hardware.faultInputs; ignored")
	}

	if len(specs) == 0 {
		return
	}

	lines, err := m.lineOpener.Open(specs)
	if err != nil {
		m.log.Error().Err(err).Msg("hardware: GPIO line request failed; telemetry-only")

		if m.controlMode == ControlModeActive {
			m.controlMode, m.controlReason = ControlModeTelemetryOnly, ControlReasonLinesUnresolved
		}

		for i := range m.inputs {
			m.inputs[i].requested = false
		}

		return
	}

	m.lines = lines
}

// openWatchdog arms the supervisor watchdog service when enabled on a
// detected V1. Telemetry-only mode still pets: petting only keeps the node
// alive, while every reset request is separately gated on active control.
func (m *Manager) openWatchdog() {
	if !m.opts.WatchdogEnable || !m.boardDetected || m.wdOpener == nil {
		return
	}

	wd, dev, err := m.wdOpener.Open(m.opts.WatchdogIdentity)
	if err != nil {
		m.log.Error().Err(err).Msg("hardware: supervisor watchdog not armed")

		return
	}

	m.watchdog, m.wdDevice = wd, dev
	m.log.Info().Str("device", dev).Msg("hardware: supervisor watchdog armed")
}

// initRadios adopts each radio's present hardware state, so a daemon
// restart neither glitches nor re-sequences a radio that is already on.
// The operator's power request is not persisted: every radio is requested
// on at daemon start.
func (m *Manager) initRadios(now time.Time) {
	m.mu.Lock()
	for i := range m.req.power {
		m.req.power[i] = true
	}
	m.mu.Unlock()

	for _, r := range Radios() {
		rm := newRadioMachine(r)
		m.radios[r.index()] = rm

		if m.bus != nil {
			rm.netdev = m.bus.Netdev(r)
		}

		if m.controlMode != ControlModeActive || m.bus == nil || !m.bus.Configured(r) {
			rm.setState(StateUnmanaged, ReasonControlDisabled, now)

			continue
		}

		rm.managed = true

		on, err := m.lines.Get(rm.timing.enableLine)
		if err != nil {
			m.log.Error().Err(err).Str("radio", r.String()).Msg("hardware: cannot read radio enable; radio unmanaged")

			rm.managed = false
			rm.setState(StateUnmanaged, ReasonControlDisabled, now)

			continue
		}

		if on {
			rm.railOn = true
			rm.enterEnumerating(now)
			rm.reason = ReasonStartup
		} else {
			rm.offSince = now.Add(-rm.timing.minOff)
			rm.setState(StateOff, ReasonStartup, now)
		}
	}

	if !m.radios[RadioWiFi.index()].managed {
		return
	}

	disabled, err := m.lines.Get(LineWiFiWDis1N)
	if err != nil {
		return
	}

	m.radios[RadioWiFi.index()].rfDisabled = disabled

	m.mu.Lock()
	m.req.rfDisable = disabled
	m.mu.Unlock()
}

// Done is closed when Run returns.
func (m *Manager) Done() <-chan struct{} {
	return m.done
}

// Run drives the manager until ctx is done, then releases the hardware.
func (m *Manager) Run(ctx context.Context) {
	defer close(m.done)
	defer m.Close()

	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-m.wake:
		}

		next := m.Step(ctx)
		timer.Reset(next)
	}
}

// Close releases every GPIO line and closes the watchdog. The watchdog is
// closed cleanly (magic close) unless a host reset was requested. Close is
// idempotent.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.stepMu.Lock()
		defer m.stepMu.Unlock()

		m.closed = true

		if m.lines != nil {
			if err := m.lines.Close(); err != nil {
				m.log.Warn().Err(err).Msg("hardware: releasing GPIO lines")
			}
		}

		if m.watchdog != nil {
			if err := m.watchdog.Close(!m.hostResetRequested); err != nil {
				m.log.Warn().Err(err).Msg("hardware: closing watchdog")
			}
		}
	})
}

// Step runs one evaluation pass at the clock's current time and returns
// how long until the next pass is due. Run calls it; tests call it
// directly with a fake clock.
func (m *Manager) Step(ctx context.Context) time.Duration {
	m.stepMu.Lock()
	defer m.stepMu.Unlock()

	if m.closed {
		return maxStepInterval
	}

	now := m.clock.Now()
	req := m.takeRequests()
	m.nextWake = maxStepInterval

	m.readInputs(now)
	m.battery.sample(now)
	m.applyBatteryPolicy(now)
	m.updatePowerFault(now)
	m.applyRFDisable(req)
	m.applyGNSSReset(ctx, req)

	for _, rm := range m.radios {
		m.stepRadio(ctx, rm, now, req)
	}

	m.checkHostReset(now)
	m.petWatchdog(now)
	m.runShutdown(ctx, now)
	m.publish(now)

	if m.nextWake < minStepInterval {
		return minStepInterval
	}

	return m.nextWake
}

// wakeAt lowers the next step delay so a timer fires on time.
func (m *Manager) wakeAt(now, at time.Time) {
	if at.IsZero() {
		return
	}

	if d := at.Sub(now); d < m.nextWake {
		m.nextWake = d
	}
}

// takeRequests copies the pending requests and clears the one-shot ones.
func (m *Manager) takeRequests() requests {
	m.mu.Lock()
	defer m.mu.Unlock()

	req := m.req
	m.req.recover = [radioCount]bool{}
	m.req.recoverCaller = [radioCount]string{}
	m.req.rfChanged = false
	m.req.rfCaller = ""
	m.req.gnssReset = false
	m.req.gnssCaller = ""

	return req
}

// readInputs samples every requested fault input.
func (m *Manager) readInputs(now time.Time) {
	if m.lines == nil {
		return
	}

	inputs := faultInputs()
	for i := range m.inputs {
		st := &m.inputs[i]
		if !st.requested {
			continue
		}

		asserted, err := m.lines.Get(inputs[i].spec.Name)
		if err != nil {
			m.log.Debug().Err(err).Str("line", inputs[i].spec.Name).Msg("hardware: fault input read failed")

			continue
		}

		fault := asserted == inputs[i].faultWhenAsserted
		if fault != st.fault {
			st.fault = fault
			st.lastChange = now
		}

		st.edges = m.lines.Edges(inputs[i].spec.Name)
	}
}

// inputFault reports whether a requested input currently reports a fault.
func (m *Manager) inputFault(l FaultLine) bool {
	st := m.inputs[int(l)-1]

	return st.requested && st.fault
}

// applyRFDisable drives WIFI_WDIS1_N when the request changed.
func (m *Manager) applyRFDisable(req requests) {
	rm := m.radios[RadioWiFi.index()]
	if !rm.managed || !req.rfChanged || req.rfDisable == rm.rfDisabled {
		return
	}

	if err := m.lines.Set(LineWiFiWDis1N, req.rfDisable); err != nil {
		m.log.Error().Err(err).Str("caller", req.rfCaller).Msg("hardware: setting Wi-Fi RF disable failed")

		return
	}

	rm.rfDisabled = req.rfDisable
	m.log.Info().Bool("disabled", req.rfDisable).Str("caller", req.rfCaller).Msg("hardware: Wi-Fi RF disable applied")
}

// gnssResetPulse is how long GNSS_RESET_N is held low.
const gnssResetPulse = 100 * time.Millisecond

// applyGNSSReset pulses GNSS_RESET_N low. It never touches PPS or the UART.
func (m *Manager) applyGNSSReset(ctx context.Context, req requests) {
	if !req.gnssReset || m.controlMode != ControlModeActive {
		return
	}

	start := m.clock.Now()

	err := m.lines.Set(LineGNSSResetN, true)
	if err == nil {
		err = m.clock.Sleep(ctx, gnssResetPulse)
	}

	// Always release, even after a failed assert or a canceled sleep.
	if rerr := m.lines.Set(LineGNSSResetN, false); err == nil {
		err = rerr
	}

	m.recordEvent(Event{
		At: start, Step: StepGNSSReset, Attempt: 1, Trigger: ReasonUser,
		Caller: req.gnssCaller, Result: resultOf(err), Detail: errText(err),
		DurationMs: durationMs(m.clock.Now().Sub(start)),
	})
}

// publish assembles the status, stores it, and notifies subscribers when
// anything other than the timestamp changed.
func (m *Manager) publish(now time.Time) {
	st := Status{
		GeneratedAt:           now,
		Battery:               m.battery.status(now),
		RecoveryBootsLastHour: m.bootsLastHour,
		ControlMode:           m.controlMode,
		ControlModeReason:     m.controlReason,
		BoardDetected:         m.boardDetected,
		PowerFault:            m.powerFault,
		ShutdownPending:       m.shutdownPending,
		Watchdog: WatchdogStatus{
			LastPetAt:          m.lastPet,
			Device:             m.wdDevice,
			PetsTotal:          m.petsTotal,
			WDOEdgesTotal:      m.inputs[FaultLineSupervisorWDO-1].edges,
			Enabled:            m.opts.WatchdogEnable,
			Armed:              m.watchdog != nil && !m.hostResetRequested,
			HostResetRequested: m.hostResetRequested,
		},
	}

	inputs := faultInputs()
	for i, in := range inputs {
		s := m.inputs[i]
		ls := LineStateUnavailable

		if s.requested {
			ls = LineStateNormal
			if s.fault {
				ls = LineStateAsserted
			}
		}

		st.FaultLines[i] = FaultLineStatus{
			LastChangeAt: s.lastChange,
			Name:         in.spec.Name,
			EdgesTotal:   s.edges,
			Line:         in.line,
			State:        ls,
		}
	}

	for i, rm := range m.radios {
		st.Radios[i] = rm.status()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for i := range st.Radios {
		st.Radios[i].PowerRequested = m.req.power[i]
	}

	prev := m.status
	prev.GeneratedAt = now
	m.status = st

	if prev == st {
		return
	}

	for _, ch := range m.subs {
		select {
		case ch <- struct{}{}:
		default: // a notification is already pending
		}
	}
}

// recordEvent logs one recovery or control action and adds it to the ring.
func (m *Manager) recordEvent(e Event) {
	ev := m.log.Warn()
	if e.Result == ResultExhausted || e.Step == StepHostReset || e.Step == StepShutdown {
		ev = m.log.Error()
	}

	ev.Str("radio", e.Radio.String()).
		Str("step", e.Step.String()).
		Uint32("attempt", e.Attempt).
		Str("trigger", e.Trigger.String()).
		Str("result", e.Result.String()).
		Uint32("duration_ms", e.DurationMs).
		Str("caller", e.Caller).
		Str("detail", e.Detail).
		Msg("hardware recovery event")

	m.mu.Lock()
	m.events.add(e)
	m.mu.Unlock()
}

func resultOf(err error) Result {
	if err != nil {
		return ResultFailed
	}

	return ResultSucceeded
}

func errText(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

func durationMs(d time.Duration) uint32 {
	if d <= 0 {
		return 0
	}

	return uint32(d / time.Millisecond)
}

// Status returns the most recently published status.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.status
}

// Events returns up to limit recovery events, newest first, optionally
// filtered to one radio. limit <= 0 returns the whole ring.
func (m *Manager) Events(limit int, r Radio) []Event {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.events.newest(limit, r)
}

// Subscribe returns a channel that receives a value whenever the published
// status changes (coalesced: at most one pending notification), and a
// cancel function that must be called to unsubscribe.
func (m *Manager) Subscribe() (<-chan struct{}, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.subs) >= maxSubscribers {
		return nil, nil, ErrTooManySubscribers
	}

	id := m.nextSubID
	m.nextSubID++
	// Capacity 1 coalesces notifications: a subscriber only needs to know
	// that the status changed since it last read it.
	ch := make(chan struct{}, 1)
	m.subs[id] = ch

	return ch, func() {
		m.mu.Lock()
		delete(m.subs, id)
		m.mu.Unlock()
	}, nil
}

// signal wakes the step loop without blocking.
func (m *Manager) signal() {
	select {
	case m.wake <- struct{}{}:
	default: // a wake-up is already pending
	}
}

// controllable validates a control request against the published status.
// Callers hold mu.
func (m *Manager) controllable(r Radio) error {
	if !r.valid() {
		return ErrUnknownRadio
	}

	if m.status.ControlMode != ControlModeActive {
		return ErrTelemetryOnly
	}

	if m.status.Radios[r.index()].State == StateUnmanaged {
		return ErrRadioUnmanaged
	}

	return nil
}

// SetRadioPower records an operator power request. The state machine acts
// on it at its next step.
func (m *Manager) SetRadioPower(r Radio, on bool, caller string) (RadioStatus, error) {
	m.mu.Lock()

	if err := m.controllable(r); err != nil {
		m.mu.Unlock()

		return RadioStatus{}, err
	}

	i := r.index()
	if on && m.status.Radios[i].State == StateFailed {
		m.mu.Unlock()

		return RadioStatus{}, ErrRadioFailed
	}

	m.req.power[i] = on
	m.req.powerCaller[i] = caller
	m.status.Radios[i].PowerRequested = on
	rs := m.status.Radios[i]
	m.mu.Unlock()

	m.log.Info().Str("radio", r.String()).Bool("enabled", on).Str("caller", caller).Msg("hardware: radio power requested")
	m.signal()

	return rs, nil
}

// SetRFDisable records an RF disable request. Only Wi-Fi has the line.
func (m *Manager) SetRFDisable(r Radio, disabled bool, caller string) (RadioStatus, error) {
	if r != RadioWiFi {
		if !r.valid() {
			return RadioStatus{}, ErrUnknownRadio
		}

		return RadioStatus{}, ErrNoRFDisable
	}

	m.mu.Lock()

	if err := m.controllable(r); err != nil {
		m.mu.Unlock()

		return RadioStatus{}, err
	}

	m.req.rfDisable = disabled
	m.req.rfChanged = true
	m.req.rfCaller = caller
	rs := m.status.Radios[r.index()]
	m.mu.Unlock()

	m.log.Info().Bool("disabled", disabled).Str("caller", caller).Msg("hardware: Wi-Fi RF disable requested")
	m.signal()

	return rs, nil
}

// Recover requests the recovery ladder from step 1, or clears a failed
// radio and powers it on again.
func (m *Manager) Recover(r Radio, caller string) (RadioStatus, error) {
	m.mu.Lock()

	if err := m.controllable(r); err != nil {
		m.mu.Unlock()

		return RadioStatus{}, err
	}

	i := r.index()
	if m.status.Radios[i].State == StateOff {
		m.mu.Unlock()

		return RadioStatus{}, ErrRadioOff
	}

	m.req.recover[i] = true
	m.req.recoverCaller[i] = caller
	rs := m.status.Radios[i]
	m.mu.Unlock()

	m.log.Info().Str("radio", r.String()).Str("caller", caller).Msg("hardware: radio recovery requested")
	m.signal()

	return rs, nil
}

// ResetGNSS requests a GNSS_RESET_N pulse and returns the request time.
func (m *Manager) ResetGNSS(caller string) (time.Time, error) {
	now := m.clock.Now()

	m.mu.Lock()

	if m.status.ControlMode != ControlModeActive {
		m.mu.Unlock()

		return time.Time{}, ErrTelemetryOnly
	}

	m.req.gnssReset = true
	m.req.gnssCaller = caller
	m.mu.Unlock()

	m.log.Info().Str("caller", caller).Msg("hardware: GNSS reset requested")
	m.signal()

	return now, nil
}

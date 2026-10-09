package hardware

import (
	"context"
	"errors"
	"time"
)

// Radio sequencing timings (design section 4, to bench-confirm in GHO-21).
const (
	haLowSettle        = 20 * time.Millisecond
	haLowDeviceTimeout = 10 * time.Second
	haLowNetdevTimeout = 30 * time.Second
	wifiSettle         = 100 * time.Millisecond
	wifiDeviceTimeout  = 5 * time.Second
	wifiNetdevTimeout  = 20 * time.Second
	radioMinOff        = 2 * time.Second
	// healthGrace is how long the device or netdev may be missing while the
	// rail is on before the ladder starts. It also gives mt76 firmware
	// self-recovery time to act before step 1w.
	healthGrace = 60 * time.Second
	// stableUpReset is how long a radio must stay up before its ladder
	// counters reset, so a flapping radio still exhausts its budget.
	stableUpReset = 10 * time.Minute
)

// radioTiming holds one radio's sequencing parameters.
type radioTiming struct {
	enableLine    string
	settle        time.Duration
	deviceTimeout time.Duration
	netdevTimeout time.Duration
	minOff        time.Duration
	firstStep     Step
	hasHubStep    bool
}

func timingFor(r Radio) radioTiming {
	if r == RadioHaLow {
		return radioTiming{
			enableLine:    LineHaLowPwrEn,
			settle:        haLowSettle,
			deviceTimeout: haLowDeviceTimeout,
			netdevTimeout: haLowNetdevTimeout,
			minOff:        radioMinOff,
			firstStep:     StepResetUSBDevice,
			hasHubStep:    true,
		}
	}

	return radioTiming{
		enableLine:    LineWiFiPwrEn,
		settle:        wifiSettle,
		deviceTimeout: wifiDeviceTimeout,
		netdevTimeout: wifiNetdevTimeout,
		minOff:        radioMinOff,
		firstStep:     StepResetPCIeFunction,
	}
}

// radioMachine is one radio's power state machine. It is used only from
// the manager's step goroutine.
type radioMachine struct {
	since            time.Time
	deadline         time.Time
	netDeadline      time.Time
	unhealthySince   time.Time
	upSince          time.Time
	offSince         time.Time
	lastStepAt       time.Time
	nextAction       time.Time
	netdev           string
	pendingCaller    string
	timing           radioTiming
	recoveriesTotal  uint64
	powerCyclesTotal uint64
	failedTotal      uint64
	stepAttempt      uint32
	powerCycles      uint32
	id               Radio
	state            State
	reason           Reason
	trigger          Reason
	step             Step
	managed          bool
	railOn           bool
	rfDisabled       bool
	devicePresent    bool
	netdevPresent    bool
}

func newRadioMachine(r Radio) *radioMachine {
	return &radioMachine{id: r, timing: timingFor(r), state: StateUnspecified}
}

func (rm *radioMachine) setState(s State, reason Reason, now time.Time) {
	rm.state, rm.reason, rm.since = s, reason, now
	rm.nextAction = time.Time{}
}

func (rm *radioMachine) enterEnumerating(now time.Time) {
	rm.setState(StateEnumerating, ReasonNone, now)
	rm.deadline = now.Add(rm.timing.deviceTimeout)
	rm.netDeadline = time.Time{}
	rm.nextAction = rm.deadline
}

// resetLadder starts the next incident from step 1.
func (rm *radioMachine) resetLadder() {
	rm.step = StepUnspecified
	rm.stepAttempt = 0
	rm.powerCycles = 0
	rm.lastStepAt = time.Time{}
}

// enterFault starts or continues the recovery ladder. skipToPowerCycle is
// set for hardware fault lines, which skip the bus-level steps.
func (rm *radioMachine) enterFault(trigger Reason, now time.Time, skipToPowerCycle bool) {
	if rm.step == StepUnspecified {
		rm.step = rm.timing.firstStep
		rm.stepAttempt = 0
	}

	if skipToPowerCycle && rm.step != StepPowerCycle {
		rm.step = StepPowerCycle
		rm.stepAttempt = 0
	}

	rm.trigger = trigger
	rm.setState(StateFault, trigger, now)
	rm.unhealthySince = time.Time{}
}

func (rm *radioMachine) status() RadioStatus {
	rs := RadioStatus{
		StateSince:       rm.since,
		NextActionAt:     rm.nextAction,
		Netdev:           rm.netdev,
		RecoveriesTotal:  rm.recoveriesTotal,
		PowerCyclesTotal: rm.powerCyclesTotal,
		FailedTotal:      rm.failedTotal,
		Radio:            rm.id,
		State:            rm.state,
		Reason:           rm.reason,
		RFDisabled:       rm.rfDisabled,
		DevicePresent:    rm.devicePresent,
		NetdevPresent:    rm.netdevPresent,
	}

	if rm.state == StateFault {
		rs.CurrentStep = rm.step
		rs.StepAttempt = rm.stepAttempt
	}

	return rs
}

// takeCaller returns and clears the operator attributed to the next event.
func (rm *radioMachine) takeCaller() string {
	c := rm.pendingCaller
	rm.pendingCaller = ""

	return c
}

// radioFaultLines returns the hardware fault inputs that trigger a power
// cycle for r.
func radioFaultLines(r Radio) []FaultLine {
	if r == RadioHaLow {
		return []FaultLine{FaultLineHaLowFault, FaultLineHaLowUSBFault}
	}

	return []FaultLine{FaultLineWiFiFault}
}

// stepRadio advances one radio's state machine.
func (m *Manager) stepRadio(ctx context.Context, rm *radioMachine, now time.Time, req requests) {
	if m.bus != nil && m.bus.Configured(rm.id) {
		rm.devicePresent = m.bus.DevicePresent(rm.id)
		rm.netdevPresent = m.bus.NetdevPresent(rm.id)
	}

	if !rm.managed {
		return
	}

	i := rm.id.index()
	if req.recover[i] {
		rm.pendingCaller = req.recoverCaller[i]
		m.manualRecovery(ctx, rm, now)
	}

	wantOn, offReason := m.wantOn(rm, req)
	if !wantOn && rm.state != StateOff && rm.state != StateFailed {
		if offReason == ReasonUser {
			rm.pendingCaller = req.powerCaller[i]
		}

		m.powerOff(ctx, rm, now, offReason)
		rm.resetLadder()
		rm.setState(StateOff, offReason, now)
	}

	if rm.state == StateEnumerating || rm.state == StateUp || rm.state == StatePowering {
		for _, l := range radioFaultLines(rm.id) {
			if m.inputFault(l) {
				rm.enterFault(ReasonFaultLine, now, true)

				break
			}
		}
	}

	m.advanceRadio(ctx, rm, now, wantOn)
	m.wakeAt(now, rm.nextAction)
}

// wantOn reports whether the radio should be powered and, if not, why.
func (m *Manager) wantOn(rm *radioMachine, req requests) (bool, Reason) {
	switch {
	case !req.power[rm.id.index()]:
		return false, ReasonUser
	case m.shutdownDone:
		return false, ReasonLowBattery
	case rm.id == RadioWiFi && m.wifiLowBattery():
		return false, ReasonLowBattery
	}

	return true, ReasonNone
}

// advanceRadio runs the per-state transition logic. A radio that enters
// StateFault during this pass gets its first ladder action in the same pass.
func (m *Manager) advanceRadio(ctx context.Context, rm *radioMachine, now time.Time, wantOn bool) {
	prev := rm.state
	m.transition(ctx, rm, now, wantOn)

	if prev != StateFault && rm.state == StateFault {
		m.runLadder(ctx, rm, now)
	}
}

func (m *Manager) transition(ctx context.Context, rm *radioMachine, now time.Time, wantOn bool) {
	switch rm.state {
	case StateOff:
		if wantOn && !m.powerFault {
			if ready := rm.offSince.Add(rm.timing.minOff); now.Before(ready) {
				rm.nextAction = ready

				return
			}

			m.powerOn(ctx, rm, now)
		}
	case StatePowering:
		if now.Before(rm.deadline) {
			return
		}

		m.release(ctx, rm, now)
		m.checkEnumeration(rm, now)
	case StateEnumerating:
		m.checkEnumeration(rm, now)
	case StateUp:
		m.checkHealth(rm, now)
	case StateFault:
		m.runLadder(ctx, rm, now)
	case StateCooldown:
		if now.Before(rm.deadline) || m.powerFault {
			return
		}

		m.powerOn(ctx, rm, now)
	case StateUnspecified, StateUnmanaged, StateFailed:
	}
}

// powerOn performs the pre-enable sequence and raises the enable.
func (m *Manager) powerOn(ctx context.Context, rm *radioMachine, now time.Time) {
	var err error

	switch rm.id {
	case RadioHaLow:
		err = errors.Join(
			m.lines.Set(LineHaLowResetN, true),
			m.lines.Set(LineHaLowWakeN, false),
		)
	case RadioWiFi:
		err = m.lines.Set(LineWiFiWDis1N, rm.rfDisabled)
		if rerr := m.bus.Remove(ctx, rm.id); rerr != nil {
			err = errors.Join(err, rerr)
		}
	case RadioUnspecified:
	}

	if err == nil {
		err = m.lines.Set(rm.timing.enableLine, true)
	}

	if err != nil {
		m.log.Error().Err(err).Str("radio", rm.id.String()).Msg("hardware: radio power-on sequence failed")
		// Count it against the power-cycle budget so retries stay bounded.
		rm.railOn = true
		rm.enterFault(ReasonHealthCheck, now, true)

		return
	}

	rm.railOn = true
	rm.setState(StatePowering, rm.reasonOr(ReasonNone), now)
	rm.deadline = now.Add(rm.timing.settle)
	rm.nextAction = rm.deadline
}

// reasonOr keeps a manual-recovery reason on the next state.
func (rm *radioMachine) reasonOr(def Reason) Reason {
	if rm.reason == ReasonManualRecovery {
		return ReasonManualRecovery
	}

	return def
}

// release lets the radio enumerate once its rail has settled.
func (m *Manager) release(ctx context.Context, rm *radioMachine, now time.Time) {
	var err error

	switch rm.id {
	case RadioHaLow:
		err = m.lines.Set(LineHaLowResetN, false)
	case RadioWiFi:
		err = m.bus.Rescan(ctx, rm.id)
	case RadioUnspecified:
	}

	if err != nil {
		m.log.Warn().Err(err).Str("radio", rm.id.String()).Msg("hardware: radio release failed; waiting for enumeration anyway")
	}

	rm.enterEnumerating(now)
}

// powerOff detaches the device, holds it in reset and drops the enable.
// Every step is attempted even if an earlier one fails.
func (m *Manager) powerOff(ctx context.Context, rm *radioMachine, now time.Time, reason Reason) {
	if !rm.railOn {
		return
	}

	start := now
	err := m.bus.Remove(ctx, rm.id)

	if rm.id == RadioHaLow {
		err = errors.Join(err, m.lines.Set(LineHaLowResetN, true))
	}

	err = errors.Join(err, m.lines.Set(rm.timing.enableLine, false))
	rm.railOn = false
	rm.offSince = now

	if reason == ReasonUser || reason == ReasonLowBattery {
		m.recordEvent(Event{
			At: start, Radio: rm.id, Step: StepPowerOff, Attempt: 1, Trigger: reason,
			Result: resultOf(err), Detail: errText(err), Caller: rm.takeCaller(),
			DurationMs: durationMs(m.clock.Now().Sub(start)),
		})

		return
	}

	if err != nil {
		m.log.Error().Err(err).Str("radio", rm.id.String()).Msg("hardware: radio power-off sequence failed")
	}
}

// checkEnumeration waits for the bus device, then the netdev.
func (m *Manager) checkEnumeration(rm *radioMachine, now time.Time) {
	if !rm.devicePresent {
		if !now.Before(rm.deadline) {
			rm.enterFault(ReasonEnumerationTimeout, now, false)
		}

		return
	}

	if !rm.netdevPresent {
		if rm.netDeadline.IsZero() {
			rm.netDeadline = now.Add(rm.timing.netdevTimeout)
			rm.nextAction = rm.netDeadline
		}

		if !now.Before(rm.netDeadline) {
			rm.enterFault(ReasonEnumerationTimeout, now, false)
		}

		return
	}

	rm.setState(StateUp, ReasonNone, now)
	rm.upSince = now
	rm.unhealthySince = time.Time{}
}

// checkHealth starts the ladder after the grace period and resets the
// ladder after a stable period.
func (m *Manager) checkHealth(rm *radioMachine, now time.Time) {
	if rm.devicePresent && rm.netdevPresent {
		rm.unhealthySince = time.Time{}
		rm.nextAction = time.Time{}

		if rm.step != StepUnspecified && now.Sub(rm.upSince) >= stableUpReset {
			rm.resetLadder()
		}

		return
	}

	if rm.unhealthySince.IsZero() {
		rm.unhealthySince = now
	}

	rm.nextAction = rm.unhealthySince.Add(healthGrace)

	if now.Sub(rm.unhealthySince) >= healthGrace {
		rm.enterFault(ReasonHealthCheck, now, false)
	}
}

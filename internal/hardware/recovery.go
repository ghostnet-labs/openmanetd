package hardware

import (
	"context"
	"errors"
	"time"
)

// Recovery ladder limits (design sections 4 and 5).
const (
	deviceResetTries   = 2
	deviceResetSpacing = 15 * time.Second
	hubResetPulse      = 10 * time.Millisecond
	hubResetMinGap     = 10 * time.Minute
	powerCycleTries    = 3
	powerGoodHold      = 5 * time.Second
	bothFailedHold     = 30 * time.Minute
	petInterval        = 10 * time.Second
	shutdownGrace      = 10 * time.Second
)

// cooldownBackoff is the power-cycle cooldown schedule; the last entry is
// the cap.
func cooldownBackoff(attempt uint32) time.Duration {
	steps := [...]time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute}
	if int(attempt) >= len(steps) {
		return steps[len(steps)-1]
	}

	return steps[attempt]
}

// Reasons a hub reset is skipped, reported in the event detail.
var (
	errHubRateLimited = errors.New("hub reset rate limited (one per 10 min)")
	errHubPTTActive   = errors.New("PTT transmit active")
)

// runLadder takes the next recovery action for a radio in StateFault. A
// lower step always runs before a higher one, except that hardware fault
// lines enter at the power cycle.
func (m *Manager) runLadder(ctx context.Context, rm *radioMachine, now time.Time) {
	if m.powerFault {
		// Node power fault: hold until POWER_GOOD is back (updatePowerFault
		// moves the radio on).
		return
	}

	// Each pass either acts, waits, or advances to the next step; three
	// passes are enough to cross every rung.
	for range 4 {
		switch rm.step {
		case StepResetUSBDevice, StepResetPCIeFunction:
			if rm.stepAttempt >= deviceResetTries {
				rm.step, rm.stepAttempt = m.afterDeviceReset(rm), 0

				continue
			}

			if !rm.lastStepAt.IsZero() && now.Sub(rm.lastStepAt) < deviceResetSpacing {
				rm.nextAction = rm.lastStepAt.Add(deviceResetSpacing)

				return
			}

			m.deviceReset(ctx, rm, now)

			return
		case StepResetHub:
			if rm.stepAttempt >= 1 {
				rm.step, rm.stepAttempt = StepPowerCycle, 0

				continue
			}

			if m.hubReset(ctx, rm, now) {
				return
			}

			continue
		case StepPowerCycle:
			m.powerCycle(ctx, rm, now)

			return
		case StepUnspecified, StepHostReset, StepGNSSReset, StepPowerOff, StepShutdown:
			rm.step, rm.stepAttempt = rm.timing.firstStep, 0
		}
	}
}

func (m *Manager) afterDeviceReset(rm *radioMachine) Step {
	if rm.timing.hasHubStep {
		return StepResetHub
	}

	return StepPowerCycle
}

// deviceReset runs ladder step 1 (1w for Wi-Fi).
func (m *Manager) deviceReset(ctx context.Context, rm *radioMachine, now time.Time) {
	rm.stepAttempt++
	rm.recoveriesTotal++
	rm.lastStepAt = now

	err := m.bus.ResetDevice(ctx, rm.id)
	m.recordEvent(Event{
		At: now, Radio: rm.id, Step: rm.step, Attempt: rm.stepAttempt, Trigger: rm.trigger,
		Result: resultOf(err), Detail: errText(err), Caller: rm.takeCaller(),
		DurationMs: durationMs(m.clock.Now().Sub(now)),
	})

	if err != nil {
		rm.nextAction = now.Add(deviceResetSpacing)

		return
	}

	rm.enterEnumerating(now)
	rm.reason = rm.trigger
}

// hubReset runs ladder step 2 for HaLow. It returns false when the step was
// skipped so the ladder moves straight to the power cycle.
func (m *Manager) hubReset(ctx context.Context, rm *radioMachine, now time.Time) bool {
	rm.stepAttempt++

	var skip error

	switch {
	case !m.lastHubReset.IsZero() && now.Sub(m.lastHubReset) < hubResetMinGap:
		skip = errHubRateLimited
	case m.comms != nil && m.comms.TransmitActive():
		skip = errHubPTTActive
	}

	if skip != nil {
		m.recordEvent(Event{
			At: now, Radio: rm.id, Step: StepResetHub, Attempt: rm.stepAttempt, Trigger: rm.trigger,
			Result: ResultSkipped, Detail: skip.Error(), Caller: rm.takeCaller(),
		})

		return false
	}

	if m.comms != nil {
		m.comms.BeforeHubReset(ctx)
	}

	rm.recoveriesTotal++
	m.lastHubReset = now

	err := m.lines.Set(LineUSBHubResetN, true)
	if err == nil {
		err = m.clock.Sleep(ctx, hubResetPulse)
	}

	// Always release the hub, even after a failed assert or canceled sleep.
	if rerr := m.lines.Set(LineUSBHubResetN, false); err == nil {
		err = rerr
	}

	m.recordEvent(Event{
		At: now, Radio: rm.id, Step: StepResetHub, Attempt: rm.stepAttempt, Trigger: rm.trigger,
		Result: resultOf(err), Detail: errText(err), Caller: rm.takeCaller(),
		DurationMs: durationMs(m.clock.Now().Sub(now)),
	})

	if err != nil {
		return false
	}

	rm.enterEnumerating(now)
	rm.reason = rm.trigger

	return true
}

// powerCycle runs ladder step 3, or fails the radio once the budget is
// spent.
func (m *Manager) powerCycle(ctx context.Context, rm *radioMachine, now time.Time) {
	if rm.powerCycles >= powerCycleTries {
		m.powerOff(ctx, rm, now, ReasonLadderExhausted)
		rm.failedTotal++
		rm.setState(StateFailed, ReasonLadderExhausted, now)
		m.recordEvent(Event{
			At: now, Radio: rm.id, Step: StepPowerCycle, Attempt: rm.powerCycles, Trigger: rm.trigger,
			Result: ResultExhausted, Caller: rm.takeCaller(),
		})

		return
	}

	backoff := max(cooldownBackoff(rm.powerCycles), rm.timing.minOff)
	rm.powerCycles++
	rm.stepAttempt = rm.powerCycles
	rm.powerCyclesTotal++
	rm.recoveriesTotal++

	m.powerOff(ctx, rm, now, ReasonNone)
	m.recordEvent(Event{
		At: now, Radio: rm.id, Step: StepPowerCycle, Attempt: rm.powerCycles, Trigger: rm.trigger,
		Result: ResultSucceeded, Caller: rm.takeCaller(), Detail: "cooldown " + backoff.String(),
		DurationMs: durationMs(m.clock.Now().Sub(now)),
	})

	trigger := rm.trigger
	rm.setState(StateCooldown, trigger, now)
	rm.deadline = now.Add(backoff)
	rm.nextAction = rm.deadline
}

// manualRecovery handles ExecuteRadioRecovery: a failed or cooling-down
// radio is powered on with a fresh ladder; a powered radio restarts the
// ladder at step 1 immediately.
func (m *Manager) manualRecovery(ctx context.Context, rm *radioMachine, now time.Time) {
	switch rm.state {
	case StateFailed, StateCooldown:
		rm.resetLadder()
		rm.reason = ReasonManualRecovery

		m.mu.Lock()
		m.req.power[rm.id.index()] = true
		m.mu.Unlock()

		m.recordEvent(Event{
			At: now, Radio: rm.id, Step: StepPowerCycle, Attempt: 1, Trigger: ReasonManualRecovery,
			Result: ResultSucceeded, Detail: "cleared " + rm.state.String(), Caller: rm.takeCaller(),
		})

		if m.powerFault {
			rm.setState(StateOff, ReasonPower, now)

			return
		}

		m.powerOn(ctx, rm, now)
	case StatePowering, StateEnumerating, StateUp, StateFault:
		rm.resetLadder()
		rm.enterFault(ReasonManualRecovery, now, false)
	case StateUnspecified, StateUnmanaged, StateOff:
	}
}

// updatePowerFault tracks EFUSE_FAULT and POWER_GOOD. A power fault marks
// every powered radio faulted with reason power and holds the ladder; the
// radios move on only after POWER_GOOD has been back for 5 s.
func (m *Manager) updatePowerFault(now time.Time) {
	fault := m.inputFault(FaultLineEFuseFault) || m.inputFault(FaultLinePowerGood)

	if fault {
		m.powerOKSince = time.Time{}

		if m.powerFault {
			return
		}

		m.powerFault = true
		m.log.Error().Msg("hardware: node power fault (EFUSE_FAULT or POWER_GOOD); radio recovery held")

		for _, rm := range m.radios {
			if rm.managed && (rm.state.railOn() || rm.state == StateCooldown) {
				rm.enterFault(ReasonPower, now, false)
			}
		}

		return
	}

	if !m.powerFault {
		return
	}

	if m.powerOKSince.IsZero() {
		m.powerOKSince = now
	}

	if ready := m.powerOKSince.Add(powerGoodHold); now.Before(ready) {
		m.wakeAt(now, ready)

		return
	}

	m.powerFault = false
	m.log.Warn().Msg("hardware: node power restored; resuming radios")

	for _, rm := range m.radios {
		if !rm.managed || rm.state != StateFault || rm.reason != ReasonPower {
			continue
		}

		if rm.railOn {
			rm.enterEnumerating(now)

			continue
		}

		rm.offSince = now.Add(-rm.timing.minOff)
		rm.setState(StateOff, ReasonPower, now)
	}
}

// wifiLowBattery reports whether the battery policy holds Wi-Fi off.
func (m *Manager) wifiLowBattery() bool {
	l := m.battery.policyLevel()

	return l == BatteryLevelCritical || l == BatteryLevelShutdown
}

// applyBatteryPolicy logs level changes and starts the graceful shutdown at
// the shutdown level or on the INA228 ALERT backstop.
func (m *Manager) applyBatteryPolicy(now time.Time) {
	level := m.battery.policyLevel()
	if level != m.lastLevel {
		ev := m.log.Info()
		if level >= BatteryLevelWarning {
			ev = m.log.Warn()
		}

		ev.Str("from", m.lastLevel.String()).Str("to", level.String()).Msg("hardware: battery level changed")
		m.lastLevel = level
	}

	alert := m.inputFault(FaultLineINA228Alert)
	if level != BatteryLevelShutdown && !alert {
		return
	}

	if m.shutdownPending || m.shutdownSkipped {
		return
	}

	trigger := ReasonLowBattery
	detail := "battery at shutdown level"

	if alert {
		detail = "INA228 ALERT backstop"
	}

	if !m.opts.BatteryShutdownEnable || m.controlMode != ControlModeActive || m.shutdowner == nil {
		m.shutdownSkipped = true
		m.recordEvent(Event{
			At: now, Step: StepShutdown, Attempt: 1, Trigger: trigger, Result: ResultSkipped,
			Detail: detail + "; graceful shutdown disabled (hardware.batteryShutdownEnable or telemetry-only)",
		})

		return
	}

	m.shutdownPending = true
	m.shutdownAt = now.Add(shutdownGrace)
	m.log.Error().Str("cause", detail).Dur("grace", shutdownGrace).Msg("hardware: shutdown_pending; powering off after grace period")
}

// runShutdown brings both radios off and powers the node off once the
// grace period for EUD clients has passed.
func (m *Manager) runShutdown(ctx context.Context, now time.Time) {
	if !m.shutdownPending || m.shutdownDone {
		return
	}

	if now.Before(m.shutdownAt) {
		m.wakeAt(now, m.shutdownAt)

		return
	}

	m.shutdownDone = true
	// The node is meant to stay off: disarm before anything else, or the
	// supervisor would power-cycle it once the heartbeat stops.
	m.disarmSupervisor("graceful poweroff")

	for _, rm := range m.radios {
		if rm.managed && rm.state != StateOff {
			m.powerOff(ctx, rm, now, ReasonLowBattery)
			rm.setState(StateOff, ReasonLowBattery, now)
		}
	}

	err := m.shutdowner.Poweroff(ctx)
	m.recordEvent(Event{
		At: now, Step: StepShutdown, Attempt: 1, Trigger: ReasonLowBattery,
		Result: resultOf(err), Detail: errText(err),
		DurationMs: durationMs(m.clock.Now().Sub(now)),
	})
}

// checkHostReset requests ladder step 4 by stopping the watchdog heartbeat
// when both mesh radios have been failed for 30 min, the battery is above
// the warning level, and fewer than three requests happened in 24 h. The
// request is persisted first so the boot-loop guard can count it.
func (m *Manager) checkHostReset(now time.Time) {
	bothFailed := true

	for _, rm := range m.radios {
		if !rm.managed || rm.state != StateFailed {
			bothFailed = false
		}
	}

	if !bothFailed {
		m.bothFailedSince = time.Time{}

		return
	}

	if m.bothFailedSince.IsZero() {
		m.bothFailedSince = now
	}

	if m.watchdog == nil || m.hostResetRequested || m.hostResetBlocked ||
		m.controlMode != ControlModeActive || !m.storeHealthy {
		return
	}

	if ready := m.bothFailedSince.Add(bothFailedHold); now.Before(ready) {
		m.wakeAt(now, ready)

		return
	}

	if !m.battery.fresh(now) || m.battery.policyLevel() != BatteryLevelNormal {
		return
	}

	if countSince(m.record.HostResetRequests, now.Add(-hostResetWindow)) >= hostResetLimit {
		m.hostResetBlocked = true
		m.recordEvent(Event{
			At: now, Step: StepHostReset, Attempt: 1, Trigger: ReasonLadderExhausted,
			Result: ResultSkipped, Detail: "host reset limit reached for 24 h",
		})

		return
	}

	rec := m.record
	rec.RecoveryResets = prune(append(rec.RecoveryResets, now), now)
	rec.HostResetRequests = prune(append(rec.HostResetRequests, now), now)

	if err := m.store.Save(rec); err != nil {
		m.hostResetBlocked = true
		m.recordEvent(Event{
			At: now, Step: StepHostReset, Attempt: 1, Trigger: ReasonLadderExhausted,
			Result: ResultFailed, Detail: "persist recovery record: " + err.Error(),
		})

		return
	}

	m.record = rec
	m.hostResetRequested = true
	m.recordEvent(Event{
		At: now, Step: StepHostReset, Attempt: uint32(countSince(rec.HostResetRequests, now.Add(-hostResetWindow))),
		Trigger: ReasonLadderExhausted, Result: ResultSucceeded,
		Detail: "both radios failed for 30 min; watchdog heartbeat stopped",
	})
}

// petWatchdog writes the heartbeat every 10 s while the step loop runs and
// no host reset was requested.
func (m *Manager) petWatchdog(now time.Time) {
	if m.watchdog == nil || m.hostResetRequested {
		return
	}

	if !m.lastPet.IsZero() && now.Sub(m.lastPet) < petInterval {
		m.wakeAt(now, m.lastPet.Add(petInterval))

		return
	}

	if err := m.watchdog.Pet(); err != nil {
		m.log.Warn().Err(err).Msg("hardware: watchdog pet failed")

		return
	}

	m.lastPet = now
	m.petsTotal++
	m.armSupervisor()
}

// armSupervisor drives SUPERVISOR_ARM high. petWatchdog calls it only after
// a heartbeat was written, so the supervisor never arms without WDI
// service. It is a no-op once armed, after a host reset request, and once
// a graceful shutdown is pending. A failed write is retried on the next
// heartbeat.
func (m *Manager) armSupervisor() {
	if !m.armRequested || m.supervisorArmed || m.hostResetRequested || m.shutdownPending {
		return
	}

	if err := m.lines.Set(LineSupervisorARM, true); err != nil {
		m.log.Warn().Err(err).Msg("hardware: arming supervisor watchdog failed; retrying on next heartbeat")

		return
	}

	m.supervisorArmed = true
	m.armTransitions++
	m.log.Info().Msg("hardware: supervisor watchdog armed (SUPERVISOR_ARM high)")
}

// disarmSupervisor drives SUPERVISOR_ARM low. Callers run it before the
// heartbeat stops (daemon stop) or before a deliberate poweroff.
func (m *Manager) disarmSupervisor(cause string) {
	if !m.supervisorArmed {
		return
	}

	if err := m.lines.Set(LineSupervisorARM, false); err != nil {
		m.log.Error().Err(err).Str("cause", cause).Msg("hardware: disarming supervisor watchdog failed")

		return
	}

	m.supervisorArmed = false
	m.armTransitions++
	m.log.Info().Str("cause", cause).Msg("hardware: supervisor watchdog disarmed (SUPERVISOR_ARM low)")
}

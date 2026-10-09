package hardware_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openmanet/openmanetd/internal/hardware"
	"github.com/openmanet/openmanetd/internal/system"
	"github.com/openmanet/openmanetd/internal/util/board"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testRig bundles a manager with every fake it was built from.
type testRig struct {
	m        *hardware.Manager
	clock    *fakeClock
	opener   *fakeLineOpener
	bus      *fakeBus
	mon      *fakeMonitor
	store    *fakeStore
	wd       *fakeWatchdog
	wdOpener *fakeWatchdogOpener
	shut     *fakeShutdowner
	comms    *fakeCommsGuard
}

// healthyReading is a 3S pack at 12.0 V drawing 0.5 A.
func healthyReading() system.MonitorReading {
	return system.MonitorReading{
		Present:         true,
		BusMillivolts:   12000,
		CurrentMilliamp: 500,
		PowerMicrowatts: 6_000_000,
		HasCurrent:      true,
		HasPower:        true,
		EnergyMicrojoul: 36_000_000,
		HasEnergy:       true,
		TempMilliC:      40500,
		HasTemp:         true,
	}
}

// newRig builds an active-mode V1 manager whose radios are already on.
// mutate may adjust options and fakes before construction.
func newRig(t *testing.T, mutate func(*hardware.Options, *testRig)) *testRig {
	t.Helper()

	rig := &testRig{
		clock:    newFakeClock(),
		opener:   newFakeLineOpener(),
		bus:      newFakeBus(),
		mon:      &fakeMonitor{reading: healthyReading()},
		store:    &fakeStore{},
		wd:       &fakeWatchdog{},
		shut:     &fakeShutdowner{},
		comms:    &fakeCommsGuard{},
		wdOpener: nil,
	}
	rig.wdOpener = &fakeWatchdogOpener{wd: rig.wd}
	rig.opener.initial[hardware.LineHaLowPwrEn] = true
	rig.opener.initial[hardware.LineWiFiPwrEn] = true

	opts := hardware.Options{
		Log:             zerolog.Nop(),
		Clock:           rig.clock,
		Lines:           rig.opener,
		Bus:             rig.bus,
		Battery:         rig.mon,
		Store:           rig.store,
		Watchdogs:       rig.wdOpener,
		Shutdowner:      rig.shut,
		Comms:           rig.comms,
		BoardID:         board.GhostnetV1,
		SeriesCells:     3,
		ActuationEnable: true,
	}

	if mutate != nil {
		mutate(&opts, rig)
	}

	rig.m = hardware.New(opts)
	t.Cleanup(rig.m.Close)

	return rig
}

func (r *testRig) step() {
	r.m.Step(context.Background())
}

// run advances virtual time by d in tick increments, stepping after each.
func (r *testRig) run(d, tick time.Duration) {
	for elapsed := time.Duration(0); elapsed < d; elapsed += tick {
		r.clock.Advance(tick)
		r.step()
	}
}

func (r *testRig) radio(id hardware.Radio) hardware.RadioStatus {
	return r.m.Status().Radios[id-1]
}

func (r *testRig) lines() *fakeLines {
	return r.opener.getLines()
}

// chronological returns a radio's events oldest first.
func (r *testRig) chronological(id hardware.Radio) []hardware.Event {
	ev := r.m.Events(0, id)
	out := make([]hardware.Event, len(ev))

	for i := range ev {
		out[len(ev)-1-i] = ev[i]
	}

	return out
}

func TestNew_ControlMode(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*hardware.Options, *testRig)
		wantMode   hardware.ControlMode
		wantReason hardware.ControlModeReason
	}{
		{
			name:       "active on detected V1 with actuation",
			wantMode:   hardware.ControlModeActive,
			wantReason: hardware.ControlReasonNone,
		},
		{
			name:       "other board",
			mutate:     func(o *hardware.Options, _ *testRig) { o.BoardID = board.BCM2712_MM8108_USB },
			wantMode:   hardware.ControlModeTelemetryOnly,
			wantReason: hardware.ControlReasonBoardNotDetected,
		},
		{
			name:       "unknown board",
			mutate:     func(o *hardware.Options, _ *testRig) { o.BoardID = "" },
			wantMode:   hardware.ControlModeTelemetryOnly,
			wantReason: hardware.ControlReasonBoardNotDetected,
		},
		{
			name:       "actuation disabled (default config)",
			mutate:     func(o *hardware.Options, _ *testRig) { o.ActuationEnable = false },
			wantMode:   hardware.ControlModeTelemetryOnly,
			wantReason: hardware.ControlReasonActuationDisabled,
		},
		{
			name: "boot loop guard",
			mutate: func(_ *hardware.Options, r *testRig) {
				now := r.clock.Now()
				r.store.rec.RecoveryResets = []time.Time{
					now.Add(-50 * time.Minute), now.Add(-20 * time.Minute), now.Add(-time.Minute),
				}
			},
			wantMode:   hardware.ControlModeTelemetryOnly,
			wantReason: hardware.ControlReasonBootLoopGuard,
		},
		{
			name: "three resets but one older than an hour",
			mutate: func(_ *hardware.Options, r *testRig) {
				now := r.clock.Now()
				r.store.rec.RecoveryResets = []time.Time{
					now.Add(-61 * time.Minute), now.Add(-20 * time.Minute), now.Add(-time.Minute),
				}
			},
			wantMode:   hardware.ControlModeActive,
			wantReason: hardware.ControlReasonNone,
		},
		{
			name:       "missing line name",
			mutate:     func(_ *hardware.Options, r *testRig) { delete(r.opener.resolvable, hardware.LineWiFiWDis1N) },
			wantMode:   hardware.ControlModeTelemetryOnly,
			wantReason: hardware.ControlReasonLinesUnresolved,
		},
		{
			name:       "line request fails",
			mutate:     func(_ *hardware.Options, r *testRig) { r.opener.openErr = errors.New("busy") },
			wantMode:   hardware.ControlModeTelemetryOnly,
			wantReason: hardware.ControlReasonLinesUnresolved,
		},
		{
			name:       "no line opener",
			mutate:     func(o *hardware.Options, _ *testRig) { o.Lines = nil },
			wantMode:   hardware.ControlModeTelemetryOnly,
			wantReason: hardware.ControlReasonLinesUnresolved,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newRig(t, tc.mutate)
			st := rig.m.Status()
			assert.Equal(t, tc.wantMode, st.ControlMode)
			assert.Equal(t, tc.wantReason, st.ControlModeReason)

			if tc.wantMode != hardware.ControlModeActive {
				for _, rs := range st.Radios {
					assert.Equal(t, hardware.StateUnmanaged, rs.State)
					assert.Equal(t, hardware.ReasonControlDisabled, rs.Reason)
				}
			}
		})
	}
}

func TestTelemetryOnly_NeverDrivesOutputs(t *testing.T) {
	rig := newRig(t, func(o *hardware.Options, _ *testRig) {
		o.ActuationEnable = false
		o.FaultInputs = []string{hardware.LinePowerGood, hardware.LineVLMUSBFaultN}
	})

	for _, s := range rig.opener.getOpened() {
		assert.False(t, s.Output, "output %s requested in telemetry-only mode", s.Name)
	}

	rig.bus.setPresent(hardware.RadioHaLow, false, false)
	rig.run(10*time.Minute, time.Second)

	assert.Empty(t, rig.lines().getSets(), "telemetry-only mode must not drive any GPIO")
	assert.Zero(t, rig.bus.getResetCalls(hardware.RadioHaLow))
	assert.Zero(t, rig.bus.getRemoveCalls(hardware.RadioWiFi))

	st := rig.m.Status()
	assert.False(t, st.Radios[0].DevicePresent, "presence is still observed")
	assert.True(t, st.Radios[1].DevicePresent)

	_, err := rig.m.SetRadioPower(hardware.RadioWiFi, false, "root")
	require.ErrorIs(t, err, hardware.ErrTelemetryOnly)
	_, err = rig.m.SetRFDisable(hardware.RadioWiFi, true, "root")
	require.ErrorIs(t, err, hardware.ErrTelemetryOnly)
	_, err = rig.m.Recover(hardware.RadioHaLow, "root")
	require.ErrorIs(t, err, hardware.ErrTelemetryOnly)
	_, err = rig.m.ResetGNSS("root")
	require.ErrorIs(t, err, hardware.ErrTelemetryOnly)
}

func TestNonV1_RequestsNoLinesAtAll(t *testing.T) {
	rig := newRig(t, func(o *hardware.Options, _ *testRig) {
		o.BoardID = board.BCM2711_RAVEN_USB
		o.FaultInputs = hardware.FaultInputNames()
		o.WatchdogEnable = true
	})

	assert.Empty(t, rig.opener.getOpened())
	assert.Zero(t, rig.wdOpener.getOpens())

	for _, fl := range rig.m.Status().FaultLines {
		assert.Equal(t, hardware.LineStateUnavailable, fl.State, fl.Name)
	}
}

func TestStartup_AdoptsPoweredRadiosWithoutGlitch(t *testing.T) {
	rig := newRig(t, nil)

	for _, rs := range rig.m.Status().Radios {
		assert.Equal(t, hardware.StateEnumerating, rs.State)
		assert.Equal(t, hardware.ReasonStartup, rs.Reason)
		assert.True(t, rs.PowerRequested)
	}

	rig.step()

	for _, rs := range rig.m.Status().Radios {
		assert.Equal(t, hardware.StateUp, rs.State)
	}

	assert.Empty(t, rig.lines().getSets(), "adopting a powered radio must not touch its lines")
}

func TestStartup_PowersOnOffRadio(t *testing.T) {
	rig := newRig(t, func(_ *hardware.Options, r *testRig) {
		r.opener.initial[hardware.LineHaLowPwrEn] = false
		r.opener.initial[hardware.LineHaLowResetN] = true
	})

	require.Equal(t, hardware.StateOff, rig.radio(hardware.RadioHaLow).State)

	rig.step()
	assert.Equal(t, hardware.StatePowering, rig.radio(hardware.RadioHaLow).State)
	assert.Equal(t, []setCall{
		{hardware.LineHaLowResetN, true},
		{hardware.LineHaLowWakeN, false},
		{hardware.LineHaLowPwrEn, true},
	}, rig.lines().getSets())

	rig.clock.Advance(20 * time.Millisecond)
	rig.step()
	assert.Equal(t, hardware.LineHaLowResetN, rig.lines().getSets()[3].name)
	assert.False(t, rig.lines().getSets()[3].asserted, "reset released after the rail settles")
	assert.Equal(t, hardware.StateUp, rig.radio(hardware.RadioHaLow).State)
}

func TestWiFiPowerOn_RemovesThenRescans(t *testing.T) {
	rig := newRig(t, func(_ *hardware.Options, r *testRig) {
		r.opener.initial[hardware.LineWiFiPwrEn] = false
	})

	rig.step()
	assert.Equal(t, hardware.StatePowering, rig.radio(hardware.RadioWiFi).State)
	assert.Equal(t, 1, rig.bus.getRemoveCalls(hardware.RadioWiFi))
	assert.Equal(t, []setCall{
		{hardware.LineWiFiWDis1N, false},
		{hardware.LineWiFiPwrEn, true},
	}, rig.lines().getSets())

	rig.clock.Advance(99 * time.Millisecond)
	rig.step()
	assert.Zero(t, rig.bus.getRescanCalls(hardware.RadioWiFi), "100 ms settle before rescan")

	rig.clock.Advance(time.Millisecond)
	rig.step()
	assert.Equal(t, 1, rig.bus.getRescanCalls(hardware.RadioWiFi))
}

func TestSetRadioPower_OffAndOn(t *testing.T) {
	rig := newRig(t, nil)
	rig.step()

	rs, err := rig.m.SetRadioPower(hardware.RadioWiFi, false, "admin")
	require.NoError(t, err)
	assert.False(t, rs.PowerRequested)

	rig.step()

	wifi := rig.radio(hardware.RadioWiFi)
	assert.Equal(t, hardware.StateOff, wifi.State)
	assert.Equal(t, hardware.ReasonUser, wifi.Reason)
	assert.False(t, rig.lines().value(hardware.LineWiFiPwrEn))
	assert.Equal(t, 1, rig.bus.getRemoveCalls(hardware.RadioWiFi))
	assert.Equal(t, hardware.StateUp, rig.radio(hardware.RadioHaLow).State, "HaLow unaffected")

	ev := rig.m.Events(1, hardware.RadioWiFi)
	require.Len(t, ev, 1)
	assert.Equal(t, hardware.StepPowerOff, ev[0].Step)
	assert.Equal(t, "admin", ev[0].Caller)
	assert.Equal(t, hardware.ResultSucceeded, ev[0].Result)

	_, err = rig.m.SetRadioPower(hardware.RadioWiFi, true, "admin")
	require.NoError(t, err)

	rig.clock.Advance(time.Second)
	rig.step()
	assert.Equal(t, hardware.StateOff, rig.radio(hardware.RadioWiFi).State, "2 s minimum off time")

	rig.clock.Advance(time.Second)
	rig.step()
	assert.Equal(t, hardware.StatePowering, rig.radio(hardware.RadioWiFi).State)
}

func TestSetRadioPower_Validation(t *testing.T) {
	rig := newRig(t, func(_ *hardware.Options, r *testRig) { r.bus.configured[hardware.RadioHaLow] = false })

	_, err := rig.m.SetRadioPower(hardware.RadioUnspecified, true, "")
	require.ErrorIs(t, err, hardware.ErrUnknownRadio)
	_, err = rig.m.SetRadioPower(hardware.RadioHaLow, true, "")
	require.ErrorIs(t, err, hardware.ErrRadioUnmanaged)
	_, err = rig.m.SetRFDisable(hardware.RadioHaLow, true, "")
	require.ErrorIs(t, err, hardware.ErrNoRFDisable)
	_, err = rig.m.SetRFDisable(hardware.Radio(9), true, "")
	require.ErrorIs(t, err, hardware.ErrUnknownRadio)
}

func TestLadder_HaLowClimbsEveryStepThenFails(t *testing.T) {
	rig := newRig(t, nil)
	rig.bus.setPresent(hardware.RadioHaLow, false, false)

	rig.step()
	rig.run(5*time.Minute, time.Second)

	halow := rig.radio(hardware.RadioHaLow)
	require.Equal(t, hardware.StateFailed, halow.State)
	assert.Equal(t, hardware.ReasonLadderExhausted, halow.Reason)
	assert.Equal(t, uint64(1), halow.FailedTotal)
	assert.Equal(t, uint64(3), halow.PowerCyclesTotal)
	assert.Equal(t, uint64(6), halow.RecoveriesTotal, "2 device resets + 1 hub reset + 3 power cycles")
	assert.False(t, rig.lines().value(hardware.LineHaLowPwrEn), "failed radio is left off")
	assert.True(t, rig.lines().value(hardware.LineHaLowResetN), "failed radio is held in reset")

	type want struct {
		step    hardware.Step
		attempt uint32
		result  hardware.Result
	}

	got := make([]want, 0, 8)
	for _, e := range rig.chronological(hardware.RadioHaLow) {
		got = append(got, want{e.Step, e.Attempt, e.Result})
	}

	assert.Equal(t, []want{
		{hardware.StepResetUSBDevice, 1, hardware.ResultSucceeded},
		{hardware.StepResetUSBDevice, 2, hardware.ResultSucceeded},
		{hardware.StepResetHub, 1, hardware.ResultSucceeded},
		{hardware.StepPowerCycle, 1, hardware.ResultSucceeded},
		{hardware.StepPowerCycle, 2, hardware.ResultSucceeded},
		{hardware.StepPowerCycle, 3, hardware.ResultSucceeded},
		{hardware.StepPowerCycle, 3, hardware.ResultExhausted},
	}, got)

	events := rig.chronological(hardware.RadioHaLow)
	assert.Equal(t, hardware.ReasonEnumerationTimeout, events[0].Trigger)
	assert.GreaterOrEqual(t, events[1].At.Sub(events[0].At), 15*time.Second, "device resets 15 s apart")
	assert.Equal(t, 1, rig.comms.getBeforeCalls(), "comms told before the hub reset")
	assert.Equal(t, 2, rig.bus.getResetCalls(hardware.RadioHaLow))

	// Cooldown backoff 10 s, 30 s, 2 min between power cycles.
	assert.GreaterOrEqual(t, events[4].At.Sub(events[3].At), 10*time.Second)
	assert.GreaterOrEqual(t, events[5].At.Sub(events[4].At), 30*time.Second)
	assert.GreaterOrEqual(t, events[6].At.Sub(events[5].At), 2*time.Minute)

	wifi := rig.radio(hardware.RadioWiFi)
	assert.Equal(t, hardware.StateUp, wifi.State, "one radio failing never touches the other")
	assert.Zero(t, wifi.RecoveriesTotal)
	assert.Empty(t, rig.m.Events(0, hardware.RadioWiFi))

	_, err := rig.m.SetRadioPower(hardware.RadioHaLow, true, "admin")
	require.ErrorIs(t, err, hardware.ErrRadioFailed)

	rig.run(time.Hour, 10*time.Second)
	assert.Equal(t, hardware.StateFailed, rig.radio(hardware.RadioHaLow).State, "failed is sticky")
	assert.Len(t, rig.chronological(hardware.RadioHaLow), 7, "no further actions")
}

func TestLadder_HubPulse(t *testing.T) {
	rig := newRig(t, nil)
	rig.bus.setPresent(hardware.RadioHaLow, false, false)
	rig.step()
	rig.run(40*time.Second, time.Second)

	var hub []setCall

	for _, s := range rig.lines().getSets() {
		if s.name == hardware.LineUSBHubResetN {
			hub = append(hub, s)
		}
	}

	assert.Equal(t, []setCall{{hardware.LineUSBHubResetN, true}, {hardware.LineUSBHubResetN, false}}, hub)
}

func TestLadder_WiFiHasNoHubStep(t *testing.T) {
	rig := newRig(t, nil)
	rig.bus.setPresent(hardware.RadioWiFi, false, false)
	rig.step()
	rig.run(5*time.Minute, time.Second)

	steps := make([]hardware.Step, 0, 6)
	for _, e := range rig.chronological(hardware.RadioWiFi) {
		steps = append(steps, e.Step)
	}

	assert.Equal(t, []hardware.Step{
		hardware.StepResetPCIeFunction, hardware.StepResetPCIeFunction,
		hardware.StepPowerCycle, hardware.StepPowerCycle, hardware.StepPowerCycle, hardware.StepPowerCycle,
	}, steps)
	assert.Equal(t, hardware.StateFailed, rig.radio(hardware.RadioWiFi).State)
	assert.Equal(t, hardware.StateUp, rig.radio(hardware.RadioHaLow).State)
}

func TestLadder_HubSkippedWhilePTTActive(t *testing.T) {
	rig := newRig(t, func(_ *hardware.Options, r *testRig) { r.comms.transmit = true })
	rig.bus.setPresent(hardware.RadioHaLow, false, false)
	rig.step()
	rig.run(50*time.Second, time.Second)

	var hub *hardware.Event

	for _, e := range rig.chronological(hardware.RadioHaLow) {
		if e.Step == hardware.StepResetHub {
			hub = &e

			break
		}
	}

	require.NotNil(t, hub)
	assert.Equal(t, hardware.ResultSkipped, hub.Result)
	assert.Contains(t, hub.Detail, "PTT")
	assert.Zero(t, rig.comms.getBeforeCalls())

	for _, s := range rig.lines().getSets() {
		assert.NotEqual(t, hardware.LineUSBHubResetN, s.name, "hub never pulsed during PTT")
	}
}

func TestLadder_RecoversAfterDeviceReset(t *testing.T) {
	rig := newRig(t, func(_ *hardware.Options, r *testRig) { r.bus.restoreOnReset = true })
	rig.step()
	rig.bus.setPresent(hardware.RadioHaLow, true, false)

	rig.run(59*time.Second, time.Second)
	assert.Equal(t, hardware.StateUp, rig.radio(hardware.RadioHaLow).State, "60 s health grace")

	rig.run(3*time.Second, time.Second)

	halow := rig.radio(hardware.RadioHaLow)
	assert.Equal(t, hardware.StateUp, halow.State)
	assert.Equal(t, uint64(1), halow.RecoveriesTotal)

	ev := rig.chronological(hardware.RadioHaLow)
	require.Len(t, ev, 1)
	assert.Equal(t, hardware.StepResetUSBDevice, ev[0].Step)
	assert.Equal(t, hardware.ReasonHealthCheck, ev[0].Trigger)
}

func TestLadder_StableUpResetsCounters(t *testing.T) {
	rig := newRig(t, func(_ *hardware.Options, r *testRig) { r.bus.restoreOnReset = true })
	rig.step()

	// Two incidents more than 10 min apart each start at step 1, so the
	// device reset never runs out.
	for range 3 {
		rig.bus.setPresent(hardware.RadioHaLow, true, false)
		rig.run(2*time.Minute, time.Second)
		rig.run(11*time.Minute, 10*time.Second)
	}

	for _, e := range rig.chronological(hardware.RadioHaLow) {
		assert.Equal(t, hardware.StepResetUSBDevice, e.Step)
		assert.Equal(t, uint32(1), e.Attempt)
	}
}

func TestFaultLine_SkipsToPowerCycle(t *testing.T) {
	rig := newRig(t, func(o *hardware.Options, _ *testRig) {
		o.FaultInputs = []string{hardware.LineHaLowUSBFaultN}
	})
	rig.step()
	require.Equal(t, hardware.StateUp, rig.radio(hardware.RadioHaLow).State)

	rig.lines().setValue(hardware.LineHaLowUSBFaultN, true)
	rig.step()

	ev := rig.chronological(hardware.RadioHaLow)
	require.Len(t, ev, 1)
	assert.Equal(t, hardware.StepPowerCycle, ev[0].Step)
	assert.Equal(t, hardware.ReasonFaultLine, ev[0].Trigger)
	assert.Equal(t, hardware.StateCooldown, rig.radio(hardware.RadioHaLow).State)

	fl := rig.m.Status().FaultLines[hardware.FaultLineHaLowUSBFault-1]
	assert.Equal(t, hardware.LineStateAsserted, fl.State)
	assert.Equal(t, uint64(1), fl.EdgesTotal)
	assert.Equal(t, hardware.LineHaLowUSBFaultN, fl.Name)

	unavailable := rig.m.Status().FaultLines[hardware.FaultLineHaLowFault-1]
	assert.Equal(t, hardware.LineStateUnavailable, unavailable.State, "unselected inputs are unavailable, never healthy")
}

func TestPowerFault_HoldsLadderUntilPowerGoodFor5s(t *testing.T) {
	rig := newRig(t, func(o *hardware.Options, r *testRig) {
		o.FaultInputs = []string{hardware.LinePowerGood, hardware.LineEFuseFault}
		r.opener.initial[hardware.LinePowerGood] = true
	})
	rig.step()

	rig.lines().setValue(hardware.LinePowerGood, false)
	rig.step()

	st := rig.m.Status()
	assert.True(t, st.PowerFault)

	for _, rs := range st.Radios {
		assert.Equal(t, hardware.StateFault, rs.State)
		assert.Equal(t, hardware.ReasonPower, rs.Reason)
	}

	rig.run(5*time.Minute, time.Second)
	assert.Empty(t, rig.m.Events(0, hardware.RadioUnspecified), "no power-cycling during a power fault")

	rig.lines().setValue(hardware.LinePowerGood, true)
	rig.run(4*time.Second, time.Second)
	assert.True(t, rig.m.Status().PowerFault, "POWER_GOOD must hold for 5 s")

	rig.run(2*time.Second, time.Second)

	st = rig.m.Status()
	assert.False(t, st.PowerFault)

	for _, rs := range st.Radios {
		assert.Equal(t, hardware.StateUp, rs.State)
	}

	assert.Empty(t, rig.m.Events(0, hardware.RadioUnspecified))
}

func TestManualRecovery(t *testing.T) {
	t.Run("clears failed and powers on", func(t *testing.T) {
		rig := newRig(t, nil)
		rig.bus.setPresent(hardware.RadioWiFi, false, false)
		rig.step()
		rig.run(5*time.Minute, time.Second)
		require.Equal(t, hardware.StateFailed, rig.radio(hardware.RadioWiFi).State)

		rig.bus.setPresent(hardware.RadioWiFi, true, true)
		_, err := rig.m.Recover(hardware.RadioWiFi, "admin")
		require.NoError(t, err)
		rig.step()

		wifi := rig.radio(hardware.RadioWiFi)
		assert.Equal(t, hardware.StatePowering, wifi.State)
		assert.Equal(t, hardware.ReasonManualRecovery, wifi.Reason)
		assert.True(t, wifi.PowerRequested)

		ev := rig.m.Events(1, hardware.RadioWiFi)
		require.Len(t, ev, 1)
		assert.Equal(t, "admin", ev[0].Caller)
		assert.Equal(t, hardware.ReasonManualRecovery, ev[0].Trigger)

		rig.run(time.Second, time.Second)
		assert.Equal(t, hardware.StateUp, rig.radio(hardware.RadioWiFi).State)
	})

	t.Run("up radio runs step 1 now", func(t *testing.T) {
		rig := newRig(t, nil)
		rig.step()

		_, err := rig.m.Recover(hardware.RadioHaLow, "admin")
		require.NoError(t, err)
		rig.step()

		ev := rig.m.Events(0, hardware.RadioHaLow)
		require.Len(t, ev, 1)
		assert.Equal(t, hardware.StepResetUSBDevice, ev[0].Step)
		assert.Equal(t, hardware.ReasonManualRecovery, ev[0].Trigger)
		assert.Equal(t, "admin", ev[0].Caller)
	})

	t.Run("off radio is refused", func(t *testing.T) {
		rig := newRig(t, nil)
		rig.step()

		_, err := rig.m.SetRadioPower(hardware.RadioHaLow, false, "admin")
		require.NoError(t, err)
		rig.step()

		_, err = rig.m.Recover(hardware.RadioHaLow, "admin")
		require.ErrorIs(t, err, hardware.ErrRadioOff)
	})
}

func TestRFDisable(t *testing.T) {
	rig := newRig(t, func(_ *hardware.Options, r *testRig) { r.opener.initial[hardware.LineWiFiWDis1N] = true })
	rig.step()
	assert.True(t, rig.radio(hardware.RadioWiFi).RFDisabled, "adopted from the line at startup")

	_, err := rig.m.SetRFDisable(hardware.RadioWiFi, false, "admin")
	require.NoError(t, err)
	rig.step()

	assert.False(t, rig.radio(hardware.RadioWiFi).RFDisabled)
	assert.Equal(t, []setCall{{hardware.LineWiFiWDis1N, false}}, rig.lines().getSets())
}

func TestGNSSReset_Pulse(t *testing.T) {
	rig := newRig(t, nil)
	rig.step()

	start := rig.clock.Now()
	at, err := rig.m.ResetGNSS("admin")
	require.NoError(t, err)
	assert.Equal(t, start, at)

	rig.step()

	assert.Equal(t, []setCall{{hardware.LineGNSSResetN, true}, {hardware.LineGNSSResetN, false}}, rig.lines().getSets())
	assert.Equal(t, 100*time.Millisecond, rig.clock.Now().Sub(start), "held low for 100 ms")

	ev := rig.m.Events(1, hardware.RadioUnspecified)
	require.Len(t, ev, 1)
	assert.Equal(t, hardware.StepGNSSReset, ev[0].Step)
	assert.Equal(t, uint32(100), ev[0].DurationMs)
	assert.Equal(t, "admin", ev[0].Caller)
}

func TestOpenDrainLinesNeverDrivenHigh(t *testing.T) {
	rig := newRig(t, func(_ *hardware.Options, r *testRig) { r.comms.transmit = false })
	rig.bus.setPresent(hardware.RadioHaLow, false, false)
	rig.bus.setPresent(hardware.RadioWiFi, false, false)
	rig.step()
	rig.run(5*time.Minute, time.Second)

	_, err := rig.m.ResetGNSS("admin")
	require.NoError(t, err)
	rig.step()

	specs := map[string]hardware.LineSpec{}
	for _, s := range rig.opener.getOpened() {
		specs[s.Name] = s
	}

	for _, name := range []string{hardware.LineGNSSResetN, hardware.LineHaLowResetN, hardware.LineHaLowWakeN, hardware.LineWiFiWDis1N} {
		s := specs[name]
		assert.True(t, s.OpenDrain, "%s must be open-drain", name)
		assert.True(t, s.ActiveLow, "%s must be active-low", name)
	}

	assert.NotEmpty(t, rig.lines().getSets())
}

func TestEvents_FilterAndLimit(t *testing.T) {
	rig := newRig(t, nil)
	rig.step()

	for range 40 {
		_, err := rig.m.SetRadioPower(hardware.RadioWiFi, false, "a")
		require.NoError(t, err)
		rig.step()

		_, err = rig.m.SetRadioPower(hardware.RadioWiFi, true, "a")
		require.NoError(t, err)
		rig.run(3*time.Second, time.Second)
	}

	_, err := rig.m.ResetGNSS("a")
	require.NoError(t, err)
	rig.step()

	all := rig.m.Events(0, hardware.RadioUnspecified)
	assert.Len(t, all, 41, "40 power-offs + 1 GNSS reset")
	assert.Equal(t, hardware.StepGNSSReset, all[0].Step, "newest first")

	assert.Len(t, rig.m.Events(5, hardware.RadioWiFi), 5)
	assert.Len(t, rig.m.Events(0, hardware.RadioWiFi), 40)
	assert.Empty(t, rig.m.Events(0, hardware.RadioHaLow))

	for range 40 {
		_, err := rig.m.ResetGNSS("a")
		require.NoError(t, err)
		rig.step()
	}

	assert.Len(t, rig.m.Events(0, hardware.RadioUnspecified), 64, "ring is bounded at 64")
}

func TestSubscribe(t *testing.T) {
	rig := newRig(t, nil)

	ch, cancel, err := rig.m.Subscribe()
	require.NoError(t, err)

	rig.step()

	select {
	case <-ch:
	default:
		t.Fatal("expected a change notification after the radios came up")
	}

	cancel()

	cancels := make([]func(), 0, 16)

	for range 16 {
		_, c, err := rig.m.Subscribe()
		require.NoError(t, err)

		cancels = append(cancels, c)
	}

	_, _, err = rig.m.Subscribe()
	require.ErrorIs(t, err, hardware.ErrTooManySubscribers)

	for _, c := range cancels {
		c()
	}

	_, c, err := rig.m.Subscribe()
	require.NoError(t, err)
	c()
}

func TestRun_StopsOnCancelAndReleasesHardware(t *testing.T) {
	rig := newRig(t, func(o *hardware.Options, _ *testRig) {
		o.Clock = nil // real clock
		o.WatchdogEnable = true
	})

	ctx, cancel := context.WithCancel(context.Background())
	go rig.m.Run(ctx)

	cancel()
	<-rig.m.Done()

	assert.True(t, rig.lines().isClosed())

	closed, clean := rig.wd.getClose()
	assert.True(t, closed)
	assert.True(t, clean, "a normal stop magic-closes so the node is not reset")
}

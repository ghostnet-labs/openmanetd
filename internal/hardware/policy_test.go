package hardware_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/openmanet/openmanetd/internal/hardware"
	"github.com/openmanet/openmanetd/internal/system"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readingAt returns a healthy reading with the given bus voltage and
// current.
func readingAt(busMV, currentMA int64) system.MonitorReading {
	r := healthyReading()
	r.BusMillivolts, r.CurrentMilliamp = busMV, currentMA

	return r
}

func TestBattery_FreshReading(t *testing.T) {
	rig := newRig(t, nil)
	rig.step()

	b := rig.m.Status().Battery
	assert.True(t, b.Present)
	assert.False(t, b.Stale)
	assert.Equal(t, hardware.AbsentReasonNone, b.AbsentReason)
	assert.Equal(t, rig.clock.Now(), b.SampledAt)
	assert.Equal(t, int32(12000), b.BusMV)
	assert.Equal(t, int32(500), b.CurrentMA)
	assert.Equal(t, int32(6000), b.PowerMW, "µW converted to mW")
	assert.Equal(t, int32(12030), b.CompensatedMV, "V + I x 0.06 Ω")
	assert.True(t, b.HasEnergy)
	assert.InDelta(t, 10.0, b.EnergyMWh, 1e-9, "36 J = 10 mWh")
	assert.True(t, b.HasTemp)
	assert.InDelta(t, 40.5, b.TempC, 1e-9)
	assert.Equal(t, uint32(3), b.SeriesCells)
	assert.Equal(t, hardware.BatteryLevelNormal, b.Level)
}

func TestBattery_StaleThenAbsent(t *testing.T) {
	rig := newRig(t, nil)
	rig.step()
	rig.mon.set(system.MonitorReading{}, errors.New("i2c timeout"))

	rig.run(5*time.Second, time.Second)

	b := rig.m.Status().Battery
	assert.False(t, b.Stale, "5 s old is still fresh")
	assert.True(t, b.Present)

	rig.run(time.Second, time.Second)

	b = rig.m.Status().Battery
	assert.True(t, b.Stale, "older than 5 s is stale")
	assert.True(t, b.Present)
	assert.Equal(t, int32(12000), b.BusMV, "values kept while stale")

	rig.run(25*time.Second, time.Second)

	b = rig.m.Status().Battery
	assert.False(t, b.Present, "older than 30 s is absent")
	assert.Equal(t, hardware.AbsentReasonMonitorUnreachable, b.AbsentReason)

	rig.mon.set(healthyReading(), nil)
	rig.run(time.Second, time.Second)

	b = rig.m.Status().Battery
	assert.True(t, b.Present)
	assert.False(t, b.Stale)
}

func TestBattery_AbsentReasons(t *testing.T) {
	t.Run("no monitor device", func(t *testing.T) {
		rig := newRig(t, func(_ *hardware.Options, r *testRig) { r.mon.reading = system.MonitorReading{} })
		rig.step()

		b := rig.m.Status().Battery
		assert.False(t, b.Present)
		assert.Equal(t, hardware.AbsentReasonNoMonitor, b.AbsentReason)
		assert.Equal(t, hardware.BatteryLevelUnknown, b.Level)
	})

	t.Run("no reader", func(t *testing.T) {
		rig := newRig(t, func(o *hardware.Options, _ *testRig) { o.Battery = nil })
		rig.step()
		assert.Equal(t, hardware.AbsentReasonNoMonitor, rig.m.Status().Battery.AbsentReason)
	})

	t.Run("never read successfully", func(t *testing.T) {
		rig := newRig(t, func(_ *hardware.Options, r *testRig) { r.mon.err = errors.New("eio") })
		rig.step()

		b := rig.m.Status().Battery
		assert.False(t, b.Present)
		assert.Equal(t, hardware.AbsentReasonMonitorUnreachable, b.AbsentReason)
		assert.True(t, b.SampledAt.IsZero())
	})
}

func TestBattery_SlowAttributesEvery10s(t *testing.T) {
	rig := newRig(t, nil)
	rig.step()
	rig.run(29*time.Second, time.Second)

	assert.Equal(t, 3, rig.mon.getFullCalls(), "energy, temperature and alarms at t=0, 10, 20 s")
}

func TestBattery_ChargeIntegration(t *testing.T) {
	rig := newRig(t, nil)
	rig.step()
	rig.run(time.Hour, time.Second)

	assert.InDelta(t, 500, rig.m.Status().Battery.ChargeMAh, 0.5, "500 mA for one hour")

	// A read gap longer than 5 s is skipped, not extrapolated.
	before := rig.m.Status().Battery.ChargeMAh
	rig.mon.set(system.MonitorReading{}, errors.New("gap"))
	rig.run(time.Minute, time.Second)
	rig.mon.set(healthyReading(), nil)
	rig.run(time.Second, time.Second)
	assert.InDelta(t, before, rig.m.Status().Battery.ChargeMAh, 1e-9)
}

func TestBattery_LevelHoldAndHysteresis(t *testing.T) {
	rig := newRig(t, nil)
	rig.step()

	// V_comp = 10370 + 500 x 0.06 = 10400 mV, below the 10.5 V warning.
	rig.mon.set(readingAt(10370, 500), nil)
	rig.run(30*time.Second, time.Second)
	assert.Equal(t, hardware.BatteryLevelNormal, rig.m.Status().Battery.Level, "30 s entry hold from the first low reading")

	rig.run(time.Second, time.Second)
	assert.Equal(t, hardware.BatteryLevelWarning, rig.m.Status().Battery.Level)

	// 10.6 V is above the threshold but inside the 0.2 V hysteresis.
	rig.mon.set(readingAt(10570, 500), nil)
	rig.run(5*time.Minute, 10*time.Second)
	assert.Equal(t, hardware.BatteryLevelWarning, rig.m.Status().Battery.Level)

	rig.mon.set(readingAt(10720, 500), nil)
	rig.run(60*time.Second, time.Second)
	assert.Equal(t, hardware.BatteryLevelWarning, rig.m.Status().Battery.Level, "60 s clear hold")

	rig.run(time.Second, time.Second)
	assert.Equal(t, hardware.BatteryLevelNormal, rig.m.Status().Battery.Level)
}

func TestBattery_LevelUnknownWithoutV1Pack(t *testing.T) {
	rig := newRig(t, func(o *hardware.Options, r *testRig) {
		o.SeriesCells = 0
		r.mon.reading = readingAt(9000, 0)
	})
	rig.run(2*time.Minute, time.Second)

	b := rig.m.Status().Battery
	assert.True(t, b.Present)
	assert.Equal(t, hardware.BatteryLevelUnknown, b.Level)
	assert.Equal(t, hardware.StateUp, rig.radio(hardware.RadioWiFi).State)
}

func TestBattery_CriticalTurnsWiFiOff(t *testing.T) {
	rig := newRig(t, nil)
	rig.step()

	rig.mon.set(readingAt(9800, 0), nil)
	rig.run(31*time.Second, time.Second)

	st := rig.m.Status()
	assert.Equal(t, hardware.BatteryLevelCritical, st.Battery.Level)
	assert.Equal(t, hardware.StateOff, st.Radios[1].State)
	assert.Equal(t, hardware.ReasonLowBattery, st.Radios[1].Reason)
	assert.True(t, st.Radios[1].PowerRequested, "the operator's request is untouched")
	assert.Equal(t, hardware.StateUp, st.Radios[0].State, "HaLow stays up")

	// Losing the monitor must not bring the load back.
	rig.mon.set(system.MonitorReading{}, errors.New("gone"))
	rig.run(time.Minute, time.Second)
	assert.Equal(t, hardware.StateOff, rig.radio(hardware.RadioWiFi).State)

	rig.mon.set(readingAt(12000, 0), nil)
	rig.run(62*time.Second, time.Second)
	assert.Equal(t, hardware.BatteryLevelNormal, rig.m.Status().Battery.Level)
	assert.NotEqual(t, hardware.StateOff, rig.radio(hardware.RadioWiFi).State, "Wi-Fi restored")
}

func TestBattery_ShutdownLevel(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		rig := newRig(t, func(o *hardware.Options, _ *testRig) { o.BatteryShutdownEnable = true })
		rig.step()

		rig.mon.set(readingAt(9500, 0), nil)
		rig.run(31*time.Second, time.Second)
		assert.True(t, rig.m.Status().ShutdownPending)
		assert.Zero(t, rig.shut.getCalls(), "10 s grace for EUD clients")

		rig.run(10*time.Second, time.Second)
		assert.Equal(t, 1, rig.shut.getCalls())

		for _, rs := range rig.m.Status().Radios {
			assert.Equal(t, hardware.StateOff, rs.State)
			assert.Equal(t, hardware.ReasonLowBattery, rs.Reason)
		}

		rig.run(time.Minute, time.Second)
		assert.Equal(t, 1, rig.shut.getCalls(), "powers off once")
	})

	t.Run("disabled by default", func(t *testing.T) {
		rig := newRig(t, nil)
		rig.step()

		rig.mon.set(readingAt(9500, 0), nil)
		rig.run(2*time.Minute, time.Second)

		assert.False(t, rig.m.Status().ShutdownPending)
		assert.Zero(t, rig.shut.getCalls())

		ev := rig.m.Events(0, hardware.RadioUnspecified)
		require.NotEmpty(t, ev)

		var skipped int

		for _, e := range ev {
			if e.Step == hardware.StepShutdown {
				skipped++

				assert.Equal(t, hardware.ResultSkipped, e.Result)
			}
		}

		assert.Equal(t, 1, skipped, "skip recorded once")
	})

	t.Run("INA228 alert backstop", func(t *testing.T) {
		rig := newRig(t, func(o *hardware.Options, _ *testRig) {
			o.BatteryShutdownEnable = true
			o.FaultInputs = []string{hardware.LineINA228AlertN}
		})
		rig.step()

		rig.lines().setValue(hardware.LineINA228AlertN, true)
		rig.step()
		assert.True(t, rig.m.Status().ShutdownPending, "ALERT starts the shutdown without the 30 s hold")
	})
}

func TestWatchdog_DisabledByDefault(t *testing.T) {
	rig := newRig(t, nil)
	rig.run(time.Minute, time.Second)

	assert.Zero(t, rig.wdOpener.getOpens())
	assert.False(t, rig.m.Status().Watchdog.Armed)
	assert.False(t, rig.m.Status().Watchdog.Enabled)
}

func TestWatchdog_PetsEvery10s(t *testing.T) {
	rig := newRig(t, func(o *hardware.Options, _ *testRig) { o.WatchdogEnable = true })
	rig.step()
	rig.run(time.Minute, time.Second)

	assert.Equal(t, 7, rig.wd.getPets(), "t=0 then every 10 s")
	assert.Equal(t, hardware.DefaultWatchdogIdentity, rig.wdOpener.identity)

	w := rig.m.Status().Watchdog
	assert.True(t, w.Enabled)
	assert.True(t, w.Armed)
	assert.Equal(t, "/dev/watchdog1", w.Device)
	assert.Equal(t, uint64(7), w.PetsTotal)
	assert.Equal(t, rig.clock.Now(), w.LastPetAt)
}

func TestWatchdog_OpenFailureLeavesUnarmed(t *testing.T) {
	rig := newRig(t, func(o *hardware.Options, r *testRig) {
		o.WatchdogEnable = true
		r.wdOpener.openErr = hardware.ErrWatchdogNotFound
	})
	rig.step()

	assert.False(t, rig.m.Status().Watchdog.Armed)
	assert.Equal(t, hardware.ControlModeActive, rig.m.Status().ControlMode)
}

// failBothRadios drives both radios to failed and then holds them there.
func failBothRadios(rig *testRig) {
	rig.bus.setPresent(hardware.RadioHaLow, false, false)
	rig.bus.setPresent(hardware.RadioWiFi, false, false)
	rig.step()
	rig.run(5*time.Minute, time.Second)
}

func TestHostReset(t *testing.T) {
	t.Run("requested after both radios failed for 30 min", func(t *testing.T) {
		rig := newRig(t, func(o *hardware.Options, _ *testRig) { o.WatchdogEnable = true })
		failBothRadios(rig)

		rig.run(25*time.Minute, 10*time.Second)
		assert.False(t, rig.m.Status().Watchdog.HostResetRequested)

		rig.run(10*time.Minute, 10*time.Second)

		w := rig.m.Status().Watchdog
		assert.True(t, w.HostResetRequested)
		assert.False(t, w.Armed)

		saves, rec := rig.store.getSaves()
		assert.Equal(t, 1, saves, "persisted before the heartbeat stops")
		assert.Len(t, rec.RecoveryResets, 1)
		assert.Len(t, rec.HostResetRequests, 1)

		pets := rig.wd.getPets()
		rig.run(5*time.Minute, 10*time.Second)
		assert.Equal(t, pets, rig.wd.getPets(), "heartbeat stopped")

		rig.m.Close()

		closed, clean := rig.wd.getClose()
		assert.True(t, closed)
		assert.False(t, clean, "no magic close: the supervisor must time out")
	})

	t.Run("not while the battery is low", func(t *testing.T) {
		rig := newRig(t, func(o *hardware.Options, r *testRig) {
			o.WatchdogEnable = true
			r.mon.reading = readingAt(10300, 0)
		})
		failBothRadios(rig)
		rig.run(time.Hour, 10*time.Second)

		assert.False(t, rig.m.Status().Watchdog.HostResetRequested)
	})

	t.Run("limit of fewer than three per 24 h", func(t *testing.T) {
		rig := newRig(t, func(o *hardware.Options, r *testRig) {
			o.WatchdogEnable = true
			now := r.clock.Now()
			r.store.rec.HostResetRequests = []time.Time{
				now.Add(-20 * time.Hour), now.Add(-10 * time.Hour), now.Add(-2 * time.Hour),
			}
		})
		failBothRadios(rig)
		rig.run(time.Hour, 10*time.Second)

		assert.False(t, rig.m.Status().Watchdog.HostResetRequested)

		saves, _ := rig.store.getSaves()
		assert.Zero(t, saves)
	})

	t.Run("persist failure blocks the request", func(t *testing.T) {
		rig := newRig(t, func(o *hardware.Options, r *testRig) {
			o.WatchdogEnable = true
			r.store.saveErr = errors.New("read-only")
		})
		failBothRadios(rig)
		rig.run(time.Hour, 10*time.Second)

		assert.False(t, rig.m.Status().Watchdog.HostResetRequested)
		assert.True(t, rig.m.Status().Watchdog.Armed, "still petting")

		var failed bool

		for _, e := range rig.m.Events(0, hardware.RadioUnspecified) {
			if e.Step == hardware.StepHostReset && e.Result == hardware.ResultFailed {
				failed = true
			}
		}

		assert.True(t, failed)
	})

	t.Run("unreadable record blocks requests", func(t *testing.T) {
		rig := newRig(t, func(o *hardware.Options, r *testRig) {
			o.WatchdogEnable = true
			r.store.loadErr = errors.New("corrupt")
		})
		failBothRadios(rig)
		rig.run(time.Hour, 10*time.Second)

		assert.False(t, rig.m.Status().Watchdog.HostResetRequested)
	})

	t.Run("one radio failing never resets the host", func(t *testing.T) {
		rig := newRig(t, func(o *hardware.Options, _ *testRig) { o.WatchdogEnable = true })
		rig.bus.setPresent(hardware.RadioHaLow, false, false)
		rig.step()
		rig.run(2*time.Hour, 10*time.Second)

		assert.Equal(t, hardware.StateFailed, rig.radio(hardware.RadioHaLow).State)
		assert.False(t, rig.m.Status().Watchdog.HostResetRequested)
	})
}

func TestBootLoopGuard_CountsResets(t *testing.T) {
	rig := newRig(t, func(_ *hardware.Options, r *testRig) {
		now := r.clock.Now()
		r.store.rec.RecoveryResets = []time.Time{now.Add(-30 * time.Minute), now.Add(-10 * time.Minute)}
	})

	st := rig.m.Status()
	assert.Equal(t, uint32(2), st.RecoveryBootsLastHour)
	assert.Equal(t, hardware.ControlModeActive, st.ControlMode, "two resets do not trip the guard")
}

func TestSnapshotter(t *testing.T) {
	rig := newRig(t, func(o *hardware.Options, _ *testRig) {
		o.WatchdogEnable = true
		o.FaultInputs = []string{hardware.LineSupervisorWDO, hardware.LineVLMUSBFaultN}
	})
	rig.step()
	rig.lines().setValue(hardware.LineVLMUSBFaultN, true)
	rig.lines().setValue(hardware.LineSupervisorWDO, true)
	rig.step()

	s := &hardware.Snapshotter{Manager: rig.m}
	s.Refresh()

	d, ok := s.Data().(*hardware.HardwareSnapshot)
	require.True(t, ok)
	assert.True(t, d.BoardDetected)
	assert.Equal(t, "active", d.ControlMode)
	assert.Equal(t, "halow", d.Radios[0].Name)
	assert.Equal(t, "up", d.Radios[0].State)
	assert.Equal(t, rig.radio(hardware.RadioHaLow).StateSince.UnixNano(), d.Radios[0].StateSinceNs)
	assert.Equal(t, int32(12000), d.BatteryBusMV)
	assert.Equal(t, int32(500), d.BatteryCurrentMA)
	assert.False(t, d.BatteryStale)
	assert.Equal(t, uint64(1), d.FaultEdgesTotal.VLMUSBFaultN)
	assert.Equal(t, uint64(1), d.FaultEdgesTotal.SupervisorWDO)
	assert.Equal(t, uint64(1), d.WDOEdgesTotal)
	assert.Equal(t, uint64(1), d.WatchdogPetsTotal)

	raw, err := json.Marshal(d)
	require.NoError(t, err)

	var keys map[string]any
	require.NoError(t, json.Unmarshal(raw, &keys))

	for _, k := range []string{
		"board_detected", "control_mode", "radios", "fault_edges_total", "battery_bus_mv",
		"battery_current_ma", "battery_stale", "watchdog_pets_total", "wdo_edges_total",
		"recovery_boots_last_hour",
	} {
		assert.Contains(t, keys, k)
	}

	allocs := testing.AllocsPerRun(100, s.Refresh)
	assert.Zero(t, allocs, "Refresh must not allocate")
}

func TestSnapshotter_NilManager(t *testing.T) {
	s := &hardware.Snapshotter{}
	s.Refresh()
	assert.NotNil(t, s.Data())
}

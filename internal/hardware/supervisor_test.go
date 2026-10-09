package hardware_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openmanet/openmanetd/internal/hardware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	armHigh = "set " + hardware.LineSupervisorARM + "=true"
	armLow  = "set " + hardware.LineSupervisorARM + "=false"
)

// watchdogRig is an active-mode V1 rig with the watchdog service enabled.
func watchdogRig(t *testing.T, mutate func(*hardware.Options, *testRig)) *testRig {
	t.Helper()

	return newRig(t, func(o *hardware.Options, r *testRig) {
		o.WatchdogEnable = true

		if mutate != nil {
			mutate(o, r)
		}
	})
}

// armSpec returns the SUPERVISOR_ARM spec the manager requested, if any.
func armSpec(rig *testRig) (hardware.LineSpec, bool) {
	for _, s := range rig.opener.getOpened() {
		if s.Name == hardware.LineSupervisorARM {
			return s, true
		}
	}

	return hardware.LineSpec{}, false
}

func TestSupervisorArm_RequestedLowAtStartup(t *testing.T) {
	rig := watchdogRig(t, func(_ *hardware.Options, r *testRig) {
		// A previous owner left the line high; the request must not adopt it.
		r.opener.initial[hardware.LineSupervisorARM] = true
	})

	s, ok := armSpec(rig)
	require.True(t, ok, "SUPERVISOR_ARM requested when the watchdog is enabled")
	assert.True(t, s.Output)
	assert.True(t, s.StartDeasserted)
	assert.False(t, rig.lines().value(hardware.LineSupervisorARM), "low before the first heartbeat")
	assert.Empty(t, rig.lines().setsOf(hardware.LineSupervisorARM))

	w := rig.m.Status().Watchdog
	assert.False(t, w.SupervisorArmed)
	assert.Zero(t, w.SupervisorArmTransitions)
}

func TestSupervisorArm_OnlyAfterFirstHeartbeat(t *testing.T) {
	rig := watchdogRig(t, nil)
	require.Zero(t, rig.wd.getPets())
	require.Equal(t, -1, rig.calls.index(armHigh))

	rig.step()

	pet, arm := rig.calls.index("pet"), rig.calls.index(armHigh)
	require.NotEqual(t, -1, pet)
	require.NotEqual(t, -1, arm)
	assert.Less(t, pet, arm, "heartbeat written before the arm goes high")
	assert.True(t, rig.lines().value(hardware.LineSupervisorARM))

	rig.run(time.Minute, time.Second)
	assert.Equal(t, []bool{true}, rig.lines().setsOf(hardware.LineSupervisorARM), "armed once, not re-driven per pet")

	w := rig.m.Status().Watchdog
	assert.True(t, w.SupervisorArmed)
	assert.Equal(t, uint64(1), w.SupervisorArmTransitions)
}

func TestSupervisorArm_DisarmBeforeHeartbeatStops(t *testing.T) {
	t.Run("Close", func(t *testing.T) {
		rig := watchdogRig(t, nil)
		rig.step()
		require.True(t, rig.m.Status().Watchdog.SupervisorArmed)

		rig.m.Close()

		low, closed, released := rig.calls.index(armLow), rig.calls.index("watchdog close"), rig.calls.index("lines close")
		require.NotEqual(t, -1, low, "disarmed on stop")
		require.NotEqual(t, -1, closed)
		require.NotEqual(t, -1, released)
		assert.Less(t, low, closed, "SUPERVISOR_ARM low before the watchdog closes")
		assert.Less(t, closed, released, "lines released last")
		assert.False(t, rig.lines().value(hardware.LineSupervisorARM))

		w := rig.m.Status().Watchdog
		assert.False(t, w.SupervisorArmed)
		assert.Equal(t, uint64(2), w.SupervisorArmTransitions)
	})

	t.Run("context cancel", func(t *testing.T) {
		rig := watchdogRig(t, func(o *hardware.Options, _ *testRig) { o.Clock = nil })

		ch, unsubscribe, err := rig.m.Subscribe()
		require.NoError(t, err)
		t.Cleanup(unsubscribe)

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		go rig.m.Run(ctx)

		deadline := time.After(10 * time.Second)

		for !rig.m.Status().Watchdog.SupervisorArmed {
			select {
			case <-ch:
			case <-deadline:
				require.FailNow(t, "manager never armed the supervisor")
			}
		}

		cancel()
		<-rig.m.Done()

		low, closed := rig.calls.index(armLow), rig.calls.index("watchdog close")
		require.NotEqual(t, -1, low, "disarmed on context cancel")
		require.NotEqual(t, -1, closed)
		assert.Less(t, low, closed, "SUPERVISOR_ARM low before the watchdog closes")

		_, clean := rig.wd.getClose()
		assert.True(t, clean)
	})
}

func TestSupervisorArm_DisarmBeforePoweroff(t *testing.T) {
	rig := watchdogRig(t, func(o *hardware.Options, _ *testRig) { o.BatteryShutdownEnable = true })
	rig.step()
	require.True(t, rig.m.Status().Watchdog.SupervisorArmed)

	rig.mon.set(readingAt(9500, 0), nil)
	rig.run(41*time.Second, time.Second)
	require.Equal(t, 1, rig.shut.getCalls())

	low, off := rig.calls.index(armLow), rig.calls.index("poweroff")
	require.NotEqual(t, -1, low, "disarmed for a deliberate poweroff")
	assert.Less(t, low, off, "SUPERVISOR_ARM low before poweroff")

	rig.run(time.Minute, time.Second)
	assert.Equal(t, []bool{true, false}, rig.lines().setsOf(hardware.LineSupervisorARM), "never re-armed after shutdown")
	assert.False(t, rig.m.Status().Watchdog.SupervisorArmed)
}

func TestSupervisorArm_StaysArmedForHostReset(t *testing.T) {
	rig := watchdogRig(t, nil)
	failBothRadios(rig)
	rig.run(35*time.Minute, 10*time.Second)

	w := rig.m.Status().Watchdog
	require.True(t, w.HostResetRequested)
	assert.True(t, w.SupervisorArmed, "the supervisor must stay armed to time out")

	rig.m.Close()

	assert.Equal(t, []bool{true}, rig.lines().setsOf(hardware.LineSupervisorARM), "no disarm on a host reset")
	assert.True(t, rig.m.Status().Watchdog.SupervisorArmed)
}

func TestSupervisorArm_StaysLow(t *testing.T) {
	t.Run("watchdog disabled", func(t *testing.T) {
		rig := newRig(t, nil)
		rig.run(time.Minute, time.Second)
		rig.m.Close()

		_, ok := armSpec(rig)
		assert.False(t, ok, "not requested")
		assert.Empty(t, rig.lines().setsOf(hardware.LineSupervisorARM))
		assert.False(t, rig.m.Status().Watchdog.SupervisorArmed)
	})

	t.Run("board not detected", func(t *testing.T) {
		rig := watchdogRig(t, func(o *hardware.Options, _ *testRig) { o.BoardID = "" })
		rig.run(time.Minute, time.Second)

		assert.Empty(t, rig.opener.getOpened())
		assert.Nil(t, rig.lines())
		assert.False(t, rig.m.Status().Watchdog.SupervisorArmed)
	})

	t.Run("watchdog open fails", func(t *testing.T) {
		rig := watchdogRig(t, func(_ *hardware.Options, r *testRig) {
			r.wdOpener.openErr = hardware.ErrWatchdogNotFound
		})
		rig.run(time.Minute, time.Second)
		rig.m.Close()

		_, ok := armSpec(rig)
		assert.True(t, ok, "requested low regardless")
		assert.Empty(t, rig.lines().setsOf(hardware.LineSupervisorARM))
		assert.False(t, rig.lines().value(hardware.LineSupervisorARM))
		assert.False(t, rig.m.Status().Watchdog.SupervisorArmed)
	})

	t.Run("heartbeat write fails", func(t *testing.T) {
		rig := watchdogRig(t, func(_ *hardware.Options, r *testRig) {
			r.wd.petErr = errors.New("EIO")
		})
		rig.run(time.Minute, time.Second)

		assert.Empty(t, rig.lines().setsOf(hardware.LineSupervisorARM), "no heartbeat, no arm")
		assert.False(t, rig.m.Status().Watchdog.SupervisorArmed)

		rig.wd.setPetErr(nil)
		rig.step()
		assert.True(t, rig.m.Status().Watchdog.SupervisorArmed, "arms once the heartbeat runs")
	})

	t.Run("arm line missing", func(t *testing.T) {
		rig := watchdogRig(t, func(_ *hardware.Options, r *testRig) {
			delete(r.opener.resolvable, hardware.LineSupervisorARM)
		})
		rig.run(time.Minute, time.Second)

		_, ok := armSpec(rig)
		assert.False(t, ok)
		assert.Positive(t, rig.wd.getPets(), "the heartbeat still runs")
		assert.Equal(t, hardware.ControlModeActive, rig.m.Status().ControlMode, "radio control is unaffected")
		assert.False(t, rig.m.Status().Watchdog.SupervisorArmed)
	})

	t.Run("line request fails", func(t *testing.T) {
		rig := watchdogRig(t, func(_ *hardware.Options, r *testRig) {
			r.opener.openErr = errors.New("busy")
		})
		rig.run(time.Minute, time.Second)

		assert.Positive(t, rig.wd.getPets())
		assert.False(t, rig.m.Status().Watchdog.SupervisorArmed)
	})
}

func TestSupervisorArm_RetriesFailedArmWrite(t *testing.T) {
	rig := watchdogRig(t, nil)
	rig.lines().setSetErr(hardware.LineSupervisorARM, errors.New("EIO"))
	rig.step()

	assert.False(t, rig.m.Status().Watchdog.SupervisorArmed)
	assert.Zero(t, rig.m.Status().Watchdog.SupervisorArmTransitions)

	rig.lines().setSetErr(hardware.LineSupervisorARM, nil)
	rig.run(10*time.Second, time.Second)

	w := rig.m.Status().Watchdog
	assert.True(t, w.SupervisorArmed, "retried on the next heartbeat")
	assert.Equal(t, uint64(1), w.SupervisorArmTransitions)
}

func TestSupervisorArm_TelemetryOnlyStillArms(t *testing.T) {
	rig := watchdogRig(t, func(o *hardware.Options, _ *testRig) { o.ActuationEnable = false })
	rig.step()

	assert.Equal(t, hardware.ControlModeTelemetryOnly, rig.m.Status().ControlMode)

	for _, s := range rig.opener.getOpened() {
		if s.Output {
			assert.Equal(t, hardware.LineSupervisorARM, s.Name, "the arm is the only output in telemetry-only mode")
		}
	}

	assert.Equal(t, []bool{true}, rig.lines().setsOf(hardware.LineSupervisorARM))
	assert.True(t, rig.m.Status().Watchdog.SupervisorArmed)
}

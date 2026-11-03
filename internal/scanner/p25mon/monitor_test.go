package p25mon_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/openmanet/openmanetd/internal/scanner/p25mon"
	"github.com/openmanet/openmanetd/internal/scanner/p25mon/p25montest"
)

const (
	fixtureRate = 1_024_000
	// The site fixture's control channel sits 100 kHz above its capture
	// center; these frequencies put it there.
	fixtureCenter  = 851_000_000
	fixtureControl = fixtureCenter + 100_000
	// Expected decodes from c4fm_1024k_site.cu8 (internal/sdr/p25 fixtures).
	fixtureGrantHz = 851_006_250 + 0x2AB*6250
	waitTimeout    = 10 * time.Second
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)

	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}

		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "module root not found")
		dir = parent
	}

	data, err := os.ReadFile(filepath.Join(dir, "testfixtures", "sdr", "p25", name))
	require.NoError(t, err)

	return data
}

// startMonitor runs m until the test ends.
func startMonitor(t *testing.T, m *p25mon.Monitor) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)

		m.Run(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// waitFor reads events until match returns true and returns the matching
// event. It fails the test after waitTimeout or when the channel closes.
func waitFor(t *testing.T, events <-chan any, match func(any) bool) any {
	t.Helper()

	timeout := time.After(waitTimeout)

	for {
		select {
		case ev, ok := <-events:
			require.True(t, ok, "event channel closed")

			if match(ev) {
				return ev
			}
		case <-timeout:
			require.FailNow(t, "timed out waiting for event")
		}
	}
}

func isLocked(ev any) bool {
	s, ok := ev.(scanner.SyncState)

	return ok && s.Locked
}

func isGrant(ev any) bool {
	_, ok := ev.(scanner.VoiceGrant)

	return ok
}

func isDecoderStatus(ev any) bool {
	_, ok := ev.(scanner.DecoderStatus)

	return ok
}

func scanPlan(control ...uint64) scanner.TunePlan {
	return scanner.TunePlan{
		SiteKey:   "UT-test",
		ControlHz: control,
		CenterHz:  fixtureCenter,
		UsableHz:  2_000_000,
		Serial:    "scan",
		Tuner:     scanner.TunerScan,
	}
}

func fixtureMonitor(runner *p25montest.Runner, hunt time.Duration) *p25mon.Monitor {
	return p25mon.New(p25mon.Options{
		Runner:      runner,
		Log:         zerolog.Nop(),
		RTLRate:     fixtureRate,
		HuntTimeout: hunt,
		MinBackoff:  time.Millisecond,
		MaxBackoff:  10 * time.Millisecond,
	})
}

func TestMonitor_decodesSiteFixture(t *testing.T) {
	runner := &p25montest.Runner{Data: fixture(t, "c4fm_1024k_site.cu8"), Passes: 4}
	m := fixtureMonitor(runner, 0)
	startMonitor(t, m)

	require.NoError(t, m.Tune(context.Background(), scanPlan(fixtureControl)))

	waitFor(t, m.Events(), isLocked)

	grant, ok := waitFor(t, m.Events(), isGrant).(scanner.VoiceGrant)
	require.True(t, ok)
	assert.Equal(t, scanner.VoiceGrant{
		FrequencyHz: fixtureGrantHz, Talkgroup: 8001, Source: 1234567, Channel: 0x12AB,
	}, grant)

	site, ok := waitFor(t, m.Events(), func(ev any) bool {
		s, ok := ev.(scanner.SiteStatus)

		return ok && s.WACN != 0
	}).(scanner.SiteStatus)
	require.True(t, ok)
	assert.Equal(t, uint32(0xBEE00), site.WACN)
	assert.Equal(t, uint32(7), site.Site)

	starts := runner.Starts()
	require.NotEmpty(t, starts)
	assert.Equal(t, "rtl_sdr", starts[0].Name)
	assert.Equal(t, []string{"-d", "scan", "-f", "851000000", "-s", "1024000", "-"}, starts[0].Args)
}

func TestMonitor_huntsAndRetunesToNextControlChannel(t *testing.T) {
	// The first control channel is outside the 2 MHz window, so the hunt
	// starts with the tuner 250 kHz below it; the fixture carries no
	// signal there. After the hunt timeout the monitor steps to the
	// in-window channel and moves the tuner back to the plan center.
	const outside = fixtureCenter + 2_500_000

	runner := &p25montest.Runner{Data: fixture(t, "c4fm_1024k_site.cu8"), Passes: 40}
	m := fixtureMonitor(runner, 300*time.Millisecond)
	startMonitor(t, m)

	require.NoError(t, m.Tune(context.Background(), scanPlan(outside, fixtureControl)))

	first, ok := waitFor(t, m.Events(), isDecoderStatus).(scanner.DecoderStatus)
	require.True(t, ok)
	require.NoError(t, first.Err)
	assert.Equal(t, uint64(outside), first.FrequencyHz)

	locked, ok := waitFor(t, m.Events(), func(ev any) bool {
		s, ok := ev.(scanner.DecoderStatus)

		return ok && s.FrequencyHz == fixtureControl
	}).(scanner.DecoderStatus)
	require.True(t, ok)
	require.NoError(t, locked.Err)

	waitFor(t, m.Events(), func(ev any) bool {
		g, ok := ev.(scanner.VoiceGrant)

		return ok && g.Talkgroup == 8001
	})

	starts := runner.Starts()
	require.GreaterOrEqual(t, len(starts), 2)
	assert.Equal(t, fmt.Sprint(outside-250_000), starts[0].Args[3], "first hunt centers off the out-of-window channel")
	assert.Equal(t, "851000000", starts[1].Args[3], "stepping back into the window retunes to the plan center")
}

func TestMonitor_captureToolMissingIsUnavailable(t *testing.T) {
	var looked []string

	m := p25mon.New(p25mon.Options{
		Log: zerolog.Nop(),
		LookPath: func(name string) (string, error) {
			looked = append(looked, name) // Run's goroutine only

			return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
		},
		MinBackoff: time.Hour, // one attempt is enough
	})
	startMonitor(t, m)

	require.NoError(t, m.Tune(context.Background(), scanPlan(fixtureControl)))

	st, ok := waitFor(t, m.Events(), isDecoderStatus).(scanner.DecoderStatus)
	require.True(t, ok)
	assert.True(t, st.Unavailable)
	require.ErrorIs(t, st.Err, exec.ErrNotFound)
	assert.Contains(t, st.Err.Error(), "rtl_sdr")
}

func TestMonitor_captureFailureBacksOff(t *testing.T) {
	runner := &p25montest.Runner{StartErr: errors.New("usb_claim_interface error -6")}
	m := p25mon.New(p25mon.Options{
		Runner: runner, Log: zerolog.Nop(), MinBackoff: time.Millisecond, MaxBackoff: 4 * time.Millisecond,
	})
	startMonitor(t, m)

	plan := scanner.TunePlan{
		SiteKey: "UT-wide", ControlHz: []uint64{770_006_250}, CenterHz: 770_503_125, UsableHz: 8_000_000,
		Serial: "AIRSPY SN:44A061DC2E395E8F", Tuner: scanner.TunerWide,
	}
	require.NoError(t, m.Tune(context.Background(), plan))

	for range 3 {
		st, ok := waitFor(t, m.Events(), func(ev any) bool {
			s, ok := ev.(scanner.DecoderStatus)

			return ok && s.Err != nil
		}).(scanner.DecoderStatus)
		require.True(t, ok)
		assert.False(t, st.Unavailable)
		assert.Contains(t, st.Err.Error(), "usb_claim_interface")
	}

	starts := runner.Starts()
	require.GreaterOrEqual(t, len(starts), 3, "the capture is retried")
	assert.Equal(t, "airspy_rx", starts[0].Name)
	assert.Equal(t, []string{"-s", "0x44A061DC2E395E8F", "-f", "770.503125", "-a", "10000000", "-t", "2", "-r", "-"},
		starts[0].Args)

	assert.Equal(t, time.Millisecond, m.BackoffForTest(1))
	assert.Equal(t, 2*time.Millisecond, m.BackoffForTest(2))
	assert.Equal(t, 4*time.Millisecond, m.BackoffForTest(3))
	assert.Equal(t, 4*time.Millisecond, m.BackoffForTest(30), "capped at MaxBackoff")
}

func TestMonitor_tuneValidation(t *testing.T) {
	m := p25mon.New(p25mon.Options{Runner: &p25montest.Runner{}, Log: zerolog.Nop()})

	wide := func(serial string) scanner.TunePlan {
		return scanner.TunePlan{ControlHz: []uint64{770_006_250}, CenterHz: 770_503_125, UsableHz: 8_000_000,
			Serial: serial, Tuner: scanner.TunerWide}
	}

	tests := []struct {
		name    string
		plan    scanner.TunePlan
		wantErr string
	}{
		{name: "stop", plan: scanner.TunePlan{}},
		{name: "scan", plan: scanPlan(fixtureControl)},
		{name: "wide", plan: wide("0x44A061DC2E395E8F")},
		{name: "no control channel", plan: scanPlan(), wantErr: "no control channels"},
		{name: "no serial", plan: wide(""), wantErr: "serial is required"},
		{name: "airspy serial not hex", plan: wide("wide"), wantErr: "decimal or 0x-prefixed hex"},
		{name: "rtl serial read as index", plan: func() scanner.TunePlan {
			p := scanPlan(fixtureControl)
			p.Serial = "1"

			return p
		}(), wantErr: "device index"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := m.Tune(context.Background(), tc.plan)
			if tc.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestMonitor_stopAndClose(t *testing.T) {
	runner := &p25montest.Runner{Data: fixture(t, "c4fm_1024k_site.cu8"), Passes: 1_000_000}
	m := fixtureMonitor(runner, 0)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)

		m.Run(ctx)
	}()

	require.NoError(t, m.Tune(context.Background(), scanPlan(fixtureControl)))
	waitFor(t, m.Events(), isLocked)

	// A zero plan stops the capture; Run then idles until canceled and
	// closes the event channel on the way out.
	require.NoError(t, m.Tune(context.Background(), scanner.TunePlan{}))
	cancel()
	<-done

	drained := 0
	for range m.Events() {
		drained++ // buffered events before the close
	}

	assert.LessOrEqual(t, drained, 64)

	assert.Len(t, runner.Starts(), 1)
}

func TestAirspySerial(t *testing.T) {
	tests := map[string]string{
		"AIRSPY SN:44A061DC2E395E8F": "0x44A061DC2E395E8F",
		"airspy sn: 44a061dc":        "0x44a061dc",
		"44a061dc2e395e8f":           "0x44a061dc2e395e8f",
		"0x44A061DC2E395E8F":         "0x44A061DC2E395E8F",
		"":                           "",
	}

	for in, want := range tests {
		t.Run(strings.ReplaceAll(in, " ", "_"), func(t *testing.T) {
			assert.Equal(t, want, p25mon.AirspySerial(in))
		})
	}
}

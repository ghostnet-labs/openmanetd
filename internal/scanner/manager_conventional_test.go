package scanner_test

import (
	"context"
	"math"
	"testing"
	"testing/fstest"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/gpsd"
	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/openmanet/openmanetd/internal/scanner/receivers"
	"github.com/openmanet/openmanetd/internal/scanner/sites"
)

func testChannel(state string, cat sites.ChannelCategory, hz uint64, label string, pos ...float64) sites.Channel {
	c := sites.Channel{State: state, Category: cat, Mode: sites.ModeNFM, SpacingHz: 12500, FrequencyHz: hz, Label: label}
	if cat == sites.CategoryAviation {
		c.Mode, c.SpacingHz = sites.ModeAM, 25000
	}

	if len(pos) == 2 {
		c.Lat, c.Lon, c.HasPosition = pos[0], pos[1], true
	}

	c.Key = sites.ChannelKey(c.State, c.Category, c.FrequencyHz, c.Label)

	return c
}

func testChannels() []sites.Channel {
	return []sites.Channel{
		testChannel("US", sites.CategoryAviation, 121_500_000, "guard"),
		testChannel("UT", sites.CategoryAviation, 118_300_000, "Near Tower", 40.0, -112.0),
		testChannel("UT", sites.CategoryAviation, 118_600_000, "Far Tower", 37.0, -109.0),
		testChannel("UT", sites.CategoryConventional, 155_475_000, "UT statewide"),
		testChannel("CO", sites.CategoryConventional, 155_000_000, "CO statewide"),
	}
}

type convHarness struct {
	mgr  *scanner.Manager
	gps  *fakeGPS
	mon  *releasingMonitor
	conv *fakeConventional
	log  *callLog
}

func newConvHarness(t *testing.T, cfg config.ScannerConfig) *convHarness {
	t.Helper()

	log := &callLog{}
	h := &convHarness{
		gps:  &fakeGPS{},
		mon:  &releasingMonitor{fakeMonitor: newFakeMonitor(), log: log},
		conv: newFakeConventional(log),
		log:  log,
	}

	sys := fstest.MapFS{}
	usbDevice(sys, "1-1.3", "1d50", "60a1", airspySerial)
	usbDevice(sys, "1-1.4", "0bda", "2838", "scan")

	h.mgr = scanner.NewManager(scanner.Options{
		Log:          zerolog.Nop(),
		Position:     h.gps,
		Monitor:      h.mon,
		Conventional: h.conv,
		RootFS:       fstest.MapFS{},
		SysFS:        sys,
		Now:          func() time.Time { return t0 },
		Sites:        testSites(),
		Channels:     testChannels(),
	}, cfg)
	h.mgr.MarkEnabledForTest()

	return h
}

func (h *convHarness) fix(lon float64) {
	h.gps.set(gpsd.PositionReport{Valid: true, Mode: 3, Latitude: 40, Longitude: lon, Timestamp: t0})
}

func labels(chs []scanner.ConventionalChannel) []string {
	out := make([]string, 0, len(chs))
	for _, c := range chs {
		out = append(out, c.Channel.Label)
	}

	return out
}

func scanRole(rs []receivers.Receiver) receivers.Receiver {
	for _, r := range rs {
		if r.Role == receivers.RoleScan {
			return r
		}
	}

	return receivers.Receiver{}
}

// With a wide receiver configured the scan RTL-SDR belongs to the
// conventional scanner: it gets the channels near the fix.
func TestManager_conventionalPlanNearFix(t *testing.T) {
	cfg := testConfig()
	cfg.WideSampleRate = 6_000_000
	h := newConvHarness(t, cfg)
	h.fix(-112.01)
	h.mgr.TickForTest(context.Background(), t0)

	plans := h.conv.getPlans()
	require.Len(t, plans, 1)
	assert.Equal(t, "scan", plans[0].Serial)
	assert.Equal(t, config.DefaultScannerRTLUsableHz, plans[0].UsableHz)
	assert.Equal(t, []string{"Near Tower", "guard", "UT statewide"}, labels(plans[0].Channels))
	assert.True(t, plans[0].Channels[1].AlwaysOn)
	assert.Less(t, plans[0].Channels[0].DistanceKm, 2.0)
	assert.True(t, math.IsNaN(plans[0].Channels[2].DistanceKm))

	assert.Equal(t, scanner.TunerWide, h.mon.getPlans()[0].Tuner)
	assert.Equal(t, 6_000_000, h.mon.getPlans()[0].SampleRate, "forced wide rate reaches the decoder")
	assert.Contains(t, h.log.get(), "p25.released:scan")

	// An unchanged list is not resent, even after a 50 km move; moving
	// into Colorado swaps the tower for the CO statewide channel.
	h.mgr.TickForTest(context.Background(), t0)
	h.fix(-111.4)
	h.mgr.TickForTest(context.Background(), t0)
	assert.Len(t, h.conv.getPlans(), 1)

	h.fix(-110.9)
	h.mgr.TickForTest(context.Background(), t0)
	require.Len(t, h.conv.getPlans(), 2)
	assert.Equal(t, []string{"guard", "CO statewide"}, labels(h.conv.getPlans()[1].Channels))

	h.conv.setStatus(scanner.ConventionalStatus{State: scanner.ConventionalScanning, Windows: 2})
	h.mgr.TickForTest(context.Background(), t0)

	st := h.mgr.Conventional()
	assert.Equal(t, scanner.ConventionalScanning, st.State)
	assert.Equal(t, 2, st.Windows)

	scan := scanRole(h.mgr.Status().Receivers)
	assert.Equal(t, receivers.StateRunning, scan.State, scan.Detail)
}

// Without a wide receiver the P25 decoder takes the scan RTL-SDR once a
// site is selected: the conventional scan is stopped first, and resumes
// once no site is selected and the decoder has released the receiver.
func TestManager_scanReceiverOwnershipIsExclusive(t *testing.T) {
	cfg := testConfig()
	cfg.WideSerial = ""
	h := newConvHarness(t, cfg)

	// No fix: no site, the conventional scanner gets nationwide channels.
	h.mgr.TickForTest(context.Background(), t0)
	require.Len(t, h.conv.getPlans(), 1)
	assert.Equal(t, []string{"guard"}, labels(h.conv.getPlans()[0].Channels))

	// A fix selects a site: conventional stops before the decoder tunes.
	h.fix(-112.01)
	h.mgr.TickForTest(context.Background(), t0)

	calls := h.log.get()
	stop := indexOf(calls, "conv.scan:")
	idle := indexOf(calls, "conv.idle")
	tune := indexOf(calls, "p25.tune:scan")

	require.NotEqual(t, -1, stop)
	require.NotEqual(t, -1, tune)
	assert.Less(t, stop, idle)
	assert.Less(t, idle, tune, "conventional capture stopped before the decoder opens the receiver")

	st := h.mgr.Conventional()
	assert.Equal(t, scanner.ConventionalReceiverBusy, st.State)
	assert.Contains(t, st.Detail, "P25 control channel")
	assert.Len(t, st.Channels, 3, "nearby channels are still listed")

	plans := h.conv.getPlans()
	assert.Empty(t, plans[len(plans)-1].Serial, "no conventional capture while the decoder owns the receiver")

	_, _, err := h.mgr.SubscribeAudio(st.Channels[0].Channel.Key)
	require.NoError(t, err, "listening is allowed; frames resume when the receiver is free")

	// Manual mode on a missing site: no selection, the decoder stops and
	// releases the receiver, and the conventional scan resumes on it.
	cfg.Mode, cfg.ManualSite = config.ScannerModeManual, "XX-none"
	cfg.Enable = false // install the config without starting the goroutine
	h.mgr.Apply(cfg)
	h.mgr.MarkEnabledForTest()
	h.mgr.TickForTest(context.Background(), t0)

	calls = h.log.get()
	zero := lastIndexOf(calls, "p25.tune:")
	released := lastIndexOf(calls, "p25.released:scan")
	resumed := lastIndexOf(calls, "conv.scan:scan")

	assert.Less(t, zero, released)
	assert.Less(t, released, resumed, "decoder released the receiver before the conventional scan resumed")
	assert.NotEqual(t, scanner.ConventionalReceiverBusy, h.mgr.Conventional().State)
}

func TestManager_subscribeAudio(t *testing.T) {
	h := newConvHarness(t, testConfig())
	h.fix(-112.01)
	h.mgr.TickForTest(context.Background(), t0)

	_, _, err := h.mgr.SubscribeAudio("C-00000000")
	require.ErrorIs(t, err, scanner.ErrUnknownChannel)

	key := h.conv.getPlans()[0].Channels[0].Channel.Key

	ch, cancel, err := h.mgr.SubscribeAudio(key)
	require.NoError(t, err)

	all, _, err := h.mgr.SubscribeAudio("")
	require.NoError(t, err)

	cancel()
	cancel()
	assert.Equal(t, 1, h.conv.getCanceled())

	_, ok := <-ch
	assert.False(t, ok)

	// Stopping the manager closes the remaining stream.
	h.mgr.Enable()
	h.mgr.Disable()

	_, ok = <-all
	assert.False(t, ok)

	_, _, err = h.mgr.SubscribeAudio("")
	require.ErrorIs(t, err, scanner.ErrDisabled)
	assert.Equal(t, scanner.ConventionalIdle, h.mgr.Conventional().State)
}

func TestManager_conventionalUnavailable(t *testing.T) {
	h := newHarness(t, true, testConfig())
	h.fix(-112.01, t0)
	h.mgr.TickForTest(context.Background(), t0)

	st := h.mgr.Conventional()
	assert.Equal(t, scanner.ConventionalUnavailable, st.State)
	assert.NotEmpty(t, st.Channels)

	_, _, err := h.mgr.SubscribeAudio("")
	require.ErrorIs(t, err, scanner.ErrConventionalUnavailable)

	scan := scanRole(h.mgr.Status().Receivers)
	assert.Contains(t, scan.Detail, "unavailable")
}

func indexOf(calls []string, prefix string) int {
	for i, c := range calls {
		if len(c) >= len(prefix) && c[:len(prefix)] == prefix {
			return i
		}
	}

	return -1
}

func lastIndexOf(calls []string, prefix string) int {
	for i := len(calls) - 1; i >= 0; i-- {
		if c := calls[i]; len(c) >= len(prefix) && c[:len(prefix)] == prefix {
			return i
		}
	}

	return -1
}

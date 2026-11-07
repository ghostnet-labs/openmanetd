package jamdetect_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openmanet/openmanetd/internal/jamdetect"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	radioIf  = "phy1-mesh0"
	radioPHY = "phy1"
	freq     = 5180.0
)

type harness struct {
	src *fakeSource
	clk *fakeClock
	det *jamdetect.Detector
}

func newHarness(t *testing.T, opts jamdetect.Options) *harness {
	t.Helper()

	h := &harness{
		src: newFakeSource(jamdetect.Radio{Ifname: radioIf, PHY: radioPHY}),
		clk: newFakeClock(),
	}

	opts.Log = zerolog.Nop()
	opts.Source = h.src
	opts.Now = h.clk.Now
	h.det = jamdetect.New(opts)

	return h
}

// healthy is a normal 5 GHz sample: -92 dBm, 30 % busy, nearly all of it
// explained by received and sent frames.
func (h *harness) healthy(n int) {
	for range n {
		h.sample(-92, 5000, 1500, 1200, 200)
	}
}

// jammed is a jammer sample: noise up 12 dB and 70 % busy time the radio
// cannot explain.
func (h *harness) jammed(n int) {
	for range n {
		h.sample(-80, 5000, 4000, 400, 100)
	}
}

func (h *harness) sample(noise int, active, busy, rx, tx uint64) {
	h.src.step(radioIf, freq, noise, active, busy, rx, tx)
	h.clk.Advance(5 * time.Second)
	h.det.Sample(context.Background())
}

func (h *harness) reading(t *testing.T) jamdetect.Reading {
	t.Helper()

	rs := h.det.Readings()
	require.Len(t, rs, 1)

	return rs[0]
}

func TestDetector_warmsUpThenClear(t *testing.T) {
	h := newHarness(t, jamdetect.Options{})

	h.healthy(1)
	assert.Equal(t, jamdetect.StateWarmingUp, h.reading(t).State)
	assert.False(t, h.reading(t).BusyKnown)

	h.healthy(jamdetect.DefaultWarmupSamples)

	r := h.reading(t)
	assert.Equal(t, jamdetect.StateClear, r.State)
	assert.Equal(t, jamdetect.Band5G, r.Band)
	assert.InDelta(t, -92, r.BaselineNoiseDBm, 0.01)
	assert.InDelta(t, 30, r.BusyPct, 0.01)
	assert.InDelta(t, 2, r.UnexplainedBusyPct, 0.01)
	assert.Empty(t, h.det.Recent())
	assert.Empty(t, h.det.Active())
}

func TestDetector_jamEpisodeLifecycle(t *testing.T) {
	h := newHarness(t, jamdetect.Options{})
	h.healthy(20)

	h.jammed(1)
	assert.Empty(t, h.det.Active(), "one abnormal sample is not enough")

	h.jammed(1)
	active := h.det.Active()
	require.Len(t, active, 1)
	assert.Equal(t, jamdetect.StateSuspected, active[0].State)
	id := active[0].ID
	assert.Contains(t, id, radioIf+"-")

	h.jammed(2)
	active = h.det.Active()
	require.Len(t, active, 1)
	assert.Equal(t, jamdetect.StateJammed, active[0].State)
	assert.Equal(t, id, active[0].ID, "episode ID is stable")
	assert.Equal(t, -80, active[0].NoiseDBm)
	assert.InDelta(t, -92, active[0].BaselineNoiseDBm, 0.01)
	assert.InDelta(t, 70, active[0].UnexplainedBusyPct, 0.01)
	assert.ElementsMatch(t, []jamdetect.Reason{jamdetect.ReasonNoiseRise, jamdetect.ReasonBusyRise}, active[0].Reasons)
	assert.Equal(t, jamdetect.StateJammed, h.reading(t).State)

	// A long jam must not be learned as the new normal.
	h.jammed(50)
	assert.InDelta(t, -92, h.reading(t).BaselineNoiseDBm, 0.01)
	assert.Equal(t, jamdetect.StateJammed, h.reading(t).State)

	h.healthy(jamdetect.DefaultClearSamples - 1)
	require.Len(t, h.det.Active(), 1, "not cleared before ClearSamples normal samples")

	h.healthy(1)
	assert.Empty(t, h.det.Active())
	assert.Equal(t, jamdetect.StateClear, h.reading(t).State)

	recent := h.det.Recent()
	require.Len(t, recent, 1, "one history entry per episode")
	assert.Equal(t, id, recent[0].ID)
	assert.Equal(t, jamdetect.StateClear, recent[0].State)
	assert.False(t, recent[0].Cleared.IsZero())
	assert.Equal(t, -80, recent[0].PeakNoiseDBm)
	assert.InDelta(t, 70, recent[0].PeakUnexplainedPct, 0.01)
}

func TestDetector_busyButHealthyChannelIsNotJamming(t *testing.T) {
	h := newHarness(t, jamdetect.Options{})
	h.healthy(20)

	// 95 % busy, but the radio received or sent nearly all of it.
	for range 20 {
		h.sample(-92, 5000, 4750, 4300, 400)
	}

	assert.Empty(t, h.det.Active())
	assert.Empty(t, h.det.Recent())
	assert.InDelta(t, 95, h.reading(t).BusyPct, 0.01)
}

func TestDetector_busyRiseAloneDetects(t *testing.T) {
	h := newHarness(t, jamdetect.Options{})
	h.healthy(20)

	for range jamdetect.DefaultJamSamples {
		h.sample(-92, 5000, 3000, 300, 100) // 52 % unexplained, noise unchanged
	}

	active := h.det.Active()
	require.Len(t, active, 1)
	assert.Equal(t, jamdetect.StateJammed, active[0].State)
	assert.Equal(t, []jamdetect.Reason{jamdetect.ReasonBusyRise}, active[0].Reasons)
}

func TestDetector_jammerPresentAtBootUsesCeilings(t *testing.T) {
	h := newHarness(t, jamdetect.Options{})

	// From the first sample: noise above the -70 dBm ceiling and 90 %
	// unexplained busy time. No baseline exists yet.
	for range jamdetect.DefaultJamSamples + 1 {
		h.sample(-60, 5000, 4600, 100, 0)
	}

	active := h.det.Active()
	require.Len(t, active, 1)
	assert.Equal(t, jamdetect.StateJammed, active[0].State)
	assert.Contains(t, active[0].Reasons, jamdetect.ReasonNoiseHigh)
	assert.Contains(t, active[0].Reasons, jamdetect.ReasonBusyHigh)
}

func TestDetector_channelChangeClosesEpisodeAndRelearns(t *testing.T) {
	h := newHarness(t, jamdetect.Options{})
	h.healthy(20)
	h.jammed(jamdetect.DefaultJamSamples)
	require.Len(t, h.det.Active(), 1)

	// Channel migration moved the radio away.
	h.src.step(radioIf, 5745, -95, 5000, 500, 400, 50)
	h.clk.Advance(5 * time.Second)
	h.det.Sample(context.Background())

	assert.Empty(t, h.det.Active())

	r := h.reading(t)
	assert.Equal(t, jamdetect.StateWarmingUp, r.State)
	assert.InDelta(t, 5745, r.FreqMHz, 0.01)
	assert.InDelta(t, -95, r.BaselineNoiseDBm, 0.01, "baseline restarts on the new channel")

	recent := h.det.Recent()
	require.Len(t, recent, 1)
	assert.Equal(t, jamdetect.StateClear, recent[0].State)
}

func TestDetector_counterResetIsIgnored(t *testing.T) {
	h := newHarness(t, jamdetect.Options{})
	h.healthy(20)

	h.src.resetCounters(radioIf)
	h.healthy(3)

	assert.Empty(t, h.det.Active())
	assert.Equal(t, jamdetect.StateClear, h.reading(t).State)
}

func TestDetector_radioGoneClosesEpisode(t *testing.T) {
	h := newHarness(t, jamdetect.Options{})
	h.healthy(20)
	h.jammed(jamdetect.DefaultJamSamples)
	require.Len(t, h.det.Active(), 1)

	h.src.setRadios()
	h.det.Sample(context.Background())

	assert.Empty(t, h.det.Active())
	assert.Empty(t, h.det.Readings())
	require.Len(t, h.det.Recent(), 1)
	assert.Equal(t, jamdetect.StateClear, h.det.Recent()[0].State)
}

func TestDetector_listErrorsAreCounted(t *testing.T) {
	h := newHarness(t, jamdetect.Options{})
	h.src.setRadiosErr(errors.New("iw missing"))
	h.det.Sample(context.Background())

	snap := &jamdetect.Snapshotter{Detector: h.det}
	snap.Refresh()

	data, ok := snap.Data().(*jamdetect.Snapshot)
	require.True(t, ok)
	assert.Equal(t, uint64(1), data.SurveyErrorsTotal)
	assert.Equal(t, uint32(0), data.RadiosMonitored)
}

func TestDetector_historyIsBounded(t *testing.T) {
	h := newHarness(t, jamdetect.Options{HistoryLen: 2})
	h.healthy(20)

	for range 3 {
		h.jammed(jamdetect.DefaultJamSamples)
		h.healthy(jamdetect.DefaultClearSamples)
	}

	assert.Len(t, h.det.Recent(), 2)
}

func TestDetector_subscribe(t *testing.T) {
	h := newHarness(t, jamdetect.Options{})
	h.healthy(20)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ch, err := h.det.Subscribe(ctx)
	require.NoError(t, err)

	h.jammed(jamdetect.DefaultJamSamples)
	h.healthy(jamdetect.DefaultClearSamples)

	want := []jamdetect.State{jamdetect.StateSuspected, jamdetect.StateJammed, jamdetect.StateClear}
	for _, st := range want {
		ev := <-ch
		assert.Equal(t, st, ev.State)
	}

	cancel()

	for range ch { //nolint:revive // drain until the detector closes the channel
	}
}

func TestDetector_subscriberLimit(t *testing.T) {
	h := newHarness(t, jamdetect.Options{})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	for range 8 {
		_, err := h.det.Subscribe(ctx)
		require.NoError(t, err)
	}

	_, err := h.det.Subscribe(ctx)
	require.ErrorIs(t, err, jamdetect.ErrTooManySubscribers)
}

func TestDetector_runStopsAndClosesSubscribers(t *testing.T) {
	h := newHarness(t, jamdetect.Options{Interval: time.Hour, GossipInterval: time.Hour, Gossip: &fakeGossip{}})
	h.src.step(radioIf, freq, -92, 5000, 1500, 1200, 200)

	ch, err := h.det.Subscribe(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)

		h.det.Run(ctx)
	}()

	cancel()
	<-done

	_, open := <-ch
	assert.False(t, open)
}

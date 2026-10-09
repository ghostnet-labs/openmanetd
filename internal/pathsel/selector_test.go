package pathsel_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/openmanet/openmanetd/internal/pathsel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const peer = "node-7"

// makeMetrics returns fresh, reachable metrics measured at now.
func makeMetrics(now time.Time, latMs, jitMs, lossPermille, capKbps int) pathsel.Metrics {
	return pathsel.Metrics{
		UpdatedAt:    now,
		Latency:      time.Duration(latMs) * time.Millisecond,
		Jitter:       time.Duration(jitMs) * time.Millisecond,
		LossPermille: lossPermille,
		CapacityKbps: capKbps,
		Reachable:    true,
	}
}

// harness bundles a selector with its fakes.
type harness struct {
	clock   *fakeClock
	metrics *fakeMetricsSource
	sel     *pathsel.Selector
}

func newSelector(t *testing.T, cfg pathsel.Config, opts ...pathsel.Option) *harness {
	t.Helper()

	h := &harness{clock: newFakeClock(), metrics: newFakeMetricsSource()}
	opts = append([]pathsel.Option{pathsel.WithClock(h.clock.Now)}, opts...)

	sel, err := pathsel.New(cfg, h.metrics, opts...)
	require.NoError(t, err)

	h.sel = sel

	return h
}

// typical sets the three bands' usual characteristics for peer:
// HaLow long-range but slow, 2.4 GHz middling, 5 GHz fast.
func (h *harness) typical() {
	now := h.clock.Now()
	h.metrics.set(peer, pathsel.BandHaLow, makeMetrics(now, 40, 10, 10, 1500))
	h.metrics.set(peer, pathsel.Band2G4, makeMetrics(now, 15, 5, 30, 20000))
	h.metrics.set(peer, pathsel.Band5G, makeMetrics(now, 5, 2, 20, 60000))
}

func (h *harness) selectOK(t *testing.T, class pathsel.Class) pathsel.Decision {
	t.Helper()

	d, err := h.sel.Select(peer, class)
	require.NoError(t, err)

	return d
}

func candidate(t *testing.T, d pathsel.Decision, b pathsel.Band) pathsel.Candidate {
	t.Helper()

	for _, c := range d.Candidates {
		if c.Band == b {
			return c
		}
	}

	require.Failf(t, "missing candidate", "band %s", b)

	return pathsel.Candidate{}
}

func TestSelect_classesPreferDifferentBands(t *testing.T) {
	h := newSelector(t, pathsel.Config{})
	h.typical()

	tests := []struct {
		class pathsel.Class
		want  pathsel.Band
	}{
		{class: pathsel.ClassControl, want: pathsel.BandHaLow},
		{class: pathsel.ClassVoice, want: pathsel.Band5G},
		{class: pathsel.ClassBulk, want: pathsel.Band5G},
	}

	for _, tc := range tests {
		t.Run(string(tc.class), func(t *testing.T) {
			d := h.selectOK(t, tc.class)
			assert.Equal(t, tc.want, d.Band, d.Explanation)
			assert.Equal(t, pathsel.ReasonInitial, d.Reason)
			assert.Empty(t, d.Duplicate, "no duplication by default")
			assert.Len(t, d.Candidates, 3)
			assert.True(t, candidate(t, d, tc.want).Selected)
		})
	}
}

func TestSelect_reachabilityAndIncompatibleEndpoint(t *testing.T) {
	h := newSelector(t, pathsel.Config{})
	h.typical()

	m := makeMetrics(h.clock.Now(), 5, 2, 20, 60000)
	m.Reachable = false
	h.metrics.set(peer, pathsel.Band5G, m)
	h.metrics.remove(peer, pathsel.BandHaLow) // peer has no HaLow radio

	d := h.selectOK(t, pathsel.ClassVoice)
	assert.Equal(t, pathsel.Band2G4, d.Band)
	assert.Equal(t, pathsel.IneligibleUnreach, candidate(t, d, pathsel.Band5G).Ineligible)
	assert.Equal(t, pathsel.IneligibleNoMetrics, candidate(t, d, pathsel.BandHaLow).Ineligible)
	assert.Contains(t, d.Explanation, "5 GHz ineligible: unreachable")
	assert.Contains(t, d.Explanation, "HaLow ineligible: no_metrics")
}

func TestSelect_classThresholds(t *testing.T) {
	h := newSelector(t, pathsel.Config{})
	now := h.clock.Now()
	// Only HaLow reachable, with latency voice cannot use.
	h.metrics.set(peer, pathsel.BandHaLow, makeMetrics(now, 300, 60, 10, 1500))

	control := h.selectOK(t, pathsel.ClassControl)
	assert.Equal(t, pathsel.BandHaLow, control.Band)

	voice := h.selectOK(t, pathsel.ClassVoice)
	assert.Empty(t, voice.Band)
	assert.Equal(t, pathsel.ReasonNoPath, voice.Reason)
	assert.Equal(t, pathsel.IneligibleLatency, candidate(t, voice, pathsel.BandHaLow).Ineligible)

	h.metrics.set(peer, pathsel.BandHaLow, makeMetrics(now, 300, 60, 10, 100))
	bulk := h.selectOK(t, pathsel.ClassBulk)
	assert.Equal(t, pathsel.IneligibleThroughput, candidate(t, bulk, pathsel.BandHaLow).Ineligible)

	h.metrics.set(peer, pathsel.BandHaLow, makeMetrics(now, 30, 60, 10, 1500))
	voice = h.selectOK(t, pathsel.ClassVoice)
	assert.Equal(t, pathsel.IneligibleJitter, candidate(t, voice, pathsel.BandHaLow).Ineligible)

	h.metrics.set(peer, pathsel.BandHaLow, makeMetrics(now, 30, 5, 400, 1500))
	control = h.selectOK(t, pathsel.ClassControl)
	assert.Equal(t, pathsel.IneligibleLoss, candidate(t, control, pathsel.BandHaLow).Ineligible)
}

func TestSelect_hysteresisPreventsOscillation(t *testing.T) {
	h := newSelector(t, pathsel.Config{MinDwell: 10 * time.Second, Margin: 10})
	h.typical()

	first := h.selectOK(t, pathsel.ClassVoice)
	require.Equal(t, pathsel.Band5G, first.Band)

	// 2.4 GHz and 5 GHz alternate being better by ~5 points (< margin)
	// every second for two minutes, well past dwell.
	switches := 0
	prev := first.Band

	for i := range 120 {
		h.clock.Advance(time.Second)
		now := h.clock.Now()

		if i%2 == 0 {
			h.metrics.set(peer, pathsel.Band5G, makeMetrics(now, 5, 2, 20, 60000))  // 84.3
			h.metrics.set(peer, pathsel.Band2G4, makeMetrics(now, 5, 2, 10, 20000)) // 89.3
		} else {
			h.metrics.set(peer, pathsel.Band5G, makeMetrics(now, 5, 2, 10, 60000))  // 89.3
			h.metrics.set(peer, pathsel.Band2G4, makeMetrics(now, 5, 2, 20, 20000)) // 84.3
		}

		d := h.selectOK(t, pathsel.ClassVoice)
		if d.Band != prev {
			switches++
			prev = d.Band
		}
	}

	assert.Zero(t, switches)

	last, ok := h.sel.Last(peer, pathsel.ClassVoice)
	require.True(t, ok)
	assert.Equal(t, pathsel.ReasonHoldMargin, last.Reason)
	assert.Equal(t, first.Since, last.Since, "since is unchanged while the path is kept")
}

func TestSelect_dwellThenMarginSwitch(t *testing.T) {
	h := newSelector(t, pathsel.Config{MinDwell: 10 * time.Second, Margin: 10})
	h.typical()
	require.Equal(t, pathsel.Band5G, h.selectOK(t, pathsel.ClassVoice).Band)

	// 5 GHz degrades but stays eligible; 2.4 GHz is now much better.
	degrade := func() {
		now := h.clock.Now()
		h.metrics.set(peer, pathsel.Band5G, makeMetrics(now, 60, 20, 40, 60000))
		h.metrics.set(peer, pathsel.Band2G4, makeMetrics(now, 5, 2, 0, 20000))
		h.metrics.set(peer, pathsel.BandHaLow, makeMetrics(now, 40, 10, 10, 1500))
	}

	h.clock.Advance(4 * time.Second)
	degrade()

	d := h.selectOK(t, pathsel.ClassVoice)
	assert.Equal(t, pathsel.Band5G, d.Band)
	assert.Equal(t, pathsel.ReasonDwell, d.Reason)
	assert.Contains(t, d.Explanation, "6s left")

	h.clock.Advance(6 * time.Second)
	degrade()

	d = h.selectOK(t, pathsel.ClassVoice)
	assert.Equal(t, pathsel.Band2G4, d.Band)
	assert.Equal(t, pathsel.ReasonBetter, d.Reason)
	assert.Equal(t, pathsel.Band5G, d.Previous)
	assert.Equal(t, h.clock.Now(), d.Since)
	assert.Contains(t, d.Explanation, "switched from 5 GHz")

	// Flipping straight back is held by the new dwell.
	h.clock.Advance(time.Second)
	h.typical()

	d = h.selectOK(t, pathsel.ClassVoice)
	assert.Equal(t, pathsel.Band2G4, d.Band)
	assert.Equal(t, pathsel.ReasonDwell, d.Reason)
}

func TestSelect_rapidFailoverIgnoresDwell(t *testing.T) {
	tests := []struct {
		name   string
		break5 func(h *harness)
		want   pathsel.Ineligibility
	}{
		{
			name: "unreachable",
			break5: func(h *harness) {
				m := makeMetrics(h.clock.Now(), 5, 2, 20, 60000)
				m.Reachable = false
				h.metrics.set(peer, pathsel.Band5G, m)
			},
			want: pathsel.IneligibleUnreach,
		},
		{
			name: "stale metrics",
			break5: func(h *harness) {
				h.metrics.set(peer, pathsel.Band5G, makeMetrics(h.clock.Now().Add(-6*time.Second), 5, 2, 20, 60000))
			},
			want: pathsel.IneligibleStale,
		},
		{
			name: "metrics gone",
			break5: func(h *harness) {
				h.metrics.remove(peer, pathsel.Band5G)
			},
			want: pathsel.IneligibleNoMetrics,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newSelector(t, pathsel.Config{MinDwell: time.Minute})
			h.typical()
			require.Equal(t, pathsel.Band5G, h.selectOK(t, pathsel.ClassVoice).Band)

			h.clock.Advance(time.Second)
			h.typical()
			tc.break5(h)

			d := h.selectOK(t, pathsel.ClassVoice)
			assert.Equal(t, pathsel.Band2G4, d.Band)
			assert.Equal(t, pathsel.ReasonFailover, d.Reason)
			assert.Equal(t, pathsel.Band5G, d.Previous)
			assert.Equal(t, tc.want, candidate(t, d, pathsel.Band5G).Ineligible)
			assert.Contains(t, d.Explanation, "failed over from 5 GHz")
			assert.Contains(t, d.Explanation, string(tc.want))
		})
	}
}

func TestSelect_capacityGate(t *testing.T) {
	gate := &fakeBandAdmitter{}
	gate.deny(pathsel.Band5G, "capacity_short")

	h := newSelector(t, pathsel.Config{}, pathsel.WithCapacity(gate))
	h.typical()

	d := h.selectOK(t, pathsel.ClassVoice)
	assert.Equal(t, pathsel.Band2G4, d.Band)

	c := candidate(t, d, pathsel.Band5G)
	assert.Equal(t, pathsel.IneligibleCapacity, c.Ineligible)
	assert.Equal(t, "capacity_short", c.Detail)
	assert.Contains(t, d.Explanation, "radio_capacity (capacity_short)")
	assert.Equal(t, 3, gate.getCalls())
}

func TestSelect_bulkYieldsToControlAndVoice(t *testing.T) {
	h := newSelector(t, pathsel.Config{})
	now := h.clock.Now()
	h.metrics.set(peer, pathsel.Band2G4, makeMetrics(now, 15, 5, 30, 1000)) // little headroom
	h.metrics.set(peer, pathsel.BandHaLow, makeMetrics(now, 40, 10, 10, 1500))

	voice := h.selectOK(t, pathsel.ClassVoice)
	require.Equal(t, pathsel.Band2G4, voice.Band)

	bulk := h.selectOK(t, pathsel.ClassBulk)
	assert.Equal(t, pathsel.BandHaLow, bulk.Band)

	c := candidate(t, bulk, pathsel.Band2G4)
	assert.Equal(t, pathsel.IneligibleProtected, c.Ineligible)
	assert.Equal(t, "carries voice", c.Detail)

	// With headroom, bulk may share the voice band.
	h.clock.Advance(time.Second)
	now = h.clock.Now()
	h.metrics.set(peer, pathsel.Band2G4, makeMetrics(now, 15, 5, 30, 20000))
	h.metrics.set(peer, pathsel.BandHaLow, makeMetrics(now, 40, 10, 10, 1500))
	h.sel.Forget(peer)
	require.Equal(t, pathsel.Band2G4, h.selectOK(t, pathsel.ClassVoice).Band)
	assert.Equal(t, pathsel.Band2G4, h.selectOK(t, pathsel.ClassBulk).Band)
}

func TestSelect_override(t *testing.T) {
	h := newSelector(t, pathsel.Config{MinDwell: time.Minute})
	h.typical()
	require.Equal(t, pathsel.Band5G, h.selectOK(t, pathsel.ClassVoice).Band)

	require.NoError(t, h.sel.SetOverride(peer, pathsel.ClassVoice, pathsel.Band2G4))

	d := h.selectOK(t, pathsel.ClassVoice)
	assert.Equal(t, pathsel.Band2G4, d.Band, "override bypasses dwell")
	assert.Equal(t, pathsel.ReasonOverride, d.Reason)
	assert.Equal(t, pathsel.Band2G4, d.Override)
	assert.Contains(t, d.Explanation, "manual override")

	// An override never sends traffic into a dead path.
	m := makeMetrics(h.clock.Now(), 15, 5, 30, 20000)
	m.Reachable = false
	h.metrics.set(peer, pathsel.Band2G4, m)

	d = h.selectOK(t, pathsel.ClassVoice)
	assert.Equal(t, pathsel.Band5G, d.Band)
	assert.Equal(t, pathsel.ReasonOverrideBlocked, d.Reason)
	assert.Contains(t, d.Explanation, "override to 2.4 GHz is ineligible")

	h.sel.ClearOverride(peer, pathsel.ClassVoice)
	h.typical()

	d = h.selectOK(t, pathsel.ClassVoice)
	assert.Empty(t, d.Override)
	assert.Equal(t, pathsel.Band5G, d.Band)

	require.ErrorIs(t, h.sel.SetOverride(peer, pathsel.ClassVoice, "60"), pathsel.ErrInvalidBand)
	require.ErrorIs(t, h.sel.SetOverride(peer, "video", pathsel.Band5G), pathsel.ErrInvalidClass)
}

func TestSelect_survivingBandCarriesEveryService(t *testing.T) {
	for _, survivor := range []pathsel.Band{pathsel.BandHaLow, pathsel.Band2G4, pathsel.Band5G} {
		t.Run(survivor.String(), func(t *testing.T) {
			h := newSelector(t, pathsel.Config{})
			h.typical()

			for _, b := range []pathsel.Band{pathsel.BandHaLow, pathsel.Band2G4, pathsel.Band5G} {
				if b != survivor {
					h.metrics.remove(peer, b)
				}
			}

			for _, class := range []pathsel.Class{pathsel.ClassControl, pathsel.ClassVoice, pathsel.ClassBulk} {
				d := h.selectOK(t, class)
				assert.Equal(t, survivor, d.Band, "%s: %s", class, d.Explanation)

				if class == pathsel.ClassBulk && survivor == pathsel.BandHaLow {
					assert.Contains(t, d.Explanation, "shared, no alternative")
				}
			}
		})
	}
}

func TestSelect_noEligiblePath(t *testing.T) {
	h := newSelector(t, pathsel.Config{})
	h.typical()
	require.Equal(t, pathsel.Band5G, h.selectOK(t, pathsel.ClassVoice).Band)

	for _, b := range []pathsel.Band{pathsel.BandHaLow, pathsel.Band2G4, pathsel.Band5G} {
		h.metrics.remove(peer, b)
	}

	d := h.selectOK(t, pathsel.ClassVoice)
	assert.Empty(t, d.Band)
	assert.Equal(t, pathsel.ReasonNoPath, d.Reason)
	assert.Equal(t, pathsel.Band5G, d.Previous)
	assert.Contains(t, d.Explanation, "no eligible path")
}

func TestSelect_voiceDuplicationIsOptIn(t *testing.T) {
	h := newSelector(t, pathsel.Config{DuplicateVoice: true})
	h.typical()

	voice := h.selectOK(t, pathsel.ClassVoice)
	assert.Equal(t, pathsel.Band5G, voice.Band)
	assert.Equal(t, pathsel.Band2G4, voice.Duplicate)
	assert.Contains(t, voice.Explanation, "duplicated on 2.4 GHz")

	control := h.selectOK(t, pathsel.ClassControl)
	assert.Empty(t, control.Duplicate, "only voice is ever duplicated")

	bulk := h.selectOK(t, pathsel.ClassBulk)
	assert.Empty(t, bulk.Duplicate)
}

func TestSelect_deterministic(t *testing.T) {
	run := func() []pathsel.Decision {
		h := newSelector(t, pathsel.Config{})
		out := make([]pathsel.Decision, 0, 30)

		for i := range 30 {
			now := h.clock.Now()
			h.metrics.set(peer, pathsel.BandHaLow, makeMetrics(now, 40+i, 10, 10, 1500))
			h.metrics.set(peer, pathsel.Band2G4, makeMetrics(now, 15, 5, 30-i, 20000))
			h.metrics.set(peer, pathsel.Band5G, makeMetrics(now, 5+2*i, 2, 20, 60000))

			for _, c := range []pathsel.Class{pathsel.ClassControl, pathsel.ClassVoice, pathsel.ClassBulk} {
				out = append(out, h.selectOK(t, c))
			}

			h.clock.Advance(3 * time.Second)
		}

		return out
	}

	assert.Equal(t, run(), run())
}

func TestSelect_tieBreakByClassPreference(t *testing.T) {
	h := newSelector(t, pathsel.Config{})
	now := h.clock.Now()

	for _, b := range []pathsel.Band{pathsel.BandHaLow, pathsel.Band2G4, pathsel.Band5G} {
		h.metrics.set(peer, b, makeMetrics(now, 0, 0, 0, 100000))
	}

	assert.Equal(t, pathsel.BandHaLow, h.selectOK(t, pathsel.ClassControl).Band)
	assert.Equal(t, pathsel.Band5G, h.selectOK(t, pathsel.ClassVoice).Band)
}

func TestSelect_invalidInput(t *testing.T) {
	h := newSelector(t, pathsel.Config{})

	_, err := h.sel.Select("", pathsel.ClassVoice)
	require.ErrorIs(t, err, pathsel.ErrInvalidDest)

	_, err = h.sel.Select(peer, "video")
	require.ErrorIs(t, err, pathsel.ErrInvalidClass)

	_, err = pathsel.New(pathsel.Config{}, nil)
	require.ErrorIs(t, err, pathsel.ErrInvalidConf)

	for _, cfg := range []pathsel.Config{
		{MinDwell: -time.Second},
		{StaleAfter: -time.Second},
		{Margin: -1},
		{Margin: 101},
		{MaxPaths: -1},
		{Bands: make([]pathsel.Band, 9)},
	} {
		_, err = pathsel.New(cfg, newFakeMetricsSource())
		assert.ErrorIs(t, err, pathsel.ErrInvalidConf, "%+v", cfg)
	}
}

func TestSelect_customProfileAndBands(t *testing.T) {
	h := newSelector(t, pathsel.Config{
		Bands:    []pathsel.Band{pathsel.Band2G4, pathsel.Band5G},
		Profiles: map[pathsel.Class]pathsel.Profile{pathsel.ClassVoice: {MaxLatency: time.Millisecond}},
	})
	h.typical()

	d := h.selectOK(t, pathsel.ClassVoice)
	assert.Len(t, d.Candidates, 2, "HaLow is not a candidate")
	assert.Empty(t, d.Band, "1 ms latency limit excludes both")

	require.ErrorIs(t, h.sel.SetOverride(peer, pathsel.ClassVoice, pathsel.BandHaLow), pathsel.ErrInvalidBand)
}

func TestSelector_boundedState(t *testing.T) {
	h := newSelector(t, pathsel.Config{MaxPaths: 2})
	now := h.clock.Now()

	for _, dest := range []string{"a", "b", "c"} {
		h.metrics.set(dest, pathsel.Band5G, makeMetrics(now, 5, 2, 0, 60000))
	}

	require.NoError(t, h.sel.SetOverride("a", pathsel.ClassVoice, pathsel.Band5G))

	_, err := h.sel.Select("a", pathsel.ClassVoice)
	require.NoError(t, err)

	h.clock.Advance(time.Second)
	_, err = h.sel.Select("b", pathsel.ClassVoice)
	require.NoError(t, err)

	h.clock.Advance(time.Second)
	_, err = h.sel.Select("c", pathsel.ClassVoice)
	require.NoError(t, err)

	_, ok := h.sel.Last("a", pathsel.ClassVoice)
	assert.True(t, ok, "entries with an override are never evicted")

	_, ok = h.sel.Last("b", pathsel.ClassVoice)
	assert.False(t, ok, "oldest override-free entry is evicted")

	_, ok = h.sel.Last("c", pathsel.ClassVoice)
	assert.True(t, ok)
}

func TestSelector_lastAndForget(t *testing.T) {
	h := newSelector(t, pathsel.Config{})
	h.typical()

	_, ok := h.sel.Last(peer, pathsel.ClassVoice)
	assert.False(t, ok)

	d := h.selectOK(t, pathsel.ClassVoice)

	last, ok := h.sel.Last(peer, pathsel.ClassVoice)
	require.True(t, ok)
	assert.Equal(t, d, last)

	last.Candidates[0].Score = -1
	again, _ := h.sel.Last(peer, pathsel.ClassVoice)
	assert.NotEqual(t, -1.0, again.Candidates[0].Score, "returned records are copies")

	h.sel.Forget(peer)

	_, ok = h.sel.Last(peer, pathsel.ClassVoice)
	assert.False(t, ok)
}

func TestDecision_JSON(t *testing.T) {
	h := newSelector(t, pathsel.Config{})
	h.typical()

	raw, err := json.Marshal(h.selectOK(t, pathsel.ClassControl))
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, "halow", got["band"])
	assert.Equal(t, "control", got["class"])
	assert.Equal(t, "initial", got["reason"])
	assert.NotEmpty(t, got["explanation"])
	assert.Len(t, got["candidates"], 3)
}

func TestSelector_concurrentUse(t *testing.T) {
	h := newSelector(t, pathsel.Config{})
	h.typical()

	var wg sync.WaitGroup

	t.Cleanup(wg.Wait)

	for i := range 8 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			class := []pathsel.Class{pathsel.ClassControl, pathsel.ClassVoice, pathsel.ClassBulk}[i%3]
			for range 50 {
				_, _ = h.sel.Select(peer, class)
				_ = h.sel.SetOverride(peer, class, pathsel.Band2G4)
				h.sel.ClearOverride(peer, class)
				_, _ = h.sel.Last(peer, class)
			}
		}()
	}

	wg.Wait()
	assert.Positive(t, h.metrics.getCalls())
}

func TestClass_Priority(t *testing.T) {
	assert.Greater(t, pathsel.ClassControl.Priority(), pathsel.ClassVoice.Priority())
	assert.Greater(t, pathsel.ClassVoice.Priority(), pathsel.ClassBulk.Priority())
	assert.Zero(t, pathsel.Class("video").Priority())
	assert.Equal(t, "none", pathsel.Band("").String())
	assert.Equal(t, "60", pathsel.Band("60").String())
}

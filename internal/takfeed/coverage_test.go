package takfeed_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coreywagehoft/go-tak/pkg/cot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/coverage"
	"github.com/openmanet/openmanetd/internal/takfeed"
)

func TestCoverageEvent_shapeAndGrades(t *testing.T) {
	k := coverage.KeyFor(selfLat, selfLon, 50)
	south, west, north, east := coverage.Bounds(k, 50)

	tests := []struct {
		name      string
		cell      coverage.Cell
		wantFill  string
		wantWords []string
		notWords  []string
	}{
		{
			name:      "good",
			cell:      coverage.Cell{Samples: 3, SignalDBm: -62, MaxLinks: 2},
			wantFill:  "-2147418368",
			wantWords: []string{"good. Best signal -62 dBm, up to 2 mesh stations heard. 3 readings"},
			notWords:  []string{"no link"},
		},
		{
			name:      "fair because of an occasional dropout",
			cell:      coverage.Cell{Samples: 4, NoLinkSamples: 1, SignalDBm: -60, MaxLinks: 1},
			wantFill:  "-2130706688",
			wantWords: []string{"fair. Best signal -60 dBm, up to 1 mesh station heard, no link 25% of the time. 4 readings"},
		},
		{
			name:      "fair signal",
			cell:      coverage.Cell{Samples: 1, SignalDBm: -78, MaxLinks: 1},
			wantFill:  "-2130706688",
			wantWords: []string{"fair."},
		},
		{
			name:      "weak",
			cell:      coverage.Cell{Samples: 1, SignalDBm: -90, MaxLinks: 1},
			wantFill:  "-2130771968",
			wantWords: []string{"weak."},
		},
		{
			name:      "dead spot",
			cell:      coverage.Cell{Samples: 2, NoLinkSamples: 2},
			wantFill:  "-2130771968",
			wantWords: []string{"no mesh link. 2 readings"},
			notWords:  []string{"Best signal"},
		},
		{
			name:      "mostly dead",
			cell:      coverage.Cell{Samples: 4, NoLinkSamples: 2, SignalDBm: -55, MaxLinks: 1},
			wantFill:  "-2130771968",
			wantWords: []string{"no mesh link. Best signal -55 dBm"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.cell
			c.Key = k
			c.LastSeen = testNow

			ev := takfeed.CoverageEvent(&c, 50, "manet01", testNow, time.Minute).GetCotEvent()

			assert.Equal(t, takfeed.TypeCoverage, ev.GetType())
			assert.Equal(t, takfeed.CoverageUID("manet01", k), ev.GetUid())
			assert.InDelta(t, (south+north)/2, ev.GetLat(), 1e-9)
			assert.InDelta(t, (west+east)/2, ev.GetLon(), 1e-9)
			assert.Equal(t, cot.TimeToMillis(testNow.Add(30*time.Minute)), ev.GetStaleTime(), "stale is at least 30 minutes")

			detail := ev.GetDetail().GetXmlDetail()
			assert.Equal(t, 4, strings.Count(detail, "<link point="))
			assert.Contains(t, detail, `<fillColor value="`+tc.wantFill+`"/>`)
			assert.Contains(t, detail, `callsign="Coverage manet01"`)

			text := remarksText(t, detail)
			assert.True(t, strings.HasPrefix(text, "Coverage here from manet01: "), text)
			assert.Contains(t, text, "last 2026-11-07 01:30Z.")

			for _, w := range tc.wantWords {
				assert.Contains(t, text, w)
			}

			for _, w := range tc.notWords {
				assert.NotContains(t, text, w)
			}
		})
	}
}

func TestCoverageUID(t *testing.T) {
	assert.Equal(t, "manet01-cov-89548_-196207", takfeed.CoverageUID("manet01", coverage.Key{Row: 89548, Col: -196207}))
}

func coverageHarness(t *testing.T, yaml string) (*harness, *coverage.Grid, *fakeSignals) {
	t.Helper()

	h := newHarness(t, yaml)
	grid := coverage.New(50, 64)
	sig := &fakeSignals{signals: map[string]takfeed.StationSignal{"bb": {SignalDBm: -71}, "cc": {SignalDBm: -64}}}
	h.opts.Coverage = grid
	h.opts.Signals = sig

	return h, grid, sig
}

func TestPublishOnce_recordsAndDrawsCoverage(t *testing.T) {
	h, grid, _ := coverageHarness(t, "")
	p := takfeed.New(h.opts)

	p.PublishOnce(context.Background())

	cells := grid.Cells(nil)
	require.Len(t, cells, 1)
	assert.Equal(t, coverage.KeyFor(selfLat, selfLon, 50), cells[0].Key)
	assert.Equal(t, 2, cells[0].MaxLinks)
	assert.InDelta(t, -64, cells[0].SignalDBm, 1e-9, "the best station counts")

	drawn := byType(h.sender.events(t), takfeed.TypeCoverage)
	require.Len(t, drawn, 1)
	assert.Equal(t, takfeed.CoverageUID("manet01-MANET", cells[0].Key), drawn[0].GetUid())
}

func TestPublishOnce_coverageSamplesOnlyOnLinkTicks(t *testing.T) {
	h, grid, _ := coverageHarness(t, "")
	p := takfeed.New(h.opts)

	for range 3 {
		p.PublishOnce(context.Background())
	}

	assert.Equal(t, 1, grid.Cells(nil)[0].Samples)

	p.PublishOnce(context.Background())
	assert.Equal(t, 2, grid.Cells(nil)[0].Samples)
}

func TestPublishOnce_noStationsIsADeadSpot(t *testing.T) {
	h, grid, sig := coverageHarness(t, "")
	sig.signals = map[string]takfeed.StationSignal{}
	p := takfeed.New(h.opts)

	p.PublishOnce(context.Background())

	cells := grid.Cells(nil)
	require.Len(t, cells, 1)
	assert.False(t, cells[0].HasLink())
}

func TestPublishOnce_coverageSkips(t *testing.T) {
	tests := []struct {
		name  string
		yaml  string
		setup func(h *harness, sig *fakeSignals)
	}{
		{name: "failed station read", setup: func(_ *harness, sig *fakeSignals) { sig.signals = nil }},
		{name: "coarse fix", setup: func(h *harness, _ *fakeSignals) {
			h.pos.set(takfeed.Position{Time: testNow, Lat: selfLat, Lon: selfLon, CE: 80}, true)
		}},
		{name: "no fix", setup: func(h *harness, _ *fakeSignals) { h.pos.set(takfeed.Position{}, false) }},
		{name: "disabled", yaml: "tak:\n  coverage:\n    enable: false\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, grid, sig := coverageHarness(t, tc.yaml)
			if tc.setup != nil {
				tc.setup(h, sig)
			}

			takfeed.New(h.opts).PublishOnce(context.Background())

			assert.Zero(t, grid.Len())
			assert.Empty(t, byType(h.sender.events(t), takfeed.TypeCoverage))
		})
	}
}

func TestPublishOnce_coverageBoundedPerTickWithCycleStale(t *testing.T) {
	h, grid, _ := coverageHarness(t, "")
	h.opts.Signals = nil

	for i := range 20 {
		grid.Record(testNow, selfLat+float64(i)*0.01, selfLon, coverage.Sample{Links: 1, BestSignalDBm: -60})
	}

	p := takfeed.New(h.opts)
	p.PublishOnce(context.Background())

	drawn := byType(h.sender.events(t), takfeed.TypeCoverage)
	require.Len(t, drawn, 8, "at most 8 cells per tick")

	// 20 cells at 8 per tick is a 3-tick cycle; at 10 s that is 30 s, so
	// the 30-minute floor applies.
	assert.Equal(t, cot.TimeToMillis(testNow.Add(30*time.Minute)), drawn[0].GetStaleTime())

	h.sender.reset()
	p.PublishOnce(context.Background())
	p.PublishOnce(context.Background())

	seen := map[string]bool{}
	for _, e := range drawn {
		seen[e.GetUid()] = true
	}

	for _, e := range byType(h.sender.events(t), takfeed.TypeCoverage) {
		seen[e.GetUid()] = true
	}

	assert.Len(t, seen, 20, "every cell goes out within one cycle")
}

func TestPublishOnce_largeMapStaleCoversTheCycle(t *testing.T) {
	h, _, _ := coverageHarness(t, "")
	h.opts.Signals = nil
	big := coverage.New(50, 1024)
	h.opts.Coverage = big

	for i := range 1000 {
		big.Record(testNow, selfLat+float64(i)*0.001, selfLon, coverage.Sample{})
	}

	takfeed.New(h.opts).PublishOnce(context.Background())

	drawn := byType(h.sender.events(t), takfeed.TypeCoverage)
	require.NotEmpty(t, drawn)
	// 1000 cells / 8 = 125 ticks * 10 s = 1250 s per cycle; stale is two
	// cycles.
	assert.Equal(t, cot.TimeToMillis(testNow.Add(2500*time.Second)), drawn[0].GetStaleTime())
}

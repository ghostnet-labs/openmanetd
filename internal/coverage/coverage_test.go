package coverage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/coverage"
	"github.com/openmanet/openmanetd/internal/database/models"
)

func t0() time.Time { return time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC) }

// Eagle Mountain, Utah.
const (
	lat = 40.3141
	lon = -112.0100
)

func TestKeyForAndBounds_cellHoldsThePoint(t *testing.T) {
	for _, p := range [][2]float64{{lat, lon}, {-33.86, 151.21}, {0.00001, -0.00001}, {89.99, 179.99}, {-0.0001, 0.0001}} {
		k := coverage.KeyFor(p[0], p[1], 50)
		south, west, north, east := coverage.Bounds(k, 50)

		assert.True(t, p[0] >= south && p[0] < north, "lat %v in [%v,%v)", p[0], south, north)
		assert.True(t, p[1] >= west && p[1] < east, "lon %v in [%v,%v)", p[1], west, east)
	}
}

func TestKeyFor_cellsAreAboutSquare(t *testing.T) {
	south, west, north, east := coverage.Bounds(coverage.KeyFor(lat, lon, 50), 50)

	heightM := (north - south) * 111_320
	widthM := (east - west) * 111_320 * 0.763 // cos(40.31 deg)

	assert.InDelta(t, 50, heightM, 0.5)
	assert.InDelta(t, 50, widthM, 1)
}

func TestKeyFor_neighborsDiffer(t *testing.T) {
	a := coverage.KeyFor(lat, lon, 50)
	b := coverage.KeyFor(lat+0.001, lon, 50) // ~111 m north
	c := coverage.KeyFor(lat, lon+0.001, 50) // ~85 m east

	assert.NotEqual(t, a, b)
	assert.NotEqual(t, a, c)
	assert.Equal(t, a, coverage.KeyFor(lat+0.00001, lon+0.00001, 50), "1 m away stays in the cell, or on its edge")
}

func TestRecord_averagesSignalAndCountsDeadSpots(t *testing.T) {
	g := coverage.New(50, 16)

	k := g.Record(t0(), lat, lon, coverage.Sample{Links: 2, BestSignalDBm: -60})
	g.Record(t0().Add(time.Minute), lat, lon, coverage.Sample{Links: 1, BestSignalDBm: -80})
	g.Record(t0().Add(2*time.Minute), lat, lon, coverage.Sample{})

	cells := g.Cells(nil)
	require.Len(t, cells, 1)

	c := cells[0]
	assert.Equal(t, k, c.Key)
	assert.Equal(t, 3, c.Samples)
	assert.Equal(t, 1, c.NoLinkSamples)
	assert.Equal(t, 2, c.MaxLinks)
	assert.InDelta(t, -66, c.SignalDBm, 1e-9, "first sample seeds, second moves 30% of the way")
	assert.InDelta(t, 33.33, c.NoLinkPct(), 0.01)
	assert.True(t, c.HasLink())
	assert.Equal(t, t0(), c.FirstSeen)
	assert.Equal(t, t0().Add(2*time.Minute), c.LastSeen)
}

func TestRecord_deadCellFirstThenLink(t *testing.T) {
	g := coverage.New(50, 16)

	g.Record(t0(), lat, lon, coverage.Sample{})
	c := g.Cells(nil)[0]
	assert.False(t, c.HasLink())
	assert.InDelta(t, 100, c.NoLinkPct(), 1e-9)

	g.Record(t0().Add(time.Minute), lat, lon, coverage.Sample{Links: 1, BestSignalDBm: -75})
	c = g.Cells(nil)[0]
	assert.InDelta(t, -75, c.SignalDBm, 1e-9, "the first linked sample seeds the average even after dead samples")
}

func TestCell_emptyNoLinkPct(t *testing.T) {
	var c coverage.Cell
	assert.Zero(t, c.NoLinkPct())
}

func TestRecord_evictsLeastRecentlyVisited(t *testing.T) {
	g := coverage.New(50, 3)

	for i := range 3 {
		g.Record(t0().Add(time.Duration(i)*time.Minute), lat+float64(i)*0.01, lon, coverage.Sample{Links: 1, BestSignalDBm: -60})
	}

	// Revisit the first so the second becomes the oldest.
	first := g.Record(t0().Add(10*time.Minute), lat, lon, coverage.Sample{Links: 1, BestSignalDBm: -60})
	newest := g.Record(t0().Add(11*time.Minute), lat+0.05, lon, coverage.Sample{Links: 1, BestSignalDBm: -60})

	assert.Equal(t, 3, g.Len())

	keys := map[coverage.Key]bool{}
	for _, c := range g.Cells(nil) {
		keys[c.Key] = true
	}

	assert.True(t, keys[first])
	assert.True(t, keys[newest])
	assert.False(t, keys[coverage.KeyFor(lat+0.01, lon, 50)], "the least recently visited cell is dropped")
}

func TestNextToPublish_changedFirstThenRoundRobin(t *testing.T) {
	g := coverage.New(50, 16)

	var keys []coverage.Key
	for i := range 5 {
		keys = append(keys, g.Record(t0(), lat+float64(i)*0.01, lon, coverage.Sample{Links: 1, BestSignalDBm: -60}))
	}

	got := g.NextToPublish(nil, 3)
	assert.Equal(t, keys[:3], cellKeys(got), "new cells go out first, in order")

	got = g.NextToPublish(nil, 3)
	assert.Equal(t, []coverage.Key{keys[3], keys[4], keys[0]}, cellKeys(got), "then the round robin fills the slots")

	// A cell that changes jumps the queue.
	g.Record(t0().Add(time.Minute), lat+0.04, lon, coverage.Sample{})
	got = g.NextToPublish(nil, 2)
	assert.Equal(t, keys[4], got[0].Key)
	assert.Equal(t, 2, got[0].Samples)
	assert.Len(t, got, 2)
	assert.NotEqual(t, got[0].Key, got[1].Key, "no cell twice in one batch")

	// Asking for more than exist returns each once.
	assert.Len(t, g.NextToPublish(nil, 50), 5)
	assert.Empty(t, coverage.New(50, 4).NextToPublish(nil, 4))
	assert.Empty(t, g.NextToPublish(nil, 0))
}

func TestNextToPublish_evictedChangedCellIsSkipped(t *testing.T) {
	g := coverage.New(50, 1)

	g.Record(t0(), lat, lon, coverage.Sample{})
	k := g.Record(t0().Add(time.Minute), lat+0.01, lon, coverage.Sample{})

	got := g.NextToPublish(nil, 4)
	assert.Equal(t, []coverage.Key{k}, cellKeys(got))
}

func cellKeys(cells []coverage.Cell) []coverage.Key {
	out := make([]coverage.Key, 0, len(cells))
	for i := range cells {
		out = append(out, cells[i].Key)
	}

	return out
}

func TestRecorder_flushLoadRoundTrip(t *testing.T) {
	store := newFakeStore()
	g := coverage.New(50, 16)
	rec := &coverage.Recorder{Grid: g, Store: store, Log: zerolog.Nop()}
	ctx := context.Background()

	g.Record(t0(), lat, lon, coverage.Sample{Links: 2, BestSignalDBm: -64})
	g.Record(t0().Add(time.Minute), lat+0.01, lon, coverage.Sample{})

	require.NoError(t, rec.Flush(ctx))
	assert.Equal(t, 2, store.count())
	assert.Equal(t, 2, store.getUpserts())

	require.NoError(t, rec.Flush(ctx))
	assert.Equal(t, 2, store.getUpserts(), "nothing changed, nothing written")

	// A cell of an old size is dropped on load.
	require.NoError(t, store.UpsertCoverageCell(ctx, models.UpsertCoverageCellParams{CellM: 100, LastSeen: t0()}))

	g2 := coverage.New(50, 16)
	require.NoError(t, (&coverage.Recorder{Grid: g2, Store: store}).Load(ctx))

	assert.Equal(t, g.Cells(nil), g2.Cells(nil))
	assert.Equal(t, 2, store.count())
	assert.Len(t, g2.NextToPublish(nil, 8), 2, "loaded cells are published")
}

func TestRecorder_evictionDeletesRow(t *testing.T) {
	store := newFakeStore()
	g := coverage.New(50, 1)
	rec := &coverage.Recorder{Grid: g, Store: store}
	ctx := context.Background()

	g.Record(t0(), lat, lon, coverage.Sample{})
	require.NoError(t, rec.Flush(ctx))

	g.Record(t0().Add(time.Minute), lat+0.01, lon, coverage.Sample{})
	require.NoError(t, rec.Flush(ctx))

	assert.Equal(t, 1, store.count())
}

func TestRecorder_failedWriteIsRetried(t *testing.T) {
	store := newFakeStore()
	g := coverage.New(50, 16)
	rec := &coverage.Recorder{Grid: g, Store: store}
	ctx := context.Background()

	g.Record(t0(), lat, lon, coverage.Sample{})
	store.setUpsertErr(errors.New("disk full"))
	require.Error(t, rec.Flush(ctx))

	store.setUpsertErr(nil)
	require.NoError(t, rec.Flush(ctx))
	assert.Equal(t, 1, store.count())
}

func TestRecorder_failedDeleteIsRetried(t *testing.T) {
	store := newFakeStore()
	g := coverage.New(50, 1)
	rec := &coverage.Recorder{Grid: g, Store: store}
	ctx := context.Background()

	g.Record(t0(), lat, lon, coverage.Sample{})
	require.NoError(t, rec.Flush(ctx))
	g.Record(t0(), lat+0.01, lon, coverage.Sample{})

	store.mu.Lock()
	store.deleteErr = errors.New("locked")
	store.mu.Unlock()
	require.Error(t, rec.Flush(ctx))
	assert.Equal(t, 2, store.count())

	store.mu.Lock()
	store.deleteErr = nil
	store.mu.Unlock()
	require.NoError(t, rec.Flush(ctx))
	assert.Equal(t, 1, store.count())
}

func TestRecorder_loadErrors(t *testing.T) {
	store := newFakeStore()
	store.dropErr = errors.New("x")
	require.Error(t, (&coverage.Recorder{Grid: coverage.New(50, 4), Store: store}).Load(context.Background()))

	store = newFakeStore()
	store.listErr = errors.New("x")
	require.Error(t, (&coverage.Recorder{Grid: coverage.New(50, 4), Store: store}).Load(context.Background()))
}

func TestRecorder_loadCapsAtMax(t *testing.T) {
	store := newFakeStore()
	ctx := context.Background()

	for i := range 5 {
		require.NoError(t, store.UpsertCoverageCell(ctx, models.UpsertCoverageCellParams{
			CellM: 50, CellRow: int64(i), LastSeen: t0().Add(time.Duration(i) * time.Minute),
		}))
	}

	g := coverage.New(50, 2)
	require.NoError(t, (&coverage.Recorder{Grid: g, Store: store}).Load(ctx))

	cells := g.Cells(nil)
	require.Len(t, cells, 2)
	assert.Equal(t, int32(4), cells[0].Key.Row, "newest cells are kept")
}

func TestRecorder_runFlushesOnShutdown(t *testing.T) {
	store := newFakeStore()
	g := coverage.New(50, 16)
	rec := &coverage.Recorder{Grid: g, Store: store, Interval: time.Hour}

	g.Record(t0(), lat, lon, coverage.Sample{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)

		rec.Run(ctx)
	}()

	cancel()
	<-done

	assert.Equal(t, 1, store.count())
}

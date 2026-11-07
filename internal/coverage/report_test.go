package coverage_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	coveragev1 "github.com/openmanet/openmanetd/internal/api/openmanet/coverage/v1"
	"github.com/openmanet/openmanetd/internal/coverage"
)

func TestCell_grade(t *testing.T) {
	tests := []struct {
		name string
		cell coverage.Cell
		want coveragev1.CoverageGrade
	}{
		{"good", coverage.Cell{Samples: 2, SignalDBm: -70}, coveragev1.CoverageGrade_COVERAGE_GRADE_GOOD},
		{"good signal with a dropout is fair", coverage.Cell{Samples: 4, NoLinkSamples: 1, SignalDBm: -60}, coveragev1.CoverageGrade_COVERAGE_GRADE_FAIR},
		{"fair", coverage.Cell{Samples: 1, SignalDBm: -82}, coveragev1.CoverageGrade_COVERAGE_GRADE_FAIR},
		{"weak", coverage.Cell{Samples: 1, SignalDBm: -83}, coveragev1.CoverageGrade_COVERAGE_GRADE_WEAK},
		{"no link", coverage.Cell{Samples: 3, NoLinkSamples: 3}, coveragev1.CoverageGrade_COVERAGE_GRADE_NO_LINK},
		{"half dead", coverage.Cell{Samples: 2, NoLinkSamples: 1, SignalDBm: -50}, coveragev1.CoverageGrade_COVERAGE_GRADE_NO_LINK},
		{"empty", coverage.Cell{}, coveragev1.CoverageGrade_COVERAGE_GRADE_NO_LINK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.cell.Grade())
		})
	}
}

func TestGrid_report(t *testing.T) {
	g := coverage.New(50, 16)
	older := g.Record(t0(), lat, lon, coverage.Sample{Links: 2, BestSignalDBm: -64})
	newer := g.Record(t0().Add(time.Minute), lat+0.01, lon, coverage.Sample{})

	rep := g.Report("AA:BB:CC:00:00:01", "manet01", t0().Add(2*time.Minute), 0)

	assert.Equal(t, "aa:bb:cc:00:00:01", rep.GetPrimaryMac())
	assert.Equal(t, "manet01", rep.GetHostname())
	assert.Equal(t, uint32(50), rep.GetCellMeters())
	assert.True(t, rep.GetCollectedAt().AsTime().Equal(t0().Add(2*time.Minute)))
	require.Len(t, rep.GetCells(), 2)

	dead := rep.GetCells()[0]
	assert.Equal(t, newer.Row, dead.GetRow())
	assert.Equal(t, coveragev1.CoverageGrade_COVERAGE_GRADE_NO_LINK, dead.GetGrade())
	assert.Zero(t, dead.GetSignalDbm(), "no signal when nothing was heard")
	assert.Equal(t, uint32(100), dead.GetNoLinkPct())

	good := rep.GetCells()[1]
	assert.Equal(t, older.Col, good.GetCol())
	assert.Equal(t, coveragev1.CoverageGrade_COVERAGE_GRADE_GOOD, good.GetGrade())
	assert.Equal(t, int32(-64), good.GetSignalDbm())
	assert.Equal(t, uint32(2), good.GetMaxLinks())
	assert.Equal(t, uint32(1), good.GetSamples())

	assert.Len(t, g.Report("", "", t0(), 1).GetCells(), 1, "limit keeps the newest")
}

func TestGrid_reportFitsInOneAlfredRecord(t *testing.T) {
	g := coverage.New(50, coverage.MaxGossipCells+100)
	for i := range coverage.MaxGossipCells + 100 {
		g.Record(t0().Add(time.Duration(i)*time.Second), lat+float64(i)*0.001, lon-float64(i)*0.001,
			coverage.Sample{Links: 9, BestSignalDBm: -99})
	}

	payload, err := g.Report("aa:bb:cc:dd:ee:ff", "a-long-hostname-for-good-measure", t0(), coverage.MaxGossipCells).MarshalVT()
	require.NoError(t, err)
	assert.Less(t, len(payload), 60_000, "alfred's payload limit is 64 KiB")
}

// fakeGossip is an in-memory alfred data type.
type fakeGossip struct {
	mu         sync.Mutex
	published  [][]byte
	records    []coverage.GossipRecord
	publishErr error
	fetchErr   error
}

func (f *fakeGossip) Publish(payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.published = append(f.published, payload)

	return f.publishErr
}

func (f *fakeGossip) Fetch() ([]coverage.GossipRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.records, f.fetchErr
}

func (f *fakeGossip) set(recs []coverage.GossipRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.records = recs
}

func (f *fakeGossip) publishCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.published)
}

func remoteRecord(t *testing.T, mac, host string, at time.Time) coverage.GossipRecord {
	t.Helper()

	data, err := (&coveragev1.CoverageReport{
		PrimaryMac:  mac,
		Hostname:    host,
		CollectedAt: timestamppb.New(at),
		CellMeters:  50,
		Cells:       []*coveragev1.CoverageCell{{Row: 1, Col: 2, Grade: coveragev1.CoverageGrade_COVERAGE_GRADE_GOOD}},
	}).MarshalVT()
	require.NoError(t, err)

	return coverage.GossipRecord{Data: data}
}

func TestSharer_publishesAndCollectsRemoteMaps(t *testing.T) {
	now := t0()
	g := coverage.New(50, 16)
	g.Record(now, lat, lon, coverage.Sample{Links: 1, BestSignalDBm: -60})

	gossip := &fakeGossip{}
	src := net.HardwareAddr{2, 0, 0, 0, 0, 3}
	gossip.set([]coverage.GossipRecord{
		remoteRecord(t, "02:00:00:00:00:02", "manet02", now),
		remoteRecord(t, "02:00:00:00:00:01", "manet01", now), // ourselves
		{Source: src, Data: mustMarshal(t, &coveragev1.CoverageReport{Hostname: "anon", CollectedAt: timestamppb.New(now)})},
		{Data: []byte{0xff, 0xff, 0xff}},
	})

	s := &coverage.Sharer{
		Grid:     g,
		Gossip:   gossip,
		Identity: func() (string, string) { return "02:00:00:00:00:01", "manet01" },
		Now:      func() time.Time { return now },
	}

	s.ShareOnce()

	require.Equal(t, 1, gossip.publishCount())

	var mine coveragev1.CoverageReport

	require.NoError(t, mine.UnmarshalVT(gossip.published[0]))
	assert.Equal(t, "manet01", mine.GetHostname())
	assert.Len(t, mine.GetCells(), 1)

	remote := s.Remote()
	require.Len(t, remote, 2, "own map and garbage are skipped")
	assert.Equal(t, "anon", remote[0].GetHostname())
	assert.Equal(t, "02:00:00:00:00:03", remote[0].GetPrimaryMac(), "falls back to the alfred source MAC")
	assert.Equal(t, "manet02", remote[1].GetHostname())

	remote[1].Hostname = "mutated"

	assert.Equal(t, "manet02", s.Remote()[1].GetHostname(), "Remote returns copies")

	local := s.Local()
	assert.Equal(t, "manet01", local.GetHostname())
	assert.Len(t, local.GetCells(), 1)
}

func mustMarshal(t *testing.T, r *coveragev1.CoverageReport) []byte {
	t.Helper()

	b, err := r.MarshalVT()
	require.NoError(t, err)

	return b
}

func TestSharer_expiresSilentNodes(t *testing.T) {
	now := t0()
	gossip := &fakeGossip{}
	s := &coverage.Sharer{Grid: coverage.New(50, 4), Gossip: gossip, Now: func() time.Time { return now }}

	gossip.set([]coverage.GossipRecord{remoteRecord(t, "02:00:00:00:00:02", "manet02", t0())})
	s.ShareOnce()
	require.Len(t, s.Remote(), 1)

	// Same collected_at for longer than the expiry: the node went quiet
	// and alfred is still serving its last record.
	now = t0().Add(coverage.DefaultShareExpiry + time.Second)

	s.ShareOnce()
	assert.Empty(t, s.Remote())

	// A fresh map brings it back.
	gossip.set([]coverage.GossipRecord{remoteRecord(t, "02:00:00:00:00:02", "manet02", now)})
	s.ShareOnce()
	assert.Len(t, s.Remote(), 1)

	// An updated map keeps it alive past the old expiry.
	later := now.Add(10 * time.Minute)
	gossip.set([]coverage.GossipRecord{remoteRecord(t, "02:00:00:00:00:02", "manet02", later)})
	now = later

	s.ShareOnce()

	now = later.Add(10 * time.Minute)

	s.ShareOnce()
	assert.Len(t, s.Remote(), 1)
}

func TestSharer_disabledStillFetches(t *testing.T) {
	gossip := &fakeGossip{}
	gossip.set([]coverage.GossipRecord{remoteRecord(t, "02:00:00:00:00:02", "manet02", t0())})
	s := &coverage.Sharer{Grid: coverage.New(50, 4), Gossip: gossip, Enabled: func() bool { return false }}

	s.ShareOnce()

	assert.Zero(t, gossip.publishCount())
	assert.Len(t, s.Remote(), 1)
}

func TestSharer_errorsAndNoGossip(t *testing.T) {
	gossip := &fakeGossip{publishErr: errors.New("x"), fetchErr: errors.New("y")}
	s := &coverage.Sharer{Grid: coverage.New(50, 4), Gossip: gossip}

	assert.NotPanics(t, s.ShareOnce)
	assert.Empty(t, s.Remote())

	none := &coverage.Sharer{Grid: coverage.New(50, 4)}
	assert.NotPanics(t, none.ShareOnce)
	assert.Empty(t, none.Remote())
	assert.Empty(t, none.Local().GetPrimaryMac())
}

func TestSharer_runSharesAtStartAndStops(t *testing.T) {
	gossip := &fakeGossip{}
	s := &coverage.Sharer{Grid: coverage.New(50, 4), Gossip: gossip, Interval: time.Hour}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)

		s.Run(ctx)
	}()

	cancel()
	<-done

	assert.Equal(t, 1, gossip.publishCount())
}

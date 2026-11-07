package coverage

import (
	"context"
	"math"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	coveragev1 "github.com/openmanet/openmanetd/internal/api/openmanet/coverage/v1"
)

// Grade thresholds (dBm of the best heard station). The ATAK heatmap and
// the web app color cells by these.
const (
	GoodDBm = -70
	FairDBm = -82
	// DeadPct is the no-link share at which a cell counts as no link.
	DeadPct = 50
)

// Gossip defaults.
const (
	DefaultShareInterval = 5 * time.Minute
	// DefaultShareExpiry drops a node's map once it stops updating for
	// three share intervals.
	DefaultShareExpiry = 15 * time.Minute
	// MaxGossipCells keeps one alfred record well under alfred's 64 KiB
	// payload limit (about 32 bytes a cell).
	MaxGossipCells = 1024
	// maxRemote bounds the remote maps kept.
	maxRemote = 256
)

// Grade rates how well the node heard the mesh in the cell.
func (c *Cell) Grade() coveragev1.CoverageGrade {
	switch {
	case !c.HasLink() || c.NoLinkPct() >= DeadPct:
		return coveragev1.CoverageGrade_COVERAGE_GRADE_NO_LINK
	case c.SignalDBm >= GoodDBm && c.NoLinkSamples == 0:
		return coveragev1.CoverageGrade_COVERAGE_GRADE_GOOD
	case c.SignalDBm >= FairDBm:
		return coveragev1.CoverageGrade_COVERAGE_GRADE_FAIR
	default:
		return coveragev1.CoverageGrade_COVERAGE_GRADE_WEAK
	}
}

// Report builds this node's coverage report from up to limit cells,
// newest first.
func (g *Grid) Report(primaryMAC, hostname string, now time.Time, limit int) *coveragev1.CoverageReport {
	cells := g.Cells(make([]Cell, 0, g.Len()))
	if limit > 0 && len(cells) > limit {
		cells = cells[:limit]
	}

	rep := &coveragev1.CoverageReport{
		PrimaryMac:  strings.ToLower(primaryMAC),
		Hostname:    hostname,
		CollectedAt: timestamppb.New(now),
		CellMeters:  uint32(g.cellM),
		Cells:       make([]*coveragev1.CoverageCell, 0, len(cells)),
	}

	for i := range cells {
		c := &cells[i]
		pc := &coveragev1.CoverageCell{
			Row:       c.Key.Row,
			Col:       c.Key.Col,
			Grade:     c.Grade(),
			Samples:   uint32(min(c.Samples, math.MaxUint32)), //nolint:gosec // clamped
			NoLinkPct: uint32(math.Round(c.NoLinkPct())),
			MaxLinks:  uint32(max(c.MaxLinks, 0)), //nolint:gosec // non-negative
			LastSeen:  timestamppb.New(c.LastSeen),
		}

		if c.HasLink() {
			pc.SignalDbm = int32(math.Round(c.SignalDBm))
		}

		rep.Cells = append(rep.Cells, pc)
	}

	return rep
}

// GossipRecord is one alfred record: the publishing node's MAC and its
// payload.
type GossipRecord struct {
	Source net.HardwareAddr
	Data   []byte
}

// Gossip is the alfred surface the Sharer needs, bound to the coverage
// data type. The daemon adapts *alfred.Client to it.
type Gossip interface {
	Publish(payload []byte) error
	Fetch() ([]GossipRecord, error)
}

// Sharer publishes this node's map to the mesh and keeps the latest map
// of every other node. Without Gossip it still serves the local map.
type Sharer struct {
	Log    zerolog.Logger
	Gossip Gossip
	Grid   *Grid
	// Identity returns this node's batman MAC and hostname.
	Identity func() (primaryMAC, hostname string)
	// Enabled gates publishing; nil means always on. Remote maps are
	// still fetched while it is off, so the app keeps showing the mesh.
	Enabled func() bool
	Now     func() time.Time
	// remote is guarded by mu.
	remote map[string]*remoteMap
	// Interval and Expiry default to DefaultShareInterval and
	// DefaultShareExpiry.
	Interval time.Duration
	Expiry   time.Duration

	mu sync.Mutex // protects remote
}

// remoteMap is the latest map heard from one node. changed is when its
// collected_at last changed, on this node's clock.
type remoteMap struct {
	changed   time.Time
	report    *coveragev1.CoverageReport
	collected int64
}

func (s *Sharer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}

	return time.Now()
}

func (s *Sharer) identity() (primaryMAC, hostname string) {
	if s.Identity == nil {
		return "", ""
	}

	mac, host := s.Identity()

	return strings.ToLower(mac), host
}

// Local returns this node's map.
func (s *Sharer) Local() *coveragev1.CoverageReport {
	mac, host := s.identity()

	return s.Grid.Report(mac, host, s.now(), 0)
}

// Remote returns the latest map of every other node, sorted by hostname
// then MAC. The reports are copies.
func (s *Sharer) Remote() []*coveragev1.CoverageReport {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]*coveragev1.CoverageReport, 0, len(s.remote))
	for _, r := range s.remote {
		cp, _ := proto.Clone(r.report).(*coveragev1.CoverageReport) //nolint:errcheck // Clone keeps the type
		out = append(out, cp)
	}

	slices.SortFunc(out, func(a, b *coveragev1.CoverageReport) int {
		if c := strings.Compare(a.GetHostname(), b.GetHostname()); c != 0 {
			return c
		}

		return strings.Compare(a.GetPrimaryMac(), b.GetPrimaryMac())
	})

	return out
}

// ShareOnce publishes this node's map and refreshes the remote ones.
// Errors are logged; the next tick retries.
func (s *Sharer) ShareOnce() {
	if s.Gossip == nil {
		return
	}

	mac, host := s.identity()

	if s.Enabled == nil || s.Enabled() {
		payload, err := s.Grid.Report(mac, host, s.now(), MaxGossipCells).MarshalVT()
		if err == nil {
			err = s.Gossip.Publish(payload)
		}

		if err != nil {
			s.Log.Warn().Err(err).Msg("Coverage map: publish failed")
		}
	}

	recs, err := s.Gossip.Fetch()
	if err != nil {
		s.Log.Warn().Err(err).Msg("Coverage map: fetch failed")

		return
	}

	s.ingest(mac, recs)
}

// ingest decodes fetched maps, skips this node's own and expires maps
// that stopped changing.
func (s *Sharer) ingest(self string, recs []GossipRecord) {
	now := s.now()
	expiry := s.Expiry

	if expiry <= 0 {
		expiry = DefaultShareExpiry
	}

	decoded := make([]*coveragev1.CoverageReport, 0, len(recs))

	for _, r := range recs {
		rep := &coveragev1.CoverageReport{}
		if err := rep.UnmarshalVT(r.Data); err != nil {
			continue
		}

		rep.PrimaryMac = strings.ToLower(rep.GetPrimaryMac())
		if rep.GetPrimaryMac() == "" && len(r.Source) > 0 {
			rep.PrimaryMac = strings.ToLower(r.Source.String())
		}

		if rep.GetPrimaryMac() == "" || (self != "" && rep.GetPrimaryMac() == self) {
			continue
		}

		decoded = append(decoded, rep)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.remote == nil {
		s.remote = make(map[string]*remoteMap, len(decoded))
	}

	for _, rep := range decoded {
		key := rep.GetPrimaryMac()
		collected := rep.GetCollectedAt().AsTime().UnixNano()

		cur, ok := s.remote[key]
		if !ok {
			if len(s.remote) >= maxRemote {
				continue
			}

			s.remote[key] = &remoteMap{report: rep, collected: collected, changed: now}

			continue
		}

		if collected != cur.collected {
			cur.changed = now
			cur.collected = collected
		}

		cur.report = rep
	}

	for key, r := range s.remote {
		if now.Sub(r.changed) > expiry {
			delete(s.remote, key)
		}
	}
}

// Run shares once straight away, then every Interval until ctx ends.
func (s *Sharer) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultShareInterval
	}

	s.ShareOnce()

	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.ShareOnce()
		}
	}
}

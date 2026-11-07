package openmanet

import (
	"context"
	"fmt"
	"sync"

	"github.com/openmanet/go-alfred"
	"github.com/rs/zerolog"

	netv1 "github.com/openmanet/openmanetd/internal/api/openmanet/network/v1"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/coverage"
	"github.com/openmanet/openmanetd/internal/database/models"
	"github.com/openmanet/openmanetd/internal/mgmt"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/openmanet/openmanetd/internal/takfeed"
)

// alfred record identity for coverage maps. The data type comes from
// openmanet.network.v1.DataType (DATA_TYPE_COVERAGE = 106).
const (
	coverageAlfredDataType    = uint8(netv1.DataType_DATA_TYPE_COVERAGE)
	coverageAlfredDataVersion = uint8(1)
)

// coverageGossip binds an alfred client to the coverage data type.
type coverageGossip struct {
	client alfred.ReadWriteClient
}

func (g coverageGossip) Publish(payload []byte) error {
	if err := g.client.Set(coverageAlfredDataType, coverageAlfredDataVersion, payload); err != nil {
		return fmt.Errorf("alfred set: %w", err)
	}

	return nil
}

// Fetch returns every coverage map alfred holds, skipping other record
// versions so a mixed-firmware mesh degrades to "node not heard".
func (g coverageGossip) Fetch() ([]coverage.GossipRecord, error) {
	recs, err := g.client.Request(coverageAlfredDataType)
	if err != nil {
		return nil, fmt.Errorf("alfred request: %w", err)
	}

	out := make([]coverage.GossipRecord, 0, len(recs))

	for _, r := range recs {
		if r.Version != coverageAlfredDataVersion {
			continue
		}

		out = append(out, coverage.GossipRecord{Source: r.Source, Data: r.Data})
	}

	return out, nil
}

// coverageMap is this node's coverage grid with its persistence and mesh
// sharing. A nil *coverageMap (no database) is valid: every method is a
// no-op.
type coverageMap struct {
	rec    *coverage.Recorder
	sharer *coverage.Sharer
}

// newCoverageMap builds the grid from the database. It is always built,
// so turning tak.coverage.enable on later needs no restart; the cell size
// and cap are read once here. Without alfred the map still records and
// shows in ATAK and the app; it just isn't shared.
func newCoverageMap(ctx context.Context, cfg *config.Config, store coverage.Store, client alfred.ReadWriteClient, log zerolog.Logger) *coverageMap {
	if store == nil {
		return nil
	}

	tc := cfg.GetTAK()
	grid := coverage.New(float64(tc.CoverageCellM), tc.CoverageMaxCells)
	rec := &coverage.Recorder{Grid: grid, Store: store, Log: log}

	if err := rec.Load(ctx); err != nil {
		log.Warn().Err(err).Msg("Coverage map: could not load saved cells, starting empty")
	}

	batIface := cfg.GetAlfredBatInterface()
	sharer := &coverage.Sharer{
		Grid:     grid,
		Log:      log,
		Identity: func() (string, string) { return nodeIdentity(batIface) },
		Enabled:  func() bool { return cfg.GetTAK().CoverageEnable },
	}

	if client != nil {
		sharer.Gossip = coverageGossip{client: client}
	}

	return &coverageMap{rec: rec, sharer: sharer}
}

// coverageStore returns the database as a true nil interface when the
// daemon runs without one.
func coverageStore(db *models.Queries) coverage.Store {
	if db == nil {
		return nil
	}

	return db
}

// alfredClient returns the manager's alfred client as a true nil
// interface when alfred is disabled.
func alfredClient(manager *mgmt.ManagementConfig) alfred.ReadWriteClient {
	if manager == nil || manager.Client() == nil {
		return nil
	}

	return manager.Client()
}

// attach hands the grid to the ATAK feed.
func (m *coverageMap) attach(opts *takfeed.Options) {
	if m == nil {
		return
	}

	opts.Coverage = m.rec.Grid
}

// start runs persistence and sharing until ctx ends; wg tracks both.
func (m *coverageMap) start(ctx context.Context, wg *sync.WaitGroup) {
	if m == nil {
		return
	}

	wg.Go(func() { m.rec.Run(ctx) })
	wg.Go(func() { m.sharer.Run(ctx) })
}

// api returns the map for CoverageService, as a nil interface when there
// is none.
func (m *coverageMap) api() handlers.CoverageMap {
	if m == nil {
		return nil
	}

	return m.sharer
}

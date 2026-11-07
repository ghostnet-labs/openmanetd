package openmanet

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/openmanet/go-alfred"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/coverage"
	"github.com/openmanet/openmanetd/internal/database/models"
	"github.com/openmanet/openmanetd/internal/takfeed"
)

func TestCoverageGossip(t *testing.T) {
	src := net.HardwareAddr{2, 0, 0, 0, 0, 9}
	client := &fakeAlfredClient{records: []alfred.Record{
		{Source: src, Version: coverageAlfredDataVersion, Data: []byte("map")},
		{Source: src, Version: coverageAlfredDataVersion + 1, Data: []byte("future format")},
	}}
	g := coverageGossip{client: client}

	require.NoError(t, g.Publish([]byte("mine")))
	assert.Equal(t, []fakeAlfredSet{{dataType: 106, version: coverageAlfredDataVersion, payload: []byte("mine")}}, client.setCalls())

	recs, err := g.Fetch()
	require.NoError(t, err)
	assert.Equal(t, []coverage.GossipRecord{{Source: src, Data: []byte("map")}}, recs)
	assert.Equal(t, []uint8{106}, client.requestCalls())

	client.setErr = errFakeAlfred
	require.ErrorIs(t, g.Publish([]byte("x")), errFakeAlfred)

	client.requestErr = errFakeAlfred
	_, err = g.Fetch()
	require.ErrorIs(t, err, errFakeAlfred)
}

func TestNewCoverageMap_loadsSavedCells(t *testing.T) {
	v := viper.New()
	v.Set("tak.coverage.cellMeters", 25)

	store := &fakeCoverageStore{rows: []models.CoverageCell{{CellM: 25, CellRow: 7, CellCol: 9, Samples: 3, LastSeen: time.Now()}}}

	m := newCoverageMap(context.Background(), config.NewWithoutWatch(v), store, &fakeAlfredClient{}, zerolog.Nop())
	require.NotNil(t, m)

	var opts takfeed.Options

	m.attach(&opts)
	require.NotNil(t, opts.Coverage)
	assert.InDelta(t, 25, opts.Coverage.CellMeters(), 1e-9)
	assert.Equal(t, 1, opts.Coverage.Len())
	assert.NotNil(t, m.api())
	assert.NotNil(t, m.sharer.Gossip)
}

func TestNewCoverageMap_loadFailureStartsEmpty(t *testing.T) {
	store := &fakeCoverageStore{listErr: errors.New("corrupt")}

	m := newCoverageMap(context.Background(), config.NewWithoutWatch(viper.New()), store, nil, zerolog.Nop())
	require.NotNil(t, m, "a bad database must not cost the live map")
	assert.Zero(t, m.rec.Grid.Len())
	assert.Nil(t, m.sharer.Gossip, "no alfred, no gossip")
}

func TestCoverageMap_enabledFollowsConfig(t *testing.T) {
	v := viper.New()
	v.Set("tak.coverage.enable", false)

	m := newCoverageMap(context.Background(), config.NewWithoutWatch(v), &fakeCoverageStore{}, nil, zerolog.Nop())
	assert.False(t, m.sharer.Enabled())
}

func TestCoverageMap_nilIsANoOp(t *testing.T) {
	var m *coverageMap

	assert.Nil(t, newCoverageMap(context.Background(), config.NewWithoutWatch(viper.New()), nil, nil, zerolog.Nop()))

	var opts takfeed.Options

	m.attach(&opts)
	assert.Nil(t, opts.Coverage)
	assert.Nil(t, m.api())

	var wg sync.WaitGroup

	m.start(context.Background(), &wg)
	wg.Wait()
}

func TestCoverageMap_startStopsWithContext(t *testing.T) {
	m := newCoverageMap(context.Background(), config.NewWithoutWatch(viper.New()), &fakeCoverageStore{}, &fakeAlfredClient{}, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup

	m.start(ctx, &wg)
	cancel()
	wg.Wait()
}

func TestAlfredClient_nilManager(t *testing.T) {
	assert.Nil(t, alfredClient(nil))
}

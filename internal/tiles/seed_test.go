package tiles_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/tiles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func waitSeed(t *testing.T, c *tiles.Cache) tiles.SeedStatus {
	t.Helper()

	var st tiles.SeedStatus

	require.Eventually(t, func() bool {
		s := c.Status().Seed
		if s == nil || s.Running {
			return false
		}

		st = *s

		return true
	}, 10*time.Second, 10*time.Millisecond)

	return st
}

// eagleMountain is a small area: 1 km around the town to zoom 12.
var eagleMountain = config.TileSeedArea{Name: "Eagle Mountain", Lat: 40.314, Lon: -112.006, RadiusKm: 1, MaxZoom: 12} //nolint:gochecknoglobals // test fixture

func TestStartSeed_downloadsAreaThenSkipsCached(t *testing.T) {
	up, ts := newFakeTileSource(t)
	c, _ := newCache(t, cacheOpts{upstream: ts.URL})

	require.NoError(t, c.StartSeed(context.Background(), eagleMountain))
	st := waitSeed(t, c)

	assert.Empty(t, st.Error)
	assert.Positive(t, st.Total)
	assert.Equal(t, st.Total, st.Done)
	assert.Equal(t, st.Total, st.Downloaded)
	assert.Equal(t, int(st.Total), up.Calls())
	assert.Equal(t, []string{"Eagle Mountain"}, st.Areas)

	require.NoError(t, c.StartSeed(context.Background(), eagleMountain))
	st = waitSeed(t, c)

	assert.Equal(t, st.Total, st.Cached, "a repeat run only checks the cache")
	assert.Equal(t, int(st.Total), up.Calls(), "nothing is downloaded twice")

	// Every seeded tile is now served offline.
	up.set(http.StatusBadGateway, nil)
	assert.Equal(t, http.StatusOK, get(t, c, "/tiles/satellite/12/773/1545").Code)
}

func TestStartSeed_rejectsBadOrHugeAreas(t *testing.T) {
	_, ts := newFakeTileSource(t)
	c, _ := newCache(t, cacheOpts{upstream: ts.URL, maxBytes: 8 << 20})

	cases := map[string]struct {
		area config.TileSeedArea
		want error
	}{
		"unknown layer": {config.TileSeedArea{Name: "x", Lat: 40, Lon: -112, RadiusKm: 1, Layers: []string{"roads"}}, tiles.ErrSeedBadArea},
		"bad latitude":  {config.TileSeedArea{Name: "x", Lat: 95, Lon: -112, RadiusKm: 1}, tiles.ErrSeedBadArea},
		"zero radius":   {config.TileSeedArea{Name: "x", Lat: 40, Lon: -112}, tiles.ErrSeedBadArea},
		"too big":       {config.TileSeedArea{Name: "x", Lat: 40, Lon: -112, RadiusKm: 100}, tiles.ErrSeedTooLarge},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, c.StartSeed(context.Background(), tc.area), tc.want)
		})
	}

	assert.Nil(t, c.Status().Seed, "a rejected area starts nothing")
}

func TestStartSeed_oneJobAtATimeAndCancel(t *testing.T) {
	block := make(chan struct{})

	t.Cleanup(func() { close(block) })

	ts := slowSource(t, block)
	c, _ := newCache(t, cacheOpts{upstream: ts})

	require.NoError(t, c.StartSeed(context.Background(), eagleMountain))
	require.ErrorIs(t, c.StartSeed(context.Background(), eagleMountain), tiles.ErrSeedRunning)

	c.CancelSeed()

	st := waitSeed(t, c)
	assert.Equal(t, "canceled", st.Error)
	assert.Less(t, st.Done, st.Total)
}

func TestStartSeed_givesUpWhenSourcesAreDown(t *testing.T) {
	up, ts := newFakeTileSource(t)
	up.set(http.StatusBadGateway, nil)

	c, _ := newCache(t, cacheOpts{upstream: ts.URL})

	area := eagleMountain
	area.RadiusKm = 20
	area.MaxZoom = 14

	require.NoError(t, c.StartSeed(context.Background(), area))
	st := waitSeed(t, c)

	assert.Contains(t, st.Error, "failed in a row")
	assert.Less(t, st.Done, st.Total, "the job stops early instead of failing every tile")
	assert.LessOrEqual(t, up.Calls(), 2, "the backoff keeps it from hammering the source")
}

func TestStart_seedsConfiguredAreas(t *testing.T) {
	up, ts := newFakeTileSource(t)

	c, err := tiles.New(tiles.Options{
		FreeFunc: func(string) (int64, error) { return plenty, nil },
		Config: config.TilesConfig{
			Dir:             t.TempDir(),
			MaxBytes:        64 << 20,
			SeedConcurrency: 1,
			Layers:          []config.TileLayer{{Layer: "topo", Upstream: ts.URL + "/{z}/{y}/{x}", TileType: "jpg", MaxZoom: 10}},
			Seed:            []config.TileSeedArea{{Name: "mast", Lat: 40.3, Lon: -112, RadiusKm: 2}},
		},
	})
	require.NoError(t, err)

	startCache(t, c)
	st := waitSeed(t, c)

	assert.Equal(t, "config", st.Source)
	assert.Equal(t, st.Total, st.Downloaded)
	assert.Equal(t, int(st.Total), up.Calls())
}

func TestStart_stopsSeedOnShutdown(t *testing.T) {
	block := make(chan struct{})

	t.Cleanup(func() { close(block) })

	c, _ := newCache(t, cacheOpts{upstream: slowSource(t, block)})

	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)

	require.NoError(t, c.StartSeed(context.Background(), eagleMountain))
	cancel()

	st := waitSeed(t, c)
	assert.Equal(t, "canceled", st.Error)

	require.Eventually(t, func() bool {
		return c.StartSeed(context.Background(), eagleMountain) != nil
	}, 5*time.Second, 10*time.Millisecond, "a stopped cache refuses new jobs")
}

// slowSource returns an upstream URL whose requests wait until block is
// closed or the request is canceled.
func slowSource(t *testing.T, block <-chan struct{}) string {
	t.Helper()

	f, ts := newFakeTileSource(t)
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
			return
		}

		f.handler(w, r)
	})

	return ts.URL
}

package tiles_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/tiles"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const plenty = int64(1 << 40)

type cacheOpts struct {
	upstream string
	peers    []string
	maxBytes int64
	free     func(string) (int64, error)
	clock    *fakeClock
	maxZoom  int
}

func newCache(t *testing.T, o cacheOpts) (*tiles.Cache, string) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "tiles")

	if o.maxBytes == 0 {
		o.maxBytes = 64 << 20
	}

	if o.free == nil {
		o.free = func(string) (int64, error) { return plenty, nil }
	}

	if o.clock == nil {
		o.clock = newFakeClock()
	}

	if o.maxZoom == 0 {
		o.maxZoom = 16
	}

	up := ""
	if o.upstream != "" {
		up = o.upstream + "/tile/{z}/{y}/{x}"
	}

	c, err := tiles.New(tiles.Options{
		Log:      zerolog.Nop(),
		Now:      o.clock.Now,
		FreeFunc: o.free,
		Config: config.TilesConfig{
			Enable:          true,
			Dir:             dir,
			MaxBytes:        o.maxBytes,
			MinFreeBytes:    1 << 20,
			FetchTimeout:    5 * time.Second,
			OfflineBackoff:  30 * time.Second,
			SeedConcurrency: 2,
			Peers:           o.peers,
			Layers: []config.TileLayer{
				{Layer: "satellite", Upstream: up, TileType: "jpg", MinZoom: 0, MaxZoom: o.maxZoom},
			},
		},
	})
	require.NoError(t, err)

	return c, dir
}

func startCache(t *testing.T, c *tiles.Cache) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c.Start(ctx)
}

func get(t *testing.T, h http.Handler, path string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

func TestServeHTTP_missFetchesUpstreamThenServesFromDisk(t *testing.T) {
	up, ts := newFakeTileSource(t)
	c, dir := newCache(t, cacheOpts{upstream: ts.URL})

	rec := get(t, c, "/tiles/satellite/3/1/2")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
	assert.Equal(t, []string{"/tile/3/2/1"}, up.Paths(), "upstream URL is z/y/x")

	assert.FileExists(t, filepath.Join(dir, "satellite", "3", "1", "2.jpg"))

	rec = get(t, c, "/tiles/satellite/3/1/2.jpg")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, up.Calls(), "second request is served from disk")
	assert.NotEmpty(t, rec.Header().Get("Cache-Control"))

	st := c.Status()
	assert.Equal(t, int64(1), st.Counters.Hits)
	assert.Equal(t, int64(1), st.Counters.Misses)
	assert.Equal(t, int64(1), st.Counters.FromInternet)
	assert.Equal(t, tiles.HealthOK, st.Internet.Health)
}

func TestServeHTTP_rejectsBadRequests(t *testing.T) {
	up, ts := newFakeTileSource(t)
	c, _ := newCache(t, cacheOpts{upstream: ts.URL, maxZoom: 5})

	cases := map[string]string{
		"unknown layer":   "/tiles/roads/1/0/0",
		"zoom too high":   "/tiles/satellite/6/0/0",
		"x off the grid":  "/tiles/satellite/2/4/0",
		"y off the grid":  "/tiles/satellite/2/0/4",
		"negative":        "/tiles/satellite/2/-1/0",
		"not a number":    "/tiles/satellite/a/0/0",
		"too few parts":   "/tiles/satellite/2/0",
		"too many parts":  "/tiles/satellite/2/0/0/1",
		"path traversal":  "/tiles/../2/0/0",
		"no layer at all": "/tiles/",
	}

	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, http.StatusNotFound, get(t, c, path).Code)
		})
	}

	req := httptest.NewRequest(http.MethodPost, "/tiles/satellite/1/0/0", nil)
	rec := httptest.NewRecorder()
	c.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

	assert.Zero(t, up.Calls(), "bad requests never reach upstream")
}

func TestServeHTTP_peerBeforeInternet(t *testing.T) {
	up, upTS := newFakeTileSource(t)
	peer, peerTS := newFakeTileSource(t)
	c, _ := newCache(t, cacheOpts{upstream: upTS.URL, peers: []string{peerTS.URL}})

	require.Equal(t, http.StatusOK, get(t, c, "/tiles/satellite/4/3/5").Code)

	assert.Equal(t, []string{"/tiles/satellite/4/3/5"}, peer.Paths())
	assert.Equal(t, []string{"1"}, peer.PeerHeaders(), "peer requests are marked")
	assert.Zero(t, up.Calls(), "internet is not used when a peer has the tile")
	assert.Equal(t, int64(1), c.Status().Counters.FromPeer)
}

func TestServeHTTP_peerMissFallsBackToInternet(t *testing.T) {
	up, upTS := newFakeTileSource(t)
	peer, peerTS := newFakeTileSource(t)
	peer.set(http.StatusNotFound, []byte("no"))

	c, _ := newCache(t, cacheOpts{upstream: upTS.URL, peers: []string{peerTS.URL}})

	require.Equal(t, http.StatusOK, get(t, c, "/tiles/satellite/4/3/5").Code)
	assert.Equal(t, 1, peer.Calls())
	assert.Equal(t, 1, up.Calls())
	assert.Equal(t, tiles.HealthOK, c.Status().Peers[0].Health, "a peer without the tile is still healthy")
}

func TestServeHTTP_peerRequestDoesNotAskPeers(t *testing.T) {
	up, upTS := newFakeTileSource(t)
	peer, peerTS := newFakeTileSource(t)
	c, _ := newCache(t, cacheOpts{upstream: upTS.URL, peers: []string{peerTS.URL}})

	rec := get(t, c, "/tiles/satellite/4/3/5", tiles.PeerHeader, "1")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Zero(t, peer.Calls(), "a request from a peer never fans out to peers (no loops)")
	assert.Equal(t, 1, up.Calls())
}

func TestServeHTTP_offlineBackoff(t *testing.T) {
	up, ts := newFakeTileSource(t)
	up.set(http.StatusBadGateway, []byte("down"))

	clock := newFakeClock()
	c, _ := newCache(t, cacheOpts{upstream: ts.URL, clock: clock})

	assert.Equal(t, http.StatusNotFound, get(t, c, "/tiles/satellite/2/1/1").Code)
	assert.Equal(t, http.StatusNotFound, get(t, c, "/tiles/satellite/2/1/2").Code)
	assert.Equal(t, 1, up.Calls(), "a failed source is skipped during its backoff")
	assert.Equal(t, tiles.HealthOffline, c.Status().Internet.Health)

	up.set(http.StatusOK, nil)
	clock.Advance(31 * time.Second)

	assert.Equal(t, http.StatusOK, get(t, c, "/tiles/satellite/2/1/2").Code)
	assert.Equal(t, 2, up.Calls())
	assert.Equal(t, tiles.HealthOK, c.Status().Internet.Health)
}

func TestServeHTTP_missingTileDoesNotBackOff(t *testing.T) {
	up, ts := newFakeTileSource(t)
	up.set(http.StatusNotFound, []byte("no tile"))

	c, _ := newCache(t, cacheOpts{upstream: ts.URL})

	assert.Equal(t, http.StatusNotFound, get(t, c, "/tiles/satellite/2/1/1").Code)
	assert.Equal(t, http.StatusNotFound, get(t, c, "/tiles/satellite/2/1/2").Code)
	assert.Equal(t, 2, up.Calls())
	assert.Equal(t, tiles.HealthOK, c.Status().Internet.Health)
}

func TestServeHTTP_neverCachesNonImages(t *testing.T) {
	up, ts := newFakeTileSource(t)
	up.set(http.StatusOK, []byte("<html>maintenance</html>"))

	c, dir := newCache(t, cacheOpts{upstream: ts.URL})

	assert.Equal(t, http.StatusNotFound, get(t, c, "/tiles/satellite/2/1/1").Code)
	assert.NoFileExists(t, filepath.Join(dir, "satellite", "2", "1", "1.jpg"))
}

func TestServeHTTP_lowDiskServesWithoutCaching(t *testing.T) {
	_, ts := newFakeTileSource(t)
	c, dir := newCache(t, cacheOpts{
		upstream: ts.URL,
		free:     func(string) (int64, error) { return 512 << 10, nil }, // below the 1 MB floor
	})

	rec := get(t, c, "/tiles/satellite/2/1/1")
	require.Equal(t, http.StatusOK, rec.Code, "the phone still gets the tile")
	assert.NoFileExists(t, filepath.Join(dir, "satellite", "2", "1", "1.jpg"))
	assert.Equal(t, int64(1), c.Status().Counters.NotCached)
}

func TestServeHTTP_offlineWithNoSourcesServesCacheOnly(t *testing.T) {
	c, dir := newCache(t, cacheOpts{})

	tile := filepath.Join(dir, "satellite", "5", "6", "7.jpg")
	require.NoError(t, os.MkdirAll(filepath.Dir(tile), 0o755))
	require.NoError(t, os.WriteFile(tile, jpegTile(64), 0o600))

	assert.Equal(t, http.StatusOK, get(t, c, "/tiles/satellite/5/6/7").Code)
	assert.Equal(t, http.StatusNotFound, get(t, c, "/tiles/satellite/5/6/8").Code)
}

func TestServeHTTP_concurrentMissesAllSucceed(t *testing.T) {
	up, ts := newFakeTileSource(t)
	c, _ := newCache(t, cacheOpts{upstream: ts.URL})

	const n = 8

	var wg sync.WaitGroup

	codes := make([]int, n)

	for i := range n {
		wg.Add(1)

		go func() {
			defer wg.Done()

			codes[i] = get(t, c, "/tiles/satellite/6/10/20").Code
		}()
	}

	wg.Wait()

	for _, code := range codes {
		assert.Equal(t, http.StatusOK, code)
	}

	assert.LessOrEqual(t, up.Calls(), n)
	assert.GreaterOrEqual(t, up.Calls(), 1)
}

func TestEviction_keepsCacheUnderMax(t *testing.T) {
	up, ts := newFakeTileSource(t)
	up.size = 10 << 10

	clock := newFakeClock()
	c, dir := newCache(t, cacheOpts{upstream: ts.URL, maxBytes: 100 << 10, clock: clock})
	startCache(t, c)

	for i := range 30 {
		clock.Advance(time.Minute)
		require.Equal(t, http.StatusOK, get(t, c, "/tiles/satellite/8/1/"+strconv.Itoa(i)).Code)

		// Give each tile a distinct mtime, oldest first.
		p := filepath.Join(dir, "satellite", "8", "1", strconv.Itoa(i)+".jpg")
		if _, err := os.Stat(p); err == nil {
			mt := time.Unix(1_700_000_000+int64(i)*60, 0)
			require.NoError(t, os.Chtimes(p, mt, mt))
		}
	}

	require.Eventually(t, func() bool {
		st := c.Status()

		return st.Measured && st.Used <= 100<<10
	}, 5*time.Second, 10*time.Millisecond)

	assert.FileExists(t, filepath.Join(dir, "satellite", "8", "1", "29.jpg"), "the newest tile stays")
	assert.NoFileExists(t, filepath.Join(dir, "satellite", "8", "1", "0.jpg"), "the oldest tile goes first")
	assert.Positive(t, c.Status().Evicted)
}

func TestStart_removesCrashLeftovers(t *testing.T) {
	clock := newFakeClock()
	c, dir := newCache(t, cacheOpts{clock: clock})

	stale := filepath.Join(dir, "satellite", "1", "0", ".tmp-123")
	require.NoError(t, os.MkdirAll(filepath.Dir(stale), 0o755))
	require.NoError(t, os.WriteFile(stale, []byte("partial"), 0o600))

	fresh := filepath.Join(dir, "satellite", "1", "0", ".tmp-456")
	require.NoError(t, os.WriteFile(fresh, []byte("in flight"), 0o600))
	require.NoError(t, os.Chtimes(fresh, clock.Now(), clock.Now()))

	old := clock.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(stale, old, old))

	startCache(t, c)

	require.Eventually(t, func() bool {
		_, err := os.Stat(stale)

		return os.IsNotExist(err) && c.Status().Measured
	}, 5*time.Second, 10*time.Millisecond)

	assert.FileExists(t, fresh, "a write still in flight is left alone")
}

func TestStatus_reportsLayersAndSources(t *testing.T) {
	_, peerTS := newFakeTileSource(t)
	c, dir := newCache(t, cacheOpts{upstream: "http://example.invalid", peers: []string{peerTS.URL}})

	st := c.Status()
	assert.Equal(t, dir, st.Dir)
	require.Len(t, st.Layers, 1)
	assert.Equal(t, tiles.LayerStatus{Layer: "satellite", TileType: "jpg", MinZoom: 0, MaxZoom: 16, Internet: true}, st.Layers[0])
	require.Len(t, st.Peers, 1)
	assert.Equal(t, tiles.HealthUnknown, st.Peers[0].Health)
	assert.Nil(t, st.Seed)
}

func TestServeHTTP_headRequest(t *testing.T) {
	_, ts := newFakeTileSource(t)
	c, _ := newCache(t, cacheOpts{upstream: ts.URL})

	req := httptest.NewRequest(http.MethodHead, "/tiles/satellite/1/1/1", nil)
	rec := httptest.NewRecorder()
	c.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	body, err := io.ReadAll(rec.Body)
	require.NoError(t, err)
	assert.Empty(t, body)
}

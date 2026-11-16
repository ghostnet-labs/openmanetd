package frontend

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openmanet/openmanetd/internal/auth"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/onboard"
	"github.com/openmanet/openmanetd/internal/tiles"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestTiles returns a tile cache over a temp dir with plenty of free
// space reported.
func newTestTiles(t *testing.T, layers []config.TileLayer) *tiles.Cache {
	t.Helper()

	c, err := tiles.New(tiles.Options{
		Log:      zerolog.Nop(),
		FreeFunc: func(string) (int64, error) { return 1 << 40, nil },
		Config: config.TilesConfig{
			Dir:             filepath.Join(t.TempDir(), "tiles"),
			MaxBytes:        64 << 20,
			MinFreeBytes:    1 << 20,
			FetchTimeout:    5 * time.Second,
			OfflineBackoff:  time.Second,
			SeedConcurrency: 1,
			Layers:          layers,
		},
	})
	require.NoError(t, err)

	return c
}

// fakeUpstream serves a small JPEG for any path and counts requests.
func fakeUpstream(t *testing.T) (string, *atomic.Int64) {
	t.Helper()

	var calls atomic.Int64

	jpeg := append([]byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F'}, bytes.Repeat([]byte{1}, 256)...)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)

		_, _ = w.Write(jpeg)
	}))
	t.Cleanup(ts.Close)

	return ts.URL, &calls
}

func TestServedMapSources(t *testing.T) {
	cases := []struct {
		name   string
		layers []config.TileLayer
		want   []config.OnboardMapSource
		noTile bool
	}{
		{
			name:   "defaults are all served",
			layers: config.DefaultTileLayers(),
			want:   config.DefaultOnboardMapSources(),
		},
		{
			name:   "tile cache off sends no maps",
			noTile: true,
		},
		{
			name:   "unserved layer is dropped",
			layers: []config.TileLayer{{Layer: "topo", TileType: "jpg", MaxZoom: 16}},
			want:   []config.OnboardMapSource{{Name: "Manet Topo", Layer: "topo", TileType: "jpg", MaxZoom: 16}},
		},
		{
			name:   "tile type mismatch is dropped",
			layers: []config.TileLayer{{Layer: "satellite", TileType: "png", MaxZoom: 16}, {Layer: "topo", TileType: "png", MaxZoom: 16}},
		},
		{
			name: "zooms are clipped to what is served",
			layers: []config.TileLayer{
				{Layer: "satellite", TileType: "jpg", MinZoom: 3, MaxZoom: 12},
				{Layer: "topo", TileType: "jpg", MinZoom: 17, MaxZoom: 18},
			},
			want: []config.OnboardMapSource{{Name: "Manet Satellite", Layer: "satellite", TileType: "jpg", MinZoom: 3, MaxZoom: 12}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(func(s *Server) {
				if !tc.noTile {
					s.tiles = newTestTiles(t, tc.layers)
				}
			})

			got := srv.servedMapSources()
			if len(tc.want) == 0 {
				assert.Empty(t, got)

				return
			}

			assert.Equal(t, tc.want, got)
		})
	}
}

// TestOnboardPackage_everyMapSourceLoads is the end-to-end guard for
// GHO-95: it takes each map source out of the phone settings package,
// fills in a tile, and fetches it from the same node. A map source the
// node cannot serve fails here.
func TestOnboardPackage_everyMapSourceLoads(t *testing.T) {
	upstream, calls := fakeUpstream(t)

	layers := config.DefaultTileLayers()
	for i := range layers {
		layers[i].Upstream = upstream + "/" + layers[i].Layer + "/{z}/{y}/{x}"
	}

	st, err := onboard.NewStore(filepath.Join(t.TempDir(), "onboard"), 1<<20)
	require.NoError(t, err)

	srv := newTestServer(func(s *Server) {
		s.onboard = st
		s.tiles = newTestTiles(t, layers)
		s.authEnabled = true
		s.sessionStore = auth.NewSessionStore(time.Hour, 4)
	})

	ts := httptest.NewServer(srv.handler())
	t.Cleanup(ts.Close)

	resp, data := do(t, http.MethodGet, ts.URL+onboardPackagePath+"?callsign=Alpha", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)

	urlRE := regexp.MustCompile(`<url>([^<]+)</url>`)
	sources := 0

	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "mapsources/") {
			continue
		}

		rc, err := f.Open()
		require.NoError(t, err)

		b, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())

		m := urlRE.FindSubmatch(b)
		require.NotNil(t, m, "%s has a URL", f.Name)

		tileURL := strings.NewReplacer("{$z}", "3", "{$x}", "1", "{$y}", "2").Replace(string(m[1]))
		_, rest, found := strings.Cut(tileURL, "/tiles/")
		require.True(t, found, "%s points at /tiles/", f.Name)

		path := "/tiles/" + rest

		// No session: phones fetch tiles without logging in.
		tileResp, body := do(t, http.MethodGet, ts.URL+path, nil, nil)
		assert.Equal(t, http.StatusOK, tileResp.StatusCode, "%s: %s", f.Name, path)
		assert.Equal(t, "image/jpeg", tileResp.Header.Get("Content-Type"))
		assert.NotEmpty(t, body)

		sources++
	}

	assert.Equal(t, len(config.DefaultOnboardMapSources()), sources, "every default map source is in the package")
	assert.Equal(t, int64(sources), calls.Load())
}

func TestTilesAdmin_needsLogin(t *testing.T) {
	srv := newTestServer(func(s *Server) {
		s.tiles = newTestTiles(t, config.DefaultTileLayers())
		s.authEnabled = true
		s.sessionStore = auth.NewSessionStore(time.Hour, 4)
	})

	ts := httptest.NewServer(srv.handler())
	t.Cleanup(ts.Close)

	resp, _ := do(t, http.MethodGet, ts.URL+tilesStatusPath, nil, nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	resp, _ = do(t, http.MethodPost, ts.URL+tilesSeedPath, strings.NewReader(`{"lat":40,"lon":-112,"radiusKm":1}`), nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestTilesStatus(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		srv := newTestServer(func(s *Server) { s.tiles = newTestTiles(t, config.DefaultTileLayers()) })

		w := httptest.NewRecorder()
		srv.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, tilesStatusPath, nil))
		require.Equal(t, http.StatusOK, w.Code)

		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, true, body["enabled"])
		assert.Len(t, body["layers"], 2)
		assert.Contains(t, body, "usedBytes")
		assert.Contains(t, body, "internet")
	})

	t.Run("disabled", func(t *testing.T) {
		srv := newTestServer()

		w := httptest.NewRecorder()
		srv.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, tilesStatusPath, nil))
		require.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"enabled":false`)

		w = httptest.NewRecorder()
		srv.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/tiles/satellite/1/0/0", nil))
		assert.NotEqual(t, "image/jpeg", w.Header().Get("Content-Type"), "no tile route when the cache is off")

		w = httptest.NewRecorder()
		srv.handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, tilesSeedPath, strings.NewReader(`{}`)))
		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	})
}

func TestTilesSeed(t *testing.T) {
	upstream, _ := fakeUpstream(t)
	srv := newTestServer(func(s *Server) {
		s.tiles = newTestTiles(t, []config.TileLayer{{Layer: "topo", Upstream: upstream + "/{z}/{y}/{x}", TileType: "jpg", MaxZoom: 16}})
	})
	h := srv.handler()

	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, tilesSeedPath, strings.NewReader(body)))

		return w
	}

	cases := map[string]struct {
		body string
		code int
	}{
		"not JSON":      {`lat=40`, http.StatusBadRequest},
		"unknown field": {`{"lat":40,"lon":-112,"radiusKm":1,"bogus":1}`, http.StatusBadRequest},
		"bad latitude":  {`{"lat":99,"lon":-112,"radiusKm":1}`, http.StatusBadRequest},
		"unknown layer": {`{"lat":40,"lon":-112,"radiusKm":1,"layers":["roads"]}`, http.StatusBadRequest},
		"too big":       {`{"lat":40,"lon":-112,"radiusKm":150}`, http.StatusBadRequest},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := post(tc.body)
			assert.Equal(t, tc.code, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), `"error"`)
		})
	}

	w := post(`{"name":"Town","lat":40.314,"lon":-112.006,"radiusKm":1,"maxZoom":10}`)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"Town"`)

	require.Eventually(t, func() bool {
		st := srv.tiles.Status().Seed

		return st != nil && !st.Running
	}, 10*time.Second, 10*time.Millisecond)

	st := srv.tiles.Status().Seed
	assert.Empty(t, st.Error)
	assert.Equal(t, st.Total, st.Downloaded)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, tilesSeedPath, nil))
	assert.Equal(t, http.StatusNoContent, w.Code)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPut, tilesSeedPath, nil))
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

package config

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetTiles_defaults(t *testing.T) {
	tc := NewWithoutWatch(viper.New()).GetTiles()

	assert.True(t, tc.Enable)
	assert.Equal(t, DefaultTilesDir, tc.Dir)
	assert.Equal(t, DefaultTilesMaxBytes, tc.MaxBytes)
	assert.Equal(t, DefaultTilesMinFreeBytes, tc.MinFreeBytes)
	assert.Equal(t, DefaultTilesFetchTimeout, tc.FetchTimeout)
	assert.Equal(t, DefaultTilesOfflineBackoff, tc.OfflineBackoff)
	assert.Equal(t, DefaultTilesSeedConcurrency, tc.SeedConcurrency)
	assert.Equal(t, DefaultTileLayers(), tc.Layers)
	assert.Empty(t, tc.Peers)
	assert.Empty(t, tc.Seed)
	assert.Empty(t, tc.Problems)
}

// TestDefaultMapSourcesAreServed keeps phone onboarding and the tile
// cache in step: every map source a phone receives by default must be a
// layer this node serves, with the same tile type, inside its zooms.
// It guards against shipping map entries that load blank (GHO-95).
func TestDefaultMapSourcesAreServed(t *testing.T) {
	layers := make(map[string]TileLayer)
	for _, l := range DefaultTileLayers() {
		layers[l.Layer] = l
	}

	require.NotEmpty(t, DefaultOnboardMapSources())

	for _, src := range DefaultOnboardMapSources() {
		l, ok := layers[src.Layer]
		require.True(t, ok, "map source %q has no tile layer %q", src.Name, src.Layer)
		assert.Equal(t, l.TileType, src.TileType, "map source %q tile type", src.Name)
		assert.GreaterOrEqual(t, src.MinZoom, l.MinZoom, "map source %q min zoom", src.Name)
		assert.LessOrEqual(t, src.MaxZoom, l.MaxZoom, "map source %q max zoom", src.Name)
		assert.NotEmpty(t, l.Upstream, "default layer %q has an internet source", l.Layer)
	}
}

func TestGetTiles_yamlBinding(t *testing.T) {
	const doc = `
tiles:
  enable: false
  dir: /mnt/data/tiles
  maxBytes: 2147483648
  minFreeBytes: 0
  fetchTimeout: 4s
  offlineBackoff: 2m
  seedConcurrency: 4
  peers:
    - http://10.41.0.1:8080/
    - http://10.41.0.1:8080
    - ftp://nope
    - ""
  layers:
    - layer: ortho
      upstream: https://tiles.example/{z}/{x}/{y}.png
      tileType: PNG
      minZoom: 5
      maxZoom: 19
    - layer: offline
      tileType: jpg
      maxZoom: 14
    - layer: ortho
      tileType: png
    - layer: "../x"
      tileType: png
    - layer: bad-type
      tileType: gif
    - layer: bad-zoom
      tileType: jpg
      minZoom: 9
      maxZoom: 3
    - layer: bad-url
      tileType: jpg
      upstream: https://tiles.example/{z}/{x}.png
  seed:
    - name: Eagle Mountain
      lat: 40.314
      lon: -112.006
      radiusKm: 12
      maxZoom: 16
      layers: [ortho]
    - lat: 40
      lon: -112
      radiusKm: 1
    - name: Nowhere
      lat: 95
      lon: 0
      radiusKm: 1
`

	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(doc)))

	tc := NewWithoutWatch(v).GetTiles()

	assert.False(t, tc.Enable)
	assert.Equal(t, "/mnt/data/tiles", tc.Dir)
	assert.Equal(t, int64(2147483648), tc.MaxBytes)
	assert.Zero(t, tc.MinFreeBytes)
	assert.Equal(t, 4*time.Second, tc.FetchTimeout)
	assert.Equal(t, 2*time.Minute, tc.OfflineBackoff)
	assert.Equal(t, 4, tc.SeedConcurrency)
	assert.Equal(t, []string{"http://10.41.0.1:8080"}, tc.Peers, "trailing slash trimmed, duplicates and non-http dropped")
	assert.Equal(t, []TileLayer{
		{Layer: "ortho", Upstream: "https://tiles.example/{z}/{x}/{y}.png", TileType: "png", MinZoom: 5, MaxZoom: 19},
		{Layer: "offline", TileType: "jpg", MaxZoom: 14},
	}, tc.Layers)
	assert.Equal(t, []TileSeedArea{
		{Name: "Eagle Mountain", Lat: 40.314, Lon: -112.006, RadiusKm: 12, MaxZoom: 16, Layers: []string{"ortho"}},
		{Name: "area 2", Lat: 40, Lon: -112, RadiusKm: 1},
	}, tc.Seed)

	joined := strings.Join(tc.Problems, "\n")
	for _, want := range []string{"ftp://nope", "duplicate layer", `"../x"`, "bad-type", "bad-zoom", "bad-url", "Nowhere"} {
		assert.Contains(t, joined, want)
	}
}

func TestGetTiles_rejectsOutOfRangeLimits(t *testing.T) {
	const doc = `
tiles:
  maxBytes: 1024
  minFreeBytes: -1
  fetchTimeout: 10m
  offlineBackoff: 0s
  seedConcurrency: 99
`

	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(doc)))

	tc := NewWithoutWatch(v).GetTiles()

	assert.Equal(t, DefaultTilesMaxBytes, tc.MaxBytes)
	assert.Equal(t, DefaultTilesMinFreeBytes, tc.MinFreeBytes)
	assert.Equal(t, DefaultTilesFetchTimeout, tc.FetchTimeout)
	assert.Equal(t, DefaultTilesOfflineBackoff, tc.OfflineBackoff)
	assert.Equal(t, DefaultTilesSeedConcurrency, tc.SeedConcurrency)
	assert.Len(t, tc.Problems, 5)
}

func TestGetTiles_returnsCopies(t *testing.T) {
	const doc = `
tiles:
  peers: [http://a:8080]
  seed:
    - name: a
      lat: 1
      lon: 1
      radiusKm: 1
      layers: [satellite]
`

	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(doc)))

	cfg := NewWithoutWatch(v)
	tc := cfg.GetTiles()
	tc.Peers[0] = "changed"
	tc.Layers[0].Layer = "changed"
	tc.Seed[0].Layers[0] = "changed"

	again := cfg.GetTiles()
	assert.Equal(t, "http://a:8080", again.Peers[0])
	assert.Equal(t, "satellite", again.Layers[0].Layer)
	assert.Equal(t, "satellite", again.Seed[0].Layers[0])
}

func TestValidateTileSeedArea(t *testing.T) {
	ok := TileSeedArea{Lat: 40, Lon: -112, RadiusKm: 5}

	cases := map[string]struct {
		mut  func(*TileSeedArea)
		want string
	}{
		"valid":            {func(*TileSeedArea) {}, ""},
		"lat too high":     {func(a *TileSeedArea) { a.Lat = 86 }, "lat"},
		"lon too low":      {func(a *TileSeedArea) { a.Lon = -181 }, "lon"},
		"radius zero":      {func(a *TileSeedArea) { a.RadiusKm = 0 }, "radiusKm"},
		"radius too large": {func(a *TileSeedArea) { a.RadiusKm = 201 }, "radiusKm"},
		"zoom too high":    {func(a *TileSeedArea) { a.MaxZoom = 23 }, "zooms"},
		"min above max":    {func(a *TileSeedArea) { a.MinZoom, a.MaxZoom = 10, 5 }, "minZoom"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := ok
			tc.mut(&a)

			err := ValidateTileSeedArea(a)
			if tc.want == "" {
				assert.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

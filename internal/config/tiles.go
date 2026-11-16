package config

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Map tile cache defaults (GHO-95). Every node serves ATAK map tiles at
// /tiles/<layer>/<z>/<x>/<y> on the frontend port. A tile it has not
// seen is fetched from a peer node (a mast that pre-seeded the area),
// then from the internet, and kept on persistent storage.
//
// The cache lives on the overlay, never in /tmp (RAM on OpenWrt), and is
// bounded twice: by MaxBytes, and by MinFreeBytes of free space left on
// that filesystem so a full cache can never stop config writes.
const (
	DefaultTilesEnable          = true
	DefaultTilesDir             = "/srv/openmanet/tiles"
	DefaultTilesMaxBytes        = int64(512 << 20)
	DefaultTilesMinFreeBytes    = int64(64 << 20)
	DefaultTilesFetchTimeout    = 10 * time.Second
	DefaultTilesOfflineBackoff  = 30 * time.Second
	DefaultTilesSeedConcurrency = 2

	minTilesMaxBytes       = int64(8 << 20)
	maxTilesFetchTimeout   = 2 * time.Minute
	maxTilesOfflineBackoff = 30 * time.Minute
	maxTilesSeedWorkers    = 8
	maxTilesZoom           = 22
	maxTilesSeedRadiusKm   = 200.0
)

// Tile image formats.
const (
	TileTypeJPG = "jpg"
	TileTypePNG = "png"
)

// USGS National Map basemaps are public domain. Their tile caches stop
// at zoom 16 and are JPEG; the URL order is z/y/x.
const (
	usgsImageryURL = "https://basemap.nationalmap.gov/arcgis/rest/services/USGSImageryOnly/MapServer/tile/{z}/{y}/{x}"
	usgsTopoURL    = "https://basemap.nationalmap.gov/arcgis/rest/services/USGSTopo/MapServer/tile/{z}/{y}/{x}"
)

// TileLayer is one tile layer the node serves.
type TileLayer struct {
	// Layer is the path segment under /tiles/.
	Layer string `mapstructure:"layer"`
	// Upstream is the internet tile URL with {z}, {x} and {y}
	// placeholders. Empty means the layer is served only from the cache
	// and peers (for example a pre-loaded offline area).
	Upstream string `mapstructure:"upstream"`
	// TileType is "jpg" or "png". It must match the onboarding map
	// source for the same layer.
	TileType string `mapstructure:"tileType"`
	MinZoom  int    `mapstructure:"minZoom"`
	MaxZoom  int    `mapstructure:"maxZoom"`
}

// TileSeedArea is an area a node downloads ahead of time, for example a
// mast covering its town. Seeding skips tiles already cached, so it is
// cheap to repeat.
type TileSeedArea struct {
	Name string `mapstructure:"name"`
	// Layers to seed; empty means every layer.
	Layers   []string `mapstructure:"layers"`
	Lat      float64  `mapstructure:"lat"`
	Lon      float64  `mapstructure:"lon"`
	RadiusKm float64  `mapstructure:"radiusKm"`
	MinZoom  int      `mapstructure:"minZoom"`
	// MaxZoom 0 means each layer's own MaxZoom.
	MaxZoom int `mapstructure:"maxZoom"`
}

// DefaultTileLayers returns the layers every node serves. Their names,
// tile types and zoom ranges match DefaultOnboardMapSources, so every
// map source a phone receives is served (a test keeps them in step).
func DefaultTileLayers() []TileLayer {
	return []TileLayer{
		{Layer: "satellite", Upstream: usgsImageryURL, TileType: TileTypeJPG, MinZoom: 0, MaxZoom: 16},
		{Layer: "topo", Upstream: usgsTopoURL, TileType: TileTypeJPG, MinZoom: 0, MaxZoom: 16},
	}
}

// TilesConfig holds the map tile cache settings. It is returned by value
// from GetTiles with its slices copied.
type TilesConfig struct {
	Dir      string
	Layers   []TileLayer
	Peers    []string
	Seed     []TileSeedArea
	Problems []string
	// MaxBytes caps the cache on disk; the oldest-used tiles go first.
	MaxBytes int64
	// MinFreeBytes is the free space the cache always leaves on its
	// filesystem.
	MinFreeBytes int64
	// FetchTimeout bounds one peer or internet tile fetch.
	FetchTimeout time.Duration
	// OfflineBackoff is how long a source that failed is skipped, so an
	// offline node answers misses at once instead of timing out per tile.
	OfflineBackoff time.Duration
	// SeedConcurrency is the number of parallel downloads while seeding.
	SeedConcurrency int
	Enable          bool
}

// loadTiles reads the tiles section, applying defaults. The caller holds
// c.mu.
func (c *Config) loadTiles() TilesConfig {
	tc := TilesConfig{
		Enable:          DefaultTilesEnable,
		Dir:             DefaultTilesDir,
		MaxBytes:        DefaultTilesMaxBytes,
		MinFreeBytes:    DefaultTilesMinFreeBytes,
		FetchTimeout:    DefaultTilesFetchTimeout,
		OfflineBackoff:  DefaultTilesOfflineBackoff,
		SeedConcurrency: DefaultTilesSeedConcurrency,
		Layers:          DefaultTileLayers(),
	}

	if c.v.IsSet("tiles.enable") {
		tc.Enable = c.v.GetBool("tiles.enable")
	}

	if val := strings.TrimSpace(c.v.GetString("tiles.dir")); val != "" {
		tc.Dir = val
	}

	c.loadTilesLimits(&tc)

	if c.v.IsSet("tiles.layers") {
		var layers []TileLayer
		if err := c.v.UnmarshalKey("tiles.layers", &layers); err != nil {
			tc.Problems = append(tc.Problems, "tiles.layers: "+err.Error())
		} else {
			tc.Layers = validTileLayers(layers, &tc.Problems)
		}
	}

	if c.v.IsSet("tiles.peers") {
		tc.Peers = validTilePeers(c.v.GetStringSlice("tiles.peers"), &tc.Problems)
	}

	if c.v.IsSet("tiles.seed") {
		var areas []TileSeedArea
		if err := c.v.UnmarshalKey("tiles.seed", &areas); err != nil {
			tc.Problems = append(tc.Problems, "tiles.seed: "+err.Error())
		} else {
			tc.Seed = validTileSeedAreas(areas, &tc.Problems)
		}
	}

	return tc
}

// loadTilesLimits reads the size, time and worker limits. The caller
// holds c.mu.
func (c *Config) loadTilesLimits(tc *TilesConfig) {
	if c.v.IsSet("tiles.maxBytes") {
		if v := c.v.GetInt64("tiles.maxBytes"); v >= minTilesMaxBytes {
			tc.MaxBytes = v
		} else {
			tc.Problems = append(tc.Problems, fmt.Sprintf("tiles.maxBytes: %d is below %d", v, minTilesMaxBytes))
		}
	}

	if c.v.IsSet("tiles.minFreeBytes") {
		if v := c.v.GetInt64("tiles.minFreeBytes"); v >= 0 {
			tc.MinFreeBytes = v
		} else {
			tc.Problems = append(tc.Problems, fmt.Sprintf("tiles.minFreeBytes: %d is negative", v))
		}
	}

	durations := []struct {
		dst *time.Duration
		key string
		max time.Duration
	}{
		{&tc.FetchTimeout, "tiles.fetchTimeout", maxTilesFetchTimeout},
		{&tc.OfflineBackoff, "tiles.offlineBackoff", maxTilesOfflineBackoff},
	}

	for _, d := range durations {
		if !c.v.IsSet(d.key) {
			continue
		}

		if v := c.v.GetDuration(d.key); v > 0 && v <= d.max {
			*d.dst = v
		} else {
			tc.Problems = append(tc.Problems, fmt.Sprintf("%s: %s is outside (0, %s]", d.key, v, d.max))
		}
	}

	if c.v.IsSet("tiles.seedConcurrency") {
		if v := c.v.GetInt("tiles.seedConcurrency"); v >= 1 && v <= maxTilesSeedWorkers {
			tc.SeedConcurrency = v
		} else {
			tc.Problems = append(tc.Problems, fmt.Sprintf("tiles.seedConcurrency: %d is outside [1, %d]", v, maxTilesSeedWorkers))
		}
	}
}

// validTileLayers drops layers that cannot be served: a bad name, an
// unknown tile type, bad zooms, an upstream URL that is not http(s) or
// lacks a placeholder, or a duplicate name.
func validTileLayers(in []TileLayer, problems *[]string) []TileLayer {
	out := make([]TileLayer, 0, len(in))

	for i, l := range in {
		l.Layer = strings.TrimSpace(l.Layer)
		l.Upstream = strings.TrimSpace(l.Upstream)
		l.TileType = strings.ToLower(strings.TrimSpace(l.TileType))

		reason := ""

		switch {
		case !validLayerName(l.Layer):
			reason = "layer must be letters, digits, '-' or '_'"
		case l.TileType != TileTypeJPG && l.TileType != TileTypePNG:
			reason = `tileType must be "jpg" or "png"`
		case l.MinZoom < 0 || l.MaxZoom > maxTilesZoom || l.MinZoom > l.MaxZoom:
			reason = fmt.Sprintf("zooms must satisfy 0 <= minZoom <= maxZoom <= %d", maxTilesZoom)
		case l.Upstream != "" && !validTileUpstream(l.Upstream):
			reason = "upstream must be an http(s) URL with {z}, {x} and {y}"
		case slices.ContainsFunc(out, func(o TileLayer) bool { return o.Layer == l.Layer }):
			reason = "duplicate layer"
		}

		if reason != "" {
			*problems = append(*problems, fmt.Sprintf("tiles.layers[%d] %q: %s", i, l.Layer, reason))

			continue
		}

		out = append(out, l)
	}

	return out
}

func validTileUpstream(u string) bool {
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return false
	}

	return strings.Contains(u, "{z}") && strings.Contains(u, "{x}") && strings.Contains(u, "{y}")
}

// validTilePeers keeps http(s) base URLs, without a trailing slash.
func validTilePeers(in []string, problems *[]string) []string {
	out := make([]string, 0, len(in))

	for _, p := range in {
		p = strings.TrimRight(strings.TrimSpace(p), "/")
		if p == "" {
			continue
		}

		if !strings.HasPrefix(p, "http://") && !strings.HasPrefix(p, "https://") {
			*problems = append(*problems, fmt.Sprintf("tiles.peers: %q is not an http(s) URL", p))

			continue
		}

		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}

	return out
}

func validTileSeedAreas(in []TileSeedArea, problems *[]string) []TileSeedArea {
	out := make([]TileSeedArea, 0, len(in))

	for i, a := range in {
		a.Name = strings.TrimSpace(a.Name)
		if a.Name == "" {
			a.Name = fmt.Sprintf("area %d", i+1)
		}

		if err := ValidateTileSeedArea(a); err != nil {
			*problems = append(*problems, fmt.Sprintf("tiles.seed[%d] %q: %s", i, a.Name, err))

			continue
		}

		out = append(out, a)
	}

	return out
}

// ValidateTileSeedArea checks an area's center, radius and zooms. Layer
// names are checked against the served layers by the tile cache.
func ValidateTileSeedArea(a TileSeedArea) error {
	switch {
	case a.Lat < -85 || a.Lat > 85:
		return fmt.Errorf("lat %v is outside [-85, 85]", a.Lat)
	case a.Lon < -180 || a.Lon > 180:
		return fmt.Errorf("lon %v is outside [-180, 180]", a.Lon)
	case a.RadiusKm <= 0 || a.RadiusKm > maxTilesSeedRadiusKm:
		return fmt.Errorf("radiusKm %v is outside (0, %v]", a.RadiusKm, maxTilesSeedRadiusKm)
	case a.MinZoom < 0 || a.MaxZoom < 0 || a.MaxZoom > maxTilesZoom:
		return fmt.Errorf("zooms must be within [0, %d]", maxTilesZoom)
	case a.MaxZoom != 0 && a.MinZoom > a.MaxZoom:
		return fmt.Errorf("minZoom %d is above maxZoom %d", a.MinZoom, a.MaxZoom)
	}

	return nil
}

// GetTiles returns the map tile cache settings.
func (c *Config) GetTiles() TilesConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()

	tc := c.Tiles
	tc.Layers = slices.Clone(tc.Layers)
	tc.Peers = slices.Clone(tc.Peers)
	tc.Problems = slices.Clone(tc.Problems)

	tc.Seed = make([]TileSeedArea, len(c.Tiles.Seed))
	for i, a := range c.Tiles.Seed {
		a.Layers = slices.Clone(a.Layers)
		tc.Seed[i] = a
	}

	return tc
}

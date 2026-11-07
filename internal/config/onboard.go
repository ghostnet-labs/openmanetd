package config

import (
	"slices"
	"strings"
)

// Phone onboarding defaults. Onboarding lets a phone that joins the
// node's access point download ATAK from the node and import a settings
// package (callsign, team, map sources) with no internet. It is on by
// default but offers nothing until an operator uploads an ATAK APK.
//
// The APK is ~200 MB, so it lives on persistent storage (the overlay on
// OpenWrt), never in /tmp, which is RAM.
const (
	DefaultOnboardEnable      = true
	DefaultOnboardDir         = "/srv/openmanet/onboard"
	DefaultOnboardMaxAPKBytes = int64(600 << 20)
)

// OnboardMapSource describes one tile layer the settings package points
// ATAK at. The tile URL is built per request from the address the phone
// used to reach the node, so one definition works on every node.
type OnboardMapSource struct {
	// Name is the label ATAK shows in its map list.
	Name string `mapstructure:"name"`
	// Layer is the path segment under /tiles/ on the node.
	Layer string `mapstructure:"layer"`
	// TileType is the tile image format: "jpg" or "png".
	TileType string `mapstructure:"tileType"`
	MinZoom  int    `mapstructure:"minZoom"`
	MaxZoom  int    `mapstructure:"maxZoom"`
}

// DefaultOnboardMapSources returns the layers served by the node tile
// cache (GHO-95): public-domain USGS imagery and topo.
func DefaultOnboardMapSources() []OnboardMapSource {
	return []OnboardMapSource{
		{Name: "Manet Satellite", Layer: "satellite", TileType: "jpg", MinZoom: 0, MaxZoom: 17},
		{Name: "Manet Topo", Layer: "topo", TileType: "png", MinZoom: 0, MaxZoom: 16},
	}
}

// OnboardConfig holds the onboarding settings. It is returned by value
// from GetOnboard with its slice copied.
type OnboardConfig struct {
	// Dir holds the uploaded APK and its metadata.
	Dir string
	// MapSources are written into the settings package. An empty list
	// leaves map sources out.
	MapSources []OnboardMapSource
	// MaxAPKBytes caps an uploaded APK.
	MaxAPKBytes int64
	// Enable serves the onboarding endpoints.
	Enable bool
}

// loadOnboard reads the onboard section, applying defaults. The caller
// holds c.mu.
func (c *Config) loadOnboard() OnboardConfig {
	oc := OnboardConfig{
		Enable:      DefaultOnboardEnable,
		Dir:         DefaultOnboardDir,
		MaxAPKBytes: DefaultOnboardMaxAPKBytes,
		MapSources:  DefaultOnboardMapSources(),
	}

	if c.v.IsSet("onboard.enable") {
		oc.Enable = c.v.GetBool("onboard.enable")
	}

	if val := strings.TrimSpace(c.v.GetString("onboard.dir")); val != "" {
		oc.Dir = val
	}

	if val := c.v.GetInt64("onboard.maxApkBytes"); val > 0 {
		oc.MaxAPKBytes = val
	}

	if c.v.IsSet("onboard.mapSources") {
		var sources []OnboardMapSource
		if err := c.v.UnmarshalKey("onboard.mapSources", &sources); err == nil {
			oc.MapSources = validOnboardMapSources(sources)
		}
	}

	return oc
}

// validOnboardMapSources drops entries that would produce a broken map
// source: a missing name or layer, an unknown tile type, or bad zooms.
func validOnboardMapSources(in []OnboardMapSource) []OnboardMapSource {
	out := make([]OnboardMapSource, 0, len(in))

	for _, s := range in {
		s.Name = strings.TrimSpace(s.Name)
		s.Layer = strings.TrimSpace(s.Layer)
		s.TileType = strings.ToLower(strings.TrimSpace(s.TileType))

		if s.Name == "" || !validLayerName(s.Layer) {
			continue
		}

		if s.TileType != "jpg" && s.TileType != "png" {
			continue
		}

		if s.MinZoom < 0 || s.MaxZoom > 22 || s.MinZoom > s.MaxZoom {
			continue
		}

		out = append(out, s)
	}

	return out
}

// validLayerName accepts a single URL path segment of letters, digits,
// '-' and '_'.
func validLayerName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}

	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}

	return true
}

// GetOnboard returns the phone onboarding settings.
func (c *Config) GetOnboard() OnboardConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()

	oc := c.Onboard
	oc.MapSources = slices.Clone(oc.MapSources)

	return oc
}

package config

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// ATAK feed (GHO-94) defaults. The feed is on by default: it only sends
// CoT to the ATAK SA multicast group, which every ATAK install already
// listens on, so every phone on the mesh sees the nodes with no setup.
const (
	DefaultTAKEnable          = true
	DefaultTAKRole            = TAKRoleRadio
	DefaultTAKInterval        = 10 * time.Second
	DefaultTAKLinks           = true
	DefaultTAKJamming         = true
	DefaultTAKWeather         = true
	DefaultTAKAircraftEnable  = true
	DefaultTAKAircraftMax     = 32
	DefaultTAKAircraftRangeKm = 150.0
	DefaultTAKCoverageEnable  = true
	DefaultTAKCoverageCellM   = 50
	DefaultTAKCoverageMax     = 1024

	minTAKInterval       = 2 * time.Second
	maxTAKInterval       = 5 * time.Minute
	maxTAKAircraft       = 256
	maxTAKAircraftRangeK = 1000.0
	minTAKCoverageCellM  = 10
	maxTAKCoverageCellM  = 500
	minTAKCoverageMax    = 64
	maxTAKCoverageMax    = 8192
)

// Node roles shown on the ATAK map.
const (
	// TAKRoleRadio is a person-worn or vehicle node (radio unit icon).
	TAKRoleRadio = "radio"
	// TAKRoleMast is a fixed relay mast (telecommunications tower icon).
	TAKRoleMast = "mast"
)

// TAKConfig holds the ATAK feed settings (YAML section "tak"). It is
// returned by value from GetTAK.
type TAKConfig struct {
	// Role picks this node's map icon: TAKRoleRadio or TAKRoleMast.
	Role string
	// Problems lists values rejected at load and replaced by defaults.
	Problems []string
	// Interval is the base publish period for this node's marker,
	// aircraft and jamming markers. Mesh links go out every third tick.
	Interval time.Duration
	// AircraftMax caps the aircraft sent per tick (nearest first).
	AircraftMax int
	// AircraftRangeKm drops aircraft farther than this from the node; 0
	// keeps every aircraft.
	AircraftRangeKm float64
	// CoverageCellM is the coverage map's grid cell size in meters. It is
	// read once at startup; changing it starts a fresh map.
	CoverageCellM int
	// CoverageMaxCells caps the cells kept; the oldest are dropped first.
	CoverageMaxCells int
	// Enable turns the whole feed on. While it is on, the feed owns this
	// node's radio marker and gpsd stops sending its own.
	Enable bool
	// Links draws this node's mesh links as lines.
	Links bool
	// Jamming places a marker for each jamming detection.
	Jamming bool
	// Weather places a marker for the weather station wired to this node.
	Weather bool
	// AircraftEnable forwards aircraft heard by this node's scanner.
	AircraftEnable bool
	// CoverageEnable records how well this node hears the mesh at each
	// spot it visits and draws that as a coverage heatmap in ATAK.
	CoverageEnable bool
}

// loadTAK reads the tak section, applying defaults. The caller holds c.mu.
func (c *Config) loadTAK() TAKConfig {
	tc := TAKConfig{
		Enable:           DefaultTAKEnable,
		Role:             DefaultTAKRole,
		Interval:         DefaultTAKInterval,
		Links:            DefaultTAKLinks,
		Jamming:          DefaultTAKJamming,
		Weather:          DefaultTAKWeather,
		AircraftEnable:   DefaultTAKAircraftEnable,
		AircraftMax:      DefaultTAKAircraftMax,
		AircraftRangeKm:  DefaultTAKAircraftRangeKm,
		CoverageEnable:   DefaultTAKCoverageEnable,
		CoverageCellM:    DefaultTAKCoverageCellM,
		CoverageMaxCells: DefaultTAKCoverageMax,
	}

	boolKeys := []struct {
		dst *bool
		key string
	}{
		{&tc.Enable, "tak.enable"},
		{&tc.Links, "tak.links"},
		{&tc.Jamming, "tak.jamming"},
		{&tc.Weather, "tak.weather"},
		{&tc.AircraftEnable, "tak.aircraft.enable"},
		{&tc.CoverageEnable, "tak.coverage.enable"},
	}
	for _, b := range boolKeys {
		if c.v.IsSet(b.key) {
			*b.dst = c.v.GetBool(b.key)
		}
	}

	if c.v.IsSet("tak.role") {
		role := strings.ToLower(strings.TrimSpace(c.v.GetString("tak.role")))
		if slices.Contains([]string{TAKRoleRadio, TAKRoleMast}, role) {
			tc.Role = role
		} else {
			tc.Problems = append(tc.Problems, fmt.Sprintf("tak.role: %q is not %q or %q", role, TAKRoleRadio, TAKRoleMast))
		}
	}

	if raw := c.v.GetString("tak.interval"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err == nil && d >= minTAKInterval && d <= maxTAKInterval {
			tc.Interval = d
		} else {
			tc.Problems = append(tc.Problems, fmt.Sprintf("tak.interval: %q is not a duration in [%s, %s]", raw, minTAKInterval, maxTAKInterval))
		}
	}

	if c.v.IsSet("tak.aircraft.max") {
		if v := c.v.GetInt("tak.aircraft.max"); v >= 0 && v <= maxTAKAircraft {
			tc.AircraftMax = v
		} else {
			tc.Problems = append(tc.Problems, fmt.Sprintf("tak.aircraft.max: %d is outside [0, %d]", v, maxTAKAircraft))
		}
	}

	if c.v.IsSet("tak.aircraft.rangeKm") {
		if v := c.v.GetFloat64("tak.aircraft.rangeKm"); v >= 0 && v <= maxTAKAircraftRangeK {
			tc.AircraftRangeKm = v
		} else {
			tc.Problems = append(tc.Problems, fmt.Sprintf("tak.aircraft.rangeKm: %v is outside [0, %v]", v, maxTAKAircraftRangeK))
		}
	}

	tc.Problems = c.intInRange(tc.Problems, "tak.coverage.cellMeters", minTAKCoverageCellM, maxTAKCoverageCellM, &tc.CoverageCellM)
	tc.Problems = c.intInRange(tc.Problems, "tak.coverage.maxCells", minTAKCoverageMax, maxTAKCoverageMax, &tc.CoverageMaxCells)

	return tc
}

// intInRange sets *dst from key when it is set and within [lo, hi], and
// otherwise records a problem and leaves the default.
func (c *Config) intInRange(problems []string, key string, lo, hi int, dst *int) []string {
	if !c.v.IsSet(key) {
		return problems
	}

	v := c.v.GetInt(key)
	if v < lo || v > hi {
		return append(problems, fmt.Sprintf("%s: %d is outside [%d, %d]", key, v, lo, hi))
	}

	*dst = v

	return problems
}

// GetTAK returns the ATAK feed settings.
func (c *Config) GetTAK() TAKConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()

	tc := c.TAK
	tc.Problems = slices.Clone(tc.Problems)

	return tc
}

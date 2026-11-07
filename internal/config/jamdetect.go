package config

import "time"

// Jamming-detection (jamdetect) defaults. Detection is on by default: it
// only reads the radios' channel surveys (`iw dev <if> survey dump`) and
// never changes a radio, so every node and mast watches for jamming
// without provisioning.
const (
	DefaultJamdetectEnable         = true
	DefaultJamdetectGossip         = true
	DefaultJamdetectInterval       = 5 * time.Second
	DefaultJamdetectGossipInterval = 10 * time.Second
	DefaultJamdetectGossipExpiry   = 2 * time.Minute
)

// JamdetectConfig holds the jamdetect settings. A zero threshold keeps
// the jamdetect package default.
type JamdetectConfig struct {
	Interval        time.Duration
	GossipInterval  time.Duration
	GossipExpiry    time.Duration
	NoiseRiseDB     float64
	NoiseCeilingDBm float64
	BusyRisePct     float64
	BusyFloorPct    float64
	BusyCeilingPct  float64
	WarmupSamples   int
	SuspectSamples  int
	JamSamples      int
	ClearSamples    int
	// Enable starts detection.
	Enable bool
	// Gossip shares reports with the mesh over alfred.
	Gossip bool
}

// loadJamdetect reads the jamdetect section, applying defaults. The
// caller holds c.mu.
func (c *Config) loadJamdetect() JamdetectConfig {
	jc := JamdetectConfig{
		Enable:         DefaultJamdetectEnable,
		Gossip:         DefaultJamdetectGossip,
		Interval:       positiveDuration(c.v.GetString("jamdetect.interval"), DefaultJamdetectInterval),
		GossipInterval: positiveDuration(c.v.GetString("jamdetect.gossipInterval"), DefaultJamdetectGossipInterval),
		GossipExpiry:   positiveDuration(c.v.GetString("jamdetect.gossipExpiry"), DefaultJamdetectGossipExpiry),
		NoiseRiseDB:    c.positiveFloat("jamdetect.noiseRiseDb"),
		BusyRisePct:    c.pctOrZero("jamdetect.busyRisePct"),
		BusyFloorPct:   c.pctOrZero("jamdetect.busyFloorPct"),
		BusyCeilingPct: c.pctOrZero("jamdetect.busyCeilingPct"),
		WarmupSamples:  c.positiveInt("jamdetect.warmupSamples"),
		SuspectSamples: c.positiveInt("jamdetect.suspectSamples"),
		JamSamples:     c.positiveInt("jamdetect.jamSamples"),
		ClearSamples:   c.positiveInt("jamdetect.clearSamples"),
	}

	if c.v.IsSet("jamdetect.enable") {
		jc.Enable = c.v.GetBool("jamdetect.enable")
	}

	if c.v.IsSet("jamdetect.gossip") {
		jc.Gossip = c.v.GetBool("jamdetect.gossip")
	}

	// A noise ceiling is negative dBm; anything else is a typo.
	if v := c.v.GetFloat64("jamdetect.noiseCeilingDbm"); v < 0 && v >= -130 {
		jc.NoiseCeilingDBm = v
	}

	return jc
}

func (c *Config) positiveFloat(key string) float64 {
	if v := c.v.GetFloat64(key); v > 0 {
		return v
	}

	return 0
}

func (c *Config) pctOrZero(key string) float64 {
	if v := c.v.GetFloat64(key); v > 0 && v <= 100 {
		return v
	}

	return 0
}

func (c *Config) positiveInt(key string) int {
	if v := c.v.GetInt(key); v > 0 {
		return v
	}

	return 0
}

// GetJamdetect returns the jamdetect settings.
func (c *Config) GetJamdetect() JamdetectConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.Jamdetect
}

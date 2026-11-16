package tiles

import (
	"slices"
	"time"
)

// Source health values in Status.
const (
	HealthOK      = "ok"
	HealthOffline = "offline"
	HealthUnknown = "unknown"
)

// LayerStatus describes one served layer.
type LayerStatus struct {
	Layer    string `json:"layer"`
	TileType string `json:"tileType"`
	MinZoom  int    `json:"minZoom"`
	MaxZoom  int    `json:"maxZoom"`
	Internet bool   `json:"internet"`
}

// SourceStatus is the health of a peer or of the internet.
type SourceStatus struct {
	LastOK *time.Time `json:"lastOk,omitempty"`
	Name   string     `json:"name"`
	Health string     `json:"health"`
}

// Status is a snapshot of the cache for the settings page.
type Status struct {
	Seed     *SeedStatus    `json:"seed,omitempty"`
	Internet SourceStatus   `json:"internet"`
	Dir      string         `json:"dir"`
	Layers   []LayerStatus  `json:"layers"`
	Peers    []SourceStatus `json:"peers"`
	Counters Counters       `json:"counters"`
	Used     int64          `json:"usedBytes"`
	Max      int64          `json:"maxBytes"`
	Free     int64          `json:"freeBytes"`
	MinFree  int64          `json:"minFreeBytes"`
	Evicted  int64          `json:"evicted"`
	Measured bool           `json:"measured"`
}

// Status returns a snapshot of the cache.
func (c *Cache) Status() Status {
	// Refresh free space outside the lock; a failure leaves the last
	// value.
	_, _ = c.freeBytes()

	names := make([]string, 0, len(c.layers))
	for n := range c.layers {
		names = append(names, n)
	}

	slices.Sort(names)

	st := Status{
		Dir:     c.dir,
		Max:     c.maxBytes,
		MinFree: c.minFree,
		Layers:  make([]LayerStatus, 0, len(names)),
		Peers:   make([]SourceStatus, 0, len(c.peers)),
	}

	hasInternet := false

	for _, n := range names {
		l := c.layers[n]
		hasInternet = hasInternet || l.Upstream != ""
		st.Layers = append(st.Layers, LayerStatus{
			Layer:    l.Layer,
			TileType: l.TileType,
			MinZoom:  l.MinZoom,
			MaxZoom:  l.MaxZoom,
			Internet: l.Upstream != "",
		})
	}

	now := c.now()

	c.mu.Lock()
	defer c.mu.Unlock()

	st.Used = c.used
	st.Free = c.free
	st.Measured = c.scanned
	st.Evicted = c.evicted
	st.Counters = c.stats

	for _, p := range c.peers {
		st.Peers = append(st.Peers, c.sourceStatusLocked(p, now))
	}

	st.Internet = c.sourceStatusLocked(sourceInternet, now)
	if !hasInternet {
		st.Internet.Health = HealthUnknown
	}

	if c.seed != nil {
		s := c.seed.status
		s.Areas = slices.Clone(s.Areas)
		st.Seed = &s
	}

	return st
}

// sourceStatusLocked reports a source's health. The caller holds c.mu.
func (c *Cache) sourceStatusLocked(name string, now time.Time) SourceStatus {
	ss := SourceStatus{Name: name, Health: HealthUnknown}

	if t, ok := c.lastOK[name]; ok {
		ss.LastOK = &t
		ss.Health = HealthOK
	}

	if now.Before(c.skipTill[name]) {
		ss.Health = HealthOffline
	}

	return ss
}

package scanner

import (
	"slices"
	"time"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/scanner/aircraft"
	"github.com/openmanet/openmanetd/internal/scanner/receivers"
	"github.com/openmanet/openmanetd/internal/scanner/sites"
)

// PositionKind says where a Position came from.
type PositionKind uint8

// PositionKind values.
const (
	PositionNone PositionKind = iota
	PositionGPSD
	PositionLastKnown
	PositionManual
)

// Position is the reference position for site selection.
type Position struct {
	FixTime time.Time
	Lat     float64
	Lon     float64
	Source  PositionKind
	// Stale is true when there is no fix or it is older than the position
	// stale timeout.
	Stale bool
}

// Selection is the selected site with its wide-tuner plan.
type Selection struct {
	SelectedAt time.Time
	Site       *sites.Site
	Mode       string
	Plan       sites.TunerPlan
	DistanceKm float64
	Reason     sites.Reason
}

// ControlState is the control-channel decoder state.
type ControlState uint8

// ControlState values.
const (
	ControlUnknown ControlState = iota
	ControlUnavailable
	ControlIdle
	ControlHunting
	ControlLocked
	ControlError
)

// ControlStatus is the decoder state and decoded site identity.
type ControlStatus struct {
	LastDecode  time.Time
	Detail      string
	FrequencyHz uint64
	WACN        uint32
	SystemID    uint32
	RFSS        uint32
	SiteID      uint32
	NAC         uint32
	State       ControlState
	Stale       bool
}

// EventKind identifies the payload of an Event.
type EventKind uint8

// EventKind values.
const (
	EventNone EventKind = iota
	EventGrant
	EventControl
	EventSite
)

// Event is one scanner event delivered to subscribers.
type Event struct {
	Time time.Time
	// Site is the new selection for EventSite (nil: no site).
	Site    *Selection
	Grant   Grant
	Control ControlStatus
	Kind    EventKind
}

// Status is a point-in-time scanner status.
type Status struct {
	GeneratedAt time.Time
	Selection   *Selection
	Receivers   []receivers.Receiver
	Position    Position
	Control     ControlStatus
	Enabled     bool
	// AircraftStale is true when no aircraft feed is fresh.
	AircraftStale bool
}

// Status returns the current status. A disabled manager reports only
// Enabled false.
func (m *Manager) Status() Status {
	now := m.now()
	feeds := m.tracker.Feeds(now)

	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.enabled {
		return Status{GeneratedAt: now}
	}

	st := Status{
		GeneratedAt:   now,
		Enabled:       true,
		Receivers:     slices.Clone(m.receivers),
		Position:      m.position,
		Control:       m.control,
		AircraftStale: true,
	}

	for _, f := range feeds {
		if !f.Stale {
			st.AircraftStale = false
		}
	}

	if m.selection != nil {
		sel := *m.selection
		st.Selection = &sel

		if st.Position.Source == PositionNone && sel.Mode == config.ScannerModeManual {
			st.Position = Position{Source: PositionManual, Lat: sel.Site.Lat, Lon: sel.Site.Lon, Stale: false}
		}
	}

	return st
}

// Aircraft returns merged aircraft (appended to dst) and feed health.
func (m *Manager) Aircraft(dst []aircraft.Aircraft, includeStale bool) ([]aircraft.Aircraft, []aircraft.FeedStatus, time.Time) {
	now := m.now()

	return m.tracker.Snapshot(dst, now, includeStale), m.tracker.Feeds(now), now
}

// Grants returns up to limit recent calls (all when limit <= 0), newest
// first, and the history capacity.
func (m *Manager) Grants(limit int) ([]Grant, int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.grants.newest(limit), m.grants.capacity()
}

// Reference returns the position sites are ranked from: the manager's
// current position when running, else a direct read of the GPS source.
func (m *Manager) Reference() Position {
	m.mu.Lock()
	enabled, pos, cfg := m.enabled, m.position, m.cfg
	m.mu.Unlock()

	if enabled {
		return pos
	}

	if m.gps == nil {
		return Position{Stale: true}
	}

	r := m.gps.GetPosition()
	if !r.Valid || r.Mode < 2 {
		return Position{Stale: true}
	}

	staleAfter := cmpOr(cfg.PositionStaleAfter, config.DefaultScannerPositionStaleAfter)

	return Position{
		Source: PositionGPSD, Lat: r.Latitude, Lon: r.Longitude, FixTime: r.Timestamp,
		Stale: m.now().Sub(r.Timestamp) > staleAfter,
	}
}

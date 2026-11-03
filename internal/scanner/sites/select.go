package sites

import (
	"cmp"
	"math"
	"slices"
)

// earthRadiusKm is the mean Earth radius used for great-circle distance.
const earthRadiusKm = 6371.0088

// DistanceKm is the haversine great-circle distance between two points in
// decimal degrees.
func DistanceKm(lat1, lon1, lat2, lon2 float64) float64 {
	const rad = math.Pi / 180

	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)

	return 2 * earthRadiusKm * math.Asin(math.Min(1, math.Sqrt(a)))
}

// StateFilter reports whether a site's state is allowed. A nil or empty
// filter allows every state.
type StateFilter []string

// Allows reports whether state passes the filter.
func (f StateFilter) Allows(state string) bool {
	return len(f) == 0 || slices.Contains(f, state)
}

// Ranked is a site with its distance from a reference point.
type Ranked struct {
	Site       *Site
	DistanceKm float64
}

// Nearest returns the site nearest to (lat, lon) that passes filter.
func Nearest(all []Site, lat, lon float64, filter StateFilter) (Ranked, bool) {
	best := Ranked{DistanceKm: math.Inf(1)}

	for i := range all {
		s := &all[i]
		if !filter.Allows(s.State) {
			continue
		}

		if d := DistanceKm(lat, lon, s.Lat, s.Lon); d < best.DistanceKm {
			best = Ranked{Site: s, DistanceKm: d}
		}
	}

	return best, best.Site != nil
}

// Rank returns the sites passing filter sorted by distance from (lat, lon),
// and the number that passed before limit was applied. limit <= 0 returns
// all of them.
func Rank(all []Site, lat, lon float64, filter StateFilter, limit int) ([]Ranked, int) {
	out := make([]Ranked, 0, len(all))

	for i := range all {
		s := &all[i]
		if filter.Allows(s.State) {
			out = append(out, Ranked{Site: s, DistanceKm: DistanceKm(lat, lon, s.Lat, s.Lon)})
		}
	}

	slices.SortFunc(out, func(a, b Ranked) int {
		return cmp.Or(cmp.Compare(a.DistanceKm, b.DistanceKm), cmp.Compare(a.Site.Key, b.Site.Key))
	})

	total := len(out)
	if limit > 0 && limit < total {
		out = out[:limit]
	}

	return out, total
}

// Reason says why Selector chose its current site.
type Reason uint8

// Reason values.
const (
	ReasonNone Reason = iota
	// ReasonNearest is the nearest site to a fresh fix.
	ReasonNearest
	// ReasonHysteresis kept the previous site: a nearer one is not nearer
	// by more than the hysteresis distance.
	ReasonHysteresis
	// ReasonLastKnown kept the previous site because the fix is stale or
	// missing.
	ReasonLastKnown
	// ReasonManual is the pinned manual site.
	ReasonManual
	// ReasonStaleFix chose the nearest site to a stale fix because nothing
	// was selected yet.
	ReasonStaleFix
)

// Fix is a reference position for Selector.Update.
type Fix struct {
	Lat   float64
	Lon   float64
	Valid bool
	// Fresh is false when the fix is older than the stale timeout.
	Fresh bool
}

// Selector holds the current site choice and applies hysteresis. It is not
// safe for concurrent use; the scanner manager serializes calls.
type Selector struct {
	current *Site
	// HysteresisKm is how much nearer a candidate must be to replace the
	// current site.
	HysteresisKm float64
	reason       Reason
}

// Current returns the selected site (nil when none) and why it was chosen.
func (s *Selector) Current() (*Site, Reason) { return s.current, s.reason }

// Reset forgets the current selection.
func (s *Selector) Reset() { s.current, s.reason = nil, ReasonNone }

// Pin selects site manually.
func (s *Selector) Pin(site *Site) { s.current, s.reason = site, ReasonManual }

// Update re-evaluates the selection for fix and reports whether the
// selected site changed. Only a fresh fix can move the selection off an
// existing site, and only when the nearest site is nearer than the current
// one by more than HysteresisKm. A current site outside filter is dropped.
func (s *Selector) Update(all []Site, fix Fix, filter StateFilter) bool {
	prev := s.current
	if prev != nil && (!filter.Allows(prev.State) || s.reason == ReasonManual) {
		s.current, s.reason = nil, ReasonNone
	}

	if !fix.Valid {
		if s.current != nil {
			s.reason = ReasonLastKnown
		}

		return s.current != prev
	}

	best, ok := Nearest(all, fix.Lat, fix.Lon, filter)
	if !ok {
		s.current, s.reason = nil, ReasonNone

		return prev != nil
	}

	switch {
	case s.current == nil && fix.Fresh:
		s.current, s.reason = best.Site, ReasonNearest
	case s.current == nil:
		s.current, s.reason = best.Site, ReasonStaleFix
	case !fix.Fresh:
		s.reason = ReasonLastKnown
	case best.Site == s.current:
		s.reason = ReasonNearest
	default:
		curDist := DistanceKm(fix.Lat, fix.Lon, s.current.Lat, s.current.Lon)
		if curDist-best.DistanceKm > s.HysteresisKm {
			s.current, s.reason = best.Site, ReasonNearest
		} else {
			s.reason = ReasonHysteresis
		}
	}

	return s.current != prev
}

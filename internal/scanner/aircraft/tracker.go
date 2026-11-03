package aircraft

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
)

// Source is a bit set of the receivers that heard an aircraft.
type Source uint8

// Source bits.
const (
	Source1090 Source = 1 << iota
	Source978
	SourceBoth = Source1090 | Source978
)

// Defaults for NewTracker.
const (
	DefaultMaxAircraft = 512
	DefaultStaleAfter  = 60 * time.Second
)

// feedIndex maps a single-bit Source to its slot.
func feedIndex(s Source) int {
	if s == Source978 {
		return 1
	}

	return 0
}

// report is one feed's latest view of an aircraft.
type report struct {
	lastSeen time.Time
	posSeen  time.Time
	callsign string
	squawk   string
	lat      float64
	lon      float64
	gs       float64
	track    float64
	rssi     float64
	altFt    int32
	vrate    int32
	hasPos   bool
	hasAlt   bool
	onGround bool
	hasGS    bool
	hasTrack bool
	hasVRate bool
	hasRSSI  bool
}

type entry struct {
	icao    string
	reports [2]report
}

func (e *entry) lastSeen() time.Time {
	a, b := e.reports[0].lastSeen, e.reports[1].lastSeen
	if b.After(a) {
		return b
	}

	return a
}

// Aircraft is a merged aircraft.
type Aircraft struct {
	LastSeen time.Time
	ICAO     string
	Callsign string
	Squawk   string
	Lat      float64
	Lon      float64
	GS       float64
	Track    float64
	RSSI     float64
	AltFt    int32
	VRate    int32
	Sources  Source
	HasPos   bool
	HasAlt   bool
	OnGround bool
	HasGS    bool
	HasTrack bool
	HasVRate bool
	HasRSSI  bool
	Stale    bool
}

// FeedStatus is the health of one aircraft.json producer.
type FeedStatus struct {
	// Updated is the producer's own "now" from the last successful read.
	Updated  time.Time
	Err      error
	Path     string
	Aircraft int
	Source   Source
	Stale    bool
}

// Feed is one aircraft.json to poll.
type Feed struct {
	// Path is relative to the polled fs.FS root (no leading slash for
	// os.DirFS("/")).
	Path   string
	Source Source
}

type feedState struct {
	updated  time.Time
	err      error
	path     string
	buf      []rawAircraft
	count    int
	lastNow  float64
	source   Source
	hasValue bool
}

// Tracker merges feeds into a bounded aircraft table. Poll is called from
// one goroutine; Snapshot and Feeds may be called concurrently with it.
type Tracker struct { //nolint:govet // fieldalignment: mu stays above the fields it guards per .claude/rules/concurrency.md
	// pollMu serializes Poll so the per-feed decode buffers are reused
	// without copying.
	pollMu sync.Mutex

	mu         sync.Mutex // protects the fields below
	aircraft   map[string]*entry
	feeds      []feedState
	max        int
	staleAfter time.Duration
}

// NewTracker returns a tracker for feeds holding at most maxAircraft
// (DefaultMaxAircraft when <= 0) aircraft, marking aircraft stale after
// staleAfter (DefaultStaleAfter when <= 0) and evicting them after twice
// that.
func NewTracker(feeds []Feed, maxAircraft int, staleAfter time.Duration) *Tracker {
	if maxAircraft <= 0 {
		maxAircraft = DefaultMaxAircraft
	}

	if staleAfter <= 0 {
		staleAfter = DefaultStaleAfter
	}

	t := &Tracker{
		aircraft:   make(map[string]*entry, maxAircraft),
		feeds:      make([]feedState, 0, len(feeds)),
		max:        maxAircraft,
		staleAfter: staleAfter,
	}
	for _, f := range feeds {
		t.feeds = append(t.feeds, feedState{path: f.Path, source: f.Source})
	}

	return t
}

// SetStaleAfter changes the stale timeout (<= 0 keeps the current one).
func (t *Tracker) SetStaleAfter(d time.Duration) {
	if d <= 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.staleAfter = d
}

// Poll reads every feed from fsys and merges it, then evicts aircraft not
// heard for twice the stale timeout. A file whose "now" has not advanced
// since the last poll is not re-merged.
func (t *Tracker) Poll(fsys fs.FS, now time.Time) {
	t.pollMu.Lock()
	defer t.pollMu.Unlock()

	for i := range t.feeds {
		f := &t.feeds[i]
		raw, err := readFeed(fsys, f.path, f.buf)

		f.buf = raw.Aircraft[:0]
		t.merge(f, &raw, err)
	}

	t.mu.Lock()
	t.evictLocked(now)
	t.mu.Unlock()
}

func readFeed(fsys fs.FS, path string, buf []rawAircraft) (rawFile, error) {
	full := buf[:cap(buf)]
	clear(full)

	raw := rawFile{Aircraft: full[:0]}

	fh, err := fsys.Open(path)
	if err != nil {
		return raw, fmt.Errorf("open %s: %w", path, err)
	}
	defer fh.Close()

	if err = json.NewDecoder(fh).Decode(&raw); err != nil {
		return raw, fmt.Errorf("decode %s: %w", path, err)
	}

	if raw.Now <= 0 {
		return raw, fmt.Errorf("decode %s: %w", path, errNoNow)
	}

	return raw, nil
}

var errNoNow = errors.New(`missing "now" timestamp`)

func unixFloat(s float64) time.Time {
	sec, frac := math.Modf(s)

	return time.Unix(int64(sec), int64(frac*1e9))
}

func (t *Tracker) merge(f *feedState, raw *rawFile, readErr error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	f.err = readErr
	if readErr != nil {
		return
	}

	if f.hasValue && raw.Now == f.lastNow {
		return
	}

	f.lastNow, f.hasValue = raw.Now, true
	f.updated = unixFloat(raw.Now)
	f.count = len(raw.Aircraft)
	slot := feedIndex(f.source)

	for i := range raw.Aircraft {
		a := &raw.Aircraft[i]

		icao := normalizeHex(a.Hex)
		if icao == "" {
			continue
		}

		e := t.aircraft[icao]
		if e == nil {
			if len(t.aircraft) >= t.max {
				t.evictOldestLocked()
			}

			e = &entry{icao: icao}
			t.aircraft[icao] = e
		}

		e.reports[slot] = toReport(a, f.updated)
	}
}

func toReport(a *rawAircraft, fileNow time.Time) report {
	r := report{
		lastSeen: fileNow,
		callsign: strings.TrimSpace(a.Flight),
		squawk:   strings.TrimSpace(a.Squawk),
		gs:       a.GS.V,
		hasGS:    a.GS.OK,
		track:    a.Track.V,
		hasTrack: a.Track.OK,
		rssi:     a.RSSI.V,
		hasRSSI:  a.RSSI.OK,
		onGround: a.AltBaro.Ground,
	}

	if a.Seen.OK {
		r.lastSeen = fileNow.Add(-time.Duration(a.Seen.V * float64(time.Second)))
	}

	if a.Lat.OK && a.Lon.OK {
		r.lat, r.lon, r.hasPos = a.Lat.V, a.Lon.V, true

		r.posSeen = r.lastSeen
		if a.SeenPos.OK {
			r.posSeen = fileNow.Add(-time.Duration(a.SeenPos.V * float64(time.Second)))
		}
	}

	switch {
	case a.AltBaro.OK:
		r.altFt, r.hasAlt = int32(math.Round(a.AltBaro.Ft)), true
	case a.AltGeom.OK && !a.AltBaro.Ground:
		r.altFt, r.hasAlt = int32(math.Round(a.AltGeom.V)), true
	}

	for _, v := range []optFloat{a.BaroRate, a.GeomRate, a.VertRate} {
		if v.OK {
			r.vrate, r.hasVRate = int32(math.Round(v.V)), true

			break
		}
	}

	return r
}

func (t *Tracker) evictOldestLocked() {
	var (
		oldestKey string
		oldest    time.Time
	)

	for k, e := range t.aircraft {
		if ls := e.lastSeen(); oldestKey == "" || ls.Before(oldest) {
			oldestKey, oldest = k, ls
		}
	}

	delete(t.aircraft, oldestKey)
}

func (t *Tracker) evictLocked(now time.Time) {
	cutoff := now.Add(-2 * t.staleAfter)

	for k, e := range t.aircraft {
		if e.lastSeen().Before(cutoff) {
			delete(t.aircraft, k)
		}
	}
}

// Len returns the number of tracked aircraft.
func (t *Tracker) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	return len(t.aircraft)
}

// Snapshot returns merged aircraft sorted by ICAO address, appended to dst
// (which may be nil). Stale aircraft are included only when includeStale.
func (t *Tracker) Snapshot(dst []Aircraft, now time.Time, includeStale bool) []Aircraft {
	t.mu.Lock()
	defer t.mu.Unlock()

	dst = slices.Grow(dst[:0], len(t.aircraft))
	cutoff := now.Add(-t.staleAfter)

	for _, e := range t.aircraft {
		a := mergeEntry(e, cutoff)
		if a.Stale && !includeStale {
			continue
		}

		dst = append(dst, a)
	}

	slices.SortFunc(dst, func(a, b Aircraft) int { return cmp.Compare(a.ICAO, b.ICAO) })

	return dst
}

// mergeEntry combines the two feed reports: scalar fields come from the
// most recently heard report and fall back to the other; the position is
// the most recent one either feed has.
func mergeEntry(e *entry, cutoff time.Time) Aircraft {
	newer, older := &e.reports[0], &e.reports[1]
	if older.lastSeen.After(newer.lastSeen) {
		newer, older = older, newer
	}

	a := Aircraft{ICAO: e.icao, LastSeen: newer.lastSeen, OnGround: newer.onGround}

	for i, r := range e.reports {
		if !r.lastSeen.IsZero() && !r.lastSeen.Before(cutoff) {
			a.Sources |= Source(1 << i)
		}
	}

	a.Stale = a.Sources == 0
	if a.Stale {
		// Still report which receivers last heard it.
		for i, r := range e.reports {
			if !r.lastSeen.IsZero() {
				a.Sources |= Source(1 << i)
			}
		}
	}

	a.Callsign = cmp.Or(newer.callsign, older.callsign)
	a.Squawk = cmp.Or(newer.squawk, older.squawk)

	pos := newer
	if !pos.hasPos || (older.hasPos && older.posSeen.After(pos.posSeen)) {
		pos = older
	}

	a.Lat, a.Lon, a.HasPos = pos.lat, pos.lon, pos.hasPos

	pick := func(nOK, oOK bool) *report {
		if nOK || !oOK {
			return newer
		}

		return older
	}

	r := pick(newer.hasAlt, older.hasAlt)
	a.AltFt, a.HasAlt = r.altFt, r.hasAlt
	r = pick(newer.hasGS, older.hasGS)
	a.GS, a.HasGS = r.gs, r.hasGS
	r = pick(newer.hasTrack, older.hasTrack)
	a.Track, a.HasTrack = r.track, r.hasTrack
	r = pick(newer.hasVRate, older.hasVRate)
	a.VRate, a.HasVRate = r.vrate, r.hasVRate

	// RSSI: strongest of the feeds that currently hear the aircraft.
	for i := range e.reports {
		rep := &e.reports[i]
		if rep.hasRSSI && (rep.lastSeen.Equal(newer.lastSeen) || !rep.lastSeen.Before(cutoff)) &&
			(!a.HasRSSI || rep.rssi > a.RSSI) {
			a.RSSI, a.HasRSSI = rep.rssi, true
		}
	}

	return a
}

// Feeds reports feed health; a feed is stale when its last read failed or
// its producer clock is older than the stale timeout.
func (t *Tracker) Feeds(now time.Time) []FeedStatus {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make([]FeedStatus, 0, len(t.feeds))
	for i := range t.feeds {
		f := &t.feeds[i]
		out = append(out, FeedStatus{
			Source:   f.source,
			Path:     f.path,
			Updated:  f.updated,
			Aircraft: f.count,
			Err:      f.err,
			Stale:    f.err != nil || f.updated.IsZero() || now.Sub(f.updated) > t.staleAfter,
		})
	}

	return out
}

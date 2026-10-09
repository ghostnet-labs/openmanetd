// Package pathsel chooses, per destination and traffic class, which radio
// band (HaLow, 2.4 GHz or 5 GHz) carries that traffic, and records why.
//
// Semantics:
//
//   - Exactly one active path per (destination, class). Traffic of a class
//     moves as a whole when the selection changes; there is no bonding and
//     no load splitting across bands.
//   - No duplication by default. Voice may be duplicated onto a second
//     eligible band only when Config.DuplicateVoice is set (off by default);
//     the second band is reported in Decision.Duplicate and is never used
//     for control or bulk.
//   - Hysteresis: a healthy path is kept for at least Config.MinDwell and
//     is only replaced by a candidate that beats it by Config.Margin score
//     points. A path that becomes unreachable, stale or capacity-rejected
//     is replaced immediately (failover ignores dwell).
//   - A manual override pins a band for a (destination, class). It bypasses
//     dwell and margin but never sends traffic into an ineligible path: an
//     ineligible override falls back to the best eligible band and says so.
//   - Priority: control > voice > bulk. Bulk is kept off a band that carries
//     control or voice to the same destination unless that band has at
//     least Config.ProtectedHeadroomKbps of spare capacity, or no other
//     eligible band exists (then bulk shares as best effort).
//
// Decisions are pure functions of the metrics, the optional capacity gate,
// the clock and the selector's own state, so scenario tests are
// deterministic. The package is not wired to transport or RPC yet.
package pathsel

import (
	"errors"
	"fmt"
	"time"
)

// Band is a radio band a path can use.
type Band string

// Bands.
const (
	BandHaLow Band = "halow"
	Band2G4   Band = "2.4"
	Band5G    Band = "5"
)

// String returns the operator-facing band name.
func (b Band) String() string {
	switch b {
	case BandHaLow:
		return "HaLow"
	case Band2G4:
		return "2.4 GHz"
	case Band5G:
		return "5 GHz"
	case "":
		return "none"
	default:
		return string(b)
	}
}

// Class is a traffic class.
type Class string

// Traffic classes, highest priority first.
const (
	ClassControl Class = "control"
	ClassVoice   Class = "voice"
	ClassBulk    Class = "bulk"
)

// Priority returns a larger number for a more important class, 0 if unknown.
func (c Class) Priority() int {
	switch c {
	case ClassControl:
		return 3
	case ClassVoice:
		return 2
	case ClassBulk:
		return 1
	default:
		return 0
	}
}

// Metrics describe one (destination, band) path.
type Metrics struct {
	// UpdatedAt is when the metrics were measured. Metrics older than
	// Config.StaleAfter make the path ineligible.
	UpdatedAt time.Time `json:"updated_at"`
	// Latency and Jitter are one-way estimates.
	Latency time.Duration `json:"latency_ns"`
	Jitter  time.Duration `json:"jitter_ns"`
	// CapacityKbps is the estimated spare capacity.
	CapacityKbps int `json:"capacity_kbps"`
	// LossPermille is packet loss in parts per thousand (0..1000).
	LossPermille int `json:"loss_permille"`
	// Reachable is false when the destination is not reachable on the band.
	Reachable bool `json:"reachable"`
}

// MetricsSource supplies path metrics. ok is false when nothing is known
// for the band, for example when the destination has no radio on it.
type MetricsSource interface {
	PathMetrics(dest string, band Band) (m Metrics, ok bool)
}

// BandAdmitter is the optional radio-capacity input (GHO-59). It reports
// whether the node's physical radio capacity currently admits using band,
// with a machine-readable reason when it does not.
type BandAdmitter interface {
	AdmitBand(band Band) (ok bool, reason string)
}

// Profile is a class's eligibility thresholds. A zero threshold disables
// that check.
type Profile struct {
	MaxLatency      time.Duration
	MaxJitter       time.Duration
	MaxLossPermille int
	MinCapacityKbps int
}

// Config tunes the selector. Zero fields take the defaults listed.
type Config struct {
	// Profiles override per-class thresholds; missing classes use
	// DefaultProfile.
	Profiles map[Class]Profile
	// Bands are the candidate bands. Default: HaLow, 2.4 GHz, 5 GHz.
	Bands []Band
	// MinDwell is the least time a healthy path is kept. Default 10s.
	MinDwell time.Duration
	// StaleAfter is the metrics age at which a path is ineligible. Default 5s.
	StaleAfter time.Duration
	// Margin is the score improvement (0..100 points) a candidate needs to
	// replace a healthy current path after dwell. Default 10.
	Margin float64
	// BulkReferenceKbps is the spare capacity that scores 100 for bulk.
	// Default 20000.
	BulkReferenceKbps int
	// ProtectedHeadroomKbps is the spare capacity a band carrying control
	// or voice to a destination must have before bulk may share it.
	// Default 2000.
	ProtectedHeadroomKbps int
	// MaxPaths bounds remembered (destination, class) entries; the least
	// recently decided entry without an override is evicted. Default 768.
	MaxPaths int
	// DuplicateVoice opts in to duplicating voice on a second band.
	DuplicateVoice bool
}

// maxBands bounds Config.Bands.
const maxBands = 8

// Defaults.
const (
	DefaultMinDwell              = 10 * time.Second
	DefaultStaleAfter            = 5 * time.Second
	DefaultMargin                = 10.0
	DefaultBulkReferenceKbps     = 20000
	DefaultProtectedHeadroomKbps = 2000
	DefaultMaxPaths              = 768
)

// DefaultProfile returns the built-in thresholds for c.
func DefaultProfile(c Class) Profile {
	switch c {
	case ClassControl:
		return Profile{MaxLatency: 2 * time.Second, MaxLossPermille: 300, MinCapacityKbps: 8}
	case ClassVoice:
		return Profile{
			MaxLatency: 150 * time.Millisecond, MaxJitter: 30 * time.Millisecond,
			MaxLossPermille: 50, MinCapacityKbps: 64,
		}
	case ClassBulk:
		return Profile{MaxLossPermille: 200, MinCapacityKbps: 256}
	default:
		return Profile{}
	}
}

// Errors.
var (
	ErrInvalidClass = errors.New("pathsel: unknown traffic class")
	ErrInvalidBand  = errors.New("pathsel: band is not a candidate")
	ErrInvalidDest  = errors.New("pathsel: empty destination")
	ErrInvalidConf  = errors.New("pathsel: invalid config")
)

func validClass(c Class) bool { return c.Priority() > 0 }

// preference orders bands for tie-breaks: control favors range and
// robustness, voice and bulk favor throughput and low latency.
func preference(c Class, b Band) int {
	order := [3]Band{Band5G, Band2G4, BandHaLow}
	if c == ClassControl {
		order = [3]Band{BandHaLow, Band2G4, Band5G}
	}

	for i, o := range order {
		if o == b {
			return i
		}
	}

	return len(order)
}

func (c Config) withDefaults() (Config, error) {
	if c.MinDwell < 0 || c.StaleAfter < 0 || c.Margin < 0 || c.Margin > 100 ||
		c.BulkReferenceKbps < 0 || c.ProtectedHeadroomKbps < 0 || c.MaxPaths < 0 {
		return Config{}, ErrInvalidConf
	}

	if len(c.Bands) == 0 {
		c.Bands = []Band{BandHaLow, Band2G4, Band5G}
	} else {
		c.Bands = append([]Band(nil), c.Bands...)
	}

	if c.MinDwell == 0 {
		c.MinDwell = DefaultMinDwell
	}

	if c.StaleAfter == 0 {
		c.StaleAfter = DefaultStaleAfter
	}

	if c.Margin == 0 {
		c.Margin = DefaultMargin
	}

	if c.BulkReferenceKbps == 0 {
		c.BulkReferenceKbps = DefaultBulkReferenceKbps
	}

	if c.ProtectedHeadroomKbps == 0 {
		c.ProtectedHeadroomKbps = DefaultProtectedHeadroomKbps
	}

	if c.MaxPaths == 0 {
		c.MaxPaths = DefaultMaxPaths
	}

	profiles := make(map[Class]Profile, 3)

	for _, cl := range []Class{ClassControl, ClassVoice, ClassBulk} {
		p, ok := c.Profiles[cl]
		if !ok {
			p = DefaultProfile(cl)
		}

		profiles[cl] = p
	}

	c.Profiles = profiles

	if len(c.Bands) > maxBands {
		return Config{}, fmt.Errorf("%w: at most %d bands", ErrInvalidConf, maxBands)
	}

	return c, nil
}

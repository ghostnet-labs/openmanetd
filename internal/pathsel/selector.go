package pathsel

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Reason is the machine-readable cause of a Decision.
type Reason string

// Decision reasons.
const (
	ReasonInitial         Reason = "initial"             // first selection
	ReasonDwell           Reason = "dwell"               // kept: inside MinDwell
	ReasonHoldMargin      Reason = "hold_margin"         // kept: no candidate beats it by Margin
	ReasonBetter          Reason = "better"              // switched: candidate beat current by Margin
	ReasonFailover        Reason = "failover"            // switched: current path became ineligible
	ReasonOverride        Reason = "override"            // pinned by manual override
	ReasonOverrideBlocked Reason = "override_ineligible" // override band ineligible; best used
	ReasonNoPath          Reason = "no_eligible_path"    // nothing eligible
)

// Ineligibility explains why a candidate band cannot carry the class.
type Ineligibility string

// Candidate ineligibility causes.
const (
	IneligibleNone       Ineligibility = ""
	IneligibleNoMetrics  Ineligibility = "no_metrics"
	IneligibleUnreach    Ineligibility = "unreachable"
	IneligibleStale      Ineligibility = "stale_metrics"
	IneligibleCapacity   Ineligibility = "radio_capacity"
	IneligibleLoss       Ineligibility = "loss_too_high"
	IneligibleLatency    Ineligibility = "latency_too_high"
	IneligibleJitter     Ineligibility = "jitter_too_high"
	IneligibleThroughput Ineligibility = "insufficient_capacity"
	IneligibleProtected  Ineligibility = "protected_for_priority"
)

// Candidate is one band as evaluated for a decision.
type Candidate struct {
	Band Band `json:"band"`
	// Ineligible is empty when the band may carry the class.
	Ineligible Ineligibility `json:"ineligible,omitempty"`
	// Detail adds context, e.g. the capacity gate's reason or metric age.
	Detail  string  `json:"detail,omitempty"`
	Metrics Metrics `json:"metrics"`
	// Score is the class score, 0..100 (higher is better).
	Score    float64 `json:"score"`
	HasData  bool    `json:"has_metrics"`
	Selected bool    `json:"selected"`
}

// Decision is the record of one selection, suitable for display to an EUD.
type Decision struct {
	// Since is when Band became the active path.
	Since     time.Time `json:"since"`
	DecidedAt time.Time `json:"decided_at"`
	// Destination and Class identify the path.
	Destination string `json:"destination"`
	Class       Class  `json:"class"`
	// Band is the single active path; "" when none is eligible.
	Band Band `json:"band"`
	// Previous is the band before this decision when it changed.
	Previous Band `json:"previous,omitempty"`
	// Duplicate is the voice duplicate band (opt-in), "" otherwise.
	Duplicate Band `json:"duplicate,omitempty"`
	// Override is the pinned band, "" when not overridden.
	Override    Band        `json:"override,omitempty"`
	Reason      Reason      `json:"reason"`
	Explanation string      `json:"explanation"`
	Candidates  []Candidate `json:"candidates"`
}

type key struct {
	dest  string
	class Class
}

type pathState struct {
	since     time.Time // when band became active
	decidedAt time.Time // last Select; zero before the first
	band      Band      // active band, "" when none
	override  Band      // pinned band, "" when automatic
	last      Decision
}

// Option configures a Selector.
type Option func(*Selector)

// WithClock injects the time source (tests use a fake clock).
func WithClock(now func() time.Time) Option {
	return func(s *Selector) { s.now = now }
}

// WithCapacity adds the optional radio-capacity gate.
func WithCapacity(a BandAdmitter) Option {
	return func(s *Selector) { s.capacity = a }
}

// Selector picks one active band per (destination, class). It is safe for
// concurrent use. Metrics and capacity inputs are queried without holding
// the selector's lock.
type Selector struct { //nolint:govet // fieldalignment: mu must sit directly above the paths map it guards (.claude/rules/concurrency.md).
	metrics  MetricsSource
	capacity BandAdmitter
	now      func() time.Time
	cfg      Config

	mu    sync.Mutex // protects paths
	paths map[key]*pathState
}

// New returns a Selector. metrics is required.
func New(cfg Config, metrics MetricsSource, opts ...Option) (*Selector, error) {
	if metrics == nil {
		return nil, fmt.Errorf("%w: nil metrics source", ErrInvalidConf)
	}

	c, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}

	s := &Selector{cfg: c, metrics: metrics, now: time.Now, paths: make(map[key]*pathState, 16)}
	for _, o := range opts {
		o(s)
	}

	return s, nil
}

// SetOverride pins band for (dest, class) until ClearOverride.
func (s *Selector) SetOverride(dest string, class Class, band Band) error {
	if err := s.checkKey(dest, class); err != nil {
		return err
	}

	if !slices.Contains(s.cfg.Bands, band) {
		return fmt.Errorf("%w: %q", ErrInvalidBand, band)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.stateLocked(key{dest: dest, class: class})
	st.override = band

	return nil
}

// ClearOverride returns (dest, class) to automatic selection.
func (s *Selector) ClearOverride(dest string, class Class) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if st, ok := s.paths[key{dest: dest, class: class}]; ok {
		st.override = ""
	}
}

// Last returns the most recent decision for (dest, class).
func (s *Selector) Last(dest string, class Class) (Decision, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, ok := s.paths[key{dest: dest, class: class}]
	if !ok || st.decidedAt.IsZero() {
		return Decision{}, false
	}

	return cloneDecision(st.last), true
}

// Forget drops all state, including overrides, for dest.
func (s *Selector) Forget(dest string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, c := range []Class{ClassControl, ClassVoice, ClassBulk} {
		delete(s.paths, key{dest: dest, class: c})
	}
}

// Select evaluates (dest, class) now and returns the decision record.
func (s *Selector) Select(dest string, class Class) (Decision, error) {
	if err := s.checkKey(dest, class); err != nil {
		return Decision{}, err
	}

	now := s.now()
	cands := s.gather(dest, class, now)

	k := key{dest: dest, class: class}

	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.stateLocked(k)
	if class == ClassBulk {
		s.protectLocked(dest, cands)
	}

	d := s.decideLocked(st, class, cands, now)
	d.Destination = dest
	d.Explanation = explain(&d, s.cfg, now)

	st.last = d
	st.decidedAt = now

	return cloneDecision(d), nil
}

func (s *Selector) checkKey(dest string, class Class) error {
	if dest == "" {
		return ErrInvalidDest
	}

	if !validClass(class) {
		return fmt.Errorf("%w: %q", ErrInvalidClass, class)
	}

	return nil
}

// gather evaluates every candidate band without holding s.mu.
func (s *Selector) gather(dest string, class Class, now time.Time) []Candidate {
	prof := s.cfg.Profiles[class]
	cands := make([]Candidate, 0, len(s.cfg.Bands))

	for _, b := range s.cfg.Bands {
		c := Candidate{Band: b}

		m, ok := s.metrics.PathMetrics(dest, b)
		c.HasData = ok
		c.Metrics = m

		c.Ineligible, c.Detail = s.eligibility(prof, b, m, ok, now)
		if ok {
			c.Score = score(class, m, s.cfg.BulkReferenceKbps)
		}

		cands = append(cands, c)
	}

	return cands
}

func (s *Selector) eligibility(prof Profile, b Band, m Metrics, ok bool, now time.Time) (Ineligibility, string) {
	switch {
	case !ok:
		return IneligibleNoMetrics, ""
	case !m.Reachable:
		return IneligibleUnreach, ""
	case now.Sub(m.UpdatedAt) > s.cfg.StaleAfter:
		return IneligibleStale, "age " + now.Sub(m.UpdatedAt).Round(time.Millisecond).String()
	}

	if s.capacity != nil {
		if admitted, reason := s.capacity.AdmitBand(b); !admitted {
			return IneligibleCapacity, reason
		}
	}

	switch {
	case prof.MaxLossPermille > 0 && m.LossPermille > prof.MaxLossPermille:
		return IneligibleLoss, strconv.Itoa(m.LossPermille) + "‰"
	case prof.MaxLatency > 0 && m.Latency > prof.MaxLatency:
		return IneligibleLatency, m.Latency.String()
	case prof.MaxJitter > 0 && m.Jitter > prof.MaxJitter:
		return IneligibleJitter, m.Jitter.String()
	case prof.MinCapacityKbps > 0 && m.CapacityKbps < prof.MinCapacityKbps:
		return IneligibleThroughput, strconv.Itoa(m.CapacityKbps) + " kbps"
	}

	return IneligibleNone, ""
}

// protectLocked keeps bulk off a band carrying control or voice to the same
// destination unless the band has the protected headroom. When every
// eligible band would be protected, bulk keeps them all (lowest priority,
// best effort) rather than losing service, and the detail says so.
func (s *Selector) protectLocked(dest string, cands []Candidate) {
	var protect [8]bool // len(cands) is the number of configured bands

	if len(cands) > len(protect) {
		return
	}

	eligible, protected := 0, 0

	for i := range cands {
		cd := &cands[i]
		if cd.Ineligible != IneligibleNone {
			continue
		}

		eligible++

		if by := s.priorityUserLocked(dest, cd.Band); by != "" && cd.Metrics.CapacityKbps < s.cfg.ProtectedHeadroomKbps {
			protect[i] = true
			protected++
			cd.Detail = "carries " + string(by)
		}
	}

	shareAll := protected == eligible

	for i := range cands {
		if !protect[i] {
			continue
		}

		if shareAll {
			cands[i].Detail += "; shared, no alternative"

			continue
		}

		cands[i].Ineligible = IneligibleProtected
	}
}

// priorityUserLocked returns the higher-priority class whose active path to
// dest is band, or "".
func (s *Selector) priorityUserLocked(dest string, band Band) Class {
	for _, c := range [2]Class{ClassControl, ClassVoice} {
		if st, ok := s.paths[key{dest: dest, class: c}]; ok && st.band == band {
			return c
		}
	}

	return ""
}

func (s *Selector) decideLocked(st *pathState, class Class, cands []Candidate, now time.Time) Decision {
	d := Decision{
		DecidedAt: now, Class: class, Candidates: cands,
		Override: st.override, Previous: st.band,
	}
	cur := findCandidate(cands, st.band)
	best := bestCandidate(cands, class, st.band)

	var chosen Band

	switch {
	case st.override != "":
		if o := findCandidate(cands, st.override); o != nil && o.Ineligible == IneligibleNone {
			chosen, d.Reason = st.override, ReasonOverride
		} else {
			chosen, d.Reason = bandOf(best), ReasonOverrideBlocked
		}
	case best == nil:
		d.Reason = ReasonNoPath
	case cur == nil:
		chosen, d.Reason = best.Band, ReasonInitial
	case cur.Ineligible != IneligibleNone:
		chosen, d.Reason = best.Band, ReasonFailover
	case now.Sub(st.since) < s.cfg.MinDwell:
		chosen, d.Reason = cur.Band, ReasonDwell
	case best.Band != cur.Band && best.Score >= cur.Score+s.cfg.Margin:
		chosen, d.Reason = best.Band, ReasonBetter
	default:
		chosen, d.Reason = cur.Band, ReasonHoldMargin
	}

	if chosen == "" && d.Reason == ReasonOverrideBlocked {
		d.Reason = ReasonNoPath
	}

	if chosen != st.band {
		st.band = chosen
		st.since = now
	}

	d.Band, d.Since = st.band, st.since
	if d.Previous == d.Band {
		d.Previous = ""
	}

	for i := range d.Candidates {
		d.Candidates[i].Selected = d.Candidates[i].Band == d.Band
	}

	if d.Class == ClassVoice && s.cfg.DuplicateVoice && d.Band != "" {
		if dup := bestCandidate(cands, ClassVoice, "", d.Band); dup != nil {
			d.Duplicate = dup.Band
		}
	}

	return d
}

// stateLocked returns the state for k, creating it and evicting the least
// recently decided override-free entry when over MaxPaths.
func (s *Selector) stateLocked(k key) *pathState {
	if st, ok := s.paths[k]; ok {
		return st
	}

	if len(s.paths) >= s.cfg.MaxPaths {
		var (
			oldest key
			found  bool
			at     time.Time
		)

		for pk, ps := range s.paths {
			if ps.override != "" {
				continue
			}

			if !found || ps.decidedAt.Before(at) {
				oldest, at, found = pk, ps.decidedAt, true
			}
		}

		if found {
			delete(s.paths, oldest)
		}
	}

	st := &pathState{}
	s.paths[k] = st

	return st
}

func findCandidate(cands []Candidate, b Band) *Candidate {
	if b == "" {
		return nil
	}

	for i := range cands {
		if cands[i].Band == b {
			return &cands[i]
		}
	}

	return nil
}

func bandOf(c *Candidate) Band {
	if c == nil {
		return ""
	}

	return c.Band
}

// bestCandidate returns the highest-scoring eligible candidate, breaking
// ties by keeping incumbent, then by class band preference. exclude bands
// are skipped.
func bestCandidate(cands []Candidate, class Class, incumbent Band, exclude ...Band) *Candidate {
	var best *Candidate

	for i := range cands {
		c := &cands[i]
		if c.Ineligible != IneligibleNone || slices.Contains(exclude, c.Band) {
			continue
		}

		if best == nil || better(c, best, class, incumbent) {
			best = c
		}
	}

	return best
}

func better(a, b *Candidate, class Class, incumbent Band) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}

	if (a.Band == incumbent) != (b.Band == incumbent) {
		return a.Band == incumbent
	}

	return preference(class, a.Band) < preference(class, b.Band)
}

// score maps metrics to 0..100 for class. Control favors low loss
// (reliability), voice favors low latency and jitter, bulk favors spare
// capacity after loss.
func score(class Class, m Metrics, bulkRefKbps int) float64 {
	lossPct := float64(m.LossPermille) / 10
	latMs := float64(m.Latency) / float64(time.Millisecond)
	jitMs := float64(m.Jitter) / float64(time.Millisecond)

	var v float64

	switch class {
	case ClassControl:
		v = 100 - 2*lossPct - latMs/20
	case ClassVoice:
		v = 100 - latMs/3 - 2*jitMs - 5*lossPct
	case ClassBulk:
		v = 100 * float64(m.CapacityKbps) * (1 - lossPct/100) / float64(bulkRefKbps)
	}

	return min(100, max(0, v))
}

func cloneDecision(d Decision) Decision {
	d.Candidates = slices.Clone(d.Candidates)

	return d
}

func explain(d *Decision, cfg Config, now time.Time) string {
	var b strings.Builder

	b.WriteString(string(d.Class))
	b.WriteString(" to ")
	b.WriteString(d.Destination)

	if d.Band == "" {
		b.WriteString(": no eligible path")
	} else {
		b.WriteString(" via ")
		b.WriteString(d.Band.String())

		if c := findCandidate(d.Candidates, d.Band); c != nil {
			b.WriteString(" (score ")
			b.WriteString(strconv.FormatFloat(c.Score, 'f', 1, 64))

			if c.Detail != "" {
				b.WriteString(", ")
				b.WriteString(c.Detail)
			}

			b.WriteByte(')')
		}
	}

	b.WriteString("; ")
	b.WriteString(reasonText(d, cfg, now))

	if d.Duplicate != "" {
		b.WriteString("; duplicated on ")
		b.WriteString(d.Duplicate.String())
	}

	for i := range d.Candidates {
		c := &d.Candidates[i]
		if c.Selected {
			continue
		}

		b.WriteString("; ")
		b.WriteString(c.Band.String())

		if c.Ineligible == IneligibleNone {
			b.WriteString(" score ")
			b.WriteString(strconv.FormatFloat(c.Score, 'f', 1, 64))

			continue
		}

		b.WriteString(" ineligible: ")
		b.WriteString(string(c.Ineligible))

		if c.Detail != "" {
			b.WriteString(" (")
			b.WriteString(c.Detail)
			b.WriteByte(')')
		}
	}

	return b.String()
}

func reasonText(d *Decision, cfg Config, now time.Time) string {
	switch d.Reason {
	case ReasonInitial:
		return "initial selection"
	case ReasonDwell:
		left := cfg.MinDwell - now.Sub(d.Since)

		return "kept: minimum dwell, " + left.Round(time.Millisecond).String() + " left"
	case ReasonHoldMargin:
		return "kept: no alternative better by " + strconv.FormatFloat(cfg.Margin, 'f', 1, 64) + " points"
	case ReasonBetter:
		return "switched from " + d.Previous.String() + ": better by at least " +
			strconv.FormatFloat(cfg.Margin, 'f', 1, 64) + " points"
	case ReasonFailover:
		return "failed over from " + d.Previous.String() + ": previous path ineligible"
	case ReasonOverride:
		return "manual override"
	case ReasonOverrideBlocked:
		return "manual override to " + d.Override.String() + " is ineligible; using best eligible path"
	case ReasonNoPath:
		return "no band is eligible"
	default:
		return string(d.Reason)
	}
}

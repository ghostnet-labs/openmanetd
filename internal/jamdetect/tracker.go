package jamdetect

import (
	"strconv"
	"time"
)

// State is a radio's detection state.
type State int

// States, in escalation order.
const (
	StateWarmingUp State = iota + 1
	StateClear
	StateSuspected
	StateJammed
)

// String returns the lowercase state name.
func (s State) String() string {
	switch s {
	case StateWarmingUp:
		return "warming_up"
	case StateClear:
		return "clear"
	case StateSuspected:
		return "suspected"
	case StateJammed:
		return "jammed"
	default:
		return "unknown"
	}
}

// Reason names a reading that made a sample abnormal.
type Reason int

// Reasons.
const (
	ReasonNoiseRise Reason = iota + 1
	ReasonNoiseHigh
	ReasonBusyRise
	ReasonBusyHigh
)

// String returns the lowercase reason name.
func (r Reason) String() string {
	switch r {
	case ReasonNoiseRise:
		return "noise_rise"
	case ReasonNoiseHigh:
		return "noise_high"
	case ReasonBusyRise:
		return "busy_rise"
	case ReasonBusyHigh:
		return "busy_high"
	default:
		return "unknown"
	}
}

// Thresholds tune detection. Zero fields take the Default values.
type Thresholds struct {
	// NoiseRiseDB marks a sample abnormal when the noise floor is this
	// many dB above its baseline.
	NoiseRiseDB float64
	// NoiseCeilingDBm marks a sample abnormal when the noise floor is at
	// or above it, baseline or not (catches a jammer present at boot).
	NoiseCeilingDBm float64
	// BusyRisePct marks a sample abnormal when unexplained busy time is
	// this many percentage points above its baseline and at least
	// BusyFloorPct.
	BusyRisePct  float64
	BusyFloorPct float64
	// BusyCeilingPct marks a sample abnormal when unexplained busy time is
	// at or above it, baseline or not.
	BusyCeilingPct float64
	// WarmupSamples is how many normal samples are learned before the
	// relative checks apply.
	WarmupSamples int
	// SuspectSamples, JamSamples and ClearSamples are the consecutive
	// sample counts for SUSPECTED, JAMMED and back to CLEAR.
	SuspectSamples int
	JamSamples     int
	ClearSamples   int
	// Alpha is the baseline EWMA weight once warmed up.
	Alpha float64
}

// Default thresholds. With the default 5 s interval a radio is SUSPECTED
// after 10 s of abnormal readings, JAMMED after 20 s and CLEAR after 30 s
// of normal readings; learning takes one minute.
const (
	DefaultNoiseRiseDB     = 10.0
	DefaultNoiseCeilingDBm = -70.0
	DefaultBusyRisePct     = 30.0
	DefaultBusyFloorPct    = 40.0
	DefaultBusyCeilingPct  = 85.0
	DefaultWarmupSamples   = 12
	DefaultSuspectSamples  = 2
	DefaultJamSamples      = 4
	DefaultClearSamples    = 6
	DefaultAlpha           = 0.05
)

// withDefaults fills zero fields.
func (t Thresholds) withDefaults() Thresholds {
	setF := func(v *float64, d float64) {
		if *v == 0 {
			*v = d
		}
	}
	setI := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}

	setF(&t.NoiseRiseDB, DefaultNoiseRiseDB)
	setF(&t.NoiseCeilingDBm, DefaultNoiseCeilingDBm)
	setF(&t.BusyRisePct, DefaultBusyRisePct)
	setF(&t.BusyFloorPct, DefaultBusyFloorPct)
	setF(&t.BusyCeilingPct, DefaultBusyCeilingPct)
	setF(&t.Alpha, DefaultAlpha)
	setI(&t.WarmupSamples, DefaultWarmupSamples)
	setI(&t.SuspectSamples, DefaultSuspectSamples)
	setI(&t.JamSamples, DefaultJamSamples)
	setI(&t.ClearSamples, DefaultClearSamples)

	if t.JamSamples < t.SuspectSamples {
		t.JamSamples = t.SuspectSamples
	}

	return t
}

// Reading is a radio's latest channel reading and detection state.
type Reading struct {
	Updated time.Time
	Radio   string
	PHY     string
	Band    Band
	Reasons []Reason
	// FreqMHz is the in-use channel as the driver reports it.
	FreqMHz float64
	// BaselineNoiseDBm, BaselineUnexplainedPct are the learned normal
	// values (zero until the first normal sample).
	BaselineNoiseDBm       float64
	BusyPct                float64
	UnexplainedBusyPct     float64
	BaselineUnexplainedPct float64
	State                  State
	NoiseDBm               int
	NoiseKnown             bool
	// BusyKnown is false until two samples with busy counters exist.
	BusyKnown bool
}

// Event is one jamming episode on one radio of this node. ID is stable for
// the whole episode. State is StateSuspected or StateJammed while open and
// StateClear once over (Cleared is then set).
type Event struct {
	Started            time.Time
	Updated            time.Time
	Cleared            time.Time
	ID                 string
	Radio              string
	PHY                string
	Band               Band
	Reasons            []Reason
	FreqMHz            float64
	BaselineNoiseDBm   float64
	BusyPct            float64
	BaselineBusyPct    float64
	UnexplainedBusyPct float64
	PeakUnexplainedPct float64
	NoiseDBm           int
	PeakNoiseDBm       int
	State              State
	NoiseKnown         bool
}

// Open reports whether the episode is still running.
func (e *Event) Open() bool { return e.State == StateSuspected || e.State == StateJammed }

// tracker is one radio's detection state. It is owned by the Detector's
// run goroutine; the Detector copies its reading out under its mutex.
type tracker struct {
	episode *Event
	reading Reading
	th      Thresholds
	prev    Survey

	learnedNoise int
	learnedBusy  int
	abnormal     int
	normal       int
	hasPrev      bool
}

func newTracker(r Radio, th Thresholds) *tracker {
	return &tracker{
		th: th,
		reading: Reading{
			Radio: r.Ifname,
			PHY:   r.PHY,
			State: StateWarmingUp,
		},
	}
}

// observe feeds one in-use survey entry taken at now and appends to out
// the episode events to publish (an episode opened, escalated or closed).
// halow selects the band.
func (t *tracker) observe(s Survey, halow bool, now time.Time, out []Event) []Event {
	if t.hasPrev && s.FreqMHz != t.prev.FreqMHz {
		out = appendEvent(out, t.closeEpisode(now))
		t.resetLearning()
	}

	rd := &t.reading
	rd.Updated = now
	rd.FreqMHz = s.FreqMHz
	rd.Band = BandFor(halow, s.FreqMHz)
	rd.NoiseKnown = s.HasNoise && s.NoiseDBm != 0
	rd.NoiseDBm = s.NoiseDBm
	rd.Reasons = rd.Reasons[:0]

	busyOK := t.updateBusy(s)
	t.prev = s
	t.hasPrev = true

	if !rd.NoiseKnown && !busyOK {
		return out
	}

	t.classify(busyOK)

	return appendEvent(out, t.step(now))
}

// close ends the open episode because the radio went away.
func (t *tracker) close(now time.Time, out []Event) []Event {
	return appendEvent(out, t.closeEpisode(now))
}

func appendEvent(out []Event, ev *Event) []Event {
	if ev == nil {
		return out
	}

	return append(out, *ev)
}

// warm reports whether either signal has finished learning.
func (t *tracker) warm() bool {
	return t.learnedNoise >= t.th.WarmupSamples || t.learnedBusy >= t.th.WarmupSamples
}

// updateBusy derives busy percentages from the counter deltas. It returns
// false when no valid delta exists (first sample, counter reset, idle).
func (t *tracker) updateBusy(s Survey) bool {
	rd := &t.reading
	p := &t.prev

	if !t.hasPrev || !s.HasActive || !s.HasBusy || !p.HasActive || !p.HasBusy {
		return false
	}

	if s.ActiveMs < p.ActiveMs || s.BusyMs < p.BusyMs {
		// Driver reset its counters; the next sample has a clean delta.
		return false
	}

	active := float64(s.ActiveMs - p.ActiveMs)
	if active <= 0 {
		return false
	}

	busy := float64(s.BusyMs - p.BusyMs)
	explained := 0.0

	if s.HasRx && p.HasRx && s.RxMs >= p.RxMs {
		explained += float64(s.RxMs - p.RxMs)
	}

	if s.HasTx && p.HasTx && s.TxMs >= p.TxMs {
		explained += float64(s.TxMs - p.TxMs)
	}

	rd.BusyPct = clampPct(busy / active * 100)
	rd.UnexplainedBusyPct = clampPct((busy - explained) / active * 100)
	rd.BusyKnown = true

	return true
}

// classify sets the reading's reasons and updates the baseline when the
// sample is normal.
func (t *tracker) classify(busyOK bool) {
	rd := &t.reading
	th := &t.th
	warmNoise := t.learnedNoise >= th.WarmupSamples
	warmBusy := t.learnedBusy >= th.WarmupSamples

	if rd.NoiseKnown {
		noise := float64(rd.NoiseDBm)

		if noise >= th.NoiseCeilingDBm {
			rd.Reasons = append(rd.Reasons, ReasonNoiseHigh)
		} else if warmNoise && noise-rd.BaselineNoiseDBm >= th.NoiseRiseDB {
			rd.Reasons = append(rd.Reasons, ReasonNoiseRise)
		}
	}

	if busyOK {
		u := rd.UnexplainedBusyPct

		if u >= th.BusyCeilingPct {
			rd.Reasons = append(rd.Reasons, ReasonBusyHigh)
		} else if warmBusy && u >= th.BusyFloorPct && u-rd.BaselineUnexplainedPct >= th.BusyRisePct {
			rd.Reasons = append(rd.Reasons, ReasonBusyRise)
		}
	}

	// Learn only from normal samples taken while no episode is open, so a
	// jammer is never folded into the baseline.
	if len(rd.Reasons) > 0 || t.episode != nil {
		return
	}

	if rd.NoiseKnown {
		rd.BaselineNoiseDBm = ewma(rd.BaselineNoiseDBm, float64(rd.NoiseDBm), t.alpha(t.learnedNoise), t.learnedNoise == 0)
		t.learnedNoise++
	}

	if busyOK {
		rd.BaselineUnexplainedPct = ewma(rd.BaselineUnexplainedPct, rd.UnexplainedBusyPct, t.alpha(t.learnedBusy), t.learnedBusy == 0)
		t.learnedBusy++
	}
}

// alpha is the baseline weight after n learned samples: a running mean
// while warming up, then the configured EWMA weight.
func (t *tracker) alpha(n int) float64 {
	if n < t.th.WarmupSamples {
		return 1 / float64(n+1)
	}

	return t.th.Alpha
}

// step advances the state machine on the classified sample.
func (t *tracker) step(now time.Time) *Event {
	rd := &t.reading
	th := &t.th

	if len(rd.Reasons) > 0 {
		t.abnormal++
		t.normal = 0
	} else {
		t.normal++
		t.abnormal = 0
	}

	if t.episode != nil {
		t.updatePeaks()
	}

	switch {
	case t.abnormal >= th.JamSamples && rd.State != StateJammed:
		rd.State = StateJammed

		return t.openOrEscalate(now)
	case t.abnormal >= th.SuspectSamples && rd.State != StateSuspected && rd.State != StateJammed:
		rd.State = StateSuspected

		return t.openOrEscalate(now)
	case t.episode != nil && t.normal >= th.ClearSamples:
		return t.closeEpisode(now)
	case t.episode == nil && rd.State == StateWarmingUp && t.warm():
		rd.State = StateClear
	}

	if t.episode != nil {
		t.fillEvent(t.episode, now)
	}

	return nil
}

func (t *tracker) openOrEscalate(now time.Time) *Event {
	if t.episode == nil {
		t.episode = &Event{
			ID:           t.reading.Radio + "-" + strconv.FormatInt(now.Unix(), 10),
			Started:      now,
			PeakNoiseDBm: t.reading.NoiseDBm,
		}
	}

	t.updatePeaks()
	t.fillEvent(t.episode, now)

	ev := t.copyEpisode()

	return &ev
}

// closeEpisode ends the open episode, if any, and returns its final event.
func (t *tracker) closeEpisode(now time.Time) *Event {
	rd := &t.reading
	if t.episode == nil {
		return nil
	}

	t.fillEvent(t.episode, now)
	t.episode.State = StateClear
	t.episode.Cleared = now

	ev := t.copyEpisode()
	t.episode = nil
	t.abnormal = 0

	if t.warm() {
		rd.State = StateClear
	} else {
		rd.State = StateWarmingUp
	}

	return &ev
}

func (t *tracker) updatePeaks() {
	rd := &t.reading
	ep := t.episode

	if rd.NoiseKnown && (ep.PeakNoiseDBm == 0 || rd.NoiseDBm > ep.PeakNoiseDBm) {
		ep.PeakNoiseDBm = rd.NoiseDBm
	}

	if rd.UnexplainedBusyPct > ep.PeakUnexplainedPct {
		ep.PeakUnexplainedPct = rd.UnexplainedBusyPct
	}
}

func (t *tracker) fillEvent(ev *Event, now time.Time) {
	rd := &t.reading

	ev.Updated = now
	ev.State = rd.State
	ev.Radio = rd.Radio
	ev.PHY = rd.PHY
	ev.Band = rd.Band
	ev.FreqMHz = rd.FreqMHz
	ev.NoiseKnown = rd.NoiseKnown
	ev.NoiseDBm = rd.NoiseDBm
	ev.BaselineNoiseDBm = rd.BaselineNoiseDBm
	ev.BusyPct = rd.BusyPct
	ev.UnexplainedBusyPct = rd.UnexplainedBusyPct
	ev.BaselineBusyPct = rd.BaselineUnexplainedPct
	ev.Reasons = append(ev.Reasons[:0], rd.Reasons...)
}

// copyEpisode returns a copy of the open episode that shares no slice
// with the tracker.
func (t *tracker) copyEpisode() Event {
	ev := *t.episode
	ev.Reasons = append([]Reason(nil), t.episode.Reasons...)

	return ev
}

// resetLearning forgets the baseline after a channel change.
func (t *tracker) resetLearning() {
	t.learnedNoise = 0
	t.learnedBusy = 0
	t.abnormal = 0
	t.normal = 0
	t.hasPrev = false
	t.reading.State = StateWarmingUp
	t.reading.BaselineNoiseDBm = 0
	t.reading.BaselineUnexplainedPct = 0
	t.reading.BusyKnown = false
	t.reading.BusyPct = 0
	t.reading.UnexplainedBusyPct = 0
}

// snapshot copies the reading for readers outside the run goroutine.
func (t *tracker) snapshot() Reading {
	r := t.reading
	r.Reasons = append([]Reason(nil), t.reading.Reasons...)

	return r
}

func ewma(prev, v, alpha float64, first bool) float64 {
	if first {
		return v
	}

	return prev + alpha*(v-prev)
}

func clampPct(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 100:
		return 100
	default:
		return v
	}
}

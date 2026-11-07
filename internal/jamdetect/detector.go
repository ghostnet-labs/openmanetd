package jamdetect

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// Defaults for Options.
const (
	DefaultInterval       = 5 * time.Second
	DefaultGossipInterval = 10 * time.Second
	DefaultGossipExpiry   = 2 * time.Minute
	DefaultHistoryLen     = 64
	// maxSubscribers bounds open event streams.
	maxSubscribers = 8
	// subscriberBuffer is each subscriber's queue. A sample raises at
	// most one event per radio, so 16 covers several slow ticks.
	subscriberBuffer = 16
	// maxRadios bounds tracked radios; a node has three or four.
	maxRadios = 16
)

// ErrTooManySubscribers is returned by Subscribe when maxSubscribers
// streams are open.
var ErrTooManySubscribers = errors.New("jamdetect: too many subscribers")

// Position is a node's GNSS fix.
type Position struct {
	Latitude  float64
	Longitude float64
	AltitudeM float64
}

// Options configures a Detector.
type Options struct {
	Log    zerolog.Logger
	Source Source
	// Gossip shares reports over the mesh. nil keeps detection local.
	Gossip Gossip
	// Identity returns this node's batman primary MAC and hostname for
	// published reports. nil leaves them empty.
	Identity func() (primaryMAC, hostname string)
	// Position returns this node's GNSS fix. nil or false omits it.
	Position func() (Position, bool)
	// Now overrides the clock in tests.
	Now            func() time.Time
	Thresholds     Thresholds
	Interval       time.Duration
	GossipInterval time.Duration
	GossipExpiry   time.Duration
	HistoryLen     int
}

// Detector samples every radio, tracks jamming episodes and shares them
// over the mesh. Run owns the sampling loop; every other method is safe to
// call from any goroutine.
type Detector struct { //nolint:govet // fieldalignment: mu must sit directly above the fields it guards (.claude/rules/concurrency.md).
	opts Options
	th   Thresholds

	mu       sync.Mutex // protects the fields below
	trackers map[string]*tracker
	radios   map[string]Radio
	history  []Event
	subs     map[int]subscriber
	nextSub  int
	remote   map[string]*remoteReport
	stats    Snapshot
}

// New builds a Detector. It starts nothing; call Run.
func New(opts Options) *Detector {
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}

	if opts.GossipInterval <= 0 {
		opts.GossipInterval = DefaultGossipInterval
	}

	if opts.GossipExpiry <= 0 {
		opts.GossipExpiry = DefaultGossipExpiry
	}

	if opts.HistoryLen <= 0 {
		opts.HistoryLen = DefaultHistoryLen
	}

	return &Detector{
		opts:     opts,
		th:       opts.Thresholds.withDefaults(),
		trackers: make(map[string]*tracker, 4),
		radios:   make(map[string]Radio, 4),
		history:  make([]Event, 0, opts.HistoryLen),
		subs:     make(map[int]subscriber, maxSubscribers),
		remote:   make(map[string]*remoteReport, 8),
	}
}

func (d *Detector) now() time.Time {
	if d.opts.Now != nil {
		return d.opts.Now()
	}

	return time.Now()
}

// Run samples every Interval and gossips every GossipInterval until ctx
// is canceled.
func (d *Detector) Run(ctx context.Context) {
	d.opts.Log.Info().Dur("interval", d.opts.Interval).Bool("gossip", d.opts.Gossip != nil).Msg("jamming detection started")

	sample := time.NewTicker(d.opts.Interval)
	defer sample.Stop()

	var gossipC <-chan time.Time

	if d.opts.Gossip != nil {
		g := time.NewTicker(d.opts.GossipInterval)
		defer g.Stop()

		gossipC = g.C
	}

	d.Sample(ctx)

	for {
		select {
		case <-ctx.Done():
			d.closeSubscribers()

			return
		case <-sample.C:
			d.Sample(ctx)
		case <-gossipC:
			d.GossipOnce()
		}
	}
}

// Sample reads every radio once and advances detection. Run calls it on
// its ticker; tests and simulations call it directly.
func (d *Detector) Sample(ctx context.Context) {
	radios, err := d.opts.Source.Radios(ctx)
	if err != nil {
		d.opts.Log.Warn().Err(err).Msg("jamdetect: list radios failed")
		d.mu.Lock()
		d.stats.SurveyErrorsTotal++
		d.mu.Unlock()

		return
	}

	if len(radios) > maxRadios {
		radios = radios[:maxRadios]
	}

	type result struct {
		radio  Radio
		survey Survey
		ok     bool
	}

	results := make([]result, 0, len(radios))
	errs := 0

	for _, r := range radios {
		surveys, err := d.opts.Source.Survey(ctx, r.Ifname)
		if err != nil {
			d.opts.Log.Debug().Err(err).Str("radio", r.Ifname).Msg("jamdetect: survey failed")

			errs++

			continue
		}

		s, ok := InUse(surveys)
		results = append(results, result{radio: r, survey: s, ok: ok})
	}

	now := d.now()
	events := make([]Event, 0, 2)

	d.mu.Lock()

	d.stats.SamplesTotal++
	d.stats.SurveyErrorsTotal += uint64(errs) //nolint:gosec // errs is bounded by maxRadios

	seen := make(map[string]struct{}, len(results))

	for i := range results {
		res := &results[i]
		if !res.ok {
			continue
		}

		key := res.radio.PHY
		seen[key] = struct{}{}

		t := d.trackers[key]
		if t == nil || t.reading.Radio != res.radio.Ifname {
			if t != nil {
				events = t.close(now, events)
			}

			t = newTracker(res.radio, d.th)
			d.trackers[key] = t
		}

		d.radios[key] = res.radio
		events = t.observe(res.survey, res.radio.HaLow, now, events)
	}

	for key, t := range d.trackers {
		if _, ok := seen[key]; ok {
			continue
		}

		events = t.close(now, events)

		delete(d.trackers, key)
		delete(d.radios, key)
	}

	for i := range events {
		d.recordLocked(&events[i])
	}

	d.mu.Unlock()

	for i := range events {
		d.logEvent(&events[i])
	}
}

// recordLocked adds an event to the history and fans it out. The caller
// holds d.mu.
func (d *Detector) recordLocked(ev *Event) {
	d.stats.EventsTotal++

	if ev.State == StateJammed {
		d.stats.JammedEventsTotal++
	}

	// One history slot per episode: an escalation or close replaces the
	// episode's earlier entry.
	if i := slices.IndexFunc(d.history, func(h Event) bool { return h.ID == ev.ID }); i >= 0 {
		d.history = slices.Delete(d.history, i, i+1)
	}

	if len(d.history) >= d.opts.HistoryLen {
		d.history = slices.Delete(d.history, 0, 1)
	}

	d.history = append(d.history, *ev)

	for _, sub := range d.subs {
		ch := sub.ch

		select {
		case ch <- *ev:
		default:
			// Drop the oldest queued event so the newest state wins.
			select {
			case <-ch:
			default:
			}

			d.stats.EventsDroppedTotal++

			select {
			case ch <- *ev:
			default:
			}
		}
	}
}

func (d *Detector) logEvent(ev *Event) {
	var e *zerolog.Event

	switch ev.State {
	case StateJammed:
		e = d.opts.Log.Warn()
	default:
		e = d.opts.Log.Info()
	}

	msg := "jamming suspected"

	switch ev.State {
	case StateJammed:
		msg = "JAMMING DETECTED"
	case StateClear:
		msg = "jamming cleared"
	}

	e.Str("id", ev.ID).
		Str("radio", ev.Radio).
		Str("band", string(ev.Band)).
		Float64("freq_mhz", ev.FreqMHz).
		Int("noise_dbm", ev.NoiseDBm).
		Float64("baseline_noise_dbm", ev.BaselineNoiseDBm).
		Float64("unexplained_busy_pct", ev.UnexplainedBusyPct).
		Str("reasons", reasonsString(ev.Reasons)).
		Msg(msg)
}

func reasonsString(rs []Reason) string {
	var b strings.Builder

	for i, r := range rs {
		if i > 0 {
			b.WriteByte(',')
		}

		b.WriteString(r.String())
	}

	return b.String()
}

// Active returns this node's open episodes (SUSPECTED or JAMMED), sorted
// by ID. Gossiped episodes of other nodes are never included.
func (d *Detector) Active() []Event {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.activeLocked()
}

func (d *Detector) activeLocked() []Event {
	out := make([]Event, 0, len(d.trackers))

	for _, t := range d.trackers {
		if t.episode != nil {
			out = append(out, t.copyEpisode())
		}
	}

	slices.SortFunc(out, func(a, b Event) int { return strings.Compare(a.ID, b.ID) })

	return out
}

// Readings returns every radio's latest reading, sorted by radio name.
func (d *Detector) Readings() []Reading {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.readingsLocked()
}

func (d *Detector) readingsLocked() []Reading {
	out := make([]Reading, 0, len(d.trackers))

	for _, t := range d.trackers {
		out = append(out, t.snapshot())
	}

	slices.SortFunc(out, func(a, b Reading) int { return strings.Compare(a.Radio, b.Radio) })

	return out
}

// Recent returns this node's latest episodes, newest first.
func (d *Detector) Recent() []Event {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]Event, 0, len(d.history))
	for i := len(d.history) - 1; i >= 0; i-- {
		ev := d.history[i]
		ev.Reasons = slices.Clone(ev.Reasons)
		out = append(out, ev)
	}

	return out
}

// Subscribe returns a channel of this node's episode changes. The channel
// closes when ctx is canceled or the Detector stops. A slow reader loses
// its oldest queued events, never the newest.
func (d *Detector) Subscribe(ctx context.Context) (<-chan Event, error) {
	d.mu.Lock()

	if len(d.subs) >= maxSubscribers {
		d.mu.Unlock()

		return nil, ErrTooManySubscribers
	}

	id := d.nextSub
	d.nextSub++

	sub := subscriber{ch: make(chan Event, subscriberBuffer), done: make(chan struct{})}
	d.subs[id] = sub
	d.mu.Unlock()

	// Exits when the caller cancels or the Detector stops.
	go func() {
		select {
		case <-ctx.Done():
			d.unsubscribe(id)
		case <-sub.done:
		}
	}()

	return sub.ch, nil
}

// subscriber is one open event stream. done closes when the Detector
// closes the stream so the watcher goroutine exits.
type subscriber struct {
	ch   chan Event
	done chan struct{}
}

func (d *Detector) unsubscribe(id int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if sub, ok := d.subs[id]; ok {
		delete(d.subs, id)
		close(sub.ch)
	}
}

func (d *Detector) closeSubscribers() {
	d.mu.Lock()
	defer d.mu.Unlock()

	for id, sub := range d.subs {
		delete(d.subs, id)
		close(sub.ch)
		close(sub.done)
	}
}

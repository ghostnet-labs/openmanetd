// Package conventional is the scanner's analog conventional receiver: it
// runs rtl_sdr on the scan RTL-SDR through internal/sdr/source, packs the
// selected channels into capture windows (one 2.048 MS/s capture covers
// up to MaxPerWindow channels inside the usable band), shifts and
// decimates every channel of the window in parallel from the one capture
// (internal/sdr/analog: AM for the 108-137 MHz aviation band, NFM
// otherwise) and squelch-scans: it stays on a window for Dwell, holds
// while any squelch is open and for Hold after it closes (up to MaxHold
// when other windows wait), then retunes to the next window.
//
// Open-squelch audio is delivered as 8 kHz PCM frames to subscribers; a
// subscription for one channel pins the scan to that channel's window.
// Nothing but demodulated audio leaves the package: no IQ is exposed.
//
// All capture work runs on the goroutine that calls Run; Scan, Status,
// WaitIdle and SubscribeAudio are safe for concurrent use.
package conventional

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/openmanet/openmanetd/internal/sdr/source"
)

// Defaults.
const (
	// DefaultSampleRate is the highest rtl_sdr rate that is stable on
	// every dongle.
	DefaultSampleRate   = 2_048_000
	DefaultDwell        = time.Second
	DefaultHold         = 2 * time.Second
	DefaultMaxHold      = 30 * time.Second
	DefaultSettle       = 50 * time.Millisecond
	DefaultMaxWindows   = 12
	DefaultMaxPerWindow = 8
	DefaultMinBackoff   = time.Second
	DefaultMaxBackoff   = time.Minute
	// MaxAudioSubscribers bounds concurrent audio streams.
	MaxAudioSubscribers = 4
	// audioBuffer is each subscriber's frame queue: 10 frames is 200 ms,
	// the same slack as the comms web audio bridge. A slower consumer
	// loses frames rather than stalling the capture.
	audioBuffer = 10
	// aviationLowHz and aviationHighHz bound the AM aviation band.
	aviationLowHz  = 108_000_000
	aviationHighHz = 137_000_000
)

// ErrTooManyAudioSubscribers is returned by SubscribeAudio when
// MaxAudioSubscribers streams are open.
var ErrTooManyAudioSubscribers = scanner.ErrTooManyAudioStreams

// errPlanChanged ends a capture because Scan installed a new plan.
var errPlanChanged = errors.New("conventional: plan changed")

// errCaptureStopped is reported when the capture tool ends without an error.
var errCaptureStopped = errors.New("conventional: capture stopped")

// Options configures a Scanner. The zero value captures with rtl_sdr on
// PATH at the defaults above.
type Options struct {
	Log zerolog.Logger
	// Runner starts capture tools; nil runs them with os/exec.
	Runner source.Runner
	// LookPath checks rtl_sdr is installed before each start; nil uses
	// exec.LookPath when Runner is nil and skips the check otherwise.
	LookPath func(name string) (string, error)
	// Now is the clock for last-heard times and frame stamps.
	Now func() time.Time
	// RTLBinary overrides the rtl_sdr tool name.
	RTLBinary string
	// SampleRate is the rtl_sdr capture rate (default DefaultSampleRate).
	SampleRate int
	// GainDB and PPM are passed to rtl_sdr (-g, -p); zero leaves AGC and
	// no correction.
	GainDB float64
	PPM    int
	// Dwell is how long (in sample time) a quiet window is listened to;
	// Hold how long the scan stays after the last squelch close; MaxHold
	// caps a busy window's stay when other windows wait; Settle is
	// discarded after each tune while the tuner and AGC settle.
	Dwell   time.Duration
	Hold    time.Duration
	MaxHold time.Duration
	Settle  time.Duration
	// MaxWindows and MaxPerWindow bound the scan list and the parallel
	// demodulators (CPU) per capture.
	MaxWindows   int
	MaxPerWindow int
	// MinBackoff and MaxBackoff bound the restart delay after a capture
	// failure; the delay doubles per consecutive failure.
	MinBackoff time.Duration
	MaxBackoff time.Duration
}

// subscriber is one audio stream.
type subscriber struct {
	ch  chan scanner.AudioFrame
	key string
	seq uint32 // touched only by the capture goroutine, under Scanner.mu
}

// Scanner implements scanner.ConventionalScanner.
type Scanner struct { //nolint:govet // fieldalignment: mu stays above the fields it guards per .claude/rules/concurrency.md
	opts     Options
	runner   source.Runner
	lookPath func(string) (string, error)
	wake     chan struct{} // capacity 1: coalesces plan changes
	pinGen   atomic.Uint64 // bumped whenever the pinned channel set changes
	dropped  atomic.Uint64

	mu      sync.Mutex // protects the fields below
	plan    scanner.ConventionalPlan
	windows []window
	gen     uint64 // bumped per installed plan
	chans   []scanner.ConventionalChannelStatus
	pins    map[string]int
	subs    map[*subscriber]struct{}
	state   scanner.ConventionalState
	detail  string
	center  uint64
	current int // index into windows of the capture, -1 when none
	// capturing is true while a capture process holds the receiver;
	// capChanged is closed and replaced whenever it changes.
	capturing  bool
	capChanged chan struct{}
}

var _ scanner.ConventionalScanner = (*Scanner)(nil)

// New returns a scanner for opts.
func New(opts Options) *Scanner {
	opts.SampleRate = cmpOr(opts.SampleRate, DefaultSampleRate)
	opts.Dwell = cmpOr(opts.Dwell, DefaultDwell)
	opts.Hold = cmpOr(opts.Hold, DefaultHold)
	opts.MaxHold = max(cmpOr(opts.MaxHold, DefaultMaxHold), opts.Dwell)
	opts.Settle = cmpOr(opts.Settle, DefaultSettle)
	opts.MaxWindows = cmpOr(opts.MaxWindows, DefaultMaxWindows)
	opts.MaxPerWindow = cmpOr(opts.MaxPerWindow, DefaultMaxPerWindow)
	opts.MinBackoff = cmpOr(opts.MinBackoff, DefaultMinBackoff)
	opts.MaxBackoff = max(cmpOr(opts.MaxBackoff, DefaultMaxBackoff), opts.MinBackoff)

	if opts.Now == nil {
		opts.Now = time.Now
	}

	s := &Scanner{
		opts:       opts,
		runner:     opts.Runner,
		lookPath:   opts.LookPath,
		wake:       make(chan struct{}, 1),
		pins:       make(map[string]int, MaxAudioSubscribers),
		subs:       make(map[*subscriber]struct{}, MaxAudioSubscribers),
		state:      scanner.ConventionalIdle,
		current:    -1,
		capChanged: make(chan struct{}),
	}

	if s.runner == nil {
		s.runner = &source.ExecRunner{}

		if s.lookPath == nil {
			s.lookPath = exec.LookPath
		}
	}

	return s
}

// SampleRate returns the capture rate.
func (s *Scanner) SampleRate() int { return s.opts.SampleRate }

// Dropped returns how many audio frames were dropped on full subscriber
// queues.
func (s *Scanner) Dropped() uint64 { return s.dropped.Load() }

// Scan implements scanner.ConventionalScanner. It plans the capture
// windows and hands the plan to Run; it never blocks on the capture. A
// channel heard before keeps its last-heard time.
func (s *Scanner) Scan(plan scanner.ConventionalPlan) error {
	var windows []window

	if plan.Serial != "" && len(plan.Channels) > 0 {
		cfg := s.sourceConfig(plan.Serial, plan.Channels[0].Channel.FrequencyHz)
		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("conventional: %w", err)
		}

		usable := min(cmpOr(plan.UsableHz, uint64(s.opts.SampleRate)*8/10), uint64(s.opts.SampleRate)*8/10)
		windows = planWindows(plan.Channels, usable, s.opts.MaxWindows, s.opts.MaxPerWindow)
	} else {
		plan = scanner.ConventionalPlan{}
	}

	plan.Channels = slices.Clone(plan.Channels)

	s.mu.Lock()

	heard := make(map[string]time.Time, len(s.chans))
	for i := range s.chans {
		if c := &s.chans[i]; !c.LastHeard.IsZero() {
			heard[c.Channel.Key] = c.LastHeard
		}
	}

	s.plan, s.windows, s.gen = plan, windows, s.gen+1
	s.chans = make([]scanner.ConventionalChannelStatus, len(plan.Channels))

	for i := range plan.Channels {
		s.chans[i] = scanner.ConventionalChannelStatus{ConventionalChannel: plan.Channels[i], LastHeard: heard[plan.Channels[i].Channel.Key]}
	}

	for wi := range windows {
		for _, ci := range windows[wi].chans {
			s.chans[ci].Window = wi + 1
		}
	}

	s.mu.Unlock()

	select {
	case s.wake <- struct{}{}:
	default: // a plan change is already pending; Run reads the latest
	}

	return nil
}

// Status implements scanner.ConventionalScanner.
func (s *Scanner) Status() scanner.ConventionalStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := scanner.ConventionalStatus{
		State:      s.state,
		Detail:     s.detail,
		Channels:   slices.Clone(s.chans),
		Windows:    len(s.windows),
		SampleRate: s.opts.SampleRate,
	}

	if s.capturing {
		st.CenterHz, st.Window = s.center, s.current+1
	}

	return st
}

// WaitIdle implements scanner.ConventionalScanner.
func (s *Scanner) WaitIdle(ctx context.Context) error {
	for {
		s.mu.Lock()
		busy, changed := s.capturing, s.capChanged
		s.mu.Unlock()

		if !busy {
			return nil
		}

		select {
		case <-changed:
		case <-ctx.Done():
			return fmt.Errorf("conventional: wait for capture to stop: %w", ctx.Err())
		}
	}
}

// SubscribeAudio implements scanner.ConventionalScanner.
func (s *Scanner) SubscribeAudio(channelKey string) (<-chan scanner.AudioFrame, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.subs) >= MaxAudioSubscribers {
		return nil, nil, ErrTooManyAudioSubscribers
	}

	sub := &subscriber{ch: make(chan scanner.AudioFrame, audioBuffer), key: channelKey}
	s.subs[sub] = struct{}{}

	if channelKey != "" {
		s.pins[channelKey]++
		s.pinGen.Add(1)
	}

	var once sync.Once

	cancel := func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()

			delete(s.subs, sub)
			close(sub.ch)

			if channelKey == "" {
				return
			}

			if s.pins[channelKey]--; s.pins[channelKey] <= 0 {
				delete(s.pins, channelKey)
			}

			s.pinGen.Add(1)
		})
	}

	return sub.ch, cancel, nil
}

// Run captures the current plan until ctx is canceled. It closes every
// audio subscription when it returns.
func (s *Scanner) Run(ctx context.Context) {
	defer s.closeSubscribers()

	var (
		v        *visitor
		failures int
		gen      uint64
	)

	for {
		select {
		case <-s.wake:
		default:
		}

		s.mu.Lock()
		plan, windows, planGen := s.plan, s.windows, s.gen
		s.mu.Unlock()

		if len(windows) == 0 {
			s.setState(scanner.ConventionalIdle, "")

			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				continue
			}
		}

		if v == nil || gen != planGen {
			v, gen, failures = newVisitor(s, plan, windows), planGen, 0
		}

		err := s.capture(ctx, v)

		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, errPlanChanged):
			continue
		case err == nil:
			failures = 0

			continue
		}

		failures++

		unavailable := errors.Is(err, exec.ErrNotFound)
		state := scanner.ConventionalError

		if unavailable {
			state = scanner.ConventionalUnavailable
		}

		s.setState(state, err.Error())

		ev := s.opts.Log.Debug()
		if failures == 1 {
			ev = s.opts.Log.Warn()
		}

		delay := s.backoff(failures)
		ev.Err(err).Bool("unavailable", unavailable).Dur("retry_in", delay).Msg("conventional: capture failed")

		t := time.NewTimer(delay)

		select {
		case <-ctx.Done():
			t.Stop()

			return
		case <-s.wake:
			t.Stop()
		case <-t.C:
		}
	}
}

// capture runs one window visit: it starts rtl_sdr on the next window
// and returns nil when the visit ends (dwell/hold elapsed), errPlanChanged
// on a new plan, or the capture error.
func (s *Scanner) capture(ctx context.Context, v *visitor) error {
	wi := v.next()
	w := &v.windows[wi]
	cfg := s.sourceConfig(v.plan.Serial, w.center)

	if s.lookPath != nil {
		name, _ := cfg.Command(w.center)
		if _, err := s.lookPath(name); err != nil {
			return fmt.Errorf("conventional: capture tool %s: %w", name, err)
		}
	}

	src, err := source.New(cfg, s.runner)
	if err != nil {
		return fmt.Errorf("conventional: %w", err)
	}

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := v.begin(wi, cancel); err != nil {
		return err
	}
	defer v.end()

	s.setCapturing(true, wi, w.center)
	defer s.setCapturing(false, -1, 0) // runs after <-errc below: the process has exited

	errc := make(chan error, 1) // Run's single result; buffered so it never blocks

	go func() { errc <- src.Run(sctx, v.consume) }()

	select {
	case <-s.wake:
		cancel()
		<-errc

		return errPlanChanged
	case <-ctx.Done():
		cancel()
		<-errc

		return fmt.Errorf("conventional: %w", ctx.Err())
	case err := <-errc:
		switch {
		case v.finished:
			return nil
		case err == nil:
			return errCaptureStopped
		default:
			return err
		}
	}
}

func (s *Scanner) setCapturing(on bool, wi int, center uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.capturing, s.current, s.center = on, wi, center
	close(s.capChanged)
	s.capChanged = make(chan struct{})

	if on {
		s.state, s.detail = scanner.ConventionalScanning, ""

		return
	}

	for i := range s.chans {
		s.chans[i].Active = false
	}
}

func (s *Scanner) setState(state scanner.ConventionalState, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.state, s.detail = state, detail
}

func (s *Scanner) closeSubscribers() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for sub := range s.subs {
		close(sub.ch)
		delete(s.subs, sub)
	}

	clear(s.pins)
	s.pinGen.Add(1)
	s.state, s.detail = scanner.ConventionalIdle, ""
}

// backoff is MinBackoff doubled per consecutive failure, capped at
// MaxBackoff.
func (s *Scanner) backoff(failures int) time.Duration {
	d := s.opts.MinBackoff
	for i := 1; i < failures && d < s.opts.MaxBackoff; i++ {
		d *= 2
	}

	return min(d, s.opts.MaxBackoff)
}

func (s *Scanner) sourceConfig(serial string, centerHz uint64) source.Config {
	return source.Config{
		Binary:      s.opts.RTLBinary,
		Serial:      serial,
		FrequencyHz: centerHz,
		SampleRate:  s.opts.SampleRate,
		GainDB:      s.opts.GainDB,
		Driver:      source.DriverRTLSDR,
		PPM:         s.opts.PPM,
	}
}

func cmpOr[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}

	return v
}

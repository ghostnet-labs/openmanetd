// Package p25mon is the scanner's P25 control-channel monitor: it runs
// airspy_rx (wide receiver) or rtl_sdr (scan receiver) through
// internal/sdr/source, shifts the control channel down from the tuner
// center and decodes it with internal/sdr/p25, and turns the decoder's
// events into the scanner.ControlChannelMonitor contract.
//
// The monitor hunts: when the decoder has no frame sync for HuntTimeout
// of samples on one control channel it moves to the next one in the plan,
// retuning the receiver when that channel is outside the current window.
// A capture that fails is retried with bounded exponential backoff; a
// capture tool that is not installed is reported as unavailable.
package p25mon

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/openmanet/openmanetd/internal/sdr/p25"
	"github.com/openmanet/openmanetd/internal/sdr/source"
)

// Capture defaults. Airspy R2 rates are 10 and 2.5 MS/s; 10 MS/s covers
// the 8 MHz wide window, 2.5 MS/s anything up to the RTL-sized 2 MHz.
// 2.048 MS/s is the highest rtl_sdr rate that is stable on every dongle.
const (
	DefaultAirspyWideRate   = 10_000_000
	DefaultAirspyNarrowRate = 2_500_000
	DefaultRTLRate          = 2_048_000
	DefaultHuntTimeout      = 3 * time.Second
	DefaultMinBackoff       = time.Second
	DefaultMaxBackoff       = time.Minute
)

const (
	// eventBuffer holds decoder events while the manager goroutine is
	// busy; a control channel produces a few TSBKs per second, so 64 is
	// several seconds of slack. A full buffer drops the event.
	eventBuffer = 64
	// dcClearHz keeps the control channel off the tuner's DC spike and
	// is the offset used when the receiver has to move to a channel.
	dcClearHz = 250_000
	// minOffsetHz is the closest a channel may sit to the tuner center
	// before it is treated as on the DC spike.
	minOffsetHz = 25_000
	// edgeGuardHz keeps a whole 12.5 kHz channel inside the window.
	edgeGuardHz = 12_500
	// narrowAirspyMaxHz is the widest window the 2.5 MS/s airspy rate
	// serves; wider plans use the 10 MS/s rate.
	narrowAirspyMaxHz = 2_000_000
)

// errRetune ends a capture session because the plan changed.
var errRetune = errors.New("p25mon: plan changed")

// errCaptureStopped is reported when the capture tool ends without an error.
var errCaptureStopped = errors.New("p25mon: capture stopped")

// Options configures a Monitor. The zero value captures with the tools on
// PATH at the default rates.
type Options struct {
	Log zerolog.Logger
	// Runner starts capture tools; nil runs them with os/exec.
	Runner source.Runner
	// LookPath checks the capture tool is installed before each start.
	// nil uses exec.LookPath when Runner is nil and skips the check for
	// an injected Runner.
	LookPath func(name string) (string, error)
	// AirspyBinary and RTLBinary override the tool names.
	AirspyBinary string
	RTLBinary    string
	// AirspyRate forces the airspy sample rate (> 1 MS/s). Zero picks
	// DefaultAirspyWideRate or DefaultAirspyNarrowRate from the plan.
	AirspyRate int
	// RTLRate is the rtl_sdr sample rate (default DefaultRTLRate).
	RTLRate int
	// HuntTimeout is how much signal (in sample time) one control channel
	// gets to reach sync before the next one is tried.
	HuntTimeout time.Duration
	// MinBackoff and MaxBackoff bound the restart delay after a capture
	// failure; the delay doubles per consecutive failure.
	MinBackoff time.Duration
	MaxBackoff time.Duration
}

// Monitor implements scanner.ControlChannelMonitor. Run must be running
// for Tune to take effect; Tune and Events are safe for concurrent use.
type Monitor struct { //nolint:govet // fieldalignment: mu stays above the field it guards per .claude/rules/concurrency.md
	runner   source.Runner
	lookPath func(string) (string, error)
	events   chan any
	wake     chan struct{} // capacity 1: coalesces plan changes
	opts     Options
	dropped  atomic.Uint64

	mu   sync.Mutex // protects plan, capSerial and capChanged
	plan scanner.TunePlan
	// capSerial is the configured serial of the receiver a running
	// capture holds ("" when none); capChanged is closed and replaced
	// whenever it changes.
	capSerial  string
	capChanged chan struct{}
}

var (
	_ scanner.ControlChannelMonitor = (*Monitor)(nil)
	_ scanner.ReceiverReleaser      = (*Monitor)(nil)
)

// New returns a monitor for opts.
func New(opts Options) *Monitor {
	opts.RTLRate = cmpOr(opts.RTLRate, DefaultRTLRate)
	opts.HuntTimeout = cmpOr(opts.HuntTimeout, DefaultHuntTimeout)
	opts.MinBackoff = cmpOr(opts.MinBackoff, DefaultMinBackoff)
	opts.MaxBackoff = max(cmpOr(opts.MaxBackoff, DefaultMaxBackoff), opts.MinBackoff)

	m := &Monitor{
		runner:     opts.Runner,
		lookPath:   opts.LookPath,
		events:     make(chan any, eventBuffer),
		wake:       make(chan struct{}, 1),
		opts:       opts,
		capChanged: make(chan struct{}),
	}

	if m.runner == nil {
		m.runner = &source.ExecRunner{}

		if m.lookPath == nil {
			m.lookPath = exec.LookPath
		}
	}

	return m
}

// Events implements scanner.ControlChannelMonitor. The channel is closed
// when Run returns.
func (m *Monitor) Events() <-chan any { return m.events }

// Dropped returns how many events were dropped on a full event buffer.
func (m *Monitor) Dropped() uint64 { return m.dropped.Load() }

// Tune implements scanner.ControlChannelMonitor. It validates the plan's
// receiver configuration and hands the plan to Run; it never blocks on
// the capture.
func (m *Monitor) Tune(_ context.Context, plan scanner.TunePlan) error {
	if plan.Tuner != scanner.TunerNone {
		if len(plan.ControlHz) == 0 {
			return errors.New("p25mon: plan has no control channels")
		}

		h := newHunter(plan, 0)
		if err := m.sourceConfig(plan, h.center).Validate(); err != nil {
			return fmt.Errorf("p25mon: %w", err)
		}
	}

	plan.ControlHz = slices.Clone(plan.ControlHz)
	plan.VoiceHz = slices.Clone(plan.VoiceHz)

	m.mu.Lock()
	m.plan = plan
	m.mu.Unlock()

	select {
	case m.wake <- struct{}{}:
	default: // a plan change is already pending; Run reads the latest
	}

	return nil
}

// WaitReleased implements scanner.ReceiverReleaser: it returns once no
// capture process holds the receiver with serial (after a plan for
// another receiver or the zero plan, once Run has stopped that capture),
// or with ctx's error.
func (m *Monitor) WaitReleased(ctx context.Context, serial string) error {
	for {
		m.mu.Lock()
		holding, changed := m.capSerial, m.capChanged
		m.mu.Unlock()

		if holding == "" || holding != serial {
			return nil
		}

		select {
		case <-changed:
		case <-ctx.Done():
			return fmt.Errorf("p25mon: wait for capture on %s to stop: %w", serial, ctx.Err())
		}
	}
}

// setCapturing records the serial a capture holds ("" when it stops) and
// wakes WaitReleased callers.
func (m *Monitor) setCapturing(serial string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.capSerial = serial
	close(m.capChanged)
	m.capChanged = make(chan struct{})
}

func (m *Monitor) currentPlan() scanner.TunePlan {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.plan
}

// Run captures and decodes the current plan until ctx is canceled, then
// closes the event channel.
func (m *Monitor) Run(ctx context.Context) {
	defer close(m.events)

	var (
		h        *hunter
		failures int
	)

	for {
		// The plan read below is the latest, so a pending wake for it is
		// already handled.
		select {
		case <-m.wake:
		default:
		}

		plan := m.currentPlan()
		if plan.Tuner == scanner.TunerNone {
			h, failures = nil, 0

			select {
			case <-ctx.Done():
				return
			case <-m.wake:
				continue
			}
		}

		if h == nil || !samePlan(h.plan, plan) {
			h, failures = newHunter(plan, m.huntSamples(plan)), 0
		}

		err := m.session(ctx, h)

		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, errRetune):
			continue
		}

		if h.everLocked {
			failures = 0
		}

		h.everLocked = false
		failures++

		unavailable := errors.Is(err, exec.ErrNotFound)
		m.send(scanner.DecoderStatus{Err: err, FrequencyHz: h.freq(), Unavailable: unavailable})

		ev := m.opts.Log.Debug()
		if failures == 1 {
			ev = m.opts.Log.Warn()
		}

		delay := m.backoff(failures)
		ev.Err(err).Bool("unavailable", unavailable).Dur("retry_in", delay).Msg("p25mon: control-channel capture failed")

		t := time.NewTimer(delay)

		select {
		case <-ctx.Done():
			t.Stop()

			return
		case <-m.wake:
			t.Stop()
		case <-t.C:
		}
	}
}

// backoff is MinBackoff doubled per consecutive failure, capped at
// MaxBackoff.
func (m *Monitor) backoff(failures int) time.Duration {
	d := m.opts.MinBackoff
	for i := 1; i < failures && d < m.opts.MaxBackoff; i++ {
		d *= 2
	}

	return min(d, m.opts.MaxBackoff)
}

// session runs one capture of h's plan. It returns errRetune when a new
// plan arrives, nil-wrapping errors when the capture fails, or ctx.Err().
func (m *Monitor) session(ctx context.Context, h *hunter) error {
	cfg := m.sourceConfig(h.plan, h.center)

	if m.lookPath != nil {
		name, _ := cfg.Command(h.center)
		if _, err := m.lookPath(name); err != nil {
			return fmt.Errorf("p25mon: capture tool %s: %w", name, err)
		}
	}

	src, err := source.New(cfg, m.runner)
	if err != nil {
		return fmt.Errorf("p25mon: %w", err)
	}

	dec, err := p25.NewDecoder(cfg.SampleRate, h.emitFunc())
	if err != nil {
		return fmt.Errorf("p25mon: %w", err)
	}

	h.attach(m, dec, src)
	m.send(scanner.DecoderStatus{FrequencyHz: h.freq()})

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errc := make(chan error, 1) // Run's single result; buffered so it never blocks

	m.setCapturing(h.plan.Serial)
	defer m.setCapturing("") // every path below has received from errc: Run (and its process) has exited

	go func() { errc <- src.Run(sctx, h.consume) }()

	select {
	case <-m.wake:
		cancel()
		<-errc

		return errRetune
	case <-ctx.Done():
		cancel()
		<-errc

		return fmt.Errorf("p25mon: %w", ctx.Err())
	case err := <-errc:
		if err == nil {
			err = errCaptureStopped
		}

		return err
	}
}

// send delivers ev without blocking; a full buffer drops it.
func (m *Monitor) send(ev any) {
	select {
	case m.events <- ev:
	default:
		m.dropped.Add(1)
	}
}

func (m *Monitor) sampleRate(plan scanner.TunePlan) int {
	if plan.Tuner != scanner.TunerWide {
		return m.opts.RTLRate
	}

	if plan.SampleRate != 0 {
		return plan.SampleRate
	}

	if m.opts.AirspyRate != 0 {
		return m.opts.AirspyRate
	}

	if plan.UsableHz != 0 && plan.UsableHz <= narrowAirspyMaxHz {
		return DefaultAirspyNarrowRate
	}

	return DefaultAirspyWideRate
}

func (m *Monitor) huntSamples(plan scanner.TunePlan) int {
	return int(float64(m.sampleRate(plan)) * m.opts.HuntTimeout.Seconds())
}

func (m *Monitor) sourceConfig(plan scanner.TunePlan, centerHz uint64) source.Config {
	cfg := source.Config{
		Serial:      plan.Serial,
		FrequencyHz: centerHz,
		SampleRate:  m.sampleRate(plan),
		Driver:      source.DriverRTLSDR,
		Binary:      m.opts.RTLBinary,
	}

	if plan.Tuner == scanner.TunerWide {
		cfg.Driver, cfg.Binary = source.DriverAirspy, m.opts.AirspyBinary
		cfg.Serial = AirspySerial(plan.Serial)
	}

	return cfg
}

// AirspySerial converts a configured or sysfs Airspy serial ("AIRSPY
// SN:44A061DC2E395E8F", "44a061dc2e395e8f") to the 0x-prefixed form
// airspy_rx -s parses.
func AirspySerial(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 10 && strings.EqualFold(s[:10], "airspy sn:") {
		s = strings.TrimSpace(s[10:])
	}

	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") || s == "" {
		return s
	}

	return "0x" + s
}

func samePlan(a, b scanner.TunePlan) bool {
	return a.SiteKey == b.SiteKey && a.CenterHz == b.CenterHz && a.UsableHz == b.UsableHz && a.SampleRate == b.SampleRate &&
		a.Serial == b.Serial && a.Tuner == b.Tuner && slices.Equal(a.ControlHz, b.ControlHz)
}

func cmpOr[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}

	return v
}

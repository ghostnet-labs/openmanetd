package conventional

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/openmanet/openmanetd/internal/scanner/sites"
	"github.com/openmanet/openmanetd/internal/sdr/analog"
	"github.com/openmanet/openmanetd/internal/sdr/iq"
)

// demodKind is a demodulator configuration; slots are pooled per kind so
// a window change reuses (Resets) demodulators instead of reallocating.
type demodKind struct {
	mode    analog.Mode
	spacing int
}

// slot is one channel demodulator: an NCO that moves the channel to 0 Hz
// and the analog demodulator (whose decimation chain does the rest).
type slot struct {
	v    *visitor
	d    *analog.Demodulator
	sh   *iq.Shifter
	ch   int // index into the plan's channels
	kind demodKind
}

func (sl *slot) emit(b analog.Block) { sl.v.onBlock(sl, b) }

// visitor holds the capture-side state of one plan. Every method except
// newVisitor and next runs on the capture goroutine (inside source.Run),
// so it needs no locking of its own; it takes Scanner.mu only to publish
// state.
type visitor struct {
	s          *Scanner
	cancel     context.CancelFunc
	pool       map[demodKind][]*slot
	windows    []window
	active     []*slot
	scratch    []complex64
	candidates []int // windows the scan may visit (pinned ones, else all)
	plan       scanner.ConventionalPlan
	gen        uint64
	pinGen     uint64
	cur        int // window being captured, -1 before the first
	since      int // samples consumed this visit (after settling)
	lastOpen   int // since at the last open squelch
	settle     int // samples still to discard
	dwell      int
	hold       int
	maxHold    int
	everOpen   bool
	holding    bool
	finished   bool
}

func newVisitor(s *Scanner, plan scanner.ConventionalPlan, windows []window) *visitor {
	rate := float64(s.opts.SampleRate)
	samples := func(d time.Duration) int { return int(rate * d.Seconds()) }

	s.mu.Lock()
	gen := s.gen
	s.mu.Unlock()

	return &visitor{
		s:       s,
		pool:    make(map[demodKind][]*slot, 3),
		plan:    plan,
		windows: windows,
		active:  make([]*slot, 0, s.opts.MaxPerWindow),
		gen:     gen,
		cur:     -1,
		dwell:   samples(s.opts.Dwell),
		hold:    samples(s.opts.Hold),
		maxHold: samples(s.opts.MaxHold),
	}
}

// next picks the window to capture: the one after the current among the
// candidates (windows holding a pinned channel, else every window).
func (v *visitor) next() int {
	v.pinGen = v.s.pinGen.Load()
	v.candidates = v.pinnedWindows(v.candidates[:0])

	for _, wi := range v.candidates {
		if wi > v.cur {
			return wi
		}
	}

	return v.candidates[0]
}

// pinnedWindows appends the windows holding a channel with an audio
// subscription, or every window when none is pinned (or no pinned channel
// fits a window).
func (v *visitor) pinnedWindows(dst []int) []int {
	v.s.mu.Lock()

	if len(v.s.pins) > 0 {
		for wi := range v.windows {
			for _, ci := range v.windows[wi].chans {
				if v.s.pins[v.plan.Channels[ci].Channel.Key] > 0 {
					dst = append(dst, wi)

					break
				}
			}
		}
	}

	v.s.mu.Unlock()

	if len(dst) > 0 {
		return dst
	}

	for wi := range v.windows {
		dst = append(dst, wi)
	}

	return dst
}

// begin prepares the demodulators of window wi for a new capture.
func (v *visitor) begin(wi int, cancel context.CancelFunc) error {
	w := &v.windows[wi]
	v.cur, v.cancel = wi, cancel
	v.since, v.lastOpen, v.everOpen, v.holding, v.finished = 0, 0, false, false, false
	v.settle = int(float64(v.s.opts.SampleRate) * v.s.opts.Settle.Seconds())

	for _, ci := range w.chans {
		c := v.plan.Channels[ci].Channel

		sl, err := v.take(kindFor(c))
		if err != nil {
			v.end()

			return err
		}

		sl.ch = ci
		sl.d.Reset()
		sl.sh.SetOffset(float64(int64(c.FrequencyHz) - int64(w.center)))
		v.active = append(v.active, sl)
	}

	return nil
}

// end returns the window's demodulators to the pool.
func (v *visitor) end() {
	for _, sl := range v.active {
		v.pool[sl.kind] = append(v.pool[sl.kind], sl)
	}

	clear(v.active)
	v.active = v.active[:0]
}

func (v *visitor) take(k demodKind) (*slot, error) {
	if free := v.pool[k]; len(free) > 0 {
		sl := free[len(free)-1]
		v.pool[k] = free[:len(free)-1]

		return sl, nil
	}

	sl := &slot{v: v, kind: k, sh: iq.NewShifter(0, float64(v.s.opts.SampleRate))}

	d, err := analog.New(analog.Config{
		SampleRate:       v.s.opts.SampleRate,
		Mode:             k.mode,
		ChannelSpacingHz: k.spacing,
		Deemphasis:       deemphasis(k.mode),
		BlockSamples:     scanner.AudioFrameSamples,
	}, sl.emit)
	if err != nil {
		return nil, fmt.Errorf("conventional: demodulator: %w", err)
	}

	sl.d = d

	return sl, nil
}

func deemphasis(m analog.Mode) time.Duration {
	if m == analog.ModeNFM {
		return analog.NFMDeemphasis
	}

	return 0
}

// DemodMode is the demodulation the scanner uses for c: AM in the
// 108-137 MHz aviation band, NFM elsewhere.
func DemodMode(c *sites.Channel) sites.ChannelMode {
	if kindFor(c).mode == analog.ModeAM {
		return sites.ModeAM
	}

	return sites.ModeNFM
}

// kindFor chooses the demodulator: AM in the aviation band, NFM
// elsewhere, with the channel's filter width.
func kindFor(c *sites.Channel) demodKind {
	if c.FrequencyHz >= aviationLowHz && c.FrequencyHz <= aviationHighHz {
		return demodKind{mode: analog.ModeAM, spacing: amSpacing(c.SpacingHz)}
	}

	return demodKind{mode: analog.ModeNFM, spacing: nfmSpacing(c.SpacingHz)}
}

func amSpacing(hz int) int {
	if hz == 8330 {
		return hz
	}

	return 25000
}

func nfmSpacing(hz int) int {
	if hz >= 20000 {
		return 25000
	}

	return 12500
}

// consume is the source.Consumer: demodulate every channel of the window
// from the one capture, then apply the dwell/hold rules. It does not
// allocate in steady state.
func (v *visitor) consume(samples []complex64) {
	if v.finished {
		return
	}

	if v.settle > 0 {
		k := min(v.settle, len(samples))
		v.settle -= k
		samples = samples[k:]
	}

	if len(samples) == 0 {
		return
	}

	open := false

	for _, sl := range v.active {
		v.scratch = sl.sh.Mix(v.scratch, samples)
		sl.d.Write(v.scratch)
		open = open || sl.d.Open()
	}

	v.since += len(samples)

	if open {
		v.lastOpen, v.everOpen = v.since, true
	}

	if g := v.s.pinGen.Load(); g != v.pinGen {
		v.pinGen = g
		v.candidates = v.pinnedWindows(v.candidates[:0])

		if !slices.Contains(v.candidates, v.cur) {
			v.finish()

			return
		}
	}

	holding := open || (v.everOpen && v.since-v.lastOpen < v.hold)
	if holding != v.holding {
		v.holding = holding

		state := scanner.ConventionalScanning
		if holding {
			state = scanner.ConventionalHolding
		}

		v.s.setState(state, "")
	}

	if len(v.candidates) < 2 {
		return // nothing else to scan: stay
	}

	if v.since >= v.maxHold || (!holding && v.since >= v.dwell) {
		v.finish()
	}
}

func (v *visitor) finish() {
	v.finished = true
	v.cancel()
}

// onBlock publishes one PCM block: channel state, and the frame to every
// matching audio subscriber when the squelch is open.
func (v *visitor) onBlock(sl *slot, b analog.Block) {
	s := v.s
	now := s.opts.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.gen != v.gen {
		return // a new plan replaced the channel list mid-capture
	}

	cs := &s.chans[sl.ch]
	cs.Level, cs.Active = b.Level, b.Open

	if !b.Open {
		return
	}

	cs.LastHeard = now

	if len(s.subs) == 0 {
		return
	}

	f := scanner.AudioFrame{
		Time:        now,
		ChannelKey:  cs.Channel.Key,
		FrequencyHz: cs.Channel.FrequencyHz,
		Level:       float32(b.Level),
	}
	copy(f.PCM[:], b.PCM)

	for sub := range s.subs {
		if sub.key != "" && sub.key != f.ChannelKey {
			continue
		}

		sub.seq++
		f.Sequence = sub.seq

		select {
		case sub.ch <- f:
		default:
			s.dropped.Add(1)
		}
	}
}

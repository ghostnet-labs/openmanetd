package gpio

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"github.com/warthog618/go-gpiocdev"
)

const (
	// DefaultVLMUSBFaultLine is the GPIO line name carrying the ghostnet,v1
	// VLM_USB_FAULT_N signal (OpenVLM host port 3 power/overcurrent fault,
	// active low). The CM5's RP1 names its header lines "GPIOn", so the
	// line is found by name rather than by chip and offset. The mapping is
	// from the board handoff and is NOT yet verified against the schematic
	// net list or hardware; the monitor is disabled by default until it is.
	DefaultVLMUSBFaultLine = "GPIO25"
	// FaultDebounce filters contact bounce and brief inrush dips on the
	// fault line, applied kernel-side via uAPI v2.
	FaultDebounce = 10 * time.Millisecond
)

// FaultLines is the requested fault line as the monitor reads it: Values
// fills a one-element slice with the current level (0 = low = fault).
type FaultLines interface {
	Values(values []int) error
	Close() error
}

// FaultMonitor watches one active-low fault line (VLM_USB_FAULT_N) and
// exposes its state and assertion count. It never acts on the port: the
// board has no software-controlled port power switch in this scope, and the
// OpenVLM device binder already rediscovers the device after the port
// recovers and the device re-enumerates. Its job is to make a fault
// observable in logs and the instrumentation snapshot.
type FaultMonitor struct {
	// Open overrides the hardware line request (tests and fakes). onEdge
	// must be invoked on every debounced edge of the line. Nil means the
	// real gpiocdev request.
	Open func(line string, onEdge func()) (FaultLines, error)
	// now overrides the clock in tests. Nil means time.Now.
	now func() time.Time
	Log zerolog.Logger
	// Line is the GPIO line name to watch. Empty means
	// DefaultVLMUSBFaultLine.
	Line string

	monitored         atomic.Bool
	asserted          atomic.Bool
	assertions        atomic.Int64
	lastAssertUnixNan atomic.Int64
	readErrors        atomic.Int64
}

// LineName returns the GPIO line name the monitor watches.
func (m *FaultMonitor) LineName() string {
	return m.line()
}

func (m *FaultMonitor) line() string {
	if m.Line != "" {
		return m.Line
	}

	return DefaultVLMUSBFaultLine
}

func (m *FaultMonitor) clock() time.Time {
	if m.now != nil {
		return m.now()
	}

	return time.Now()
}

// Start requests the fault line and launches the watch goroutine, which
// exits when ctx ends or after readErrorBreaker consecutive read failures.
// The returned channel is closed when the goroutine has exited. A request
// failure is returned immediately and nothing is started.
func (m *FaultMonitor) Start(ctx context.Context) (<-chan struct{}, error) {
	open := m.Open
	if open == nil {
		open = openFaultLine
	}

	edge := make(chan struct{}, 1) // coalesces edges; each wakeup re-reads the level
	notify := func() {
		select {
		case edge <- struct{}{}:
		default:
		}
	}

	lines, err := open(m.line(), notify)
	if err != nil {
		return nil, fmt.Errorf("gpio: request fault line %s: %w", m.line(), err)
	}

	m.monitored.Store(true)

	done := make(chan struct{})

	go m.watch(ctx, lines, edge, done)

	return done, nil
}

// openFaultLine finds the named line on any chip and requests it as an
// input with pull-up bias (the fault output is open-drain), both-edge
// events and kernel debounce.
func openFaultLine(name string, handler func()) (FaultLines, error) {
	chip, offset, err := gpiocdev.FindLine(name)
	if err != nil {
		return nil, fmt.Errorf("gpio: find line %q: %w", name, err)
	}

	lines, err := gpiocdev.RequestLines(chip, []int{offset},
		gpiocdev.AsInput,
		gpiocdev.WithPullUp,
		gpiocdev.WithBothEdges,
		gpiocdev.WithDebounce(FaultDebounce),
		gpiocdev.WithConsumer("openmanetd-vlm-usb-fault"),
		gpiocdev.WithEventHandler(func(gpiocdev.LineEvent) { handler() }),
	)
	if err != nil {
		return nil, fmt.Errorf("gpio: request %s offset %d: %w", chip, offset, err)
	}

	return lines, nil
}

func (m *FaultMonitor) watch(ctx context.Context, lines FaultLines, edge <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	defer m.monitored.Store(false)
	defer func() {
		if err := lines.Close(); err != nil {
			m.Log.Warn().Err(err).Msg("gpio: close fault line")
		}
	}()

	vals := make([]int, 1)
	errStreak := 0

	read := func() bool {
		if err := lines.Values(vals); err != nil {
			errStreak++

			m.readErrors.Add(1)

			m.Log.Warn().Err(err).Int("streak", errStreak).Str("line", m.line()).
				Msg("gpio: read VLM USB fault line")

			return errStreak < readErrorBreaker
		}

		errStreak = 0

		m.apply(vals[0] == 0)

		return true
	}

	if !read() {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-edge:
			if !read() {
				m.Log.Error().Str("line", m.line()).
					Msg("gpio: VLM USB fault line read breaker tripped; fault monitoring stopped")

				return
			}
		}
	}
}

// apply records the current fault level and logs transitions.
func (m *FaultMonitor) apply(fault bool) {
	if m.asserted.Swap(fault) == fault {
		return
	}

	if fault {
		m.assertions.Add(1)
		m.lastAssertUnixNan.Store(m.clock().UnixNano())

		m.Log.Error().Str("line", m.line()).
			Msg("comms: OpenVLM USB port fault asserted (VLM_USB_FAULT_N low): port power or overcurrent fault")

		return
	}

	m.Log.Info().Str("line", m.line()).
		Msg("comms: OpenVLM USB port fault cleared; device will be rediscovered when it re-enumerates")
}

// FaultSnapshot is the fault monitor's instrumentation section, published
// as comms.vlm_usb_fault.
type FaultSnapshot struct {
	// LastAssertUnixNano is the wall-clock time of the latest assertion;
	// 0 when none was seen.
	LastAssertUnixNano int64 `json:"last_assert_unix_nano"`
	// Assertions counts high→low (fault) transitions, including a fault
	// already present when monitoring started.
	Assertions int64 `json:"assertions"`
	// ReadErrors counts failed line reads.
	ReadErrors int64 `json:"read_errors"`
	// Monitored is true while the watch goroutine is running.
	Monitored bool `json:"monitored"`
	// Asserted is true while the fault line reads low.
	Asserted bool `json:"asserted"`
}

// Snapshot fills dst. Nil-receiver safe, zero-alloc.
func (m *FaultMonitor) Snapshot(dst *FaultSnapshot) {
	if dst == nil {
		return
	}

	if m == nil {
		*dst = FaultSnapshot{}

		return
	}

	dst.Monitored = m.monitored.Load()
	dst.Asserted = m.asserted.Load()
	dst.Assertions = m.assertions.Load()
	dst.LastAssertUnixNano = m.lastAssertUnixNan.Load()
	dst.ReadErrors = m.readErrors.Load()
}

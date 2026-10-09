package gpio

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// faultHarness drives a FaultMonitor over a fakeLines. The edge handler is
// captured from the monitor's Open call under mu because Start runs it on
// the test goroutine but the harness may be read after.
type faultHarness struct {
	mon *FaultMonitor
	fl  *fakeLines

	mu       sync.Mutex // protects the fields below
	onEdge   func()
	openLine string
}

func newFaultHarness(level int, line string) *faultHarness {
	h := &faultHarness{
		fl: &fakeLines{
			vals:         [5]int{level, 1, 1, 1, 1},
			valuesCalled: make(chan struct{}, readErrorBreaker+4),
		},
	}

	h.mon = &FaultMonitor{
		Log:  zerolog.Nop(),
		Line: line,
		now:  func() time.Time { return time.Unix(1_700_000_000, 42) },
		Open: func(name string, onEdge func()) (FaultLines, error) {
			h.mu.Lock()
			defer h.mu.Unlock()

			h.onEdge = onEdge
			h.openLine = name

			return h.fl, nil
		},
	}

	return h
}

func (h *faultHarness) start(t *testing.T) (context.CancelFunc, <-chan struct{}) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	done, err := h.mon.Start(ctx)
	require.NoError(t, err)

	t.Cleanup(func() {
		cancel()
		<-done
	})

	waitValuesCall(t, h.fl) // initial level read

	return cancel, done
}

// edge fires one edge and waits for the watcher to read the line.
func (h *faultHarness) edge(t *testing.T) {
	t.Helper()

	h.mu.Lock()
	fire := h.onEdge
	h.mu.Unlock()

	fire()
	waitValuesCall(t, h.fl)
}

// settle guarantees the watcher finished applying its previous read: the
// barrier edge is only consumed after that apply returns.
func (h *faultHarness) settle(t *testing.T) {
	t.Helper()

	h.edge(t)
}

func (h *faultHarness) snap() FaultSnapshot {
	var s FaultSnapshot

	h.mon.Snapshot(&s)

	return s
}

func TestFaultMonitor_InitialLevel(t *testing.T) {
	tests := []struct {
		name           string
		level          int
		wantAsserted   bool
		wantAssertions int64
		wantLastAssert int64
	}{
		{name: "line high, no fault", level: 1},
		{
			name:           "line low at boot counts as an assertion",
			level:          0,
			wantAsserted:   true,
			wantAssertions: 1,
			wantLastAssert: time.Unix(1_700_000_000, 42).UnixNano(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newFaultHarness(tc.level, "")
			h.start(t)
			h.settle(t)

			s := h.snap()
			assert.True(t, s.Monitored)
			assert.Equal(t, tc.wantAsserted, s.Asserted)
			assert.Equal(t, tc.wantAssertions, s.Assertions)
			assert.Equal(t, tc.wantLastAssert, s.LastAssertUnixNano)
			assert.Zero(t, s.ReadErrors)
		})
	}
}

func TestFaultMonitor_DefaultAndCustomLine(t *testing.T) {
	h := newFaultHarness(1, "")
	h.start(t)

	h.mu.Lock()
	assert.Equal(t, DefaultVLMUSBFaultLine, h.openLine)
	h.mu.Unlock()
	assert.Equal(t, "GPIO25", h.mon.LineName())

	custom := newFaultHarness(1, "GPIO26")
	custom.start(t)

	custom.mu.Lock()
	assert.Equal(t, "GPIO26", custom.openLine)
	custom.mu.Unlock()
}

func TestFaultMonitor_EdgesCountAssertions(t *testing.T) {
	h := newFaultHarness(1, "")
	h.start(t)

	// fault → clear → fault: two assertions, ends asserted.
	for i, level := range []int{0, 1, 0} {
		h.fl.set([5]int{level, 1, 1, 1, 1})
		h.edge(t)
		h.settle(t)

		s := h.snap()
		assert.Equal(t, level == 0, s.Asserted, "step %d", i)
	}

	s := h.snap()
	assert.Equal(t, int64(2), s.Assertions)
	assert.NotZero(t, s.LastAssertUnixNano)

	// Repeated edges at the same level (bounce surviving debounce) must
	// not inflate the count.
	h.edge(t)
	h.settle(t)
	assert.Equal(t, int64(2), h.snap().Assertions)
}

func TestFaultMonitor_ReadErrorBreakerStops(t *testing.T) {
	h := newFaultHarness(1, "")
	_, done := h.start(t)

	h.fl.setErr(errors.New("ioctl: line request revoked"))

	for range readErrorBreaker {
		h.edge(t)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watch goroutine did not stop after the read breaker tripped")
	}

	s := h.snap()
	assert.False(t, s.Monitored, "monitored clears when the watcher stops")
	assert.Equal(t, int64(readErrorBreaker), s.ReadErrors)

	h.fl.mu.Lock()
	defer h.fl.mu.Unlock()

	assert.True(t, h.fl.closed, "line released after breaker trip")
}

func TestFaultMonitor_ReadErrorStreakResets(t *testing.T) {
	h := newFaultHarness(1, "")
	h.start(t)

	h.fl.setErr(errors.New("transient"))

	for range readErrorBreaker - 1 {
		h.edge(t)
	}

	h.fl.setErr(nil)
	h.edge(t) // success resets the streak
	h.fl.setErr(errors.New("transient"))

	for range readErrorBreaker - 1 {
		h.edge(t)
	}

	h.fl.setErr(nil)
	h.settle(t)

	s := h.snap()
	assert.True(t, s.Monitored, "non-consecutive failures must not trip the breaker")
	assert.Equal(t, int64(2*(readErrorBreaker-1)), s.ReadErrors)
}

func TestFaultMonitor_CtxCancelReleasesLine(t *testing.T) {
	h := newFaultHarness(0, "")
	cancel, done := h.start(t)

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watch goroutine did not exit on ctx cancel")
	}

	s := h.snap()
	assert.False(t, s.Monitored)
	assert.True(t, s.Asserted, "last observed level is kept for post-mortem")

	h.fl.mu.Lock()
	defer h.fl.mu.Unlock()

	assert.True(t, h.fl.closed)
}

func TestFaultMonitor_OpenErrorStartsNothing(t *testing.T) {
	m := &FaultMonitor{
		Log: zerolog.Nop(),
		Open: func(string, func()) (FaultLines, error) {
			return nil, errors.New("line GPIO25 not found")
		},
	}

	done, err := m.Start(context.Background())
	require.Error(t, err)
	assert.Nil(t, done)
	assert.Contains(t, err.Error(), "GPIO25")

	var s FaultSnapshot

	m.Snapshot(&s)
	assert.False(t, s.Monitored)
}

func TestFaultMonitor_SnapshotNilSafe(t *testing.T) {
	var m *FaultMonitor

	s := FaultSnapshot{Assertions: 7, Monitored: true}

	assert.NotPanics(t, func() { m.Snapshot(&s) })
	assert.Equal(t, FaultSnapshot{}, s)
	assert.NotPanics(t, func() { (&FaultMonitor{}).Snapshot(nil) })
}

func TestFaultMonitor_SnapshotZeroAlloc(t *testing.T) {
	h := newFaultHarness(0, "")
	h.start(t)
	h.settle(t)

	var s FaultSnapshot

	allocs := testing.AllocsPerRun(100, func() { h.mon.Snapshot(&s) })
	assert.Zero(t, allocs)
}

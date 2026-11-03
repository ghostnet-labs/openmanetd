package scanner_test

import (
	"context"
	"sync"

	"github.com/openmanet/openmanetd/internal/gpsd"
	"github.com/openmanet/openmanetd/internal/scanner"
)

// fakeGPS is a settable PositionSource.
type fakeGPS struct {
	mu  sync.Mutex
	pos gpsd.PositionReport
}

func (f *fakeGPS) GetPosition() gpsd.PositionReport {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.pos
}

func (f *fakeGPS) set(p gpsd.PositionReport) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.pos = p
}

// fakeMonitor records Tune calls and exposes an event channel.
type fakeMonitor struct {
	events chan any
	// tuned receives every plan passed to Tune (buffered; tests read it
	// to wait for the manager goroutine).
	tuned chan scanner.TunePlan

	mu      sync.Mutex
	plans   []scanner.TunePlan
	tuneErr error
}

func newFakeMonitor() *fakeMonitor {
	return &fakeMonitor{events: make(chan any, 16), tuned: make(chan scanner.TunePlan, 64)}
}

func (f *fakeMonitor) Tune(_ context.Context, plan scanner.TunePlan) error {
	f.mu.Lock()
	f.plans = append(f.plans, plan)
	err := f.tuneErr
	f.mu.Unlock()

	select {
	case f.tuned <- plan:
	default:
	}

	return err
}

func (f *fakeMonitor) Events() <-chan any { return f.events }

func (f *fakeMonitor) setTuneErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.tuneErr = err
}

func (f *fakeMonitor) getPlans() []scanner.TunePlan {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]scanner.TunePlan(nil), f.plans...)
}

// fakeUCI is a map-backed UCI reader.
type fakeUCI struct {
	mu   sync.Mutex
	vals map[string]string
}

func (f *fakeUCI) Get(config, section, option string) ([]string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	v, ok := f.vals[config+"."+section+"."+option]
	if !ok {
		return nil, false
	}

	return []string{v}, true
}

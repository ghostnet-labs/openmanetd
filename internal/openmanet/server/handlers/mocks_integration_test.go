//go:build integration

package handlers_test

import (
	"context"
	"sync"

	"github.com/openmanet/openmanetd/internal/blos"
	"github.com/openmanet/openmanetd/internal/gpsd"
	"github.com/openmanet/openmanetd/internal/scanner"
)

// fireEvent synchronously delivers an event to all registered listeners.
// Lives behind the integration build tag because it is only used by the
// integration-test lifecycle check; including it in the default test build
// would trip the linter's unused-method rule.
func (f *fakeBLOSManager) fireEvent(ev blos.Event) {
	f.mu.Lock()
	listeners := make([]func(blos.Event), 0, len(f.listeners))

	for _, fn := range f.listeners {
		listeners = append(listeners, fn)
	}
	f.mu.Unlock()

	for _, fn := range listeners {
		fn(ev)
	}
}

// fakeScannerGPS is a fixed scanner.PositionSource.
type fakeScannerGPS struct {
	mu  sync.Mutex
	pos gpsd.PositionReport
}

func (f *fakeScannerGPS) GetPosition() gpsd.PositionReport {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.pos
}

// fakeScannerMonitor is a scanner.ControlChannelMonitor that records plans.
type fakeScannerMonitor struct {
	events chan any

	mu    sync.Mutex
	plans []scanner.TunePlan
}

func (f *fakeScannerMonitor) Tune(_ context.Context, plan scanner.TunePlan) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.plans = append(f.plans, plan)

	return nil
}

func (f *fakeScannerMonitor) Events() <-chan any { return f.events }

func (f *fakeScannerMonitor) getPlans() []scanner.TunePlan {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]scanner.TunePlan(nil), f.plans...)
}

// fakeScannerUCI is a map-backed receivers.UCIGetter.
type fakeScannerUCI struct {
	vals map[string]string // read-only after construction
}

func (f fakeScannerUCI) Get(config, section, option string) ([]string, bool) {
	v, ok := f.vals[config+"."+section+"."+option]
	if !ok {
		return nil, false
	}

	return []string{v}, true
}

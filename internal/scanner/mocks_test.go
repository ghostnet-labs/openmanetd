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

// callLog records the order of calls across fakes.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(s string) {
	if l == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.calls = append(l.calls, s)
}

func (l *callLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.calls...)
}

// releasingMonitor is a fakeMonitor that also reports receiver release.
type releasingMonitor struct {
	*fakeMonitor
	log *callLog
}

func (r *releasingMonitor) Tune(ctx context.Context, plan scanner.TunePlan) error {
	r.log.add("p25.tune:" + plan.Serial)

	return r.fakeMonitor.Tune(ctx, plan)
}

func (r *releasingMonitor) WaitReleased(_ context.Context, serial string) error {
	r.log.add("p25.released:" + serial)

	return nil
}

// fakeConventional records plans and serves a settable status and audio.
type fakeConventional struct {
	log *callLog

	mu       sync.Mutex
	plans    []scanner.ConventionalPlan
	status   scanner.ConventionalStatus
	scanErr  error
	subs     map[string]chan scanner.AudioFrame
	canceled int
}

func newFakeConventional(log *callLog) *fakeConventional {
	return &fakeConventional{log: log, subs: map[string]chan scanner.AudioFrame{}}
}

func (f *fakeConventional) Scan(plan scanner.ConventionalPlan) error {
	f.log.add("conv.scan:" + plan.Serial)

	f.mu.Lock()
	defer f.mu.Unlock()

	f.plans = append(f.plans, plan)

	return f.scanErr
}

func (f *fakeConventional) WaitIdle(context.Context) error {
	f.log.add("conv.idle")

	return nil
}

func (f *fakeConventional) Status() scanner.ConventionalStatus {
	f.mu.Lock()
	defer f.mu.Unlock()

	st := f.status
	st.Channels = append([]scanner.ConventionalChannelStatus(nil), f.status.Channels...)

	return st
}

func (f *fakeConventional) SubscribeAudio(key string) (<-chan scanner.AudioFrame, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ch := make(chan scanner.AudioFrame, 4)
	f.subs[key] = ch

	var once sync.Once

	return ch, func() {
		once.Do(func() {
			f.mu.Lock()
			defer f.mu.Unlock()

			f.canceled++

			close(ch)
		})
	}, nil
}

func (f *fakeConventional) setStatus(st scanner.ConventionalStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.status = st
}

func (f *fakeConventional) getPlans() []scanner.ConventionalPlan {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]scanner.ConventionalPlan(nil), f.plans...)
}

func (f *fakeConventional) getCanceled() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.canceled
}

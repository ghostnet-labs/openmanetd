package pathsel_test

import (
	"sync"
	"time"

	"github.com/openmanet/openmanetd/internal/pathsel"
)

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu sync.Mutex // protects t
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.t = c.t.Add(d)
}

type pathKey struct {
	dest string
	band pathsel.Band
}

// fakeMetricsSource serves per-(dest, band) metrics.
type fakeMetricsSource struct {
	mu    sync.Mutex // protects the fields below
	data  map[pathKey]pathsel.Metrics
	calls int
}

func newFakeMetricsSource() *fakeMetricsSource {
	return &fakeMetricsSource{data: make(map[pathKey]pathsel.Metrics)}
}

func (f *fakeMetricsSource) PathMetrics(dest string, band pathsel.Band) (pathsel.Metrics, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++
	m, ok := f.data[pathKey{dest: dest, band: band}]

	return m, ok
}

func (f *fakeMetricsSource) set(dest string, band pathsel.Band, m pathsel.Metrics) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.data[pathKey{dest: dest, band: band}] = m
}

func (f *fakeMetricsSource) remove(dest string, band pathsel.Band) {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.data, pathKey{dest: dest, band: band})
}

func (f *fakeMetricsSource) getCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls
}

// fakeBandAdmitter denies the bands in denied with the mapped reason.
type fakeBandAdmitter struct {
	mu     sync.Mutex // protects the fields below
	denied map[pathsel.Band]string
	calls  int
}

func (f *fakeBandAdmitter) AdmitBand(band pathsel.Band) (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++
	if reason, ok := f.denied[band]; ok {
		return false, reason
	}

	return true, ""
}

func (f *fakeBandAdmitter) deny(band pathsel.Band, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.denied == nil {
		f.denied = make(map[pathsel.Band]string)
	}

	f.denied[band] = reason
}

func (f *fakeBandAdmitter) getCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls
}

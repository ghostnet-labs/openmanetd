package jamdetect_test

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/openmanet/openmanetd/internal/jamdetect"
)

// fakeSource serves scripted radios and surveys. Counters advance by
// the per-sample deltas set with setChannel.
type fakeSource struct {
	mu        sync.Mutex
	radios    []jamdetect.Radio
	surveys   map[string]jamdetect.Survey
	radiosErr error
	surveyErr map[string]error
	calls     int
}

func newFakeSource(radios ...jamdetect.Radio) *fakeSource {
	return &fakeSource{
		radios:    radios,
		surveys:   make(map[string]jamdetect.Survey, len(radios)),
		surveyErr: make(map[string]error),
	}
}

// step advances one radio's counters by activeMs with the given busy,
// rx and tx shares and sets its noise floor and frequency.
func (f *fakeSource) step(ifname string, freq float64, noise int, activeMs, busyMs, rxMs, txMs uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()

	s := f.surveys[ifname]
	s.FreqMHz = freq
	s.InUse = true
	s.NoiseDBm = noise
	s.HasNoise = noise != 0
	s.HasActive, s.HasBusy, s.HasRx, s.HasTx = true, true, true, true
	s.ActiveMs += activeMs
	s.BusyMs += busyMs
	s.RxMs += rxMs
	s.TxMs += txMs
	f.surveys[ifname] = s
}

func (f *fakeSource) resetCounters(ifname string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	s := f.surveys[ifname]
	s.ActiveMs, s.BusyMs, s.RxMs, s.TxMs = 0, 0, 0, 0
	f.surveys[ifname] = s
}

func (f *fakeSource) setRadios(radios ...jamdetect.Radio) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.radios = radios
}

func (f *fakeSource) setRadiosErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.radiosErr = err
}

func (f *fakeSource) Radios(context.Context) ([]jamdetect.Radio, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++

	if f.radiosErr != nil {
		return nil, f.radiosErr
	}

	return append([]jamdetect.Radio(nil), f.radios...), nil
}

func (f *fakeSource) Survey(_ context.Context, ifname string) ([]jamdetect.Survey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.surveyErr[ifname]; err != nil {
		return nil, err
	}

	s, ok := f.surveys[ifname]
	if !ok {
		return nil, errors.New("no survey")
	}

	return []jamdetect.Survey{{FreqMHz: 2412}, s}, nil
}

// fakeGossip records publishes and serves scripted records.
type fakeGossip struct {
	mu         sync.Mutex
	published  [][]byte
	records    []jamdetect.GossipRecord
	publishErr error
	fetchErr   error
}

func (g *fakeGossip) Publish(p []byte) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.publishErr != nil {
		return g.publishErr
	}

	g.published = append(g.published, append([]byte(nil), p...))

	return nil
}

func (g *fakeGossip) Fetch() ([]jamdetect.GossipRecord, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.fetchErr != nil {
		return nil, g.fetchErr
	}

	return append([]jamdetect.GossipRecord(nil), g.records...), nil
}

func (g *fakeGossip) setRecords(recs ...jamdetect.GossipRecord) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.records = recs
}

func (g *fakeGossip) lastPublished() []byte {
	g.mu.Lock()
	defer g.mu.Unlock()

	if len(g.published) == 0 {
		return nil
	}

	return g.published[len(g.published)-1]
}

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 7, 1, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

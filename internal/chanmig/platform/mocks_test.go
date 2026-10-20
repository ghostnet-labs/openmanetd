package platform_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/digineo/go-uci/v2"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/chanmig"
	"github.com/openmanet/openmanetd/internal/chanmig/platform"
)

type gossipRecord = platform.GossipRecord

var errInjected = errors.New("injected failure")

// fakeAlfred is an in-memory alfred: one record per source, every client
// sees every record.
type fakeAlfred struct {
	records map[string][]byte
	order   []string
	mu      sync.Mutex
}

func newFakeAlfred() *fakeAlfred {
	return &fakeAlfred{records: map[string][]byte{}}
}

// client returns a Gossip handle publishing as mac.
func (a *fakeAlfred) client(t *testing.T, mac string) *fakeGossip {
	t.Helper()

	hw, err := net.ParseMAC(mac)
	require.NoError(t, err)

	return &fakeGossip{net: a, mac: hw}
}

func (a *fakeAlfred) set(mac net.HardwareAddr, data []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()

	key := mac.String()
	if _, ok := a.records[key]; !ok {
		a.order = append(a.order, key)
	}

	a.records[key] = bytes.Clone(data)
}

func (a *fakeAlfred) drop(mac string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	delete(a.records, mac)
	a.order = slices.DeleteFunc(a.order, func(s string) bool { return s == mac })
}

func (a *fakeAlfred) record(mac string) []byte {
	a.mu.Lock()
	defer a.mu.Unlock()

	return bytes.Clone(a.records[mac])
}

func (a *fakeAlfred) all() []struct {
	mac  string
	data []byte
} {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]struct {
		mac  string
		data []byte
	}, 0, len(a.order))

	for _, k := range a.order {
		out = append(out, struct {
			mac  string
			data []byte
		}{mac: k, data: bytes.Clone(a.records[k])})
	}

	return out
}

// fakeGossip implements platform.Gossip against a fakeAlfred.
type fakeGossip struct {
	net        *fakeAlfred
	publishErr error
	fetchErr   error
	mac        net.HardwareAddr
	mu         sync.Mutex
	publishes  int
	fetches    int
}

func (g *fakeGossip) Publish(payload []byte) error {
	g.mu.Lock()
	g.publishes++
	err := g.publishErr
	g.mu.Unlock()

	if err != nil {
		return err
	}

	g.net.set(g.mac, payload)

	return nil
}

func (g *fakeGossip) Fetch() ([]gossipRecord, error) {
	g.mu.Lock()
	g.fetches++
	err := g.fetchErr
	g.mu.Unlock()

	if err != nil {
		return nil, err
	}

	all := g.net.all()
	out := make([]gossipRecord, 0, len(all))

	for _, r := range all {
		hw, perr := net.ParseMAC(r.mac)
		if perr != nil {
			return nil, fmt.Errorf("parse stored mac: %w", perr)
		}

		out = append(out, gossipRecord{Source: hw, Data: r.data})
	}

	return out, nil
}

func (g *fakeGossip) setErrors(publish, fetch error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.publishErr, g.fetchErr = publish, fetch
}

func (g *fakeGossip) publishCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.publishes
}

// delivery is one message a Handler received.
type delivery struct {
	payload []byte
	band    chanmig.Band
}

// sink collects deliveries.
type sink struct {
	got []delivery
	mu  sync.Mutex
}

func (s *sink) handle(_ context.Context, band chanmig.Band, payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.got = append(s.got, delivery{band: band, payload: bytes.Clone(payload)})
}

func (s *sink) take() []delivery {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := s.got
	s.got = nil

	return out
}

// fakeUCI implements network.ConfigReader over an in-memory tree of the
// wireless config.
type fakeUCI struct {
	options    map[string]map[string][]string // section -> option -> values
	types      map[string]string              // section -> section type
	commitErr  error
	reloadErr  error
	sections   []string
	mu         sync.Mutex
	commits    int
	reloads    int
	setOptions int
}

func newFakeUCI() *fakeUCI {
	return &fakeUCI{options: map[string]map[string][]string{}, types: map[string]string{}}
}

func (f *fakeUCI) addDevice(name, band, channel, htmode string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.sections = append(f.sections, name)
	f.types[name] = "wifi-device"
	f.options[name] = map[string][]string{
		"band":    {band},
		"channel": {channel},
		"htmode":  {htmode},
	}
}

func (f *fakeUCI) option(section, option string) string {
	f.mu.Lock()
	defer f.mu.Unlock()

	if v := f.options[section][option]; len(v) > 0 {
		return v[0]
	}

	return ""
}

func (f *fakeUCI) counts() (commits, reloads, sets int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.commits, f.reloads, f.setOptions
}

func (f *fakeUCI) setErrors(commit, reload error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.commitErr, f.reloadErr = commit, reload
}

func (f *fakeUCI) Get(config, section, option string) ([]string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if config != "wireless" {
		return nil, false
	}

	v, ok := f.options[section][option]

	return slices.Clone(v), ok
}

func (f *fakeUCI) GetSections(config, secType string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if config != "wireless" {
		return nil, nil
	}

	out := make([]string, 0, len(f.sections))

	for _, s := range f.sections {
		if f.types[s] == secType {
			out = append(out, s)
		}
	}

	return out, nil
}

func (f *fakeUCI) SetType(_, section, option string, _ uci.OptionType, values ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.options[section] == nil {
		f.options[section] = map[string][]string{}
	}

	f.options[section][option] = slices.Clone(values)
	f.setOptions++

	return nil
}

func (f *fakeUCI) Del(_, section, option string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.options[section], option)

	return nil
}

func (f *fakeUCI) AddSection(_, section, typ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.types[section]; !ok {
		f.sections = append(f.sections, section)
		f.types[section] = typ
	}

	return nil
}

func (f *fakeUCI) DelSection(_, section string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.types, section)
	delete(f.options, section)

	return nil
}

func (f *fakeUCI) Commit() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.commits++

	return f.commitErr
}

func (f *fakeUCI) ReloadConfig() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.reloads++

	return f.reloadErr
}

// fakeReloader records system reloads.
type fakeReloader struct {
	err      error
	ctxErrs  []error
	mu       sync.Mutex
	reloaded int
}

func (r *fakeReloader) reload(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.reloaded++
	r.ctxErrs = append(r.ctxErrs, ctx.Err())

	return r.err
}

func (r *fakeReloader) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.reloaded
}

func (r *fakeReloader) contextErrors() []error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.ctxErrs)
}

// stepClock is a manually advanced chanmig.Clock.
type stepClock struct {
	now    time.Time
	mu     sync.Mutex
	synced bool
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *stepClock) Synced() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.synced
}

func (c *stepClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

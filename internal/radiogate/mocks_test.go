package radiogate_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/digineo/go-uci/v2"
	"github.com/openmanet/openmanetd/internal/iwinfo"
	"github.com/openmanet/openmanetd/internal/network"
	"github.com/openmanet/openmanetd/internal/radiocap"
	"github.com/stretchr/testify/require"
)

// fixture reads testfixtures/radiocap/<name> relative to this file.
func fixture(t *testing.T, name string) []byte {
	t.Helper()

	_, here, _, ok := runtime.Caller(0)
	require.True(t, ok)

	data, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "..", "testfixtures", "radiocap", name))
	require.NoError(t, err)

	return data
}

// fakeExec serves canned `iw list` output and counts runs.
type fakeExec struct {
	mu    sync.Mutex // protects the fields below
	out   []byte
	err   error
	calls int
}

func (f *fakeExec) Run(_ context.Context) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++
	if f.err != nil {
		return nil, f.err
	}

	return f.out, nil
}

func (f *fakeExec) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.err = err
}

func (f *fakeExec) getCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls
}

// fakeHaLow returns a fixed HaLow wiphy list.
type fakeHaLow struct {
	mu    sync.Mutex // protects the fields below
	phys  []string
	err   error
	calls int
}

func (f *fakeHaLow) PHYs(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++

	return f.phys, f.err
}

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu sync.Mutex // protects t
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
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

// fakeSource is a ModelSource with a fixed model or error.
type fakeSource struct {
	mu    sync.Mutex // protects the fields below
	model *radiocap.Model
	err   error
	calls int
}

func (f *fakeSource) Model(_ context.Context) (*radiocap.Model, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++

	return f.model, f.err
}

func (f *fakeSource) set(m *radiocap.Model, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.model, f.err = m, err
}

func (f *fakeSource) getCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls
}

// fakeConfigReader is a read-only in-memory UCI tree.
type fakeConfigReader struct {
	data     map[string]map[string]string // section -> option -> value
	types    map[string]string            // section -> type
	sectsErr error
}

func (f *fakeConfigReader) Get(_, section, option string) ([]string, bool) {
	v, ok := f.data[section][option]
	if !ok {
		return nil, false
	}

	return []string{v}, true
}

func (f *fakeConfigReader) GetSections(_, secType string) ([]string, error) {
	if f.sectsErr != nil {
		return nil, f.sectsErr
	}

	out := make([]string, 0, len(f.types))
	for s, typ := range f.types {
		if typ == secType {
			out = append(out, s)
		}
	}

	sort.Strings(out)

	return out, nil
}

func (f *fakeConfigReader) SetType(string, string, string, uci.OptionType, ...string) error {
	return nil
}
func (f *fakeConfigReader) Del(string, string, string) error        { return nil }
func (f *fakeConfigReader) AddSection(string, string, string) error { return nil }
func (f *fakeConfigReader) DelSection(string, string) error         { return nil }
func (f *fakeConfigReader) Commit() error                           { return nil }
func (f *fakeConfigReader) ReloadConfig() error                     { return nil }

var _ network.ConfigReader = (*fakeConfigReader)(nil)

// fakeWirelessStatus serves a canned ubus wireless status.
type fakeWirelessStatus struct {
	status map[string]*network.WirelessRadioStatus
	err    error
}

func (f *fakeWirelessStatus) GetWirelessStatus(_ context.Context) (map[string]*network.WirelessRadioStatus, error) {
	return f.status, f.err
}

// fakeIwinfo serves canned iwinfo data.
type fakeIwinfo struct {
	info map[string]*iwinfo.InterfaceInfo
	err  error
}

func (f *fakeIwinfo) GetDevices(_ context.Context) ([]string, error) { return nil, nil }

func (f *fakeIwinfo) GetInfo(_ context.Context, device string) (*iwinfo.InterfaceInfo, error) {
	return f.info[device], f.err
}

func (f *fakeIwinfo) GetInfoForAll(_ context.Context) (map[string]*iwinfo.InterfaceInfo, error) {
	return f.info, f.err
}

package hardware_test

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/openmanet/openmanetd/internal/hardware"
	"github.com/openmanet/openmanetd/internal/system"
)

// fakeClock is a manual clock. Sleep advances virtual time instantly.
type fakeClock struct {
	mu  sync.Mutex // protects now
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("sleep: %w", err)
	}

	c.Advance(d)

	return nil
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// callLog records cross-fake calls in order, so a test can check that one
// hardware action happened before another (for example SUPERVISOR_ARM low
// before the watchdog closes). A nil *callLog records nothing.
type callLog struct {
	mu    sync.Mutex // protects calls
	calls []string
}

func (c *callLog) add(call string) {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.calls = append(c.calls, call)
}

func (c *callLog) get() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.calls...)
}

// index returns the position of the first call equal to want, or -1.
func (c *callLog) index(want string) int {
	for i, call := range c.get() {
		if call == want {
			return i
		}
	}

	return -1
}

// setCall is one recorded Lines.Set.
type setCall struct {
	name     string
	asserted bool
}

// fakeLines records every Set and serves Get from a value map.
type fakeLines struct {
	mu        sync.Mutex // protects the fields below
	values    map[string]bool
	edges     map[string]uint64
	requested map[string]hardware.LineSpec
	sets      []setCall
	setErr    map[string]error
	log       *callLog
	closed    bool
}

func (l *fakeLines) Set(name string, asserted bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, ok := l.requested[name]; !ok {
		return fmt.Errorf("%s: %w", name, hardware.ErrLineNotRequested)
	}

	if err := l.setErr[name]; err != nil {
		return err
	}

	l.sets = append(l.sets, setCall{name: name, asserted: asserted})
	l.values[name] = asserted
	l.log.add("set " + name + "=" + strconv.FormatBool(asserted))

	return nil
}

func (l *fakeLines) Get(name string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, ok := l.requested[name]; !ok {
		return false, fmt.Errorf("%s: %w", name, hardware.ErrLineNotRequested)
	}

	return l.values[name], nil
}

func (l *fakeLines) Edges(name string) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.edges[name]
}

func (l *fakeLines) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.closed = true
	l.log.add("lines close")

	return nil
}

// setSetErr makes Set fail for one line (nil clears it).
func (l *fakeLines) setSetErr(name string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.setErr[name] = err
}

// setsOf returns the recorded Set calls for one line.
func (l *fakeLines) setsOf(name string) []bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]bool, 0, len(l.sets))

	for _, c := range l.sets {
		if c.name == name {
			out = append(out, c.asserted)
		}
	}

	return out
}

// setValue changes a line level as the hardware would (input edge).
func (l *fakeLines) setValue(name string, v bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.values[name] != v {
		l.edges[name]++
	}

	l.values[name] = v
}

func (l *fakeLines) getSets() []setCall {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]setCall(nil), l.sets...)
}

func (l *fakeLines) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.closed
}

func (l *fakeLines) value(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.values[name]
}

// fakeLineOpener resolves names from a set and hands out one fakeLines.
type fakeLineOpener struct {
	mu         sync.Mutex // protects the fields below
	resolvable map[string]bool
	initial    map[string]bool
	openErr    error
	opened     []hardware.LineSpec
	lines      *fakeLines
	log        *callLog
	openCalls  int
}

func newFakeLineOpener() *fakeLineOpener {
	o := &fakeLineOpener{resolvable: map[string]bool{}, initial: map[string]bool{}}

	for _, n := range []string{
		hardware.LineHaLowPwrEn, hardware.LineWiFiPwrEn, hardware.LineGNSSResetN,
		hardware.LineHaLowResetN, hardware.LineHaLowWakeN, hardware.LineWiFiWDis1N,
		hardware.LineUSBHubResetN, hardware.LineSupervisorARM,
	} {
		o.resolvable[n] = true
	}

	for _, n := range hardware.FaultInputNames() {
		o.resolvable[n] = true
	}

	return o
}

func (o *fakeLineOpener) Resolve(name string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.resolvable[name]
}

func (o *fakeLineOpener) Open(specs []hardware.LineSpec) (hardware.Lines, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.openCalls++

	if o.openErr != nil {
		return nil, o.openErr
	}

	l := &fakeLines{
		values:    make(map[string]bool, len(specs)),
		edges:     make(map[string]uint64, len(specs)),
		requested: make(map[string]hardware.LineSpec, len(specs)),
		setErr:    map[string]error{},
		log:       o.log,
	}

	for _, s := range specs {
		l.requested[s.Name] = s
		// An output requested StartDeasserted is driven inactive at
		// request time, whatever level it had before.
		l.values[s.Name] = o.initial[s.Name] && !s.StartDeasserted
	}

	o.opened = append(o.opened, specs...)
	o.lines = l

	return l, nil
}

func (o *fakeLineOpener) getOpened() []hardware.LineSpec {
	o.mu.Lock()
	defer o.mu.Unlock()

	return append([]hardware.LineSpec(nil), o.opened...)
}

func (o *fakeLineOpener) getLines() *fakeLines {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.lines
}

// fakeBus models device and netdev presence per radio.
type fakeBus struct {
	mu          sync.Mutex // protects the fields below
	configured  [3]bool
	device      [3]bool
	netdev      [3]bool
	resetErr    error
	resetCalls  [3]int
	removeCalls [3]int
	rescanCalls [3]int
	// restoreOnReset makes ResetDevice bring the device back.
	restoreOnReset bool
}

func newFakeBus() *fakeBus {
	b := &fakeBus{}
	for _, r := range []hardware.Radio{hardware.RadioHaLow, hardware.RadioWiFi} {
		b.configured[r] = true
		b.device[r] = true
		b.netdev[r] = true
	}

	return b
}

func (b *fakeBus) Configured(r hardware.Radio) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.configured[r]
}

func (b *fakeBus) DevicePresent(r hardware.Radio) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.device[r]
}

func (b *fakeBus) NetdevPresent(r hardware.Radio) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.netdev[r]
}

func (b *fakeBus) Netdev(r hardware.Radio) string {
	return r.String() + "0"
}

func (b *fakeBus) ResetDevice(_ context.Context, r hardware.Radio) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.resetCalls[r]++

	if b.restoreOnReset {
		b.device[r], b.netdev[r] = true, true
	}

	return b.resetErr
}

func (b *fakeBus) Remove(_ context.Context, r hardware.Radio) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.removeCalls[r]++

	return nil
}

func (b *fakeBus) Rescan(_ context.Context, r hardware.Radio) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.rescanCalls[r]++

	return nil
}

func (b *fakeBus) setPresent(r hardware.Radio, device, netdev bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.device[r], b.netdev[r] = device, netdev
}

func (b *fakeBus) getResetCalls(r hardware.Radio) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.resetCalls[r]
}

func (b *fakeBus) getRemoveCalls(r hardware.Radio) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.removeCalls[r]
}

func (b *fakeBus) getRescanCalls(r hardware.Radio) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.rescanCalls[r]
}

// fakeMonitor serves a fixed INA228 reading.
type fakeMonitor struct {
	mu        sync.Mutex // protects the fields below
	reading   system.MonitorReading
	err       error
	calls     int
	fullCalls int
}

func (f *fakeMonitor) ReadMonitor(full bool) (system.MonitorReading, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++
	if full {
		f.fullCalls++
	}

	r := f.reading
	if !full {
		r.HasEnergy, r.HasTemp, r.Alarms = false, false, 0
	}

	return r, f.err
}

func (f *fakeMonitor) set(r system.MonitorReading, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.reading, f.err = r, err
}

func (f *fakeMonitor) getFullCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.fullCalls
}

// fakeStore is an in-memory RecoveryStore with error injection.
type fakeStore struct {
	mu      sync.Mutex // protects the fields below
	rec     hardware.RecoveryRecord
	loadErr error
	saveErr error
	saves   int
}

func (s *fakeStore) Load() (hardware.RecoveryRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.rec, s.loadErr
}

func (s *fakeStore) Save(rec hardware.RecoveryRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.saveErr != nil {
		return s.saveErr
	}

	s.saves++
	s.rec = rec

	return nil
}

func (s *fakeStore) getSaves() (int, hardware.RecoveryRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.saves, s.rec
}

// fakeWatchdog counts pets and records how it was closed.
type fakeWatchdog struct {
	mu         sync.Mutex // protects the fields below
	petErr     error
	log        *callLog
	pets       int
	closed     bool
	cleanClose bool
}

func (w *fakeWatchdog) Pet() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.petErr != nil {
		return w.petErr
	}

	w.pets++
	w.log.add("pet")

	return nil
}

func (w *fakeWatchdog) Close(clean bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.closed, w.cleanClose = true, clean
	w.log.add("watchdog close")

	return nil
}

func (w *fakeWatchdog) setPetErr(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.petErr = err
}

func (w *fakeWatchdog) getPets() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.pets
}

func (w *fakeWatchdog) getClose() (closed, clean bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.closed, w.cleanClose
}

type fakeWatchdogOpener struct {
	mu       sync.Mutex // protects the fields below
	wd       *fakeWatchdog
	openErr  error
	identity string
	opens    int
}

func (o *fakeWatchdogOpener) Open(identity string) (hardware.Watchdog, string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.opens++
	o.identity = identity

	if o.openErr != nil {
		return nil, "", o.openErr
	}

	return o.wd, "/dev/watchdog1", nil
}

func (o *fakeWatchdogOpener) getOpens() int {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.opens
}

type fakeShutdowner struct {
	mu    sync.Mutex // protects the fields below
	log   *callLog
	calls int
}

func (s *fakeShutdowner) Poweroff(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls++
	s.log.add("poweroff")

	return nil
}

func (s *fakeShutdowner) getCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.calls
}

type fakeCommsGuard struct {
	mu          sync.Mutex // protects the fields below
	transmit    bool
	beforeCalls int
}

func (c *fakeCommsGuard) TransmitActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.transmit
}

func (c *fakeCommsGuard) BeforeHubReset(context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.beforeCalls++
}

func (c *fakeCommsGuard) getBeforeCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.beforeCalls
}

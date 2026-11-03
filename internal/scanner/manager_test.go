package scanner_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"
	"time"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/gpsd"
	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/openmanet/openmanetd/internal/scanner/aircraft"
	"github.com/openmanet/openmanetd/internal/scanner/receivers"
	"github.com/openmanet/openmanetd/internal/scanner/sites"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mhz = 1_000_000

// t0 is the "now" of the dump1090 fixture.
var t0 = time.Unix(1762150000, 0) //nolint:gochecknoglobals // test constant

const airspySerial = "AIRSPY SN:44A061DC2E395E8F"

func testSites() []sites.Site {
	return []sites.Site{
		{
			Key: "UT-west", State: "UT", Name: "West Peak", Lat: 40, Lon: -112,
			ControlHz: []uint64{770_006_250, 770_256_250}, VoiceHz: []uint64{770_500_000, 771_000_000},
		},
		{
			Key: "UT-mid", State: "UT", Name: "Mid Peak", Lat: 40, Lon: -111.5,
			ControlHz: []uint64{851_012_500}, VoiceHz: []uint64{852_000_000, 860_000_000},
		},
		{Key: "CO-east", State: "CO", Name: "East Peak", Lat: 40, Lon: -111, ControlHz: []uint64{155_000_000}},
	}
}

func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)

	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}

		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir)
		dir = parent
	}

	b, err := os.ReadFile(filepath.Join(dir, "testfixtures", "scanner", name))
	require.NoError(t, err)

	return b
}

func usbDevice(fsys fstest.MapFS, dir, vendor, product, serial string) {
	fsys[dir+"/idVendor"] = &fstest.MapFile{Data: []byte(vendor)}
	fsys[dir+"/idProduct"] = &fstest.MapFile{Data: []byte(product)}
	fsys[dir+"/serial"] = &fstest.MapFile{Data: []byte(serial)}
}

func testConfig() config.ScannerConfig {
	return config.ScannerConfig{
		Enable:             true,
		Mode:               config.ScannerModeAuto,
		HysteresisKm:       10,
		PositionStaleAfter: time.Minute,
		AircraftStaleAfter: time.Minute,
		DecodeStaleAfter:   10 * time.Second,
		WideSerial:         "44a061dc2e395e8f",
		ScanSerial:         "scan",
		WideUsableHz:       8 * mhz,
		TalkgroupHistory:   4,
	}
}

type harness struct {
	mgr *scanner.Manager
	gps *fakeGPS
	mon *fakeMonitor
	sys fstest.MapFS
}

func newHarness(t *testing.T, withMonitor bool, cfg config.ScannerConfig) *harness {
	t.Helper()

	h := &harness{gps: &fakeGPS{}, sys: fstest.MapFS{}}
	usbDevice(h.sys, "1-1.1", "0bda", "2838", "1090")
	usbDevice(h.sys, "1-1.2", "0bda", "2838", "978")
	usbDevice(h.sys, "1-1.3", "1d50", "60a1", airspySerial)
	usbDevice(h.sys, "1-1.4", "0bda", "2838", "scan")

	opts := scanner.Options{
		Log:      zerolog.Nop(),
		Position: h.gps,
		UCI: &fakeUCI{vals: map[string]string{
			"dump1090.main.device_index": "1090",
			"dump978-fa.main.serial":     "978",
			"dump978-fa.main.enabled":    "1",
		}},
		RootFS: fstest.MapFS{"var/run/dump1090/aircraft.json": {Data: fixtureBytes(t, "dump1090-aircraft.json")}},
		SysFS:  h.sys,
		Now:    func() time.Time { return t0 },
		Sites:  testSites(),
	}

	if withMonitor {
		h.mon = newFakeMonitor()
		opts.Monitor = h.mon
	}

	h.mgr = scanner.NewManager(opts, cfg)
	h.mgr.MarkEnabledForTest()

	return h
}

func (h *harness) fix(lon float64, at time.Time) {
	h.gps.set(gpsd.PositionReport{Valid: true, Mode: 3, Latitude: 40, Longitude: lon, Timestamp: at})
}

func TestManager_selectsNearestSiteAndTunes(t *testing.T) {
	h := newHarness(t, true, testConfig())
	h.fix(-112.01, t0)
	h.mgr.TickForTest(context.Background(), t0)

	st := h.mgr.Status()
	require.True(t, st.Enabled)
	assert.Equal(t, scanner.PositionGPSD, st.Position.Source)
	assert.False(t, st.Position.Stale)
	require.NotNil(t, st.Selection)
	assert.Equal(t, "UT-west", st.Selection.Site.Key)
	assert.Equal(t, sites.ReasonNearest, st.Selection.Reason)
	assert.InDelta(t, 0.85, st.Selection.DistanceKm, 0.05)
	assert.True(t, st.Selection.Plan.Fits)
	assert.Equal(t, scanner.ControlHunting, st.Control.State)
	assert.Equal(t, uint64(770_006_250), st.Control.FrequencyHz)
	assert.False(t, st.AircraftStale, "the dump1090 feed is fresh")

	plans := h.mon.getPlans()
	require.Len(t, plans, 1)
	assert.Equal(t, scanner.TunePlan{
		SiteKey:   "UT-west",
		ControlHz: []uint64{770_006_250, 770_256_250},
		VoiceHz:   []uint64{770_500_000, 771_000_000},
		CenterHz:  770_503_125,
		UsableHz:  8 * mhz,
		Serial:    "44a061dc2e395e8f",
		Tuner:     scanner.TunerWide,
	}, plans[0])

	// Same site next tick: no retune.
	h.mgr.TickForTest(context.Background(), t0.Add(time.Second))
	assert.Len(t, h.mon.getPlans(), 1)

	require.Len(t, st.Receivers, 4)
	assert.Equal(t, receivers.StateRunning, st.Receivers[0].State, "1090: present and fresh aircraft.json")
	assert.Equal(t, receivers.StateStopped, st.Receivers[1].State, "978: present, no aircraft.json")
	assert.Contains(t, st.Receivers[1].Detail, "aircraft.json not found")
	assert.Equal(t, receivers.StateStopped, st.Receivers[2].State, "wide: tuned but nothing decoded yet")
	assert.Equal(t, receivers.HardwareAirspy, st.Receivers[2].Hardware)
	assert.Equal(t, receivers.StateStopped, st.Receivers[3].State)
	assert.Contains(t, st.Receivers[3].Detail, "not wired")
}

func TestManager_hysteresisAndSiteEvents(t *testing.T) {
	h := newHarness(t, true, testConfig())
	events, cancel, err := h.mgr.Subscribe()
	require.NoError(t, err)
	t.Cleanup(cancel)

	h.fix(-112, t0)
	h.mgr.TickForTest(context.Background(), t0)

	ev := <-events
	assert.Equal(t, scanner.EventSite, ev.Kind)
	require.NotNil(t, ev.Site)
	assert.Equal(t, "UT-west", ev.Site.Site.Key)
	assert.Equal(t, scanner.EventControl, (<-events).Kind)

	// Slightly nearer to mid: held by hysteresis.
	h.fix(-111.725, t0.Add(time.Second))
	h.mgr.TickForTest(context.Background(), t0.Add(time.Second))
	assert.Equal(t, sites.ReasonHysteresis, h.mgr.Status().Selection.Reason)
	assert.Len(t, h.mon.getPlans(), 1)

	// Well past: switch and retune, the 8 MHz window drops 860 MHz.
	h.fix(-111.6, t0.Add(2*time.Second))
	h.mgr.TickForTest(context.Background(), t0.Add(2*time.Second))

	ev = <-events
	require.Equal(t, scanner.EventSite, ev.Kind)
	assert.Equal(t, "UT-mid", ev.Site.Site.Key)

	plans := h.mon.getPlans()
	require.Len(t, plans, 2)
	assert.Equal(t, []uint64{852_000_000}, plans[1].VoiceHz)
	assert.False(t, h.mgr.Status().Selection.Plan.Fits)
	assert.Equal(t, []uint64{860_000_000}, h.mgr.Status().Selection.Plan.Uncovered)
}

func TestManager_decoderEventsAndHistory(t *testing.T) {
	h := newHarness(t, true, testConfig())
	h.fix(-112, t0)
	h.mgr.TickForTest(context.Background(), t0)

	events, cancel, err := h.mgr.Subscribe()
	require.NoError(t, err)
	t.Cleanup(cancel)

	h.mgr.HandleEventForTest(scanner.SyncState{Locked: true}, t0.Add(time.Second))
	assert.Equal(t, scanner.ControlLocked, h.mgr.Status().Control.State)
	assert.Equal(t, scanner.ControlLocked, (<-events).Control.State)

	h.mgr.HandleEventForTest(scanner.SiteStatus{WACN: 0xBEE00, SystemID: 0x3A1, RFSS: 2, Site: 1, NAC: 0x3A1}, t0.Add(time.Second))

	grant := scanner.VoiceGrant{Talkgroup: 101, Source: 1234567, Channel: 0x1123, FrequencyHz: 770_500_000}
	h.mgr.HandleEventForTest(grant, t0.Add(2*time.Second))
	update := grant
	update.Source, update.Emergency = 0, true
	h.mgr.HandleEventForTest(update, t0.Add(3*time.Second))
	h.mgr.HandleEventForTest(scanner.VoiceGrant{Talkgroup: 202, FrequencyHz: 771_000_000, Encrypted: true}, t0.Add(4*time.Second))
	// Same talkgroup after the merge window: a new call.
	h.mgr.HandleEventForTest(grant, t0.Add(10*time.Second))
	h.mgr.HandleEventForTest("not a decoder event", t0.Add(11*time.Second))

	first := <-events
	require.Equal(t, scanner.EventGrant, first.Kind)
	assert.Equal(t, uint32(1), first.Grant.Grants)
	assert.Equal(t, uint32(2), (<-events).Grant.Grants)

	grants, capacity := h.mgr.Grants(0)
	assert.Equal(t, 4, capacity)
	require.Len(t, grants, 3)
	assert.Equal(t, t0.Add(10*time.Second), grants[0].Last)
	assert.Equal(t, uint32(202), grants[1].Talkgroup)
	assert.True(t, grants[1].Encrypted)
	assert.Equal(t, scanner.Grant{
		Start: t0.Add(2 * time.Second), Last: t0.Add(3 * time.Second), SiteKey: "UT-west",
		FrequencyHz: 770_500_000, Talkgroup: 101, Source: 1234567, Channel: 0x1123, Grants: 2, Emergency: true,
	}, grants[2])

	limited, _ := h.mgr.Grants(1)
	assert.Len(t, limited, 1)

	st := h.mgr.Status()
	assert.Equal(t, uint32(0xBEE00), st.Control.WACN)
	assert.Equal(t, uint32(0x3A1), st.Control.NAC)
	assert.Equal(t, t0.Add(10*time.Second), st.Control.LastDecode)

	// The ring holds 4 calls: two more push the oldest out.
	h.mgr.HandleEventForTest(scanner.VoiceGrant{Talkgroup: 303}, t0.Add(20*time.Second))
	h.mgr.HandleEventForTest(scanner.VoiceGrant{Talkgroup: 404}, t0.Add(21*time.Second))
	grants, _ = h.mgr.Grants(0)
	require.Len(t, grants, 4)
	assert.Equal(t, uint32(404), grants[0].Talkgroup)
	assert.Equal(t, uint32(202), grants[3].Talkgroup)
}

func TestManager_decodeStaleAndWideRunning(t *testing.T) {
	h := newHarness(t, true, testConfig())
	h.fix(-112, t0)
	h.mgr.TickForTest(context.Background(), t0)

	h.mgr.HandleEventForTest(scanner.SiteStatus{NAC: 1}, t0.Add(time.Second))
	h.mgr.TickForTest(context.Background(), t0.Add(2*time.Second))

	st := h.mgr.Status()
	assert.False(t, st.Control.Stale)
	assert.Equal(t, scanner.ControlLocked, st.Control.State, "a decoded message implies sync")
	assert.Equal(t, receivers.StateRunning, st.Receivers[2].State)

	h.fix(-112, t0.Add(20*time.Second))
	h.mgr.TickForTest(context.Background(), t0.Add(20*time.Second))

	st = h.mgr.Status()
	assert.True(t, st.Control.Stale)
	assert.Equal(t, receivers.StateStopped, st.Receivers[2].State)
}

func TestManager_tuneErrorRetries(t *testing.T) {
	h := newHarness(t, true, testConfig())
	h.mon.setTuneErr(errors.New("airspy open failed"))
	h.fix(-112, t0)
	h.mgr.TickForTest(context.Background(), t0)

	st := h.mgr.Status()
	assert.Equal(t, scanner.ControlError, st.Control.State)
	assert.Equal(t, "airspy open failed", st.Control.Detail)
	assert.Equal(t, receivers.StateError, st.Receivers[2].State)

	h.mgr.TickForTest(context.Background(), t0.Add(5*time.Second))
	assert.Len(t, h.mon.getPlans(), 1, "no retry inside the back-off")

	h.mon.setTuneErr(nil)
	h.fix(-112, t0.Add(11*time.Second))
	h.mgr.TickForTest(context.Background(), t0.Add(11*time.Second))
	assert.Len(t, h.mon.getPlans(), 2)
	assert.Equal(t, scanner.ControlHunting, h.mgr.Status().Control.State)
}

func TestManager_noMonitor(t *testing.T) {
	h := newHarness(t, false, testConfig())
	h.fix(-112, t0)
	h.mgr.TickForTest(context.Background(), t0)

	st := h.mgr.Status()
	assert.Equal(t, scanner.ControlUnavailable, st.Control.State)
	require.NotNil(t, st.Selection, "site selection works without a decoder")
	assert.Equal(t, "control-channel decoder not wired", st.Receivers[2].Detail)
}

func TestManager_positionStates(t *testing.T) {
	h := newHarness(t, true, testConfig())

	// No fix ever: no site.
	h.mgr.TickForTest(context.Background(), t0)
	st := h.mgr.Status()
	assert.Equal(t, scanner.PositionNone, st.Position.Source)
	assert.True(t, st.Position.Stale)
	assert.Nil(t, st.Selection)
	assert.Equal(t, scanner.ControlIdle, st.Control.State)

	// A fix, then the receiver loses it: last known, site held.
	h.fix(-112, t0)
	h.mgr.TickForTest(context.Background(), t0)
	h.gps.set(gpsd.PositionReport{Mode: 1})
	h.mgr.TickForTest(context.Background(), t0.Add(2*time.Minute))

	st = h.mgr.Status()
	assert.Equal(t, scanner.PositionLastKnown, st.Position.Source)
	assert.True(t, st.Position.Stale)
	require.NotNil(t, st.Selection)
	assert.Equal(t, sites.ReasonLastKnown, st.Selection.Reason)
}

func TestManager_manualMode(t *testing.T) {
	cfg := testConfig()
	cfg.Mode, cfg.ManualSite = config.ScannerModeManual, "CO-east"
	h := newHarness(t, true, cfg)
	h.mgr.TickForTest(context.Background(), t0)

	st := h.mgr.Status()
	require.NotNil(t, st.Selection)
	assert.Equal(t, "CO-east", st.Selection.Site.Key)
	assert.Equal(t, sites.ReasonManual, st.Selection.Reason)
	assert.Equal(t, scanner.PositionManual, st.Position.Source)

	cfg.ManualSite = "XX-missing"
	h.mgr.Apply(withoutEnable(cfg))
	h.mgr.MarkEnabledForTest()
	h.mgr.TickForTest(context.Background(), t0)
	assert.Nil(t, h.mgr.Status().Selection)
	assert.Equal(t, scanner.TunePlan{}, h.mon.getPlans()[1], "unknown manual site stops the decoder")
}

// withoutEnable keeps Apply from starting the goroutine in tick-driven
// tests.
func withoutEnable(cfg config.ScannerConfig) config.ScannerConfig {
	cfg.Enable = false

	return cfg
}

func TestManager_lifecycle(t *testing.T) {
	gps := &fakeGPS{}
	gps.set(gpsd.PositionReport{Valid: true, Mode: 3, Latitude: 40, Longitude: -112, Timestamp: t0})

	mon := newFakeMonitor()

	cfg := testConfig()
	cfg.Enable = false
	mgr := scanner.NewManager(scanner.Options{
		Log: zerolog.Nop(), Position: gps, Monitor: mon, RootFS: fstest.MapFS{}, SysFS: fstest.MapFS{},
		Now: func() time.Time { return t0 }, Sites: testSites(), PollInterval: time.Hour,
	}, cfg)

	assert.False(t, mgr.Enabled())
	assert.False(t, mgr.Status().Enabled)

	_, _, err := mgr.Subscribe()
	require.ErrorIs(t, err, scanner.ErrDisabled)

	ref := mgr.Reference()
	assert.Equal(t, scanner.PositionGPSD, ref.Source, "reference reads gpsd directly while disabled")

	cfg.Enable = true
	mgr.Apply(cfg)
	t.Cleanup(mgr.Disable)
	mgr.Enable() // idempotent

	plan := <-mon.tuned
	assert.Equal(t, "UT-west", plan.SiteKey)
	assert.True(t, mgr.Enabled())

	events, cancel, err := mgr.Subscribe()
	require.NoError(t, err)

	defer cancel()

	// The decoder's events flow through the manager goroutine.
	mon.events <- scanner.SyncState{Locked: true}

	assert.Equal(t, scanner.ControlLocked, (<-events).Control.State)

	cfg.Enable = false
	mgr.Apply(cfg)
	assert.False(t, mgr.Enabled())
	assert.Equal(t, scanner.TunePlan{}, <-mon.tuned, "decoder stopped on disable")

	_, open := <-events
	assert.False(t, open, "streams close when the manager stops")

	mgr.Disable() // idempotent
}

func TestManager_decoderChannelClosed(t *testing.T) {
	mon := newFakeMonitor()
	gps := &fakeGPS{}
	gps.set(gpsd.PositionReport{Valid: true, Mode: 3, Latitude: 40, Longitude: -112, Timestamp: t0})

	mgr := scanner.NewManager(scanner.Options{
		Log: zerolog.Nop(), Position: gps, Monitor: mon, RootFS: fstest.MapFS{}, SysFS: fstest.MapFS{},
		Now: func() time.Time { return t0 }, Sites: testSites(), PollInterval: time.Hour,
	}, testConfig())
	mgr.Enable()
	t.Cleanup(mgr.Disable)

	<-mon.tuned

	events, cancel, err := mgr.Subscribe()
	require.NoError(t, err)
	t.Cleanup(cancel)

	close(mon.events)

	ev := <-events
	assert.Equal(t, scanner.ControlError, ev.Control.State)
	assert.Equal(t, "decoder stopped", ev.Control.Detail)
}

func TestManager_subscriberLimit(t *testing.T) {
	h := newHarness(t, false, testConfig())

	for range 8 {
		_, cancel, err := h.mgr.Subscribe()
		require.NoError(t, err)
		t.Cleanup(cancel)
	}

	_, _, err := h.mgr.Subscribe()
	require.ErrorIs(t, err, scanner.ErrTooManySubscribers)
}

func TestManager_aircraft(t *testing.T) {
	h := newHarness(t, false, testConfig())
	h.mgr.TickForTest(context.Background(), t0)

	list, feeds, now := h.mgr.Aircraft(nil, false)
	assert.Equal(t, t0, now)
	assert.Len(t, list, 3)
	require.Len(t, feeds, 2)
	assert.Equal(t, aircraft.Source1090, feeds[0].Source)
	assert.Equal(t, "var/run/dump1090/aircraft.json", feeds[0].Path)
	assert.True(t, feeds[1].Stale)
}

func TestManager_sitesDefaultTable(t *testing.T) {
	mgr := scanner.NewManager(scanner.Options{Log: zerolog.Nop()}, config.ScannerConfig{})

	all, err := mgr.Sites()
	require.NoError(t, err)
	assert.Len(t, all, 260)
	assert.False(t, mgr.Status().Enabled)
	assert.Equal(t, scanner.PositionNone, mgr.Reference().Source)
}

func TestManager_decoderStatus(t *testing.T) {
	h := newHarness(t, true, testConfig())
	h.fix(-112, t0)
	h.mgr.TickForTest(context.Background(), t0)

	events, cancel, err := h.mgr.Subscribe()
	require.NoError(t, err)
	t.Cleanup(cancel)

	missing := scanner.DecoderStatus{Err: errors.New("airspy_rx: executable file not found in $PATH"), Unavailable: true}
	h.mgr.HandleEventForTest(missing, t0.Add(time.Second))
	h.mgr.TickForTest(context.Background(), t0.Add(time.Second))

	st := h.mgr.Status()
	assert.Equal(t, scanner.ControlUnavailable, st.Control.State)
	assert.Contains(t, st.Control.Detail, "not found")
	assert.False(t, st.Control.Stale, "an unavailable decoder is not a stale one")
	assert.Equal(t, receivers.StateStopped, st.Receivers[2].State)
	assert.Contains(t, st.Receivers[2].Detail, "decoder unavailable")
	assert.Equal(t, scanner.ControlUnavailable, (<-events).Control.State)

	// The same fault again publishes nothing new.
	h.mgr.HandleEventForTest(missing, t0.Add(2*time.Second))

	h.mgr.HandleEventForTest(scanner.DecoderStatus{Err: errors.New("source: capture process exited")}, t0.Add(3*time.Second))
	assert.Equal(t, scanner.ControlError, h.mgr.Status().Control.State)
	assert.Equal(t, scanner.ControlError, (<-events).Control.State)

	// The capture is back, hunting the second control channel.
	h.mgr.HandleEventForTest(scanner.DecoderStatus{FrequencyHz: 770_256_250}, t0.Add(4*time.Second))
	st = h.mgr.Status()
	assert.Equal(t, scanner.ControlHunting, st.Control.State)
	assert.Empty(t, st.Control.Detail)
	assert.Equal(t, uint64(770_256_250), st.Control.FrequencyHz)
	assert.True(t, st.Control.LastDecode.IsZero(), "a status is not a decode")
	assert.Equal(t, scanner.ControlHunting, (<-events).Control.State)

	h.mgr.HandleEventForTest(scanner.SyncState{Locked: true}, t0.Add(5*time.Second))
	assert.Equal(t, scanner.ControlLocked, h.mgr.Status().Control.State)

	// A hunting report while locked keeps the lock.
	h.mgr.HandleEventForTest(scanner.DecoderStatus{FrequencyHz: 770_256_250}, t0.Add(6*time.Second))
	assert.Equal(t, scanner.ControlLocked, h.mgr.Status().Control.State)

	// A decode after a fault means the capture is running and in sync.
	h.mgr.HandleEventForTest(scanner.DecoderStatus{Err: errors.New("exited")}, t0.Add(7*time.Second))
	h.mgr.HandleEventForTest(scanner.AdjacentSite{Site: 2}, t0.Add(8*time.Second))
	st = h.mgr.Status()
	assert.Equal(t, scanner.ControlLocked, st.Control.State)
	assert.Empty(t, st.Control.Detail)
}

func TestManager_scanReceiverFallback(t *testing.T) {
	cfg := testConfig()
	cfg.WideSerial = ""
	cfg.RTLUsableHz = 2 * mhz

	h := newHarness(t, true, cfg)
	h.fix(-112, t0)
	h.mgr.TickForTest(context.Background(), t0)

	plans := h.mon.getPlans()
	require.Len(t, plans, 1)
	assert.Equal(t, scanner.TunerScan, plans[0].Tuner)
	assert.Equal(t, "scan", plans[0].Serial)
	assert.Equal(t, uint64(2*mhz), plans[0].UsableHz)
	assert.Equal(t, []uint64{770_006_250, 770_256_250}, plans[0].ControlHz)

	st := h.mgr.Status()
	require.NotNil(t, st.Selection)
	assert.Equal(t, uint64(2*mhz), st.Selection.Plan.UsableHz)
	require.Len(t, st.Receivers, 5, "the Airspy is listed unassigned")
	assert.Equal(t, receivers.StateStopped, st.Receivers[3].State)
	assert.Equal(t, "no decoder output yet", st.Receivers[3].Detail, "the scan receiver carries the decoder")

	h.mgr.HandleEventForTest(scanner.SiteStatus{NAC: 1}, t0.Add(time.Second))
	h.mgr.TickForTest(context.Background(), t0.Add(2*time.Second))
	assert.Equal(t, receivers.StateRunning, h.mgr.Status().Receivers[3].State)

	// Configuring the wide serial moves the decoder to the Airspy: same
	// site, new receiver, so the manager retunes with an 8 MHz plan.
	cfg.WideSerial = "44a061dc2e395e8f"
	h.mgr.Apply(cfg)
	h.mgr.Disable() // Apply started the goroutine; drive ticks by hand
	h.mgr.MarkEnabledForTest()
	h.fix(-112, t0.Add(3*time.Second))
	h.mgr.TickForTest(context.Background(), t0.Add(3*time.Second))

	plans = h.mon.getPlans()
	require.GreaterOrEqual(t, len(plans), 2)
	last := plans[len(plans)-1]
	assert.Equal(t, scanner.TunerWide, last.Tuner)
	assert.Equal(t, uint64(8*mhz), last.UsableHz)
}

func TestHuntOrder_inWindowFirst(t *testing.T) {
	site := &sites.Site{
		Key: "T", ControlHz: []uint64{860_000_000, 851_012_500, 851_512_500},
		VoiceHz: []uint64{851_200_000, 851_400_000},
	}
	sel := &scanner.Selection{Site: site, Plan: sites.PlanTuner(site, 2*mhz)}

	assert.Equal(t, []uint64{851_012_500, 851_512_500, 860_000_000}, scanner.HuntOrderForTest(sel))
}

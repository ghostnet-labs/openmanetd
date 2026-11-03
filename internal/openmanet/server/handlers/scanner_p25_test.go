package handlers_test

// scanner_p25_test.go — end-to-end control-channel path: a synthesized P25
// capture replayed through the real p25mon monitor (internal/sdr/source +
// p25 decoder) into a real scanner.Manager, read back through the
// ScannerService handler.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"
	"time"

	scannerv1 "github.com/openmanet/openmanetd/internal/api/openmanet/scanner/v1"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/openmanet/openmanetd/internal/scanner/p25mon"
	"github.com/openmanet/openmanetd/internal/scanner/p25mon/p25montest"
	"github.com/openmanet/openmanetd/internal/scanner/sites"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// p25FixtureSite puts the c4fm_1024k_site.cu8 control channel 100 kHz
// above the 2 MHz plan center (851.0 MHz), where the fixture carries it.
func p25FixtureSite() sites.Site {
	return sites.Site{
		Key: "UT-fixture", State: "UT", System: "Synthesized", Name: "Fixture Peak", Lat: 40.76, Lon: -111.89,
		ControlHz: []uint64{851_100_000}, VoiceHz: []uint64{850_900_000},
	}
}

func p25Fixture(t *testing.T, name string) []byte {
	t.Helper()

	_, here, _, ok := runtime.Caller(0)
	require.True(t, ok)

	root := filepath.Dir(here)
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}

		parent := filepath.Dir(root)
		require.NotEqual(t, root, parent, "module root not found")
		root = parent
	}

	data, err := os.ReadFile(filepath.Join(root, "testfixtures", "sdr", "p25", name))
	require.NoError(t, err)

	return data
}

func TestScannerService_p25FixtureEndToEnd(t *testing.T) {
	runner := &p25montest.Runner{Data: p25Fixture(t, "c4fm_1024k_site.cu8"), Passes: 1_000}
	mon := p25mon.New(p25mon.Options{
		Runner: runner, Log: zerolog.Nop(), RTLRate: 1_024_000,
		MinBackoff: time.Millisecond, MaxBackoff: 10 * time.Millisecond,
	})

	monCtx, monCancel := context.WithCancel(context.Background())
	monDone := make(chan struct{})

	go func() {
		defer close(monDone)

		mon.Run(monCtx)
	}()

	site := p25FixtureSite()
	mgr := scanner.NewManager(scanner.Options{
		Log: zerolog.Nop(), Monitor: mon, RootFS: fstest.MapFS{}, SysFS: fstest.MapFS{},
		Sites: []sites.Site{site}, PollInterval: time.Hour,
	}, config.ScannerConfig{})

	t.Cleanup(func() {
		mgr.Disable()
		monCancel()
		<-monDone
	})

	events, unsubscribe, err := subscribeAfterEnable(t, mgr, config.ScannerConfig{
		Enable: true, Mode: config.ScannerModeManual, ManualSite: site.Key,
		ScanSerial: "scan", RTLUsableHz: 2_000_000, TalkgroupHistory: 16,
		DecodeStaleAfter: time.Minute,
	})
	require.NoError(t, err)
	t.Cleanup(unsubscribe)

	timeout := time.After(10 * time.Second)

	for grant := false; !grant; {
		select {
		case ev, ok := <-events:
			require.True(t, ok, "manager stopped")

			grant = ev.Kind == scanner.EventGrant
		case <-timeout:
			require.FailNow(t, "no talkgroup grant decoded from the fixture")
		}
	}

	svc := &handlers.ScannerService{Log: zerolog.Nop(), Manager: mgr}

	act, err := svc.ListTalkgroupActivity(context.Background(), &scannerv1.ListTalkgroupActivityRequest{})
	require.NoError(t, err)
	assert.True(t, act.GetEnabled())
	require.NotEmpty(t, act.GetGrants())

	g := act.GetGrants()[0]
	assert.Equal(t, uint32(8001), g.GetTalkgroup())
	assert.Equal(t, uint32(1234567), g.GetSourceUnit())
	assert.Equal(t, uint64(851_006_250+0x2AB*6250), g.GetFrequencyHz())
	assert.False(t, g.GetEncrypted())
	assert.False(t, g.GetEmergency())

	st, err := svc.GetScannerStatus(context.Background(), &scannerv1.GetScannerStatusRequest{})
	require.NoError(t, err)
	assert.False(t, st.GetDecoderUnavailable())
	assert.Equal(t, scannerv1.ControlChannelState_CONTROL_CHANNEL_STATE_LOCKED, st.GetControlChannel().GetState())
	// The fixture's RFSS status announces channel 0x1123 as the control
	// channel, and the decoded value replaces the planned 851.1 MHz.
	assert.Equal(t, uint64(851_006_250+0x123*6250), st.GetSelectedSite().GetControlChannelHz())
	assert.Equal(t, uint32(0xBEE00), st.GetControlChannel().GetWacn())

	starts := runner.Starts()
	require.NotEmpty(t, starts)
	assert.Equal(t, "rtl_sdr", starts[0].Name, "no wide serial: the scan RTL-SDR decodes the control channel")
	assert.Equal(t, []string{"-d", "scan", "-f", "851000000", "-s", "1024000", "-"}, starts[0].Args)
}

func TestScannerService_p25CaptureToolMissing(t *testing.T) {
	mon := p25mon.New(p25mon.Options{
		Log:        zerolog.Nop(),
		LookPath:   func(name string) (string, error) { return "", &exec.Error{Name: name, Err: exec.ErrNotFound} },
		MinBackoff: time.Hour,
	})

	monCtx, monCancel := context.WithCancel(context.Background())
	monDone := make(chan struct{})

	go func() {
		defer close(monDone)

		mon.Run(monCtx)
	}()

	site := p25FixtureSite()
	mgr := scanner.NewManager(scanner.Options{
		Log: zerolog.Nop(), Monitor: mon, RootFS: fstest.MapFS{}, SysFS: fstest.MapFS{},
		Sites: []sites.Site{site}, PollInterval: time.Hour,
	}, config.ScannerConfig{})

	t.Cleanup(func() {
		mgr.Disable()
		monCancel()
		<-monDone
	})

	mgr.Apply(config.ScannerConfig{
		Enable: true, Mode: config.ScannerModeManual, ManualSite: site.Key, ScanSerial: "scan",
	})

	// One attempt (hour-long backoff) whose result may land before any
	// subscription, so poll the status instead of waiting for an event.
	require.Eventually(t, func() bool {
		return mgr.Status().Control.State == scanner.ControlUnavailable
	}, 10*time.Second, 5*time.Millisecond)

	svc := &handlers.ScannerService{Log: zerolog.Nop(), Manager: mgr}
	st, err := svc.GetScannerStatus(context.Background(), &scannerv1.GetScannerStatusRequest{})
	require.NoError(t, err)
	assert.True(t, st.GetDecoderUnavailable())
	assert.Contains(t, st.GetControlChannel().GetDetail(), "rtl_sdr")
}

// subscribeAfterEnable applies cfg (starting the manager) and subscribes.
// The manager's first tick races the subscription, so the site and the
// first control events may be missed; the tests only wait for later ones.
func subscribeAfterEnable(t *testing.T, mgr *scanner.Manager, cfg config.ScannerConfig) (<-chan scanner.Event, func(), error) {
	t.Helper()

	mgr.Apply(cfg)

	return mgr.Subscribe() //nolint:wrapcheck // test helper returns the manager's error as is
}

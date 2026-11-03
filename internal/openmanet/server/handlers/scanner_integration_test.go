//go:build integration

package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	scannerv1 "github.com/openmanet/openmanetd/internal/api/openmanet/scanner/v1"
	"github.com/openmanet/openmanetd/internal/api/openmanet/scanner/v1/scannerv1connect"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/gpsd"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newScannerIntegration wires a real config file, a real scanner.Manager
// (disabled, as on a stock node) with fixture aircraft.json and sysfs, and
// ScannerService behind the validate interceptor.
func newScannerIntegration(t *testing.T) (scannerv1connect.ScannerServiceClient, *fakeScannerMonitor, *scanner.Manager) {
	t.Helper()

	cfgPath := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("scanner:\n  receivers:\n    wideSerial: 44a061dc2e395e8f\n"), 0o600))

	v := viper.New()
	v.SetConfigFile(cfgPath)
	require.NoError(t, v.ReadInConfig())

	cfg := config.NewWithoutWatch(v)
	require.False(t, cfg.GetScanner().Enable, "scanner is off by default")

	dump1090, err := os.ReadFile(filepath.Join(filepath.Dir(fixtureRoot(t)), "scanner", "dump1090-aircraft.json"))
	require.NoError(t, err)
	skyaware, err := os.ReadFile(filepath.Join(filepath.Dir(fixtureRoot(t)), "scanner", "skyaware978-aircraft.json"))
	require.NoError(t, err)

	sysfs := fstest.MapFS{
		"1-1.1/idVendor": {Data: []byte("0bda")}, "1-1.1/idProduct": {Data: []byte("2838")}, "1-1.1/serial": {Data: []byte("1090")},
		"1-1.3/idVendor": {Data: []byte("1d50")}, "1-1.3/idProduct": {Data: []byte("60a1")},
		"1-1.3/serial": {Data: []byte("AIRSPY SN:44A061DC2E395E8F")},
	}

	now := time.Unix(1762150000, 0)
	mon := &fakeScannerMonitor{events: make(chan any, 8)}
	gps := &fakeScannerGPS{pos: gpsd.PositionReport{Valid: true, Mode: 3, Latitude: 40.7608, Longitude: -111.8910, Timestamp: now}}

	mgr := scanner.NewManager(scanner.Options{
		Log:      zerolog.Nop(),
		Position: gps,
		Monitor:  mon,
		UCI: fakeScannerUCI{vals: map[string]string{
			"dump1090.main.device_index": "1090", "dump978-fa.main.serial": "978", "dump978-fa.main.enabled": "1",
		}},
		RootFS: fstest.MapFS{
			"var/run/dump1090/aircraft.json":    {Data: dump1090},
			"var/run/skyaware978/aircraft.json": {Data: skyaware},
		},
		SysFS:        sysfs,
		Now:          func() time.Time { return now },
		PollInterval: 10 * time.Millisecond,
	}, cfg.GetScanner())
	t.Cleanup(mgr.Disable)

	mux := http.NewServeMux()
	mux.Handle(scannerv1connect.NewScannerServiceHandler(&handlers.ScannerService{
		Log: zerolog.Nop(), Cfg: cfg, Manager: mgr,
	}, connect.WithInterceptors(validate.NewInterceptor())))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return scannerv1connect.NewScannerServiceClient(http.DefaultClient, srv.URL, connect.WithGRPCWeb()), mon, mgr
}

func TestIntegration_Scanner_lifecycle(t *testing.T) {
	client, mon, mgr := newScannerIntegration(t)
	ctx := context.Background()

	st, err := client.GetScannerStatus(ctx, &scannerv1.GetScannerStatusRequest{})
	require.NoError(t, err)
	assert.False(t, st.GetEnabled())

	// Sites work while disabled (manual-site picker).
	sitesRes, err := client.ListSites(ctx, &scannerv1.ListSitesRequest{States: []string{"UT"}, Limit: 3})
	require.NoError(t, err)
	require.Len(t, sitesRes.GetSites(), 3)
	assert.Equal(t, scannerv1.PositionSource_POSITION_SOURCE_GPSD, sitesRes.GetReference().GetSource())
	assert.Less(t, sitesRes.GetSites()[0].GetDistanceKm(), 30.0)

	set, err := client.SetScannerConfig(ctx, &scannerv1.SetScannerConfigRequest{Config: &scannerv1.ScannerConfig{
		Enabled: true, Mode: scannerv1.SiteSelectionMode_SITE_SELECTION_MODE_AUTO, HysteresisKm: 5,
	}})
	require.NoError(t, err)
	assert.True(t, set.GetConfig().GetEnabled())
	assert.True(t, mgr.Enabled())

	require.Eventually(t, func() bool {
		s, gerr := client.GetScannerStatus(ctx, &scannerv1.GetScannerStatusRequest{})

		return gerr == nil && s.GetSelectedSite() != nil && len(s.GetReceivers()) >= 4
	}, 5*time.Second, 10*time.Millisecond)

	st, err = client.GetScannerStatus(ctx, &scannerv1.GetScannerStatusRequest{})
	require.NoError(t, err)
	assert.True(t, st.GetEnabled())
	assert.Equal(t, "UT", st.GetSelectedSite().GetSite().GetState())
	assert.Equal(t, scannerv1.SiteSelectionReason_SITE_SELECTION_REASON_NEAREST, st.GetSelectedSite().GetReason())
	assert.NotZero(t, st.GetSelectedSite().GetWideTuner().GetCenterHz())
	assert.Equal(t, scannerv1.ControlChannelState_CONTROL_CHANNEL_STATE_HUNTING, st.GetControlChannel().GetState())
	assert.Equal(t, scannerv1.ReceiverState_RECEIVER_STATE_RUNNING, st.GetReceivers()[0].GetState(), "1090 present and fresh")
	assert.Equal(t, scannerv1.ReceiverState_RECEIVER_STATE_MISSING, st.GetReceivers()[1].GetState(), "no 978 receiver plugged in")
	assert.Equal(t, scannerv1.ReceiverHardware_RECEIVER_HARDWARE_AIRSPY, st.GetReceivers()[2].GetHardware())
	assert.False(t, st.GetDecoderUnavailable())
	assert.False(t, st.GetAircraftStale())
	require.NotEmpty(t, mon.getPlans())
	assert.Equal(t, st.GetSelectedSite().GetSite().GetKey(), mon.getPlans()[0].SiteKey)

	ac, err := client.ListAircraft(ctx, &scannerv1.ListAircraftRequest{})
	require.NoError(t, err)
	require.Len(t, ac.GetAircraft(), 5)
	assert.Equal(t, scannerv1.AircraftSource_AIRCRAFT_SOURCE_BOTH, ac.GetAircraft()[0].GetSource(), "4b1805 heard on 1090 and 978")

	streamCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	stream, err := client.StreamScannerEvents(streamCtx, &scannerv1.StreamScannerEventsRequest{})
	require.NoError(t, err)

	// The subscription is registered when the handler starts; keep feeding
	// grants until one arrives so the test does not race the handshake.
	got := make(chan *scannerv1.VoiceGrant, 1)

	go func() {
		for stream.Receive() {
			if g := stream.Msg().GetVoiceGrant(); g != nil {
				got <- g

				return
			}
		}
	}()

	var grant *scannerv1.VoiceGrant

	require.Eventually(t, func() bool {
		select {
		case grant = <-got:
			return true
		default:
			mon.events <- scanner.VoiceGrant{Talkgroup: 4242, FrequencyHz: 770_500_000, Emergency: true}

			return false
		}
	}, 5*time.Second, 20*time.Millisecond)

	assert.Equal(t, uint32(4242), grant.GetTalkgroup())
	assert.True(t, grant.GetEmergency())
	cancel()

	activity, err := client.ListTalkgroupActivity(ctx, &scannerv1.ListTalkgroupActivityRequest{Limit: 10})
	require.NoError(t, err)
	require.NotEmpty(t, activity.GetGrants())
	assert.Equal(t, uint32(4242), activity.GetGrants()[0].GetTalkgroup())

	// Disabling stops the manager and is persisted.
	_, err = client.SetScannerConfig(ctx, &scannerv1.SetScannerConfigRequest{Config: &scannerv1.ScannerConfig{
		Mode: scannerv1.SiteSelectionMode_SITE_SELECTION_MODE_AUTO,
	}})
	require.NoError(t, err)
	assert.False(t, mgr.Enabled())

	cfgRes, err := client.GetScannerConfig(ctx, &scannerv1.GetScannerConfigRequest{})
	require.NoError(t, err)
	assert.False(t, cfgRes.GetConfig().GetEnabled())

	st, err = client.GetScannerStatus(ctx, &scannerv1.GetScannerStatusRequest{})
	require.NoError(t, err)
	assert.False(t, st.GetEnabled())
}

func TestIntegration_Scanner_validation(t *testing.T) {
	client, _, _ := newScannerIntegration(t)

	_, err := client.SetScannerConfig(context.Background(), &scannerv1.SetScannerConfigRequest{Config: &scannerv1.ScannerConfig{
		Mode: scannerv1.SiteSelectionMode_SITE_SELECTION_MODE_MANUAL,
	}})

	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
	assert.Contains(t, connectErr.Message(), "manual_site_key is required")

	_, err = client.SetScannerConfig(context.Background(), &scannerv1.SetScannerConfigRequest{Config: &scannerv1.ScannerConfig{
		Mode: scannerv1.SiteSelectionMode_SITE_SELECTION_MODE_MANUAL, ManualSiteKey: "UT-00000000",
	}})
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
	assert.Contains(t, connectErr.Message(), "not in the site table")

	_, err = client.ListSites(context.Background(), &scannerv1.ListSitesRequest{States: []string{"TX"}})
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
}

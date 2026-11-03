package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	scannerv1 "github.com/openmanet/openmanetd/internal/api/openmanet/scanner/v1"
	"github.com/openmanet/openmanetd/internal/api/openmanet/scanner/v1/scannerv1connect"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/openmanet/openmanetd/internal/scanner/aircraft"
	"github.com/openmanet/openmanetd/internal/scanner/receivers"
	"github.com/openmanet/openmanetd/internal/scanner/sites"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

// scannerT0 is the fixed clock used by the scanner handler tests.
var scannerT0 = time.Unix(1762150000, 0) //nolint:gochecknoglobals // test constant

func scannerTestSite() *sites.Site {
	return &sites.Site{
		Key: "UT-west", State: "UT", System: "UCA", Name: "West Peak", County: "Salt Lake", Lat: 40, Lon: -112,
		ControlHz: []uint64{770_006_250}, VoiceHz: []uint64{770_500_000}, LowHz: 770_006_250, HighHz: 770_500_000,
		RTLNeeded: 1, WideNeeded: 1, Band: sites.Band700, Phase: sites.Phase2, Encryption: sites.EncryptionPartial,
		PhaseDetail: "P25 Phase II",
	}
}

func enabledScannerStatus() scanner.Status {
	site := scannerTestSite()

	return scanner.Status{
		GeneratedAt: scannerT0,
		Enabled:     true,
		Receivers: []receivers.Receiver{
			{Role: receivers.RoleADSB1090, Serial: "1090", Owner: receivers.OwnerDump1090, Present: true,
				Hardware: receivers.HardwareRTLSDR, State: receivers.StateRunning, LastSample: scannerT0},
			{Role: receivers.RoleWide, Owner: receivers.OwnerOpenMANETd, State: receivers.StateMissing, Detail: "no serial configured"},
		},
		Position: scanner.Position{Source: scanner.PositionGPSD, Lat: 40.01, Lon: -112, FixTime: scannerT0},
		Selection: &scanner.Selection{
			Site: site, Mode: config.ScannerModeAuto, Reason: sites.ReasonHysteresis, DistanceKm: 1.1, SelectedAt: scannerT0,
			Plan: sites.TunerPlan{CenterHz: 770_253_125, UsableHz: 8_000_000, Fits: true,
				Covered: []uint64{770_006_250, 770_500_000}, Control: 770_006_250},
		},
		Control: scanner.ControlStatus{State: scanner.ControlLocked, FrequencyHz: 770_006_250, LastDecode: scannerT0,
			WACN: 0xBEE00, SystemID: 0x3A1, RFSS: 1, SiteID: 2, NAC: 0x3A1},
		AircraftStale: true,
	}
}

func TestScannerService_GetScannerStatus(t *testing.T) {
	t.Run("nil manager", func(t *testing.T) {
		svc := &handlers.ScannerService{Log: zerolog.Nop()}
		res, err := svc.GetScannerStatus(context.Background(), &scannerv1.GetScannerStatusRequest{})
		require.NoError(t, err)
		assert.False(t, res.GetEnabled())
		assert.NotNil(t, res.GetGeneratedAt())
	})

	t.Run("disabled", func(t *testing.T) {
		svc := &handlers.ScannerService{Log: zerolog.Nop(), Manager: &fakeScannerManager{status: scanner.Status{GeneratedAt: scannerT0}}}
		res, err := svc.GetScannerStatus(context.Background(), &scannerv1.GetScannerStatusRequest{})
		require.NoError(t, err)
		assert.False(t, res.GetEnabled())
		assert.Empty(t, res.GetReceivers())
		assert.Nil(t, res.GetSelectedSite())
	})

	t.Run("enabled", func(t *testing.T) {
		svc := &handlers.ScannerService{Log: zerolog.Nop(), Manager: &fakeScannerManager{enabled: true, status: enabledScannerStatus()}}
		res, err := svc.GetScannerStatus(context.Background(), &scannerv1.GetScannerStatusRequest{})
		require.NoError(t, err)

		assert.True(t, res.GetEnabled())
		require.Len(t, res.GetReceivers(), 2)
		assert.Equal(t, scannerv1.ReceiverRole_RECEIVER_ROLE_ADSB_1090, res.GetReceivers()[0].GetRole())
		assert.Equal(t, scannerv1.ReceiverState_RECEIVER_STATE_RUNNING, res.GetReceivers()[0].GetState())
		assert.Equal(t, scannerv1.ReceiverHardware_RECEIVER_HARDWARE_RTL_SDR, res.GetReceivers()[0].GetHardware())
		assert.Equal(t, "dump1090", res.GetReceivers()[0].GetOwner())
		assert.Equal(t, scannerT0.Unix(), res.GetReceivers()[0].GetLastSampleTime().GetSeconds())
		assert.Nil(t, res.GetReceivers()[1].GetLastSampleTime())
		assert.Equal(t, "no serial configured", res.GetReceivers()[1].GetDetail())

		assert.Equal(t, scannerv1.PositionSource_POSITION_SOURCE_GPSD, res.GetPosition().GetSource())
		assert.InDelta(t, 40.01, res.GetPosition().GetLatitude(), 1e-9)

		sel := res.GetSelectedSite()
		require.NotNil(t, sel)
		assert.Equal(t, "UT-west", sel.GetSite().GetKey())
		assert.InDelta(t, 1.1, sel.GetSite().GetDistanceKm(), 1e-9)
		assert.Equal(t, scannerv1.SiteSelectionReason_SITE_SELECTION_REASON_HYSTERESIS, sel.GetReason())
		assert.Equal(t, scannerv1.SiteSelectionMode_SITE_SELECTION_MODE_AUTO, sel.GetMode())
		assert.Equal(t, uint64(770_006_250), sel.GetControlChannelHz())
		assert.Equal(t, uint64(770_253_125), sel.GetWideTuner().GetCenterHz())
		assert.True(t, sel.GetWideTuner().GetFits())
		assert.Equal(t, scannerv1.SiteBand_SITE_BAND_700, sel.GetSite().GetBand())
		assert.Equal(t, scannerv1.P25Phase_P25_PHASE_2, sel.GetSite().GetPhase())
		assert.Equal(t, scannerv1.SiteEncryption_SITE_ENCRYPTION_PARTIAL, sel.GetSite().GetEncryption())
		assert.True(t, sel.GetSite().GetRtlSdrFits())
		assert.Equal(t, uint64(493_750), sel.GetSite().GetSpanHz())

		cc := res.GetControlChannel()
		assert.Equal(t, scannerv1.ControlChannelState_CONTROL_CHANNEL_STATE_LOCKED, cc.GetState())
		assert.Equal(t, uint32(0xBEE00), cc.GetWacn())
		assert.Equal(t, uint32(0x3A1), cc.GetNac())
		assert.Equal(t, scannerT0.Unix(), res.GetLastDecodeTime().GetSeconds())

		assert.False(t, res.GetPositionStale())
		assert.False(t, res.GetSiteUnavailable())
		assert.False(t, res.GetDecoderUnavailable())
		assert.False(t, res.GetDecodeStale())
		assert.True(t, res.GetAircraftStale())
	})

	t.Run("unavailable flags", func(t *testing.T) {
		st := scanner.Status{Enabled: true, Position: scanner.Position{Stale: true},
			Control: scanner.ControlStatus{State: scanner.ControlUnavailable}}
		svc := &handlers.ScannerService{Log: zerolog.Nop(), Manager: &fakeScannerManager{enabled: true, status: st}}
		res, err := svc.GetScannerStatus(context.Background(), &scannerv1.GetScannerStatusRequest{})
		require.NoError(t, err)
		assert.True(t, res.GetPositionStale())
		assert.True(t, res.GetSiteUnavailable())
		assert.True(t, res.GetDecoderUnavailable())
		assert.Equal(t, scannerv1.PositionSource_POSITION_SOURCE_NONE, res.GetPosition().GetSource())
		assert.Nil(t, res.GetLastDecodeTime())
	})
}

// TestScannerEnumNumbering pins the handler's direct enum casts: each Go
// enum value must carry the number of the matching proto value.
func TestScannerEnumNumbering(t *testing.T) {
	assert.Equal(t, scannerv1.ReceiverRole_RECEIVER_ROLE_ADSB_1090, scannerv1.ReceiverRole(receivers.RoleADSB1090))
	assert.Equal(t, scannerv1.ReceiverRole_RECEIVER_ROLE_UAT_978, scannerv1.ReceiverRole(receivers.RoleUAT978))
	assert.Equal(t, scannerv1.ReceiverRole_RECEIVER_ROLE_WIDE, scannerv1.ReceiverRole(receivers.RoleWide))
	assert.Equal(t, scannerv1.ReceiverRole_RECEIVER_ROLE_SCAN, scannerv1.ReceiverRole(receivers.RoleScan))
	assert.Equal(t, scannerv1.ReceiverRole_RECEIVER_ROLE_UNASSIGNED, scannerv1.ReceiverRole(receivers.RoleUnassigned))
	assert.Equal(t, scannerv1.ReceiverState_RECEIVER_STATE_RUNNING, scannerv1.ReceiverState(receivers.StateRunning))
	assert.Equal(t, scannerv1.ReceiverState_RECEIVER_STATE_STOPPED, scannerv1.ReceiverState(receivers.StateStopped))
	assert.Equal(t, scannerv1.ReceiverState_RECEIVER_STATE_MISSING, scannerv1.ReceiverState(receivers.StateMissing))
	assert.Equal(t, scannerv1.ReceiverState_RECEIVER_STATE_ERROR, scannerv1.ReceiverState(receivers.StateError))
	assert.Equal(t, scannerv1.ReceiverHardware_RECEIVER_HARDWARE_RTL_SDR, scannerv1.ReceiverHardware(receivers.HardwareRTLSDR))
	assert.Equal(t, scannerv1.ReceiverHardware_RECEIVER_HARDWARE_AIRSPY, scannerv1.ReceiverHardware(receivers.HardwareAirspy))
	assert.Equal(t, scannerv1.SiteBand_SITE_BAND_VHF, scannerv1.SiteBand(sites.BandVHF))
	assert.Equal(t, scannerv1.SiteBand_SITE_BAND_800, scannerv1.SiteBand(sites.Band800))
	assert.Equal(t, scannerv1.SiteBand_SITE_BAND_MIXED, scannerv1.SiteBand(sites.BandMixed))
	assert.Equal(t, scannerv1.P25Phase_P25_PHASE_1, scannerv1.P25Phase(sites.Phase1))
	assert.Equal(t, scannerv1.SiteEncryption_SITE_ENCRYPTION_NONE, scannerv1.SiteEncryption(sites.EncryptionNone))
	assert.Equal(t, scannerv1.SiteEncryption_SITE_ENCRYPTION_FULL, scannerv1.SiteEncryption(sites.EncryptionFull))
	assert.Equal(t, scannerv1.SiteSelectionReason_SITE_SELECTION_REASON_NEAREST, scannerv1.SiteSelectionReason(sites.ReasonNearest))
	assert.Equal(t, scannerv1.SiteSelectionReason_SITE_SELECTION_REASON_LAST_KNOWN, scannerv1.SiteSelectionReason(sites.ReasonLastKnown))
	assert.Equal(t, scannerv1.SiteSelectionReason_SITE_SELECTION_REASON_MANUAL, scannerv1.SiteSelectionReason(sites.ReasonManual))
	assert.Equal(t, scannerv1.SiteSelectionReason_SITE_SELECTION_REASON_STALE_FIX, scannerv1.SiteSelectionReason(sites.ReasonStaleFix))
	assert.Equal(t, scannerv1.ControlChannelState_CONTROL_CHANNEL_STATE_UNAVAILABLE, scannerv1.ControlChannelState(scanner.ControlUnavailable))
	assert.Equal(t, scannerv1.ControlChannelState_CONTROL_CHANNEL_STATE_IDLE, scannerv1.ControlChannelState(scanner.ControlIdle))
	assert.Equal(t, scannerv1.ControlChannelState_CONTROL_CHANNEL_STATE_HUNTING, scannerv1.ControlChannelState(scanner.ControlHunting))
	assert.Equal(t, scannerv1.ControlChannelState_CONTROL_CHANNEL_STATE_ERROR, scannerv1.ControlChannelState(scanner.ControlError))
	assert.Equal(t, scannerv1.AircraftSource_AIRCRAFT_SOURCE_ADSB_1090, scannerv1.AircraftSource(aircraft.Source1090))
	assert.Equal(t, scannerv1.AircraftSource_AIRCRAFT_SOURCE_UAT_978, scannerv1.AircraftSource(aircraft.Source978))
	assert.Equal(t, scannerv1.AircraftSource_AIRCRAFT_SOURCE_BOTH, scannerv1.AircraftSource(aircraft.SourceBoth))
}

func TestScannerService_ListAircraft(t *testing.T) {
	mgr := &fakeScannerManager{
		enabled: true,
		now:     scannerT0,
		aircraft: []aircraft.Aircraft{
			{ICAO: "a1b2c3", Callsign: "UAL123", Squawk: "1200", Lat: 40.7, Lon: -111.9, HasPos: true, AltFt: 35000, HasAlt: true,
				GS: 450, HasGS: true, Track: 270, HasTrack: true, VRate: -64, HasVRate: true, RSSI: -12.3, HasRSSI: true,
				Sources: aircraft.SourceBoth, LastSeen: scannerT0.Add(-1500 * time.Millisecond)},
			{ICAO: "abc123", OnGround: true, AltFt: 4200, HasAlt: true, Sources: aircraft.Source978, LastSeen: scannerT0, Stale: true},
		},
		feeds: []aircraft.FeedStatus{
			{Source: aircraft.Source1090, Path: "var/run/dump1090/aircraft.json", Updated: scannerT0, Aircraft: 1},
			{Source: aircraft.Source978, Path: "var/run/skyaware978/aircraft.json", Err: errors.New("no file"), Stale: true},
		},
	}
	svc := &handlers.ScannerService{Log: zerolog.Nop(), Manager: mgr}

	res, err := svc.ListAircraft(context.Background(), &scannerv1.ListAircraftRequest{IncludeStale: true})
	require.NoError(t, err)
	assert.True(t, res.GetEnabled())
	require.Len(t, res.GetAircraft(), 2)

	a := res.GetAircraft()[0]
	assert.Equal(t, "a1b2c3", a.GetIcao())
	assert.Equal(t, "UAL123", a.GetCallsign())
	require.NotNil(t, a.Latitude)
	assert.InDelta(t, 40.7, a.GetLatitude(), 1e-9)
	assert.Equal(t, int32(35000), a.GetAltitudeFt())
	assert.Equal(t, int32(-64), a.GetVerticalRateFpm())
	assert.InDelta(t, -12.3, a.GetRssiDbfs(), 1e-9)
	assert.InDelta(t, 1.5, a.GetSeenSeconds(), 1e-9)
	assert.Equal(t, scannerv1.AircraftSource_AIRCRAFT_SOURCE_BOTH, a.GetSource())

	g := res.GetAircraft()[1]
	assert.Nil(t, g.Latitude)
	assert.Nil(t, g.AltitudeFt, "altitude is unset on the ground")
	assert.Nil(t, g.GroundSpeedKt)
	assert.Nil(t, g.RssiDbfs)
	assert.True(t, g.GetOnGround())
	assert.True(t, g.GetStale())

	require.Len(t, res.GetFeeds(), 2)
	assert.Equal(t, "/var/run/dump1090/aircraft.json", res.GetFeeds()[0].GetPath())
	assert.Equal(t, uint32(1), res.GetFeeds()[0].GetAircraft())
	assert.Equal(t, "no file", res.GetFeeds()[1].GetError())
	assert.True(t, res.GetFeeds()[1].GetStale())

	mgr.enabled = false
	res, err = svc.ListAircraft(context.Background(), &scannerv1.ListAircraftRequest{})
	require.NoError(t, err)
	assert.False(t, res.GetEnabled())
	assert.Empty(t, res.GetAircraft())
}

func TestScannerService_ListSites(t *testing.T) {
	lat, lon := 40.7608, -111.8910

	tests := []struct {
		name      string
		mgr       handlers.ScannerManager
		req       *scannerv1.ListSitesRequest
		wantLen   int
		wantTotal uint32
		wantRef   scannerv1.PositionSource
		check     func(t *testing.T, res *scannerv1.ListSitesResponse)
	}{
		{
			name: "request point, nearest first", req: &scannerv1.ListSitesRequest{Latitude: &lat, Longitude: &lon, Limit: 5},
			wantLen: 5, wantTotal: 260, wantRef: scannerv1.PositionSource_POSITION_SOURCE_MANUAL,
			check: func(t *testing.T, res *scannerv1.ListSitesResponse) {
				t.Helper()
				assert.Equal(t, "UT", res.GetSites()[0].GetState())
				assert.Less(t, res.GetSites()[0].GetDistanceKm(), 30.0)
				assert.LessOrEqual(t, res.GetSites()[0].GetDistanceKm(), res.GetSites()[4].GetDistanceKm())
				assert.NotEmpty(t, res.GetSites()[0].GetControlChannelHz())
			},
		},
		{
			name: "no reference: table order, default limit", req: &scannerv1.ListSitesRequest{States: []string{"WY"}},
			wantLen: 25, wantTotal: 28, wantRef: scannerv1.PositionSource_POSITION_SOURCE_NONE,
			check: func(t *testing.T, res *scannerv1.ListSitesResponse) {
				t.Helper()

				for _, s := range res.GetSites() {
					assert.Equal(t, "WY", s.GetState())
					assert.Zero(t, s.GetDistanceKm())
				}
			},
		},
		{
			name: "manager reference", mgr: &fakeScannerManager{reference: scanner.Position{Source: scanner.PositionGPSD, Lat: 35.08, Lon: -106.65}},
			req: &scannerv1.ListSitesRequest{Limit: 1}, wantLen: 1, wantTotal: 260, wantRef: scannerv1.PositionSource_POSITION_SOURCE_GPSD,
			check: func(t *testing.T, res *scannerv1.ListSitesResponse) {
				t.Helper()
				assert.Equal(t, "NM", res.GetSites()[0].GetState(), "nearest to Albuquerque")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &handlers.ScannerService{Log: zerolog.Nop(), Manager: tc.mgr}
			res, err := svc.ListSites(context.Background(), tc.req)
			require.NoError(t, err)
			assert.Len(t, res.GetSites(), tc.wantLen)
			assert.Equal(t, tc.wantTotal, res.GetTotal())
			assert.Equal(t, tc.wantRef, res.GetReference().GetSource())
			tc.check(t, res)
		})
	}

	svc := &handlers.ScannerService{Log: zerolog.Nop(), Manager: &fakeScannerManager{sitesErr: errors.New("corrupt")}}
	_, err := svc.ListSites(context.Background(), &scannerv1.ListSitesRequest{})

	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeInternal, connectErr.Code())
}

func TestScannerService_ListTalkgroupActivity(t *testing.T) {
	mgr := &fakeScannerManager{enabled: true, grants: []scanner.Grant{
		{Talkgroup: 202, Encrypted: true, Start: scannerT0, Last: scannerT0, Grants: 1, SiteKey: "UT-west"},
		{Talkgroup: 101, Source: 1234567, FrequencyHz: 770_500_000, Channel: 0x1123, Emergency: true, Start: scannerT0, Last: scannerT0, Grants: 3},
	}}
	svc := &handlers.ScannerService{Log: zerolog.Nop(), Manager: mgr}

	res, err := svc.ListTalkgroupActivity(context.Background(), &scannerv1.ListTalkgroupActivityRequest{})
	require.NoError(t, err)
	assert.True(t, res.GetEnabled())
	assert.Equal(t, uint32(128), res.GetHistoryCapacity())
	require.Len(t, res.GetGrants(), 2)
	assert.True(t, res.GetGrants()[0].GetEncrypted())
	assert.Equal(t, "UT-west", res.GetGrants()[0].GetSiteKey())
	assert.True(t, res.GetGrants()[1].GetEmergency())
	assert.Equal(t, uint32(1234567), res.GetGrants()[1].GetSourceUnit())
	assert.Equal(t, uint32(3), res.GetGrants()[1].GetGrants())

	res, err = svc.ListTalkgroupActivity(context.Background(), &scannerv1.ListTalkgroupActivityRequest{Limit: 1})
	require.NoError(t, err)
	assert.Len(t, res.GetGrants(), 1)

	res, err = (&handlers.ScannerService{Log: zerolog.Nop()}).ListTalkgroupActivity(context.Background(), &scannerv1.ListTalkgroupActivityRequest{})
	require.NoError(t, err)
	assert.False(t, res.GetEnabled())
}

func TestScannerService_Config(t *testing.T) {
	all, err := sites.Table()
	require.NoError(t, err)

	key := all[0].Key

	store := &fakeScannerConfigStore{cfg: config.ScannerConfig{Mode: config.ScannerModeAuto, ScanSerial: "scan",
		PositionStaleAfter: time.Minute, AircraftStaleAfter: time.Minute, DecodeStaleAfter: 10 * time.Second}}
	mgr := &fakeScannerManager{}
	svc := &handlers.ScannerService{Log: zerolog.Nop(), Cfg: store, Manager: mgr}

	got, err := svc.GetScannerConfig(context.Background(), &scannerv1.GetScannerConfigRequest{})
	require.NoError(t, err)
	assert.False(t, got.GetConfig().GetEnabled())
	assert.Equal(t, scannerv1.SiteSelectionMode_SITE_SELECTION_MODE_AUTO, got.GetConfig().GetMode())
	assert.Equal(t, int64(60), got.GetConfig().GetPositionStaleAfter().GetSeconds())

	set, err := svc.SetScannerConfig(context.Background(), &scannerv1.SetScannerConfigRequest{Config: &scannerv1.ScannerConfig{
		Enabled: true, Mode: scannerv1.SiteSelectionMode_SITE_SELECTION_MODE_MANUAL, ManualSiteKey: key,
		States: []string{"UT"}, HysteresisKm: 3, DecodeStaleAfter: durationpb.New(5 * time.Second),
	}})
	require.NoError(t, err)
	assert.True(t, set.GetConfig().GetEnabled())
	assert.Equal(t, scannerv1.SiteSelectionMode_SITE_SELECTION_MODE_MANUAL, set.GetConfig().GetMode())
	assert.Equal(t, key, set.GetConfig().GetManualSiteKey())

	persisted := store.getPersisted()
	require.Len(t, persisted, 1)
	assert.Equal(t, config.ScannerSettings{
		Enable: true, Mode: config.ScannerModeManual, ManualSite: key, States: []string{"UT"}, HysteresisKm: 3,
		PositionStaleAfter: config.DefaultScannerPositionStaleAfter, AircraftStaleAfter: config.DefaultScannerAircraftStaleAfter,
		DecodeStaleAfter: 5 * time.Second,
	}, persisted[0], "unset durations take the defaults")

	applied := mgr.getApplied()
	require.Len(t, applied, 1)
	assert.True(t, applied[0].Enable)
	assert.Equal(t, "scan", applied[0].ScanSerial, "the store's full config is applied")

	_, err = svc.SetScannerConfig(context.Background(), &scannerv1.SetScannerConfigRequest{Config: &scannerv1.ScannerConfig{
		Mode: scannerv1.SiteSelectionMode_SITE_SELECTION_MODE_MANUAL, ManualSiteKey: "XX-00000000",
	}})

	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
	assert.Contains(t, connectErr.Message(), "not in the site table")

	store.persistErr = errors.New("read-only filesystem")
	_, err = svc.SetScannerConfig(context.Background(), &scannerv1.SetScannerConfigRequest{Config: &scannerv1.ScannerConfig{
		Mode: scannerv1.SiteSelectionMode_SITE_SELECTION_MODE_AUTO,
	}})
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeInternal, connectErr.Code())
	assert.Len(t, mgr.getApplied(), 1, "a failed persist applies nothing")

	noCfg := &handlers.ScannerService{Log: zerolog.Nop()}
	_, err = noCfg.GetScannerConfig(context.Background(), &scannerv1.GetScannerConfigRequest{})
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())

	_, err = noCfg.SetScannerConfig(context.Background(), &scannerv1.SetScannerConfigRequest{Config: &scannerv1.ScannerConfig{}})
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
}

func newScannerTestClient(t *testing.T, svc *handlers.ScannerService) scannerv1connect.ScannerServiceClient {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle(scannerv1connect.NewScannerServiceHandler(svc, connect.WithInterceptors(validate.NewInterceptor())))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return scannerv1connect.NewScannerServiceClient(http.DefaultClient, srv.URL, connect.WithGRPCWeb())
}

func TestScannerService_StreamAircraft(t *testing.T) {
	mgr := &fakeScannerManager{enabled: true, now: scannerT0, aircraft: []aircraft.Aircraft{{ICAO: "a1b2c3", LastSeen: scannerT0}}}
	client := newScannerTestClient(t, &handlers.ScannerService{Log: zerolog.Nop(), Manager: mgr})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	stream, err := client.StreamAircraft(ctx, &scannerv1.StreamAircraftRequest{Interval: durationpb.New(500 * time.Millisecond)})
	require.NoError(t, err)

	for range 2 {
		require.True(t, stream.Receive(), "stream error: %v", stream.Err())
		assert.True(t, stream.Msg().GetEnabled())
		require.Len(t, stream.Msg().GetAircraft(), 1)
		assert.Equal(t, "a1b2c3", stream.Msg().GetAircraft()[0].GetIcao())
	}

	cancel()
	require.NoError(t, stream.Close())

	bad, err := client.StreamAircraft(context.Background(), &scannerv1.StreamAircraftRequest{Interval: durationpb.New(time.Millisecond)})
	require.NoError(t, err)
	assert.False(t, bad.Receive())

	var connectErr *connect.Error
	require.ErrorAs(t, bad.Err(), &connectErr)
	assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
}

func TestScannerService_StreamScannerEvents(t *testing.T) {
	events := make(chan scanner.Event, 4)
	mgr := &fakeScannerManager{enabled: true, events: events, status: scanner.Status{
		Enabled: true, Control: scanner.ControlStatus{State: scanner.ControlHunting},
	}, grants: []scanner.Grant{
		{Talkgroup: 2, Last: scannerT0.Add(time.Second)}, {Talkgroup: 1, Last: scannerT0},
	}}
	client := newScannerTestClient(t, &handlers.ScannerService{Log: zerolog.Nop(), Manager: mgr})

	for _, ev := range []scanner.Event{
		{Time: scannerT0, Kind: scanner.EventControl, Control: scanner.ControlStatus{State: scanner.ControlLocked}},
		{Time: scannerT0, Kind: scanner.EventSite, Site: nil},
		{Time: scannerT0, Kind: scanner.EventNone},
		{Time: scannerT0, Kind: scanner.EventGrant, Grant: scanner.Grant{Talkgroup: 3, Encrypted: true}},
	} {
		events <- ev
	}

	close(events)

	stream, err := client.StreamScannerEvents(context.Background(), &scannerv1.StreamScannerEventsRequest{ReplayRecent: true})
	require.NoError(t, err)

	var got []*scannerv1.StreamScannerEventsResponse
	for stream.Receive() {
		got = append(got, proto.Clone(stream.Msg()).(*scannerv1.StreamScannerEventsResponse))
	}

	require.NoError(t, stream.Err())
	require.Len(t, got, 6)
	assert.Equal(t, uint32(1), got[0].GetVoiceGrant().GetTalkgroup(), "replay is oldest first")
	assert.Equal(t, uint32(2), got[1].GetVoiceGrant().GetTalkgroup())
	assert.Equal(t, scannerv1.ControlChannelState_CONTROL_CHANNEL_STATE_HUNTING, got[2].GetControlChannel().GetState(),
		"current state first")
	assert.Equal(t, scannerv1.ControlChannelState_CONTROL_CHANNEL_STATE_LOCKED, got[3].GetControlChannel().GetState())
	assert.NotNil(t, got[4].GetSiteChanged())
	assert.Nil(t, got[4].GetSiteChanged().GetSite(), "no site")
	assert.True(t, got[5].GetVoiceGrant().GetEncrypted())
	assert.Equal(t, 1, mgr.getCancels())

	tests := []struct {
		err  error
		code connect.Code
	}{
		{err: scanner.ErrTooManySubscribers, code: connect.CodeResourceExhausted},
		{err: scanner.ErrDisabled, code: connect.CodeFailedPrecondition},
	}

	for _, tc := range tests {
		c := newScannerTestClient(t, &handlers.ScannerService{Log: zerolog.Nop(), Manager: &fakeScannerManager{subErr: tc.err}})
		s, err := c.StreamScannerEvents(context.Background(), &scannerv1.StreamScannerEventsRequest{})
		require.NoError(t, err)
		assert.False(t, s.Receive())

		var connectErr *connect.Error
		require.ErrorAs(t, s.Err(), &connectErr)
		assert.Equal(t, tc.code, connectErr.Code())
	}
}

func TestScannerValidation(t *testing.T) {
	v := newValidator(t)
	lat := 40.0
	badLat := 91.0

	tests := []struct {
		name  string
		msg   proto.Message
		valid bool
	}{
		{name: "auto config", msg: &scannerv1.SetScannerConfigRequest{Config: &scannerv1.ScannerConfig{Mode: scannerv1.SiteSelectionMode_SITE_SELECTION_MODE_AUTO}}, valid: true},
		{name: "config required", msg: &scannerv1.SetScannerConfigRequest{}},
		{name: "mode unspecified", msg: &scannerv1.ScannerConfig{}},
		{name: "manual without key", msg: &scannerv1.ScannerConfig{Mode: scannerv1.SiteSelectionMode_SITE_SELECTION_MODE_MANUAL}},
		{name: "manual with key", msg: &scannerv1.ScannerConfig{Mode: scannerv1.SiteSelectionMode_SITE_SELECTION_MODE_MANUAL, ManualSiteKey: "UT-1"}, valid: true},
		{name: "unknown state", msg: &scannerv1.ScannerConfig{Mode: 1, States: []string{"TX"}}},
		{name: "duplicate state", msg: &scannerv1.ScannerConfig{Mode: 1, States: []string{"UT", "UT"}}},
		{name: "hysteresis max", msg: &scannerv1.ScannerConfig{Mode: 1, HysteresisKm: 100}, valid: true},
		{name: "hysteresis above max", msg: &scannerv1.ScannerConfig{Mode: 1, HysteresisKm: 100.5}},
		{name: "hysteresis negative", msg: &scannerv1.ScannerConfig{Mode: 1, HysteresisKm: -1}},
		{name: "position stale min", msg: &scannerv1.ScannerConfig{Mode: 1, PositionStaleAfter: durationpb.New(5 * time.Second)}, valid: true},
		{name: "position stale below min", msg: &scannerv1.ScannerConfig{Mode: 1, PositionStaleAfter: durationpb.New(4 * time.Second)}},
		{name: "decode stale above max", msg: &scannerv1.ScannerConfig{Mode: 1, DecodeStaleAfter: durationpb.New(11 * time.Minute)}},
		{name: "aircraft stale max", msg: &scannerv1.ScannerConfig{Mode: 1, AircraftStaleAfter: durationpb.New(10 * time.Minute)}, valid: true},
		{name: "sites point", msg: &scannerv1.ListSitesRequest{Latitude: &lat, Longitude: &lat}, valid: true},
		{name: "sites lat only", msg: &scannerv1.ListSitesRequest{Latitude: &lat}},
		{name: "sites bad lat", msg: &scannerv1.ListSitesRequest{Latitude: &badLat, Longitude: &lat}},
		{name: "sites limit max", msg: &scannerv1.ListSitesRequest{Limit: 500}, valid: true},
		{name: "sites limit above max", msg: &scannerv1.ListSitesRequest{Limit: 501}},
		{name: "activity limit above max", msg: &scannerv1.ListTalkgroupActivityRequest{Limit: 257}},
		{name: "stream interval below min", msg: &scannerv1.StreamAircraftRequest{Interval: durationpb.New(499 * time.Millisecond)}},
		{name: "stream interval max", msg: &scannerv1.StreamAircraftRequest{Interval: durationpb.New(10 * time.Second)}, valid: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := v.Validate(tc.msg)
			if tc.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

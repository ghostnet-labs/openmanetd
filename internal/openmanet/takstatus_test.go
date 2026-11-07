package openmanet

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/mdlayher/wifi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"

	jamv1 "github.com/openmanet/openmanetd/internal/api/openmanet/jamdetect/v1"
	"github.com/openmanet/openmanetd/internal/chanmig"
	"github.com/openmanet/openmanetd/internal/gpsd"
	"github.com/openmanet/openmanetd/internal/hardware"
	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/openmanet/openmanetd/internal/scanner/sites"
	"github.com/openmanet/openmanetd/internal/system"
	"github.com/openmanet/openmanetd/internal/takfeed"
)

func statusTime() time.Time { return time.Date(2026, 11, 7, 14, 0, 0, 0, time.UTC) }

func hwStatus(halow, wifiState hardware.State, level hardware.BatteryLevel) hardware.Status {
	var st hardware.Status

	st.BoardDetected = true
	st.Radios[0] = hardware.RadioStatus{Radio: hardware.RadioHaLow, State: halow, Netdev: "wlan0"}
	st.Radios[1] = hardware.RadioStatus{Radio: hardware.RadioWiFi, State: wifiState, Netdev: "wlan1"}
	st.Battery.Level = level

	return st
}

func TestTAKStatus_everything(t *testing.T) {
	peers := map[key.NodePublic]*ipnstate.PeerStatus{
		key.NewNode().Public(): {Online: true},
		key.NewNode().Public(): {Online: false},
		key.NewNode().Public(): {Online: true},
	}

	s := &takStatus{
		model:    "Raspberry Pi CM5",
		firmware: "24.10.2",
		now:      statusTime,
		sys: &fakeSysReader{
			uptime: 5 * time.Hour, load: 12.5, temp: 83.2,
			mem: &system.MemoryInfo{TotalBytes: 1000, AvailableBytes: 250},
		},
		hw: &fakeHardwareReader{st: hwStatus(hardware.StateUp, hardware.StateFault, hardware.BatteryLevelCritical)},
		jam: &fakeJamReporter{report: &jamv1.JamReport{Radios: []*jamv1.RadioJamStatus{
			{Radio: "wlan0", Band: jamv1.RadioBand_RADIO_BAND_S1G, FreqMhz: 915.5, NoiseKnown: true, NoiseDbm: -99, BusyPct: 8},
			{Radio: "wlan2", Band: jamv1.RadioBand_RADIO_BAND_5G, FreqMhz: 5180, BusyPct: 3},
		}}},
		gps: &fakeGPSReader{
			pos: gpsd.PositionReport{Mode: 3, SatellitesUsed: 8},
			sky: gpsd.SatelliteReport{NSat: 13, USat: 9, HDOP: 1.1},
		},
		comms: func() commsReader { return &fakeCommsReader{talkGroup: 3, transmitting: true} },
		scanner: &fakeScannerReader{
			st: scanner.Status{
				Enabled:   true,
				Selection: &scanner.Selection{Site: &sites.Site{Name: "Utah County"}},
				Control:   scanner.ControlStatus{State: scanner.ControlLocked},
			},
			grants: []scanner.Grant{{Last: statusTime().Add(-40 * time.Second), Talkgroup: 1201, Emergency: true}},
			conv: scanner.ConventionalStatus{Channels: []scanner.ConventionalChannelStatus{
				{Active: true}, {Active: false}, {Active: true},
			}},
		},
		blos: &fakeBLOSReader{running: true, st: &ipnstate.Status{BackendState: "Running", Peer: peers}},
		chanmig: &fakeChanmigReader{snap: chanmig.Snapshot{Bands: []chanmig.BandSnapshot{
			{SwitchAt: statusTime().Add(5 * time.Minute), SwitchTo: chanmig.Assignment{Band: chanmig.BandHaLow, Channel: 28}},
			{Mode: chanmig.BandSearching, Operating: chanmig.Assignment{Band: chanmig.Band5G}},
		}}},
	}

	st := s.NodeStatus(context.Background())

	require.NotNil(t, st.Health)
	assert.InDelta(t, 75.0, st.Health.MemUsedPct, 1e-9)
	assert.True(t, st.Health.HaveTemp)
	assert.Equal(t, "24.10.2", st.Health.Firmware)

	assert.Equal(t, []takfeed.RadioState{
		{Name: "HaLow", State: "up", Band: "s1g", FreqMHz: 915.5, NoiseDBm: -99, BusyPct: 8, HaveAir: true, NoiseKnown: true},
		{Name: "Wi-Fi", State: "fault"},
		{Name: "wlan2", Band: "5g", FreqMHz: 5180, BusyPct: 3, HaveAir: true},
	}, st.Radios)

	assert.Equal(t, &takfeed.GPSQuality{Mode: 3, SatsUsed: 9, SatsSeen: 13, HDOP: 1.1}, st.GPS)
	assert.Equal(t, &takfeed.CommsState{TalkGroup: 3, Transmitting: true}, st.Comms)
	assert.Equal(t, "P25 site Utah County, control channel locked; voice on talk group 1201 40 s ago (EMERGENCY); 2 conventional channels active", st.Scanner)
	assert.Equal(t, "BLOS connected, 2 of 3 peers online", st.Uplink)
	assert.Equal(t, "HaLow moves to channel 28 at 14:05Z; 5 GHz searching for the mesh", st.ChannelMove)
	assert.Equal(t, []string{"CPU hot (83 C)", "Wi-Fi radio fault", "battery critical"}, st.Alerts)
}

func TestTAKStatus_nothingRunning(t *testing.T) {
	s := &takStatus{comms: func() commsReader { return &fakeCommsReader{} }}

	st := s.NodeStatus(context.Background())

	assert.Nil(t, st.Health, "no health line without any source")
	assert.Empty(t, st.Radios)
	assert.Nil(t, st.GPS)
	assert.Nil(t, st.Comms, "talk group 0 means comms is off")
	assert.Empty(t, st.Scanner)
	assert.Empty(t, st.Uplink)
	assert.Empty(t, st.ChannelMove)
	assert.Empty(t, st.Alerts)
}

func TestTAKStatus_sourceErrorsAndEdgeStates(t *testing.T) {
	s := &takStatus{
		now: statusTime,
		sys: &fakeSysReader{loadErr: errors.New("x"), tempErr: errors.New("x")},
		// No V1 board: the hardware manager's radio states and battery
		// level are meaningless and must not raise alerts.
		hw:  &fakeHardwareReader{st: hardware.Status{}},
		gps: &fakeGPSReader{},
		scanner: &fakeScannerReader{
			st:     scanner.Status{Enabled: true},
			grants: []scanner.Grant{{Last: statusTime().Add(-10 * time.Minute), Talkgroup: 5}},
		},
		blos:    &fakeBLOSReader{running: true},
		chanmig: &fakeChanmigReader{snap: chanmig.Snapshot{Bands: []chanmig.BandSnapshot{{Mode: chanmig.BandSettled}}}},
	}

	st := s.NodeStatus(context.Background())

	assert.Nil(t, st.Health)
	assert.Empty(t, st.Radios)
	assert.Nil(t, st.GPS, "no fix and no sky report means GNSS isn't there")
	assert.Empty(t, st.Scanner, "an old voice grant is not shown")
	assert.Equal(t, "BLOS starting", st.Uplink)
	assert.Empty(t, st.ChannelMove)
	assert.Empty(t, st.Alerts)
}

func TestTAKStatus_uplinkAndAlertVariants(t *testing.T) {
	s := &takStatus{blos: &fakeBLOSReader{running: true, st: &ipnstate.Status{BackendState: "NeedsLogin"}}}
	assert.Equal(t, "BLOS needslogin", s.uplinkLine())

	s.blos = &fakeBLOSReader{running: false}
	assert.Empty(t, s.uplinkLine())

	hw := hwStatus(hardware.StateCooldown, hardware.StateUp, hardware.BatteryLevelShutdown)
	hw.PowerFault = true
	hw.ShutdownPending = true
	assert.Equal(t, []string{"HaLow radio cooldown", "battery empty, shutting down", "power fault"}, alerts(&hw, nil))

	hw = hwStatus(hardware.StateUp, hardware.StateUp, hardware.BatteryLevelWarning)
	hw.ShutdownPending = true
	assert.Equal(t, []string{"battery low", "shutting down"}, alerts(&hw, nil))
}

func TestStationSignals(t *testing.T) {
	mac := func(s string) net.HardwareAddr {
		m, err := net.ParseMAC(s)
		require.NoError(t, err)

		return m
	}

	lister := &fakeStationLister{
		ifaces: []*wifi.Interface{{Name: "mesh0"}, {Name: "gone"}},
		stations: map[string][]*wifi.StationInfo{"mesh0": {
			{HardwareAddr: mac("AA:BB:CC:00:00:01"), Signal: -60, SignalAverage: -62, TransmitBitrate: 26_000_000},
			{HardwareAddr: mac("aa:bb:cc:00:00:02"), Signal: -75},
			nil,
			{},
		}},
	}

	got := stationSignals{wifi: lister}.StationSignals(context.Background())

	assert.Equal(t, map[string]takfeed.StationSignal{
		"aa:bb:cc:00:00:01": {SignalDBm: -62, TxBitrateKbps: 26000},
		"aa:bb:cc:00:00:02": {SignalDBm: -75},
	}, got)

	lister.ifacesErr = errors.New("netlink down")
	assert.Nil(t, stationSignals{wifi: lister}.StationSignals(context.Background()))
}

func TestNewTAKStatus_nilSourcesStayNilInterfaces(t *testing.T) {
	st := newTAKStatus("model", nil, nil, nil, nil)

	assert.Nil(t, st.gps)
	assert.Nil(t, st.scanner)
	assert.Nil(t, st.jam)
	assert.Nil(t, st.sys)
	assert.Nil(t, st.hw)
	assert.Nil(t, st.blos)
	assert.Nil(t, st.chanmig)
	assert.NotNil(t, st.comms)
	assert.Equal(t, "model", st.model)

	// comms.Default() is nil here; the accessor must still be safe.
	assert.NotPanics(t, func() { st.NodeStatus(context.Background()) })
}

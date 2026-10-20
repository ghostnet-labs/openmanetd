package handlers_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"connectrpc.com/connect"
	"github.com/digineo/go-uci/v2"
	meshjoinv1 "github.com/openmanet/openmanetd/internal/api/openmanet/mesh_join/v1"
	wificonfigv1 "github.com/openmanet/openmanetd/internal/api/openmanet/wifi_config/v1"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/openmanet/openmanetd/internal/radiocap"
	"github.com/openmanet/openmanetd/internal/radiogate"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// iwListFixture reads testfixtures/radiocap/node_mt7916_halow.txt: phy0
// 2.4 GHz and phy1 5 GHz (MT7916 DBDC) plus a Morse HaLow phy2.
func iwListFixture(t *testing.T) []byte {
	t.Helper()

	_, here, _, ok := runtime.Caller(0)
	require.True(t, ok)

	root := here
	for range 5 {
		root = filepath.Dir(root)
	}

	data, err := os.ReadFile(filepath.Join(root, "testfixtures", "radiocap", "node_mt7916_halow.txt"))
	require.NoError(t, err)

	return data
}

// fullEvidence qualifies every fixture PHY, including same-channel sharing.
func fullEvidence() radiogate.Settings {
	all := []string{"phy0", "phy1", "phy2"}

	return radiogate.Settings{QualifiedPHYs: all, SharingQualifiedPHYs: all}
}

// newCapacityGate builds a real gate over the iw list fixture. A non-nil
// probeErr makes every `iw list` run fail.
func newCapacityGate(t *testing.T, mode radiogate.Mode, s radiogate.Settings, probeErr error) *radiogate.Gate {
	t.Helper()

	out := iwListFixture(t)

	return &radiogate.Gate{
		Log: zerolog.Nop(),
		Source: &radiogate.Provider{
			Exec: func(context.Context) ([]byte, error) {
				if probeErr != nil {
					return nil, probeErr
				}

				return out, nil
			},
			HaLowPHYs: func(context.Context) ([]string, error) { return []string{"phy2"}, nil },
			Settings:  func() radiogate.Settings { return s },
		},
		Mode: func() radiogate.Mode { return mode },
	}
}

func s1gChannel(n int, widthKHz int) radiocap.Channel {
	return radiocap.Channel{Band: radiocap.BandS1G, CenterKHz: 902000 + 500*n, WidthKHz: widthKHz}
}

func TestApplyRadioSettingsBatch_capacityDemands(t *testing.T) {
	apOn2G := func(center, width int) radiocap.Demand {
		return radiocap.Demand{ID: "default_radio2", Purpose: radiocap.PurposeEUD, Role: radiocap.RoleAP,
			Channel: radiocap.Channel{Band: radiocap.Band2G4, CenterKHz: center, WidthKHz: width}}
	}
	halowMesh := radiocap.Demand{ID: "default_radio3", Purpose: radiocap.PurposeMesh, Role: radiocap.RoleMesh,
		Channel: s1gChannel(42, 1000)}

	tests := []struct {
		name          string
		mutate        func(t *testing.T, r *fakeConfigReader)
		settings      *wificonfigv1.RadioSettings
		wantRequested []radiocap.Demand
		wantCurrent   []radiocap.Demand
	}{
		{
			name:          "channel change retunes the radio",
			settings:      &wificonfigv1.RadioSettings{Ssid: "ap", Channel: "6"},
			wantRequested: []radiocap.Demand{apOn2G(2437000, 20000)},
			wantCurrent:   []radiocap.Demand{halowMesh},
		},
		{
			name: "mode and width change",
			settings: &wificonfigv1.RadioSettings{
				Ssid: "m", MeshId: strPtr("m"), Channel: "11", Mode: wificonfigv1.WifiMode_WIFI_MODE_MESH,
				Bandwidth: wificonfigv1.WifiHTMode_WIFI_HT_MODE_HT40,
			},
			wantRequested: []radiocap.Demand{{
				ID: "default_radio2", Purpose: radiocap.PurposeMesh, Role: radiocap.RoleMesh,
				Channel: radiocap.Channel{Band: radiocap.Band2G4, CenterKHz: 2462000, WidthKHz: 40000},
			}},
			wantCurrent: []radiocap.Demand{halowMesh},
		},
		{
			name:     "station mode",
			settings: &wificonfigv1.RadioSettings{Ssid: "up", Channel: "1", Mode: wificonfigv1.WifiMode_WIFI_MODE_STA},
			wantRequested: []radiocap.Demand{{
				ID: "default_radio2", Purpose: radiocap.PurposeOther, Role: radiocap.RoleStation,
				Channel: radiocap.Channel{Band: radiocap.Band2G4, CenterKHz: 2412000, WidthKHz: 20000},
			}},
			wantCurrent: []radiocap.Demand{halowMesh},
		},
		{
			name:        "disabling the only iface requests nothing",
			settings:    &wificonfigv1.RadioSettings{Ssid: "ap", Channel: "1", Disabled: proto.Bool(true)},
			wantCurrent: []radiocap.Demand{halowMesh},
		},
		{
			name: "auto channel",
			mutate: func(t *testing.T, r *fakeConfigReader) {
				t.Helper()
				require.NoError(t, r.SetType("wireless", "radio3", "htmode", uci.TypeOption, "8 MHz"))
			},
			settings:      &wificonfigv1.RadioSettings{Ssid: "ap", Channel: "auto"},
			wantRequested: []radiocap.Demand{apOn2G(1, 20000)}, // radio2 is device slot 0
			wantCurrent: []radiocap.Demand{{ID: "default_radio3", Purpose: radiocap.PurposeMesh, Role: radiocap.RoleMesh,
				Channel: s1gChannel(42, 8000)}},
		},
		{
			name: "disabled device is not current",
			mutate: func(t *testing.T, r *fakeConfigReader) {
				t.Helper()
				require.NoError(t, r.SetType("wireless", "radio3", "disabled", uci.TypeOption, "1"))
			},
			settings:      &wificonfigv1.RadioSettings{Ssid: "ap", Channel: "6"},
			wantRequested: []radiocap.Demand{apOn2G(2437000, 20000)},
		},
		{
			name: "modes without a capacity role are skipped",
			mutate: func(t *testing.T, r *fakeConfigReader) {
				t.Helper()
				require.NoError(t, r.SetType("wireless", "default_radio3", "mode", uci.TypeOption, "monitor"))
			},
			settings:      &wificonfigv1.RadioSettings{Ssid: "ap", Channel: "6"},
			wantRequested: []radiocap.Demand{apOn2G(2437000, 20000)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := newWifiConfigMockReader()
			if tc.mutate != nil {
				tc.mutate(t, reader)
			}

			gate := &fakeRadioCapacityGate{}
			svc := newTestWifiConfigService(t)
			svc.ConfigReader = reader
			svc.Capacity = gate

			require.NoError(t, svc.ApplyRadioSettingsBatch(context.Background(), []handlers.RadioSettingsUpdate{
				{RadioName: "radio2", Settings: tc.settings},
			}))

			reqs := gate.getRequests()
			require.Len(t, reqs, 1)
			assert.Equal(t, "wifi_config.apply_radio_settings_batch", reqs[0].Origin)
			assert.Equal(t, []string{"radio2"}, reqs[0].Radios)
			assert.Equal(t, tc.wantRequested, nilIfEmpty(reqs[0].Requested))
			assert.Equal(t, tc.wantCurrent, nilIfEmpty(reqs[0].Current))
		})
	}
}

func nilIfEmpty(ds []radiocap.Demand) []radiocap.Demand {
	if len(ds) == 0 {
		return nil
	}

	return ds
}

func TestApplyRadioSettingsBatch_capacityDeniedStagesNothing(t *testing.T) {
	reader := newWifiConfigMockReader()
	rl := &fakeRadioReloader{}
	gate := &fakeRadioCapacityGate{evaluateErr: &radiogate.DeniedError{Decision: radiocap.Decision{
		Outcome: radiocap.OutcomeReject, Reason: radiocap.ReasonCapacityShort, Explanation: "Rejected: short",
	}}}

	svc := newTestWifiConfigService(t)
	svc.ConfigReader = reader
	svc.ReloadServices = rl.Reload
	svc.Capacity = gate

	err := svc.ApplyRadioSettingsBatch(context.Background(), []handlers.RadioSettingsUpdate{
		{RadioName: "radio2", Settings: &wificonfigv1.RadioSettings{Ssid: "ap", Channel: "6"}},
	})

	var cerr *connect.Error
	require.ErrorAs(t, err, &cerr)
	assert.Equal(t, connect.CodeFailedPrecondition, cerr.Code())
	assert.Contains(t, cerr.Message(), "capacity_short")
	assert.Equal(t, "reject", cerr.Meta().Get("Radio-Capacity-Outcome"))
	assert.Equal(t, "capacity_short", cerr.Meta().Get("Radio-Capacity-Reason"))

	ch, _ := reader.Get("wireless", "radio2", "channel")
	assert.Equal(t, []string{"1"}, ch, "nothing staged")
	assert.Equal(t, 0, rl.count(), "no reload")
}

func TestApplyRadioSettingsBatch_capacityGateErrorProceeds(t *testing.T) {
	reader := newWifiConfigMockReader()
	svc := newTestWifiConfigService(t)
	svc.ConfigReader = reader
	svc.Capacity = &fakeRadioCapacityGate{evaluateErr: errors.New("unexpected")}

	require.NoError(t, svc.ApplyRadioSettingsBatch(context.Background(), []handlers.RadioSettingsUpdate{
		{RadioName: "radio2", Settings: &wificonfigv1.RadioSettings{Ssid: "ap", Channel: "6"}},
	}))

	ch, _ := reader.Get("wireless", "radio2", "channel")
	assert.Equal(t, []string{"6"}, ch)
}

func TestApplyRadioSettingsBatch_capacityDemandReadErrorProceeds(t *testing.T) {
	// Staging only lists wifi-iface sections; the full-node demand read
	// also lists wifi-device sections, which fail here.
	reader := &failingSectionsReader{fakeConfigReader: newWifiConfigMockReader(), failType: "wifi-device"}
	gate := &fakeRadioCapacityGate{}
	svc := newTestWifiConfigService(t)
	svc.ConfigReader = reader
	svc.Capacity = gate

	require.NoError(t, svc.ApplyRadioSettingsBatch(context.Background(), []handlers.RadioSettingsUpdate{
		{RadioName: "radio2", Settings: &wificonfigv1.RadioSettings{Ssid: "ap", Channel: "6"}},
	}))

	assert.Empty(t, gate.getRequests(), "no demands, no evaluation")

	ch, _ := reader.Get("wireless", "radio2", "channel")
	assert.Equal(t, []string{"6"}, ch, "the change still applies")
}

func TestApplyRadioSettingsBatch_realGateModes(t *testing.T) {
	tests := []struct {
		name        string
		mode        radiogate.Mode
		settings    radiogate.Settings
		probeErr    error
		wantBlocked bool
		wantRecords int
		wantReason  radiocap.Reason
		wantErrRec  bool
	}{
		{name: "off", mode: radiogate.ModeOff},
		{
			name: "advisory reject proceeds", mode: radiogate.ModeAdvisory,
			wantRecords: 1, wantReason: radiocap.ReasonUnqualifiedOnly,
		},
		{
			name: "enforce reject blocks", mode: radiogate.ModeEnforce,
			wantBlocked: true, wantRecords: 1, wantReason: radiocap.ReasonUnqualifiedOnly,
		},
		{
			name: "enforce admit", mode: radiogate.ModeEnforce, settings: fullEvidence(),
			wantRecords: 1, wantReason: radiocap.ReasonFits,
		},
		{
			name: "enforce defer blocks", mode: radiogate.ModeEnforce,
			settings: radiogate.Settings{
				QualifiedPHYs: fullEvidence().QualifiedPHYs, SharingQualifiedPHYs: fullEvidence().QualifiedPHYs,
				UnavailablePHYs: []string{"phy0"},
			},
			wantBlocked: true, wantRecords: 1, wantReason: radiocap.ReasonPHYUnavailable,
		},
		{
			name: "enforce probe failure proceeds", mode: radiogate.ModeEnforce,
			probeErr: errors.New("exec: iw: not found"), wantRecords: 1, wantErrRec: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := newWifiConfigMockReader()
			gate := newCapacityGate(t, tc.mode, tc.settings, tc.probeErr)
			svc := newTestWifiConfigService(t)
			svc.ConfigReader = reader
			svc.Capacity = gate

			err := svc.ApplyRadioSettingsBatch(context.Background(), []handlers.RadioSettingsUpdate{
				{RadioName: "radio2", Settings: &wificonfigv1.RadioSettings{Ssid: "ap", Channel: "6"}},
			})

			ch, _ := reader.Get("wireless", "radio2", "channel")

			if tc.wantBlocked {
				var cerr *connect.Error
				require.ErrorAs(t, err, &cerr)
				assert.Equal(t, connect.CodeFailedPrecondition, cerr.Code())
				assert.Equal(t, string(tc.wantReason), cerr.Meta().Get("Radio-Capacity-Reason"))
				assert.Equal(t, []string{"1"}, ch, "a blocked change stages nothing")
			} else {
				require.NoError(t, err)
				assert.Equal(t, []string{"6"}, ch)
			}

			recs := gate.Recent()
			require.Len(t, recs, tc.wantRecords)

			if tc.wantRecords == 0 {
				return
			}

			assert.Equal(t, tc.wantBlocked, recs[0].Blocked)

			if tc.wantErrRec {
				assert.Contains(t, recs[0].Error, "iw list")
				assert.Nil(t, recs[0].Decision)

				return
			}

			require.NotNil(t, recs[0].Decision)
			assert.Equal(t, tc.wantReason, recs[0].Decision.Reason)
		})
	}
}

func TestUpdateRadioSettings_capacityEnforced(t *testing.T) {
	reader := newWifiConfigMockReader()
	gate := newCapacityGate(t, radiogate.ModeEnforce, radiogate.Settings{}, nil)
	svc := newTestWifiConfigService(t)
	svc.ConfigReader = reader
	svc.Capacity = gate

	_, err := svc.UpdateRadioSettings(context.Background(), &wificonfigv1.UpdateRadioSettingsRequest{
		RadioName: "radio2",
		Settings:  &wificonfigv1.RadioSettings{Ssid: "ap", Channel: "6"},
	})

	var cerr *connect.Error
	require.ErrorAs(t, err, &cerr)
	assert.Equal(t, connect.CodeFailedPrecondition, cerr.Code())
	assert.Contains(t, cerr.Message(), "unqualified_only")

	recs := gate.Recent()
	require.Len(t, recs, 1)
	assert.Equal(t, "wifi_config.update_radio_settings", recs[0].Origin)

	ch, _ := reader.Get("wireless", "radio2", "channel")
	assert.Equal(t, []string{"1"}, ch)
}

func TestUpdateRadioSettings_capacityAdvisoryProceeds(t *testing.T) {
	gate := newCapacityGate(t, radiogate.ModeAdvisory, radiogate.Settings{}, nil)
	svc := newTestWifiConfigService(t)
	svc.Capacity = gate

	resp, err := svc.UpdateRadioSettings(context.Background(), &wificonfigv1.UpdateRadioSettingsRequest{
		RadioName: "radio2",
		Settings:  &wificonfigv1.RadioSettings{Ssid: "ap", Channel: "6"},
	})
	require.NoError(t, err)
	assert.True(t, resp.GetSuccess())

	recs := gate.Recent()
	require.Len(t, recs, 1)
	assert.Equal(t, radiocap.OutcomeReject, recs[0].Decision.Outcome)
	assert.False(t, recs[0].Blocked)
}

func TestApplyMeshJoin_capacityDeniedIsFailedPrecondition(t *testing.T) {
	svc, applier := newTestMeshJoinApplyService(t, newMeshJoinReader())

	denied := &radiogate.DeniedError{Decision: radiocap.Decision{
		Outcome: radiocap.OutcomeDefer, Reason: radiocap.ReasonPHYUnavailable, Explanation: "Deferred",
	}}
	applier.applyErr = connect.NewError(connect.CodeFailedPrecondition, denied)

	_, err := svc.ApplyMeshJoin(context.Background(), &meshjoinv1.ApplyMeshJoinRequest{Payload: joinPayload()})

	var cerr *connect.Error
	require.ErrorAs(t, err, &cerr)
	assert.Equal(t, connect.CodeFailedPrecondition, cerr.Code(), "a capacity denial is not an internal error")
	assert.Contains(t, cerr.Message(), "phy_unavailable")
	assert.Equal(t, "defer", cerr.Meta().Get("Radio-Capacity-Outcome"))
	assert.Equal(t, "phy_unavailable", cerr.Meta().Get("Radio-Capacity-Reason"))
}

// TestApplyMeshJoin_capacityGateThroughWifiService runs mesh join against
// the real WifiConfigService so the join targets are what the gate sees.
func TestApplyMeshJoin_capacityGateThroughWifiService(t *testing.T) {
	tests := []struct {
		name        string
		mode        radiogate.Mode
		settings    radiogate.Settings
		wantBlocked bool
	}{
		{name: "advisory", mode: radiogate.ModeAdvisory},
		{name: "enforce unqualified", mode: radiogate.ModeEnforce, wantBlocked: true},
		{name: "enforce qualified", mode: radiogate.ModeEnforce, settings: fullEvidence()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := newMeshJoinReader()
			addBatmanSections(reader)

			gate := newCapacityGate(t, tc.mode, tc.settings, nil)
			wifiSvc := newTestWifiConfigService(t)
			wifiSvc.ConfigReader = reader
			wifiSvc.Capacity = gate

			svc := newTestMeshJoinService(reader)
			svc.Radios = wifiSvc
			svc.RegDBPath = regdbFixturePath(t)

			_, err := svc.ApplyMeshJoin(context.Background(), &meshjoinv1.ApplyMeshJoinRequest{Payload: joinPayload()})

			if tc.wantBlocked {
				var cerr *connect.Error
				require.ErrorAs(t, err, &cerr)
				assert.Equal(t, connect.CodeFailedPrecondition, cerr.Code())
			} else {
				require.NoError(t, err)
			}

			recs := gate.Recent()
			require.Len(t, recs, 1)
			assert.ElementsMatch(t, []string{"radio3", "radio0"}, recs[0].Radios)
			assert.Equal(t, 2, recs[0].Requested, "HaLow and backhaul mesh")
			assert.Equal(t, 1, recs[0].Current, "the untouched EUD AP")
			assert.Equal(t, tc.wantBlocked, recs[0].Blocked)
		})
	}
}

// addBatmanSections gives the mesh join reader the bat0 device and both
// hardifs mesh-mode staging requires.
func addBatmanSections(r *fakeConfigReader) {
	r.data["network"] = map[string]map[string][]string{
		"bat0":     {"proto": {"batadv"}},
		"batmesh0": {"proto": {"batadv_hardif"}, "master": {"bat0"}},
		"batmesh1": {"proto": {"batadv_hardif"}, "master": {"bat0"}},
	}
	r.sectionTypes["network"] = map[string]string{
		"bat0":     "interface",
		"batmesh0": "interface",
		"batmesh1": "interface",
	}
}

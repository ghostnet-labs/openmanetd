//go:build integration

package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	meshjoinv1 "github.com/openmanet/openmanetd/internal/api/openmanet/mesh_join/v1"
	meshjoinconnect "github.com/openmanet/openmanetd/internal/api/openmanet/mesh_join/v1/mesh_joinv1connect"
	wificonfigv1 "github.com/openmanet/openmanetd/internal/api/openmanet/wifi_config/v1"
	wificonfigconnect "github.com/openmanet/openmanetd/internal/api/openmanet/wifi_config/v1/wifi_configv1connect"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/openmanet/openmanetd/internal/radiocap"
	"github.com/openmanet/openmanetd/internal/radiogate"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newRadioCapacityTestServer serves WifiConfigService and MeshJoinService
// over one seeded wireless tree, both behind a real capacity gate.
func newRadioCapacityTestServer(t *testing.T, gate *radiogate.Gate) (*httptest.Server, *fakeConfigReader) {
	t.Helper()

	reader := newMeshJoinReader()
	addBatmanSections(reader)

	wifiSvc := newTestWifiConfigService(t)
	wifiSvc.ConfigReader = reader
	wifiSvc.Capacity = gate

	opt := connect.WithInterceptors(validate.NewInterceptor())

	mux := http.NewServeMux()
	mux.Handle(wificonfigconnect.NewWifiConfigServiceHandler(wifiSvc, opt))
	mux.Handle(meshjoinconnect.NewMeshJoinServiceHandler(&handlers.MeshJoinService{
		Log:          zerolog.Nop(),
		ConfigReader: reader,
		Radios:       wifiSvc,
		Hostname:     func() (string, error) { return "alpha", nil },
		RegDBPath:    regdbFixturePath(t),
	}, opt))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv, reader
}

func apSettingsRequest() *wificonfigv1.UpdateRadioSettingsRequest {
	return &wificonfigv1.UpdateRadioSettingsRequest{
		RadioName: "radio2",
		Settings: &wificonfigv1.RadioSettings{
			Ssid:       "openmanet",
			Channel:    "40",
			Bandwidth:  wificonfigv1.WifiHTMode_WIFI_HT_MODE_VHT80,
			TxPower:    20,
			Encryption: wificonfigv1.WifiEncryption_WIFI_ENCRYPTION_PSK2,
			Mode:       wificonfigv1.WifiMode_WIFI_MODE_AP,
		},
	}
}

func TestIntegration_UpdateRadioSettings_capacityEnforced(t *testing.T) {
	gate := newCapacityGate(t, radiogate.ModeEnforce, radiogate.Settings{}, nil)
	srv, reader := newRadioCapacityTestServer(t, gate)
	client := wificonfigconnect.NewWifiConfigServiceClient(http.DefaultClient, srv.URL, connect.WithGRPCWeb())

	_, err := client.UpdateRadioSettings(context.Background(), apSettingsRequest())

	var cerr *connect.Error
	require.ErrorAs(t, err, &cerr)
	assert.Equal(t, connect.CodeFailedPrecondition, cerr.Code())
	assert.Contains(t, cerr.Message(), string(radiocap.ReasonUnqualifiedOnly))
	assert.Equal(t, string(radiocap.ReasonUnqualifiedOnly), cerr.Meta().Get("Radio-Capacity-Reason"),
		"the machine-readable reason crosses the wire as error metadata")

	ch, _ := reader.Get("wireless", "radio2", "channel")
	assert.Equal(t, []string{"36"}, ch, "nothing staged")
}

func TestIntegration_UpdateRadioSettings_capacityAdvisory(t *testing.T) {
	gate := newCapacityGate(t, radiogate.ModeAdvisory, radiogate.Settings{}, nil)
	srv, reader := newRadioCapacityTestServer(t, gate)
	client := wificonfigconnect.NewWifiConfigServiceClient(http.DefaultClient, srv.URL, connect.WithGRPCWeb())

	resp, err := client.UpdateRadioSettings(context.Background(), apSettingsRequest())
	require.NoError(t, err)
	assert.True(t, resp.GetSuccess())

	ch, _ := reader.Get("wireless", "radio2", "channel")
	assert.Equal(t, []string{"40"}, ch)

	recs := gate.Recent()
	require.Len(t, recs, 1)
	assert.Equal(t, radiocap.OutcomeReject, recs[0].Decision.Outcome)
	assert.False(t, recs[0].Blocked)
}

func TestIntegration_ApplyMeshJoin_capacityGate(t *testing.T) {
	tests := []struct {
		name        string
		mode        radiogate.Mode
		settings    radiogate.Settings
		probeErr    error
		wantBlocked bool
	}{
		{name: "enforce unqualified blocks", mode: radiogate.ModeEnforce, wantBlocked: true},
		{name: "enforce qualified applies", mode: radiogate.ModeEnforce, settings: fullEvidence()},
		{name: "advisory applies", mode: radiogate.ModeAdvisory},
		{name: "enforce probe failure applies", mode: radiogate.ModeEnforce, probeErr: assert.AnError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gate := newCapacityGate(t, tc.mode, tc.settings, tc.probeErr)
			srv, reader := newRadioCapacityTestServer(t, gate)
			client := meshjoinconnect.NewMeshJoinServiceClient(http.DefaultClient, srv.URL, connect.WithGRPCWeb())

			resp, err := client.ApplyMeshJoin(context.Background(), &meshjoinv1.ApplyMeshJoinRequest{Payload: joinPayload()})

			ch, _ := reader.Get("wireless", "radio3", "channel")

			if tc.wantBlocked {
				var cerr *connect.Error
				require.ErrorAs(t, err, &cerr)
				assert.Equal(t, connect.CodeFailedPrecondition, cerr.Code())
				assert.NotEmpty(t, cerr.Meta().Get("Radio-Capacity-Reason"))
				assert.Equal(t, []string{"44"}, ch, "nothing staged")

				return
			}

			require.NoError(t, err)
			assert.Len(t, resp.GetRadios(), 2)
			assert.Equal(t, []string{"28"}, ch)
		})
	}
}

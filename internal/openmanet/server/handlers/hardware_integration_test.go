//go:build integration

package handlers_test

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	hardwarev1 "github.com/openmanet/openmanetd/internal/api/openmanet/hardware/v1"
	"github.com/openmanet/openmanetd/internal/api/openmanet/hardware/v1/hardwarev1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
)

func newHardwareIntegrationClient(t *testing.T) hardwarev1connect.HardwareServiceClient {
	t.Helper()

	srv := newTestServer(t)

	return hardwarev1connect.NewHardwareServiceClient(http.DefaultClient, srv.URL, connect.WithGRPCWeb())
}

func TestIntegration_GetHardwareStatus_telemetryOnlyByDefault(t *testing.T) {
	client := newHardwareIntegrationClient(t)

	resp, err := client.GetHardwareStatus(context.Background(), &emptypb.Empty{})
	require.NoError(t, err)

	st := resp.GetStatus()
	assert.True(t, st.GetBoardDetected())
	assert.Equal(t, hardwarev1.ControlMode_CONTROL_MODE_TELEMETRY_ONLY, st.GetControlMode())
	assert.Equal(t, hardwarev1.ControlModeReason_CONTROL_MODE_REASON_ACTUATION_DISABLED, st.GetControlModeReason())
	require.Len(t, st.GetRadios(), 2)

	for _, r := range st.GetRadios() {
		assert.Equal(t, hardwarev1.RadioState_RADIO_STATE_UNMANAGED, r.GetState(), r.GetRadio().String())
	}

	require.Len(t, st.GetFaultLines(), 7)

	for _, fl := range st.GetFaultLines() {
		assert.Equal(t, hardwarev1.LineState_LINE_STATE_UNAVAILABLE, fl.GetState(), fl.GetName())
	}

	assert.False(t, st.GetBattery().GetPresent())
	assert.Equal(t, hardwarev1.BatteryAbsentReason_BATTERY_ABSENT_REASON_NO_MONITOR, st.GetBattery().GetAbsentReason())
	assert.False(t, st.GetWatchdog().GetEnabled())
	assert.False(t, st.GetWatchdog().GetArmed())
}

func TestIntegration_StreamHardwareStatus_sendsImmediately(t *testing.T) {
	client := newHardwareIntegrationClient(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	stream, err := client.StreamHardwareStatus(ctx, &hardwarev1.StreamHardwareStatusRequest{})
	require.NoError(t, err)

	require.True(t, stream.Receive(), "first message: %v", stream.Err())
	assert.Equal(t, hardwarev1.ControlMode_CONTROL_MODE_TELEMETRY_ONLY, stream.Msg().GetStatus().GetControlMode())
	require.NoError(t, stream.Close())
}

func TestIntegration_HardwareControl_refusedWhenTelemetryOnly(t *testing.T) {
	client := newHardwareIntegrationClient(t)
	ctx := context.Background()

	assertFailedPrecondition := func(t *testing.T, err error) {
		t.Helper()

		var connectErr *connect.Error
		require.ErrorAs(t, err, &connectErr)
		assert.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
		assert.Contains(t, connectErr.Message(), "telemetry-only")
	}

	_, err := client.SetRadioPower(ctx, &hardwarev1.SetRadioPowerRequest{Radio: hardwarev1.Radio_RADIO_HALOW, Enabled: false})
	assertFailedPrecondition(t, err)

	_, err = client.SetRadioRfDisable(ctx, &hardwarev1.SetRadioRfDisableRequest{Radio: hardwarev1.Radio_RADIO_WIFI, Disabled: true})
	assertFailedPrecondition(t, err)

	_, err = client.ExecuteRadioRecovery(ctx, &hardwarev1.ExecuteRadioRecoveryRequest{Radio: hardwarev1.Radio_RADIO_WIFI})
	assertFailedPrecondition(t, err)

	_, err = client.ExecuteGnssReset(ctx, &hardwarev1.ExecuteGnssResetRequest{})
	assertFailedPrecondition(t, err)

	events, err := client.ListRecoveryEvents(ctx, &hardwarev1.ListRecoveryEventsRequest{Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, events.GetEvents(), "refused controls record no event")
}

func TestIntegration_Hardware_validation(t *testing.T) {
	client := newHardwareIntegrationClient(t)
	ctx := context.Background()

	tests := []struct {
		call func() error
		name string
	}{
		{name: "power radio unspecified", call: func() error {
			_, err := client.SetRadioPower(ctx, &hardwarev1.SetRadioPowerRequest{})

			return err //nolint:wrapcheck // returned to the assertion as-is
		}},
		{name: "power radio undefined", call: func() error {
			_, err := client.SetRadioPower(ctx, &hardwarev1.SetRadioPowerRequest{Radio: hardwarev1.Radio(7)})

			return err //nolint:wrapcheck // returned to the assertion as-is
		}},
		{name: "rf disable on halow", call: func() error {
			_, err := client.SetRadioRfDisable(ctx, &hardwarev1.SetRadioRfDisableRequest{Radio: hardwarev1.Radio_RADIO_HALOW})

			return err //nolint:wrapcheck // returned to the assertion as-is
		}},
		{name: "recovery radio unspecified", call: func() error {
			_, err := client.ExecuteRadioRecovery(ctx, &hardwarev1.ExecuteRadioRecoveryRequest{})

			return err //nolint:wrapcheck // returned to the assertion as-is
		}},
		{name: "events limit 65", call: func() error {
			_, err := client.ListRecoveryEvents(ctx, &hardwarev1.ListRecoveryEventsRequest{Limit: 65})

			return err //nolint:wrapcheck // returned to the assertion as-is
		}},
		{name: "events radio undefined", call: func() error {
			_, err := client.ListRecoveryEvents(ctx, &hardwarev1.ListRecoveryEventsRequest{Radio: hardwarev1.Radio(9)})

			return err //nolint:wrapcheck // returned to the assertion as-is
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var connectErr *connect.Error
			require.ErrorAs(t, tc.call(), &connectErr)
			assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
		})
	}

	_, err := client.ListRecoveryEvents(ctx, &hardwarev1.ListRecoveryEventsRequest{Limit: 64})
	require.NoError(t, err)
}

package handlers_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	hardwarev1 "github.com/openmanet/openmanetd/internal/api/openmanet/hardware/v1"
	"github.com/openmanet/openmanetd/internal/api/openmanet/hardware/v1/hardwarev1connect"
	"github.com/openmanet/openmanetd/internal/auth"
	"github.com/openmanet/openmanetd/internal/hardware"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/openmanet/openmanetd/internal/system"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
)

func newHardwareService(m handlers.HardwareManager) *handlers.HardwareService {
	return &handlers.HardwareService{Log: zerolog.Nop(), Manager: m}
}

func sampleHardwareStatus() hardware.Status {
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

	st := hardware.Status{
		GeneratedAt:           at,
		BoardDetected:         true,
		ControlMode:           hardware.ControlModeTelemetryOnly,
		ControlModeReason:     hardware.ControlReasonActuationDisabled,
		RecoveryBootsLastHour: 1,
		PowerFault:            true,
		Battery: hardware.BatteryStatus{
			SampledAt:     at,
			BusMV:         11800,
			CurrentMA:     900,
			PowerMW:       10620,
			CompensatedMV: 11854,
			EnergyMWh:     12.5,
			ChargeMAh:     3.25,
			Alarms:        system.AlarmBusUndervoltage,
			SeriesCells:   3,
			Level:         hardware.BatteryLevelNormal,
			AbsentReason:  hardware.AbsentReasonNone,
			Present:       true,
			HasEnergy:     true,
		},
		Watchdog: hardware.WatchdogStatus{Enabled: true, Device: "/dev/watchdog1", PetsTotal: 7, LastPetAt: at},
	}

	st.Radios[0] = hardware.RadioStatus{
		Radio: hardware.RadioHaLow, State: hardware.StateUp, Reason: hardware.ReasonStartup,
		StateSince: at, Netdev: "wlan0", DevicePresent: true, NetdevPresent: true, PowerRequested: true,
	}
	st.Radios[1] = hardware.RadioStatus{
		Radio: hardware.RadioWiFi, State: hardware.StateFault, Reason: hardware.ReasonHealthCheck,
		CurrentStep: hardware.StepResetPCIeFunction, StepAttempt: 1, NextActionAt: at.Add(time.Minute),
		RecoveriesTotal: 2, PowerCyclesTotal: 1, FailedTotal: 0, RFDisabled: true,
	}

	for i, name := range hardware.FaultInputNames() {
		st.FaultLines[i] = hardware.FaultLineStatus{
			Line:  hardware.FaultLine(i + 1),
			Name:  name,
			State: hardware.LineStateUnavailable,
		}
	}

	st.FaultLines[2].State = hardware.LineStateAsserted
	st.FaultLines[2].EdgesTotal = 4
	st.FaultLines[2].LastChangeAt = at

	return st
}

func TestHardwareService_GetHardwareStatus(t *testing.T) {
	fake := &fakeHardwareManager{status: sampleHardwareStatus()}
	svc := newHardwareService(fake)

	resp, err := svc.GetHardwareStatus(context.Background(), &emptypb.Empty{})
	require.NoError(t, err)

	st := resp.GetStatus()
	require.NotNil(t, st)
	assert.True(t, st.GetBoardDetected())
	assert.Equal(t, hardwarev1.ControlMode_CONTROL_MODE_TELEMETRY_ONLY, st.GetControlMode())
	assert.Equal(t, hardwarev1.ControlModeReason_CONTROL_MODE_REASON_ACTUATION_DISABLED, st.GetControlModeReason())
	assert.True(t, st.GetPowerFault())
	assert.Equal(t, uint32(1), st.GetRecoveryBootsLastHour())

	require.Len(t, st.GetRadios(), 2)
	halow, wifi := st.GetRadios()[0], st.GetRadios()[1]
	assert.Equal(t, hardwarev1.Radio_RADIO_HALOW, halow.GetRadio())
	assert.Equal(t, hardwarev1.RadioState_RADIO_STATE_UP, halow.GetState())
	assert.Equal(t, hardwarev1.StateReason_STATE_REASON_STARTUP, halow.GetReason())
	assert.Equal(t, "wlan0", halow.GetNetdev())
	assert.True(t, halow.GetPowerRequested())
	assert.Nil(t, halow.GetNextActionAt(), "zero time maps to an unset timestamp")
	assert.Equal(t, hardwarev1.RadioState_RADIO_STATE_FAULT, wifi.GetState())
	assert.Equal(t, hardwarev1.RecoveryStep_RECOVERY_STEP_RESET_PCIE_FUNCTION, wifi.GetCurrentStep())
	assert.True(t, wifi.GetRfDisabled())
	assert.Equal(t, uint64(2), wifi.GetRecoveriesTotal())
	assert.NotNil(t, wifi.GetNextActionAt())

	require.Len(t, st.GetFaultLines(), 8)
	assert.Equal(t, hardwarev1.FaultLine_FAULT_LINE_POWER_GOOD, st.GetFaultLines()[2].GetLine())
	assert.Equal(t, "POWER_GOOD", st.GetFaultLines()[2].GetName())
	assert.Equal(t, hardwarev1.LineState_LINE_STATE_ASSERTED, st.GetFaultLines()[2].GetState())
	assert.Equal(t, uint64(4), st.GetFaultLines()[2].GetEdgesTotal())
	assert.Equal(t, hardwarev1.LineState_LINE_STATE_UNAVAILABLE, st.GetFaultLines()[0].GetState())

	b := st.GetBattery()
	assert.True(t, b.GetPresent())
	assert.False(t, b.GetStale())
	assert.Equal(t, int32(11800), b.GetBusVoltageMv())
	assert.Equal(t, int32(900), b.GetCurrentMa())
	assert.Equal(t, int32(10620), b.GetPowerMw())
	assert.Equal(t, int32(11854), b.GetCompensatedVoltageMv())
	require.NotNil(t, b.EnergyMwh)
	assert.InDelta(t, 12.5, b.GetEnergyMwh(), 1e-9)
	assert.Nil(t, b.MonitorTempC, "temperature unset when the driver lacks it")
	assert.InDelta(t, 3.25, b.GetChargeMah(), 1e-9)
	assert.Equal(t, uint32(1), b.GetAlarms())
	assert.Equal(t, uint32(3), b.GetSeriesCells())
	assert.Equal(t, hardwarev1.BatteryLevel_BATTERY_LEVEL_NORMAL, b.GetLevel())
	assert.Equal(t, hardwarev1.BatteryAbsentReason_BATTERY_ABSENT_REASON_NONE, b.GetAbsentReason())

	w := st.GetWatchdog()
	assert.True(t, w.GetEnabled())
	assert.False(t, w.GetArmed())
	assert.Equal(t, "/dev/watchdog1", w.GetDevice())
	assert.Equal(t, uint64(7), w.GetPetsTotal())
}

func TestHardwareService_nilManager(t *testing.T) {
	svc := newHardwareService(nil)
	ctx := context.Background()

	_, err := svc.GetHardwareStatus(ctx, &emptypb.Empty{})
	assertConnectCode(t, err, connect.CodeFailedPrecondition)

	_, err = svc.ListRecoveryEvents(ctx, &hardwarev1.ListRecoveryEventsRequest{})
	assertConnectCode(t, err, connect.CodeFailedPrecondition)

	_, err = svc.SetRadioPower(ctx, &hardwarev1.SetRadioPowerRequest{Radio: hardwarev1.Radio_RADIO_HALOW})
	assertConnectCode(t, err, connect.CodeFailedPrecondition)

	_, err = svc.SetRadioRfDisable(ctx, &hardwarev1.SetRadioRfDisableRequest{Radio: hardwarev1.Radio_RADIO_WIFI})
	assertConnectCode(t, err, connect.CodeFailedPrecondition)

	_, err = svc.ExecuteRadioRecovery(ctx, &hardwarev1.ExecuteRadioRecoveryRequest{Radio: hardwarev1.Radio_RADIO_WIFI})
	assertConnectCode(t, err, connect.CodeFailedPrecondition)

	_, err = svc.ExecuteGnssReset(ctx, &hardwarev1.ExecuteGnssResetRequest{})
	assertConnectCode(t, err, connect.CodeFailedPrecondition)
}

func assertConnectCode(t *testing.T, err error, code connect.Code) {
	t.Helper()

	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, code, connectErr.Code())
}

func TestHardwareService_controlPassesRadioAndCaller(t *testing.T) {
	fake := &fakeHardwareManager{gnssAt: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	svc := newHardwareService(fake)
	ctx := auth.ContextWithUsername(context.Background(), "root")

	resp, err := svc.SetRadioPower(ctx, &hardwarev1.SetRadioPowerRequest{Radio: hardwarev1.Radio_RADIO_WIFI, Enabled: true})
	require.NoError(t, err)
	assert.Equal(t, hardwarev1.Radio_RADIO_WIFI, resp.GetRadio().GetRadio())

	r, on, caller, _ := fake.last()
	assert.Equal(t, hardware.RadioWiFi, r)
	assert.True(t, on)
	assert.Equal(t, "root", caller)

	_, err = svc.SetRadioRfDisable(ctx, &hardwarev1.SetRadioRfDisableRequest{Radio: hardwarev1.Radio_RADIO_WIFI, Disabled: true})
	require.NoError(t, err)

	_, err = svc.ExecuteRadioRecovery(ctx, &hardwarev1.ExecuteRadioRecoveryRequest{Radio: hardwarev1.Radio_RADIO_HALOW})
	require.NoError(t, err)

	r, _, _, _ = fake.last()
	assert.Equal(t, hardware.RadioHaLow, r)

	gnss, err := svc.ExecuteGnssReset(ctx, &hardwarev1.ExecuteGnssResetRequest{})
	require.NoError(t, err)
	assert.True(t, gnss.GetRequestedAt().AsTime().Equal(fake.gnssAt))

	power, rf, recov, g := fake.calls()
	assert.Equal(t, [4]int{1, 1, 1, 1}, [4]int{power, rf, recov, g})
}

func TestHardwareService_requireSession(t *testing.T) {
	fake := &fakeHardwareManager{}
	svc := newHardwareService(fake)
	svc.RequireSession = true
	ctx := context.Background()

	_, err := svc.SetRadioPower(ctx, &hardwarev1.SetRadioPowerRequest{Radio: hardwarev1.Radio_RADIO_HALOW})
	assertConnectCode(t, err, connect.CodeUnauthenticated)

	_, err = svc.SetRadioRfDisable(ctx, &hardwarev1.SetRadioRfDisableRequest{Radio: hardwarev1.Radio_RADIO_WIFI})
	assertConnectCode(t, err, connect.CodeUnauthenticated)

	_, err = svc.ExecuteRadioRecovery(ctx, &hardwarev1.ExecuteRadioRecoveryRequest{Radio: hardwarev1.Radio_RADIO_HALOW})
	assertConnectCode(t, err, connect.CodeUnauthenticated)

	_, err = svc.ExecuteGnssReset(ctx, &hardwarev1.ExecuteGnssResetRequest{})
	assertConnectCode(t, err, connect.CodeUnauthenticated)

	power, rf, recov, g := fake.calls()
	assert.Zero(t, power+rf+recov+g, "no control reached the manager")

	// Reads stay available (the auth middleware gates them).
	_, err = svc.GetHardwareStatus(ctx, &emptypb.Empty{})
	require.NoError(t, err)

	_, err = svc.SetRadioPower(auth.ContextWithUsername(ctx, "admin"), &hardwarev1.SetRadioPowerRequest{Radio: hardwarev1.Radio_RADIO_HALOW})
	require.NoError(t, err)
}

func TestHardwareService_errorMapping(t *testing.T) {
	tests := []struct {
		err  error
		code connect.Code
	}{
		{err: hardware.ErrTelemetryOnly, code: connect.CodeFailedPrecondition},
		{err: hardware.ErrRadioUnmanaged, code: connect.CodeFailedPrecondition},
		{err: hardware.ErrRadioFailed, code: connect.CodeFailedPrecondition},
		{err: hardware.ErrRadioOff, code: connect.CodeFailedPrecondition},
		{err: hardware.ErrNoRFDisable, code: connect.CodeFailedPrecondition},
		{err: fmt.Errorf("wrapped: %w", hardware.ErrUnknownRadio), code: connect.CodeInvalidArgument},
		{err: errors.New("boom"), code: connect.CodeInternal},
	}

	for _, tc := range tests {
		t.Run(tc.err.Error(), func(t *testing.T) {
			fake := &fakeHardwareManager{controlErr: tc.err}
			svc := newHardwareService(fake)
			ctx := context.Background()

			_, err := svc.SetRadioPower(ctx, &hardwarev1.SetRadioPowerRequest{Radio: hardwarev1.Radio_RADIO_HALOW})
			assertConnectCode(t, err, tc.code)

			_, err = svc.SetRadioRfDisable(ctx, &hardwarev1.SetRadioRfDisableRequest{Radio: hardwarev1.Radio_RADIO_WIFI})
			assertConnectCode(t, err, tc.code)

			_, err = svc.ExecuteRadioRecovery(ctx, &hardwarev1.ExecuteRadioRecoveryRequest{Radio: hardwarev1.Radio_RADIO_WIFI})
			assertConnectCode(t, err, tc.code)

			_, err = svc.ExecuteGnssReset(ctx, &hardwarev1.ExecuteGnssResetRequest{})
			assertConnectCode(t, err, tc.code)
		})
	}
}

func TestHardwareService_ListRecoveryEvents(t *testing.T) {
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	fake := &fakeHardwareManager{events: []hardware.Event{
		{
			At: at, Radio: hardware.RadioWiFi, Step: hardware.StepPowerCycle, Attempt: 2,
			Trigger: hardware.ReasonHealthCheck, Result: hardware.ResultFailed, DurationMs: 150,
			Detail: "no netdev", Caller: "",
		},
		{
			At: at, Radio: hardware.RadioUnspecified, Step: hardware.StepGNSSReset, Attempt: 1,
			Trigger: hardware.ReasonUser, Result: hardware.ResultSucceeded, Caller: "root",
		},
	}}
	svc := newHardwareService(fake)

	resp, err := svc.ListRecoveryEvents(context.Background(), &hardwarev1.ListRecoveryEventsRequest{
		Limit: 10, Radio: hardwarev1.Radio_RADIO_WIFI,
	})
	require.NoError(t, err)

	r, _, _, limit := fake.last()
	assert.Equal(t, 10, limit)
	assert.Equal(t, hardware.RadioWiFi, r)

	require.Len(t, resp.GetEvents(), 2)
	e := resp.GetEvents()[0]
	assert.Equal(t, hardwarev1.Radio_RADIO_WIFI, e.GetRadio())
	assert.Equal(t, hardwarev1.RecoveryStep_RECOVERY_STEP_POWER_CYCLE, e.GetStep())
	assert.Equal(t, uint32(2), e.GetAttempt())
	assert.Equal(t, hardwarev1.StateReason_STATE_REASON_HEALTH_CHECK, e.GetTrigger())
	assert.Equal(t, hardwarev1.RecoveryResult_RECOVERY_RESULT_FAILED, e.GetResult())
	assert.Equal(t, uint32(150), e.GetDurationMs())
	assert.Equal(t, "no netdev", e.GetDetail())
	assert.True(t, e.GetOccurredAt().AsTime().Equal(at))
	assert.Equal(t, "root", resp.GetEvents()[1].GetCaller())
	assert.Equal(t, hardwarev1.RecoveryStep_RECOVERY_STEP_GNSS_RESET, resp.GetEvents()[1].GetStep())

	// Unspecified radio means all radios.
	_, err = svc.ListRecoveryEvents(context.Background(), &hardwarev1.ListRecoveryEventsRequest{})
	require.NoError(t, err)

	r, _, _, limit = fake.last()
	assert.Equal(t, hardware.RadioUnspecified, r)
	assert.Zero(t, limit)
}

// TestHardwareEnumNumbering pins the manager enums to the proto numbering
// the handler relies on when it casts between them.
func TestHardwareEnumNumbering(t *testing.T) {
	type enumCase struct {
		name   func(i int32) string
		proto  map[int32]string
		prefix string
	}

	cases := []enumCase{
		{prefix: "RADIO_", proto: hardwarev1.Radio_name, name: func(i int32) string { return hardware.Radio(i).String() }},
		{prefix: "RADIO_STATE_", proto: hardwarev1.RadioState_name, name: func(i int32) string { return hardware.State(i).String() }},
		{prefix: "STATE_REASON_", proto: hardwarev1.StateReason_name, name: func(i int32) string { return hardware.Reason(i).String() }},
		{prefix: "RECOVERY_STEP_", proto: hardwarev1.RecoveryStep_name, name: func(i int32) string { return hardware.Step(i).String() }},
		{prefix: "RECOVERY_RESULT_", proto: hardwarev1.RecoveryResult_name, name: func(i int32) string { return hardware.Result(i).String() }},
		{prefix: "CONTROL_MODE_", proto: hardwarev1.ControlMode_name, name: func(i int32) string { return hardware.ControlMode(i).String() }},
		{prefix: "CONTROL_MODE_REASON_", proto: hardwarev1.ControlModeReason_name, name: func(i int32) string { return hardware.ControlModeReason(i).String() }},
		{prefix: "BATTERY_LEVEL_", proto: hardwarev1.BatteryLevel_name, name: func(i int32) string { return hardware.BatteryLevel(i).String() }},
	}

	for _, tc := range cases {
		t.Run(tc.prefix, func(t *testing.T) {
			for i := int32(1); i < int32(len(tc.proto)); i++ {
				want := strings.ToLower(strings.TrimPrefix(tc.proto[i], tc.prefix))
				assert.Equal(t, want, tc.name(i), "value %d", i)
			}
		})
	}

	assert.Equal(t, int32(hardwarev1.FaultLine_FAULT_LINE_HALOW_FAULT), int32(hardware.FaultLineHaLowFault))
	assert.Equal(t, int32(hardwarev1.FaultLine_FAULT_LINE_WIFI_FAULT), int32(hardware.FaultLineWiFiFault))
	assert.Equal(t, int32(hardwarev1.FaultLine_FAULT_LINE_POWER_GOOD), int32(hardware.FaultLinePowerGood))
	assert.Equal(t, int32(hardwarev1.FaultLine_FAULT_LINE_EFUSE_FAULT), int32(hardware.FaultLineEFuseFault))
	assert.Equal(t, int32(hardwarev1.FaultLine_FAULT_LINE_HALOW_USB_FAULT), int32(hardware.FaultLineHaLowUSBFault))
	assert.Equal(t, int32(hardwarev1.FaultLine_FAULT_LINE_VLM_USB_FAULT), int32(hardware.FaultLineVLMUSBFault))
	assert.Equal(t, int32(hardwarev1.FaultLine_FAULT_LINE_INA228_ALERT), int32(hardware.FaultLineINA228Alert))
	assert.Equal(t, int32(hardwarev1.FaultLine_FAULT_LINE_SUPERVISOR_WDO), int32(hardware.FaultLineSupervisorWDO))
	assert.Len(t, hardwarev1.FaultLine_name, 9)

	assert.Equal(t, int32(hardwarev1.LineState_LINE_STATE_UNAVAILABLE), int32(hardware.LineStateUnavailable))
	assert.Equal(t, int32(hardwarev1.LineState_LINE_STATE_NORMAL), int32(hardware.LineStateNormal))
	assert.Equal(t, int32(hardwarev1.LineState_LINE_STATE_ASSERTED), int32(hardware.LineStateAsserted))

	assert.Equal(t, int32(hardwarev1.BatteryAbsentReason_BATTERY_ABSENT_REASON_NONE), int32(hardware.AbsentReasonNone))
	assert.Equal(t, int32(hardwarev1.BatteryAbsentReason_BATTERY_ABSENT_REASON_NO_MONITOR), int32(hardware.AbsentReasonNoMonitor))
	assert.Equal(t, int32(hardwarev1.BatteryAbsentReason_BATTERY_ABSENT_REASON_MONITOR_UNREACHABLE), int32(hardware.AbsentReasonMonitorUnreachable))
}

func newHardwareTestServer(t *testing.T, svc *handlers.HardwareService) hardwarev1connect.HardwareServiceClient {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle(hardwarev1connect.NewHardwareServiceHandler(svc, connect.WithInterceptors(validate.NewInterceptor())))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return hardwarev1connect.NewHardwareServiceClient(http.DefaultClient, srv.URL, connect.WithGRPCWeb())
}

func TestHardwareService_StreamHardwareStatus(t *testing.T) {
	fake := &fakeHardwareManager{status: sampleHardwareStatus()}
	svc := newHardwareService(fake)
	svc.StreamInterval = time.Millisecond
	client := newHardwareTestServer(t, svc)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	stream, err := client.StreamHardwareStatus(ctx, &hardwarev1.StreamHardwareStatusRequest{})
	require.NoError(t, err)

	// The current status arrives immediately, before any change.
	require.True(t, stream.Receive(), "first message: %v", stream.Err())
	assert.Equal(t, hardwarev1.RadioState_RADIO_STATE_UP, stream.Msg().GetStatus().GetRadios()[0].GetState())

	// A change is delivered after the rate-limit interval.
	next := sampleHardwareStatus()
	next.Radios[0].State = hardware.StateOff
	fake.setStatus(next)

	require.True(t, stream.Receive(), "second message: %v", stream.Err())
	assert.Equal(t, hardwarev1.RadioState_RADIO_STATE_OFF, stream.Msg().GetStatus().GetRadios()[0].GetState())

	require.NoError(t, stream.Close())
	cancel()
	assert.Equal(t, 1, fake.subscriberCount())
	assert.Eventually(t, func() bool { return fake.getCancels() == 1 }, 2*time.Second, 5*time.Millisecond,
		"the handler unsubscribes when the client goes away")
}

func TestHardwareService_StreamHardwareStatus_subscribeRefused(t *testing.T) {
	fake := &fakeHardwareManager{subscribeErr: hardware.ErrTooManySubscribers}
	client := newHardwareTestServer(t, newHardwareService(fake))

	stream, err := client.StreamHardwareStatus(context.Background(), &hardwarev1.StreamHardwareStatusRequest{})
	if err == nil {
		assert.False(t, stream.Receive())
		err = stream.Err()
	}

	assertConnectCode(t, err, connect.CodeResourceExhausted)
}

func TestHardwareService_validation(t *testing.T) {
	fake := &fakeHardwareManager{}
	client := newHardwareTestServer(t, newHardwareService(fake))
	ctx := context.Background()

	_, err := client.SetRadioPower(ctx, &hardwarev1.SetRadioPowerRequest{})
	assertConnectCode(t, err, connect.CodeInvalidArgument)

	_, err = client.SetRadioPower(ctx, &hardwarev1.SetRadioPowerRequest{Radio: hardwarev1.Radio(9)})
	assertConnectCode(t, err, connect.CodeInvalidArgument)

	_, err = client.SetRadioRfDisable(ctx, &hardwarev1.SetRadioRfDisableRequest{Radio: hardwarev1.Radio_RADIO_HALOW})
	assertConnectCode(t, err, connect.CodeInvalidArgument)

	_, err = client.ExecuteRadioRecovery(ctx, &hardwarev1.ExecuteRadioRecoveryRequest{})
	assertConnectCode(t, err, connect.CodeInvalidArgument)

	_, err = client.ListRecoveryEvents(ctx, &hardwarev1.ListRecoveryEventsRequest{Limit: 65})
	assertConnectCode(t, err, connect.CodeInvalidArgument)

	_, err = client.ListRecoveryEvents(ctx, &hardwarev1.ListRecoveryEventsRequest{Limit: 64})
	require.NoError(t, err)

	power, rf, recov, _ := fake.calls()
	assert.Zero(t, power+rf+recov, "invalid requests never reach the manager")
}

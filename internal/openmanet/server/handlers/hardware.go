package handlers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	hardwarev1 "github.com/openmanet/openmanetd/internal/api/openmanet/hardware/v1"
	"github.com/openmanet/openmanetd/internal/auth"
	"github.com/openmanet/openmanetd/internal/hardware"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// DefaultHardwareStreamInterval is the minimum gap between two
// StreamHardwareStatus messages (the design's "at most 1 Hz").
const DefaultHardwareStreamInterval = time.Second

// errHardwareUnavailable is returned when the daemon runs without a
// hardware manager (hardware.enable: false).
var errHardwareUnavailable = errors.New("hardware manager is not running")

// errSessionRequired is returned by control RPCs called without a session
// while authentication is enabled.
var errSessionRequired = errors.New("a signed-in session is required for hardware control")

// HardwareManager is the subset of *hardware.Manager the handler uses.
type HardwareManager interface {
	Status() hardware.Status
	Events(limit int, r hardware.Radio) []hardware.Event
	Subscribe() (<-chan struct{}, func(), error)
	SetRadioPower(r hardware.Radio, on bool, caller string) (hardware.RadioStatus, error)
	SetRFDisable(r hardware.Radio, disabled bool, caller string) (hardware.RadioStatus, error)
	Recover(r hardware.Radio, caller string) (hardware.RadioStatus, error)
	ResetGNSS(caller string) (time.Time, error)
}

// HardwareService implements hardwarev1connect.HardwareServiceHandler.
type HardwareService struct {
	Log zerolog.Logger
	// Manager is nil when the hardware manager is disabled in config.
	Manager HardwareManager
	// StreamInterval overrides DefaultHardwareStreamInterval when > 0.
	StreamInterval time.Duration
	// RequireSession refuses control RPCs that carry no authenticated
	// username (set when auth is enabled).
	RequireSession bool
}

// GetHardwareStatus returns the current hardware manager view.
func (s *HardwareService) GetHardwareStatus(_ context.Context, _ *emptypb.Empty) (*hardwarev1.GetHardwareStatusResponse, error) {
	if s.Manager == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errHardwareUnavailable)
	}

	return &hardwarev1.GetHardwareStatusResponse{Status: hardwareStatusToProto(s.Manager.Status())}, nil
}

// StreamHardwareStatus sends the current status, then again whenever it
// changes, no more often than StreamInterval.
func (s *HardwareService) StreamHardwareStatus(
	ctx context.Context,
	_ *hardwarev1.StreamHardwareStatusRequest,
	stream *connect.ServerStream[hardwarev1.StreamHardwareStatusResponse],
) error {
	if s.Manager == nil {
		return connect.NewError(connect.CodeFailedPrecondition, errHardwareUnavailable)
	}

	changed, cancel, err := s.Manager.Subscribe()
	if err != nil {
		s.Log.Warn().Err(err).Msg("Hardware status stream refused")

		return mapHardwareError(err)
	}
	defer cancel()

	interval := s.StreamInterval
	if interval <= 0 {
		interval = DefaultHardwareStreamInterval
	}

	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		if err := stream.Send(&hardwarev1.StreamHardwareStatusResponse{
			Status: hardwareStatusToProto(s.Manager.Status()),
		}); err != nil {
			return fmt.Errorf("send hardware status: %w", err)
		}

		// Rate limit: hold the next send for interval. Notifications that
		// arrive meanwhile coalesce in the capacity-1 channel.
		timer.Reset(interval)

		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}

		select {
		case <-ctx.Done():
			return nil
		case <-changed:
		}
	}
}

// SetRadioPower requests a radio rail on or off through its state machine.
func (s *HardwareService) SetRadioPower(ctx context.Context, req *hardwarev1.SetRadioPowerRequest) (*hardwarev1.SetRadioPowerResponse, error) {
	caller, err := s.control(ctx)
	if err != nil {
		return nil, err
	}

	r := radioFromProto(req.GetRadio())

	st, err := s.Manager.SetRadioPower(r, req.GetEnabled(), caller)
	if err != nil {
		s.Log.Warn().Err(err).Str("radio", r.String()).Str("caller", caller).Bool("enabled", req.GetEnabled()).Msg("SetRadioPower refused")

		return nil, mapHardwareError(err)
	}

	s.Log.Info().Str("radio", r.String()).Str("caller", caller).Bool("enabled", req.GetEnabled()).Msg("Radio power requested")

	return &hardwarev1.SetRadioPowerResponse{Radio: radioStatusToProto(&st)}, nil
}

// SetRadioRfDisable asserts or releases the Wi-Fi hardware RF disable.
func (s *HardwareService) SetRadioRfDisable(ctx context.Context, req *hardwarev1.SetRadioRfDisableRequest) (*hardwarev1.SetRadioRfDisableResponse, error) {
	caller, err := s.control(ctx)
	if err != nil {
		return nil, err
	}

	r := radioFromProto(req.GetRadio())

	st, err := s.Manager.SetRFDisable(r, req.GetDisabled(), caller)
	if err != nil {
		s.Log.Warn().Err(err).Str("radio", r.String()).Str("caller", caller).Bool("disabled", req.GetDisabled()).Msg("SetRadioRfDisable refused")

		return nil, mapHardwareError(err)
	}

	s.Log.Info().Str("radio", r.String()).Str("caller", caller).Bool("disabled", req.GetDisabled()).Msg("Radio RF disable requested")

	return &hardwarev1.SetRadioRfDisableResponse{Radio: radioStatusToProto(&st)}, nil
}

// ExecuteRadioRecovery restarts a radio's recovery ladder from the start.
func (s *HardwareService) ExecuteRadioRecovery(ctx context.Context, req *hardwarev1.ExecuteRadioRecoveryRequest) (*hardwarev1.ExecuteRadioRecoveryResponse, error) {
	caller, err := s.control(ctx)
	if err != nil {
		return nil, err
	}

	r := radioFromProto(req.GetRadio())

	st, err := s.Manager.Recover(r, caller)
	if err != nil {
		s.Log.Warn().Err(err).Str("radio", r.String()).Str("caller", caller).Msg("ExecuteRadioRecovery refused")

		return nil, mapHardwareError(err)
	}

	s.Log.Info().Str("radio", r.String()).Str("caller", caller).Msg("Radio recovery requested")

	return &hardwarev1.ExecuteRadioRecoveryResponse{Radio: radioStatusToProto(&st)}, nil
}

// ListRecoveryEvents returns the newest recovery events, newest first.
func (s *HardwareService) ListRecoveryEvents(_ context.Context, req *hardwarev1.ListRecoveryEventsRequest) (*hardwarev1.ListRecoveryEventsResponse, error) {
	if s.Manager == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errHardwareUnavailable)
	}

	events := s.Manager.Events(int(req.GetLimit()), radioFromProto(req.GetRadio()))

	out := make([]*hardwarev1.RecoveryEvent, 0, len(events))
	for i := range events {
		out = append(out, recoveryEventToProto(&events[i]))
	}

	return &hardwarev1.ListRecoveryEventsResponse{Events: out}, nil
}

// ExecuteGnssReset pulses GNSS_RESET_N.
func (s *HardwareService) ExecuteGnssReset(ctx context.Context, _ *hardwarev1.ExecuteGnssResetRequest) (*hardwarev1.ExecuteGnssResetResponse, error) {
	caller, err := s.control(ctx)
	if err != nil {
		return nil, err
	}

	at, err := s.Manager.ResetGNSS(caller)
	if err != nil {
		s.Log.Warn().Err(err).Str("caller", caller).Msg("ExecuteGnssReset refused")

		return nil, mapHardwareError(err)
	}

	s.Log.Info().Str("caller", caller).Msg("GNSS reset requested")

	return &hardwarev1.ExecuteGnssResetResponse{RequestedAt: timestamppb.New(at)}, nil
}

// control checks the preconditions shared by every control RPC and returns
// the caller's username for the audit trail.
func (s *HardwareService) control(ctx context.Context) (string, error) {
	if s.Manager == nil {
		return "", connect.NewError(connect.CodeFailedPrecondition, errHardwareUnavailable)
	}

	caller := auth.UsernameFromContext(ctx)
	if s.RequireSession && caller == "" {
		return "", connect.NewError(connect.CodeUnauthenticated, errSessionRequired)
	}

	return caller, nil
}

// mapHardwareError converts a hardware manager error to a connect error.
func mapHardwareError(err error) error {
	switch {
	case errors.Is(err, hardware.ErrUnknownRadio):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, hardware.ErrTooManySubscribers):
		return connect.NewError(connect.CodeResourceExhausted, err)
	case errors.Is(err, hardware.ErrTelemetryOnly),
		errors.Is(err, hardware.ErrRadioUnmanaged),
		errors.Is(err, hardware.ErrRadioFailed),
		errors.Is(err, hardware.ErrRadioOff),
		errors.Is(err, hardware.ErrNoRFDisable):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

// radioFromProto maps the wire radio to the manager's. Unknown values map
// to RadioUnspecified, which the manager rejects as ErrUnknownRadio (and
// which ListRecoveryEvents treats as "all radios").
func radioFromProto(r hardwarev1.Radio) hardware.Radio {
	switch r {
	case hardwarev1.Radio_RADIO_HALOW:
		return hardware.RadioHaLow
	case hardwarev1.Radio_RADIO_WIFI:
		return hardware.RadioWiFi
	default:
		return hardware.RadioUnspecified
	}
}

// optionalTimestamp returns nil for the zero time.
func optionalTimestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}

	return timestamppb.New(t)
}

// hardwareStatusToProto converts the manager status. Enum values share the
// proto numbering (pinned by TestHardwareEnumNumbering).
func hardwareStatusToProto(st hardware.Status) *hardwarev1.HardwareStatus {
	out := &hardwarev1.HardwareStatus{
		GeneratedAt:           optionalTimestamp(st.GeneratedAt),
		BoardDetected:         st.BoardDetected,
		ControlMode:           hardwarev1.ControlMode(st.ControlMode),
		ControlModeReason:     hardwarev1.ControlModeReason(st.ControlModeReason),
		Radios:                make([]*hardwarev1.RadioStatus, 0, len(st.Radios)),
		FaultLines:            make([]*hardwarev1.FaultLineStatus, 0, len(st.FaultLines)),
		PowerFault:            st.PowerFault,
		Battery:               batteryToProto(&st.Battery),
		RecoveryBootsLastHour: st.RecoveryBootsLastHour,
		ShutdownPending:       st.ShutdownPending,
		Watchdog: &hardwarev1.WatchdogStatus{
			Enabled:            st.Watchdog.Enabled,
			Armed:              st.Watchdog.Armed,
			Device:             st.Watchdog.Device,
			PetsTotal:          st.Watchdog.PetsTotal,
			LastPetAt:          optionalTimestamp(st.Watchdog.LastPetAt),
			HostResetRequested: st.Watchdog.HostResetRequested,
		},
	}

	for i := range st.Radios {
		out.Radios = append(out.Radios, radioStatusToProto(&st.Radios[i]))
	}

	for i := range st.FaultLines {
		fl := &st.FaultLines[i]
		out.FaultLines = append(out.FaultLines, &hardwarev1.FaultLineStatus{
			Line:         hardwarev1.FaultLine(fl.Line),
			Name:         fl.Name,
			State:        hardwarev1.LineState(fl.State),
			EdgesTotal:   fl.EdgesTotal,
			LastChangeAt: optionalTimestamp(fl.LastChangeAt),
		})
	}

	return out
}

func radioStatusToProto(r *hardware.RadioStatus) *hardwarev1.RadioStatus {
	return &hardwarev1.RadioStatus{
		Radio:            hardwarev1.Radio(r.Radio),
		State:            hardwarev1.RadioState(r.State),
		Reason:           hardwarev1.StateReason(r.Reason),
		StateSince:       optionalTimestamp(r.StateSince),
		PowerRequested:   r.PowerRequested,
		RfDisabled:       r.RFDisabled,
		DevicePresent:    r.DevicePresent,
		NetdevPresent:    r.NetdevPresent,
		Netdev:           r.Netdev,
		CurrentStep:      hardwarev1.RecoveryStep(r.CurrentStep),
		StepAttempt:      r.StepAttempt,
		NextActionAt:     optionalTimestamp(r.NextActionAt),
		RecoveriesTotal:  r.RecoveriesTotal,
		PowerCyclesTotal: r.PowerCyclesTotal,
		FailedTotal:      r.FailedTotal,
	}
}

func batteryToProto(b *hardware.BatteryStatus) *hardwarev1.BatteryTelemetry {
	out := &hardwarev1.BatteryTelemetry{
		Present:              b.Present,
		AbsentReason:         hardwarev1.BatteryAbsentReason(b.AbsentReason),
		Stale:                b.Stale,
		SampledAt:            optionalTimestamp(b.SampledAt),
		BusVoltageMv:         b.BusMV,
		CurrentMa:            b.CurrentMA,
		PowerMw:              b.PowerMW,
		ChargeMah:            b.ChargeMAh,
		Alarms:               b.Alarms,
		CompensatedVoltageMv: b.CompensatedMV,
		Level:                hardwarev1.BatteryLevel(b.Level),
		SeriesCells:          b.SeriesCells,
	}

	if b.HasEnergy {
		v := b.EnergyMWh
		out.EnergyMwh = &v
	}

	if b.HasTemp {
		v := b.TempC
		out.MonitorTempC = &v
	}

	return out
}

func recoveryEventToProto(e *hardware.Event) *hardwarev1.RecoveryEvent {
	return &hardwarev1.RecoveryEvent{
		OccurredAt: optionalTimestamp(e.At),
		Radio:      hardwarev1.Radio(e.Radio),
		Step:       hardwarev1.RecoveryStep(e.Step),
		Attempt:    e.Attempt,
		Trigger:    hardwarev1.StateReason(e.Trigger),
		Result:     hardwarev1.RecoveryResult(e.Result),
		DurationMs: e.DurationMs,
		Detail:     e.Detail,
		Caller:     e.Caller,
	}
}

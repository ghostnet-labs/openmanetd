// Package hardware is the V1 carrier hardware manager ("hwmgr"): battery
// telemetry with stale-data handling, per-radio power state machines, the
// bounded fault-recovery ladder, the boot-loop guard and the supervisor
// watchdog service. The design lives in the docs repository at
// project/software/v1-hw-telemetry-recovery.md; the pin map in
// project/hardware/v1-pinout-and-sequencing.md is authoritative for every
// GPIO name used here.
//
// Nothing in this package drives a GPIO unless the board is detected as
// ghostnet,v1, every required line name resolves, and the operator set
// hardware.actuationEnable. The V1 pin mapping is not yet verified on
// hardware (GHO-9, GHO-21), so the default is telemetry-only.
//
// Enum values in this package use the same numbering as the matching
// openmanet.hardware.v1 protobuf enums so the API handler can convert by
// value; TestEnumNumbering pins that contract.
package hardware

import (
	"errors"
	"time"
)

// Names shared by every enum String method.
const (
	nameUnspecified = "unspecified"
	nameUnknown     = "unknown"
)

// Radio identifies a switchable mesh radio.
type Radio uint8

// Radio values.
const (
	RadioUnspecified Radio = iota
	RadioHaLow
	RadioWiFi
)

// radioCount is the number of switchable radios.
const radioCount = 2

// Radios lists every switchable radio in status order.
func Radios() [radioCount]Radio {
	return [radioCount]Radio{RadioHaLow, RadioWiFi}
}

// String returns the lowercase radio name used in logs and snapshots.
func (r Radio) String() string {
	switch r {
	case RadioHaLow:
		return "halow"
	case RadioWiFi:
		return "wifi"
	case RadioUnspecified:
		return nameUnspecified
	}

	return nameUnknown
}

// valid reports whether r names a switchable radio.
func (r Radio) valid() bool {
	return r == RadioHaLow || r == RadioWiFi
}

// index returns r's position in per-radio arrays. r must be valid.
func (r Radio) index() int {
	return int(r) - 1
}

// State is a radio power state machine state.
type State uint8

// State values (numbering matches hardwarev1.RadioState).
const (
	StateUnspecified State = iota
	StateUnmanaged
	StateOff
	StatePowering
	StateEnumerating
	StateUp
	StateFault
	StateCooldown
	StateFailed
)

// String returns the lowercase state name used in logs and snapshots.
func (s State) String() string {
	switch s {
	case StateUnmanaged:
		return "unmanaged"
	case StateOff:
		return "off"
	case StatePowering:
		return "powering"
	case StateEnumerating:
		return "enumerating"
	case StateUp:
		return "up"
	case StateFault:
		return "fault"
	case StateCooldown:
		return "cooldown"
	case StateFailed:
		return "failed"
	case StateUnspecified:
		return nameUnspecified
	}

	return nameUnknown
}

// railOn reports whether the radio's enable is high in state s.
func (s State) railOn() bool {
	switch s {
	case StatePowering, StateEnumerating, StateUp, StateFault:
		return true
	case StateUnspecified, StateUnmanaged, StateOff, StateCooldown, StateFailed:
		return false
	}

	return false
}

// Reason explains a state entry or triggers a recovery action.
type Reason uint8

// Reason values (numbering matches hardwarev1.StateReason).
const (
	ReasonUnspecified Reason = iota
	ReasonNone
	ReasonUser
	ReasonLowBattery
	ReasonEnumerationTimeout
	ReasonHealthCheck
	ReasonFaultLine
	ReasonPower
	ReasonManualRecovery
	ReasonLadderExhausted
	ReasonStartup
	ReasonControlDisabled
)

// String returns the lowercase reason name used in logs.
func (r Reason) String() string {
	switch r {
	case ReasonNone:
		return "none"
	case ReasonUser:
		return "user"
	case ReasonLowBattery:
		return "low_battery"
	case ReasonEnumerationTimeout:
		return "enumeration_timeout"
	case ReasonHealthCheck:
		return "health_check"
	case ReasonFaultLine:
		return "fault_line"
	case ReasonPower:
		return "power"
	case ReasonManualRecovery:
		return "manual_recovery"
	case ReasonLadderExhausted:
		return "ladder_exhausted"
	case ReasonStartup:
		return "startup"
	case ReasonControlDisabled:
		return "control_disabled"
	case ReasonUnspecified:
		return nameUnspecified
	}

	return nameUnknown
}

// Step is a recovery ladder step or another audited hardware action.
type Step uint8

// Step values (numbering matches hardwarev1.RecoveryStep).
const (
	StepUnspecified Step = iota
	StepResetUSBDevice
	StepResetPCIeFunction
	StepResetHub
	StepPowerCycle
	StepHostReset
	StepGNSSReset
	StepPowerOff
	StepShutdown
)

// String returns the lowercase step name used in logs.
func (s Step) String() string {
	switch s {
	case StepResetUSBDevice:
		return "reset_usb_device"
	case StepResetPCIeFunction:
		return "reset_pcie_function"
	case StepResetHub:
		return "reset_hub"
	case StepPowerCycle:
		return "power_cycle"
	case StepHostReset:
		return "host_reset"
	case StepGNSSReset:
		return "gnss_reset"
	case StepPowerOff:
		return "power_off"
	case StepShutdown:
		return "shutdown"
	case StepUnspecified:
		return nameUnspecified
	}

	return nameUnknown
}

// Result is the outcome of a recorded action.
type Result uint8

// Result values (numbering matches hardwarev1.RecoveryResult).
const (
	ResultUnspecified Result = iota
	ResultSucceeded
	ResultFailed
	ResultSkipped
	ResultExhausted
)

// String returns the lowercase result name used in logs.
func (r Result) String() string {
	switch r {
	case ResultSucceeded:
		return "succeeded"
	case ResultFailed:
		return "failed"
	case ResultSkipped:
		return "skipped"
	case ResultExhausted:
		return "exhausted"
	case ResultUnspecified:
		return nameUnspecified
	}

	return nameUnknown
}

// ControlMode says whether the manager may drive hardware lines.
type ControlMode uint8

// ControlMode values (numbering matches hardwarev1.ControlMode).
const (
	ControlModeUnspecified ControlMode = iota
	ControlModeTelemetryOnly
	ControlModeActive
)

// String returns the lowercase mode name used in logs and snapshots.
func (c ControlMode) String() string {
	switch c {
	case ControlModeTelemetryOnly:
		return "telemetry_only"
	case ControlModeActive:
		return "active"
	case ControlModeUnspecified:
		return nameUnspecified
	}

	return nameUnknown
}

// ControlModeReason explains the control mode.
type ControlModeReason uint8

// ControlModeReason values (numbering matches hardwarev1.ControlModeReason).
const (
	ControlReasonUnspecified ControlModeReason = iota
	ControlReasonNone
	ControlReasonActuationDisabled
	ControlReasonBoardNotDetected
	ControlReasonLinesUnresolved
	ControlReasonBootLoopGuard
)

// String returns the lowercase reason name used in logs.
func (c ControlModeReason) String() string {
	switch c {
	case ControlReasonNone:
		return "none"
	case ControlReasonActuationDisabled:
		return "actuation_disabled"
	case ControlReasonBoardNotDetected:
		return "board_not_detected"
	case ControlReasonLinesUnresolved:
		return "lines_unresolved"
	case ControlReasonBootLoopGuard:
		return "boot_loop_guard"
	case ControlReasonUnspecified:
		return nameUnspecified
	}

	return nameUnknown
}

// FaultLine names a hardware status input.
type FaultLine uint8

// FaultLine values (numbering matches hardwarev1.FaultLine).
const (
	FaultLineUnspecified FaultLine = iota
	FaultLineHaLowFault
	FaultLineWiFiFault
	FaultLinePowerGood
	FaultLineEFuseFault
	FaultLineHaLowUSBFault
	FaultLineVLMUSBFault
	FaultLineINA228Alert
	FaultLineSupervisorWDO
)

// faultLineCount is the number of hardware status inputs.
const faultLineCount = 8

// LineState is the observed state of a fault input.
type LineState uint8

// LineState values (numbering matches hardwarev1.LineState).
const (
	LineStateUnspecified LineState = iota
	LineStateUnavailable
	LineStateNormal
	LineStateAsserted
)

// BatteryLevel is the low-battery policy level.
type BatteryLevel uint8

// BatteryLevel values (numbering matches hardwarev1.BatteryLevel).
const (
	BatteryLevelUnspecified BatteryLevel = iota
	BatteryLevelUnknown
	BatteryLevelNormal
	BatteryLevelWarning
	BatteryLevelCritical
	BatteryLevelShutdown
)

// String returns the lowercase level name used in logs.
func (b BatteryLevel) String() string {
	switch b {
	case BatteryLevelUnknown:
		return nameUnknown
	case BatteryLevelNormal:
		return "normal"
	case BatteryLevelWarning:
		return "warning"
	case BatteryLevelCritical:
		return "critical"
	case BatteryLevelShutdown:
		return "shutdown"
	case BatteryLevelUnspecified:
		return nameUnspecified
	}

	return nameUnknown
}

// AbsentReason explains BatteryStatus.Present = false.
type AbsentReason uint8

// AbsentReason values (numbering matches hardwarev1.BatteryAbsentReason).
const (
	AbsentReasonUnspecified AbsentReason = iota
	AbsentReasonNone
	AbsentReasonNoMonitor
	AbsentReasonMonitorUnreachable
)

// Errors returned by the control API. The handler maps every one of them to
// a ConnectRPC code.
var (
	// ErrTelemetryOnly means the manager drives no hardware this boot.
	ErrTelemetryOnly = errors.New("hardware control is disabled: telemetry-only mode")
	// ErrUnknownRadio means the radio value is not a switchable radio.
	ErrUnknownRadio = errors.New("unknown radio")
	// ErrRadioUnmanaged means the radio's bus identity is not configured.
	ErrRadioUnmanaged = errors.New("radio is not managed: bus identity not configured")
	// ErrRadioFailed means the radio exhausted its ladder; recover it first.
	ErrRadioFailed = errors.New("radio is failed: run recovery to clear it")
	// ErrRadioOff means recovery was requested for a radio that is off.
	ErrRadioOff = errors.New("radio is off by request: power it on instead")
	// ErrNoRFDisable means the radio has no hardware RF disable line.
	ErrNoRFDisable = errors.New("radio has no hardware RF disable line")
	// ErrTooManySubscribers means the status subscriber limit is reached.
	ErrTooManySubscribers = errors.New("too many hardware status subscribers")
)

// RadioStatus is one radio's published state.
type RadioStatus struct {
	StateSince       time.Time
	NextActionAt     time.Time
	Netdev           string
	RecoveriesTotal  uint64
	PowerCyclesTotal uint64
	FailedTotal      uint64
	StepAttempt      uint32
	Radio            Radio
	State            State
	Reason           Reason
	CurrentStep      Step
	PowerRequested   bool
	RFDisabled       bool
	DevicePresent    bool
	NetdevPresent    bool
}

// FaultLineStatus is one fault input's published state.
type FaultLineStatus struct {
	LastChangeAt time.Time
	Name         string
	EdgesTotal   uint64
	Line         FaultLine
	State        LineState
}

// BatteryStatus is the published battery-monitor block. Units follow the
// field names; see the proto BatteryTelemetry message for meanings.
type BatteryStatus struct {
	SampledAt     time.Time
	EnergyMWh     float64
	ChargeMAh     float64
	TempC         float64
	BusMV         int32
	CurrentMA     int32
	PowerMW       int32
	CompensatedMV int32
	Alarms        uint32
	SeriesCells   uint32
	Level         BatteryLevel
	AbsentReason  AbsentReason
	Present       bool
	Stale         bool
	HasEnergy     bool
	HasTemp       bool
}

// WatchdogStatus is the published supervisor watchdog state.
type WatchdogStatus struct {
	LastPetAt          time.Time
	Device             string
	PetsTotal          uint64
	WDOEdgesTotal      uint64
	Enabled            bool
	Armed              bool
	HostResetRequested bool
}

// Status is the full published hardware manager view. It is a plain value
// (arrays, no slices or maps) so callers get an independent copy and two
// statuses compare with ==.
type Status struct {
	GeneratedAt           time.Time
	FaultLines            [faultLineCount]FaultLineStatus
	Watchdog              WatchdogStatus
	Radios                [radioCount]RadioStatus
	Battery               BatteryStatus
	RecoveryBootsLastHour uint32
	ControlMode           ControlMode
	ControlModeReason     ControlModeReason
	BoardDetected         bool
	PowerFault            bool
	ShutdownPending       bool
}

// Event is one entry of the bounded recovery event ring.
type Event struct {
	At         time.Time
	Detail     string
	Caller     string
	Attempt    uint32
	DurationMs uint32
	Radio      Radio
	Step       Step
	Trigger    Reason
	Result     Result
}

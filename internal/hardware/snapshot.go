package hardware

// RadioSnapshot is one radio in the instrumentation snapshot. Field
// semantics are documented in docs/instrumentation-snapshot.md; keep that
// file in sync when adding or renaming fields here.
type RadioSnapshot struct {
	Name             string `json:"name"`
	State            string `json:"state"`
	StateSinceNs     int64  `json:"state_since_ns"`
	RecoveriesTotal  uint64 `json:"recoveries_total"`
	PowerCyclesTotal uint64 `json:"power_cycles_total"`
	FailedTotal      uint64 `json:"failed_total"`
}

// FaultEdgeCounts holds the edge counter of every fault input, keyed by
// the lowercase line name.
type FaultEdgeCounts struct {
	HaLowFaultN    uint64 `json:"halow_fault_n"`
	WiFiFaultN     uint64 `json:"wifi_fault_n"`
	PowerGood      uint64 `json:"power_good"`
	EFuseFault     uint64 `json:"efuse_fault"`
	HaLowUSBFaultN uint64 `json:"halow_usb_fault_n"`
	VLMUSBFaultN   uint64 `json:"vlm_usb_fault_n"`
	INA228AlertN   uint64 `json:"ina228_alert_n"`
	SupervisorWDO  uint64 `json:"supervisor_wdo"`
}

// HardwareSnapshot is the "hardware" instrumentation section.
type HardwareSnapshot struct {
	ControlMode           string                    `json:"control_mode"`
	Radios                [radioCount]RadioSnapshot `json:"radios"`
	FaultEdgesTotal       FaultEdgeCounts           `json:"fault_edges_total"`
	WatchdogPetsTotal     uint64                    `json:"watchdog_pets_total"`
	WDOEdgesTotal         uint64                    `json:"wdo_edges_total"`
	BatteryBusMV          int32                     `json:"battery_bus_mv"`
	BatteryCurrentMA      int32                     `json:"battery_current_ma"`
	RecoveryBootsLastHour uint32                    `json:"recovery_boots_last_hour"`
	BoardDetected         bool                      `json:"board_detected"`
	BatteryStale          bool                      `json:"battery_stale"`
}

// Snapshotter adapts a Manager to the instrumentation registry. Refresh
// copies the published status under the manager's mutex (no I/O, no
// allocation); Data returns a stable pointer to the owned snapshot.
type Snapshotter struct {
	Manager *Manager
	data    HardwareSnapshot
}

// Refresh implements instrumentation.Snapshotter.
func (s *Snapshotter) Refresh() {
	if s.Manager == nil {
		return
	}

	m := s.Manager

	m.mu.Lock()
	defer m.mu.Unlock()

	st := &m.status
	d := &s.data

	d.BoardDetected = st.BoardDetected
	d.ControlMode = st.ControlMode.String()
	d.WatchdogPetsTotal = st.Watchdog.PetsTotal
	d.WDOEdgesTotal = st.Watchdog.WDOEdgesTotal
	d.BatteryBusMV = st.Battery.BusMV
	d.BatteryCurrentMA = st.Battery.CurrentMA
	d.BatteryStale = st.Battery.Stale || !st.Battery.Present
	d.RecoveryBootsLastHour = st.RecoveryBootsLastHour

	for i := range st.Radios {
		r := &st.Radios[i]
		d.Radios[i] = RadioSnapshot{
			Name:             r.Radio.String(),
			State:            r.State.String(),
			RecoveriesTotal:  r.RecoveriesTotal,
			PowerCyclesTotal: r.PowerCyclesTotal,
			FailedTotal:      r.FailedTotal,
		}

		if !r.StateSince.IsZero() {
			d.Radios[i].StateSinceNs = r.StateSince.UnixNano()
		}
	}

	e := &d.FaultEdgesTotal
	e.HaLowFaultN = st.FaultLines[FaultLineHaLowFault-1].EdgesTotal
	e.WiFiFaultN = st.FaultLines[FaultLineWiFiFault-1].EdgesTotal
	e.PowerGood = st.FaultLines[FaultLinePowerGood-1].EdgesTotal
	e.EFuseFault = st.FaultLines[FaultLineEFuseFault-1].EdgesTotal
	e.HaLowUSBFaultN = st.FaultLines[FaultLineHaLowUSBFault-1].EdgesTotal
	e.VLMUSBFaultN = st.FaultLines[FaultLineVLMUSBFault-1].EdgesTotal
	e.INA228AlertN = st.FaultLines[FaultLineINA228Alert-1].EdgesTotal
	e.SupervisorWDO = st.FaultLines[FaultLineSupervisorWDO-1].EdgesTotal
}

// Data implements instrumentation.Snapshotter.
func (s *Snapshotter) Data() any {
	return &s.data
}

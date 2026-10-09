package system

import (
	"fmt"
	"path/filepath"
)

// Monitor alarm bits reported in MonitorReading.Alarms. Each bit mirrors one
// hwmon alarm attribute of the INA228 (ina238 driver).
const (
	AlarmBusUndervoltage  uint32 = 1 << 0 // in1_min_alarm
	AlarmShuntOvervoltage uint32 = 1 << 1 // in0_max_alarm
	AlarmPowerOver        uint32 = 1 << 2 // power1_max_alarm
	AlarmTempOver         uint32 = 1 << 3 // temp1_max_alarm
)

// MonitorReading is one raw read of an INA2xx hwmon device, in the units the
// kernel driver reports. Optional attributes carry a Has* flag because the
// ina2xx and ina238 drivers expose different attribute sets.
type MonitorReading struct {
	BusMillivolts   int64
	CurrentMilliamp int64
	PowerMicrowatts int64
	EnergyMicrojoul int64
	TempMilliC      int64
	Alarms          uint32
	// Present is false when no INA2xx monitor exists (not an error).
	Present    bool
	HasCurrent bool
	HasPower   bool
	HasEnergy  bool
	HasTemp    bool
}

// alarmFiles maps hwmon alarm attributes to their MonitorReading bit.
var alarmFiles = [...]struct { //nolint:gochecknoglobals // constant lookup table
	file string
	bit  uint32
}{
	{"in1_min_alarm", AlarmBusUndervoltage},
	{"in0_max_alarm", AlarmShuntOvervoltage},
	{"power1_max_alarm", AlarmPowerOver},
	{"temp1_max_alarm", AlarmTempOver},
}

// ReadMonitor reads the first INA2xx monitor's raw attributes. full selects
// the slow-cadence attributes (energy, die temperature, alarm flags) as well
// as voltage, current and power, so callers can sample those less often and
// keep I2C traffic down. A missing monitor returns Present=false and no
// error; a missing or malformed bus voltage is an error.
func (h *HwmonBatteryProvider) ReadMonitor(full bool) (MonitorReading, error) {
	var r MonitorReading

	dir, err := h.findMonitor()
	if err != nil {
		return r, err
	}

	if dir == "" {
		return r, nil
	}

	r.BusMillivolts, err = readHwmonInt(filepath.Join(dir, "in1_input"))
	if err != nil {
		return r, fmt.Errorf("read battery voltage: %w", err)
	}

	r.Present = true

	if v, err := readHwmonInt(filepath.Join(dir, "curr1_input")); err == nil {
		r.CurrentMilliamp, r.HasCurrent = v, true
	}

	if v, err := readHwmonInt(filepath.Join(dir, "power1_input")); err == nil {
		r.PowerMicrowatts, r.HasPower = v, true
	}

	if !full {
		return r, nil
	}

	if v, err := readHwmonInt(filepath.Join(dir, "energy1_input")); err == nil {
		r.EnergyMicrojoul, r.HasEnergy = v, true
	}

	if v, err := readHwmonInt(filepath.Join(dir, "temp1_input")); err == nil {
		r.TempMilliC, r.HasTemp = v, true
	}

	for _, a := range alarmFiles {
		if v, err := readHwmonInt(filepath.Join(dir, a.file)); err == nil && v != 0 {
			r.Alarms |= a.bit
		}
	}

	return r, nil
}

// SeriesCells returns the pack's series cell count when the board profile
// knows it, or 0.
func (h *HwmonBatteryProvider) SeriesCells() uint32 {
	return h.seriesCells
}

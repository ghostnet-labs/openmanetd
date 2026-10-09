package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

func TestGetHardware_defaults(t *testing.T) {
	cfg := NewWithoutWatch(viper.New())

	h := cfg.GetHardware()
	assert.True(t, h.Enable)
	assert.False(t, h.ActuationEnable, "GPIO actuation must be off by default")
	assert.False(t, h.WatchdogEnable, "watchdog must be off by default")
	assert.False(t, h.BatteryShutdownEnable, "low-battery poweroff must be off by default")
	assert.Equal(t, DefaultHardwareRecoveryStateFile, h.RecoveryStateFile)
	assert.Equal(t, DefaultHardwareWatchdogIdentity, h.WatchdogIdentity)
	assert.InDelta(t, DefaultHardwarePackResistanceOhm, h.PackResistanceOhm, 1e-9)
	assert.Empty(t, h.FaultInputs)
	assert.Empty(t, h.HaLowUSBDevice)
	assert.Empty(t, h.WiFiPCIAddress)
}

func TestGetHardware_overrides(t *testing.T) {
	v := viper.New()
	v.Set("hardware.enable", false)
	v.Set("hardware.actuationEnable", true)
	v.Set("hardware.watchdogEnable", true)
	v.Set("hardware.batteryShutdownEnable", true)
	v.Set("hardware.recoveryStateFile", "/tmp/hw.json")
	v.Set("hardware.watchdogIdentity", "other")
	v.Set("hardware.packResistanceOhm", 0.1)
	v.Set("hardware.faultInputs", []string{"HALOW_FAULT_N", "POWER_GOOD"})
	v.Set("hardware.halow.usbDevice", "1-1.2")
	v.Set("hardware.halow.netdev", "wlan0")
	v.Set("hardware.wifi.pciAddress", "0001:01:00.0")
	v.Set("hardware.wifi.netdev", "wlan1")

	cfg := NewWithoutWatch(v)

	h := cfg.GetHardware()
	assert.False(t, h.Enable)
	assert.True(t, h.ActuationEnable)
	assert.True(t, h.WatchdogEnable)
	assert.True(t, h.BatteryShutdownEnable)
	assert.Equal(t, "/tmp/hw.json", h.RecoveryStateFile)
	assert.Equal(t, "other", h.WatchdogIdentity)
	assert.InDelta(t, 0.1, h.PackResistanceOhm, 1e-9)
	assert.Equal(t, []string{"HALOW_FAULT_N", "POWER_GOOD"}, h.FaultInputs)
	assert.Equal(t, "1-1.2", h.HaLowUSBDevice)
	assert.Equal(t, "wlan0", h.HaLowNetdev)
	assert.Equal(t, "0001:01:00.0", h.WiFiPCIAddress)
	assert.Equal(t, "wlan1", h.WiFiNetdev)

	// The returned slice is a copy.
	h.FaultInputs[0] = "changed"
	assert.Equal(t, "HALOW_FAULT_N", cfg.GetHardware().FaultInputs[0])
}

func TestGetHardware_nonPositiveResistanceUsesDefault(t *testing.T) {
	v := viper.New()
	v.Set("hardware.packResistanceOhm", -1.0)

	h := NewWithoutWatch(v).GetHardware()
	assert.InDelta(t, DefaultHardwarePackResistanceOhm, h.PackResistanceOhm, 1e-9)
}

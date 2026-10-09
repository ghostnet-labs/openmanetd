package system

import (
	"path/filepath"
	"testing"

	"github.com/openmanet/openmanetd/internal/util/board"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHwmonBatteryProvider_ReadMonitor(t *testing.T) {
	ina228 := map[string]string{
		"name":             "ina228",
		"in1_input":        "11850",
		"curr1_input":      "1234",
		"power1_input":     "14620000",
		"energy1_input":    "3600000000",
		"temp1_input":      "41250",
		"in1_min_alarm":    "1",
		"in0_max_alarm":    "0",
		"power1_max_alarm": "0",
		"temp1_max_alarm":  "1",
	}

	t.Run("fast read skips slow attributes", func(t *testing.T) {
		root := t.TempDir()
		writeHwmon(t, filepath.Join(root, "hwmon2"), ina228)

		r, err := (&HwmonBatteryProvider{HwmonDir: root}).ReadMonitor(false)
		require.NoError(t, err)
		assert.True(t, r.Present)
		assert.Equal(t, int64(11850), r.BusMillivolts)
		assert.True(t, r.HasCurrent)
		assert.Equal(t, int64(1234), r.CurrentMilliamp)
		assert.True(t, r.HasPower)
		assert.Equal(t, int64(14620000), r.PowerMicrowatts)
		assert.False(t, r.HasEnergy)
		assert.False(t, r.HasTemp)
		assert.Zero(t, r.Alarms)
	})

	t.Run("full read", func(t *testing.T) {
		root := t.TempDir()
		writeHwmon(t, filepath.Join(root, "hwmon2"), ina228)

		r, err := (&HwmonBatteryProvider{HwmonDir: root}).ReadMonitor(true)
		require.NoError(t, err)
		assert.True(t, r.HasEnergy)
		assert.Equal(t, int64(3600000000), r.EnergyMicrojoul)
		assert.True(t, r.HasTemp)
		assert.Equal(t, int64(41250), r.TempMilliC)
		assert.Equal(t, AlarmBusUndervoltage|AlarmTempOver, r.Alarms)
	})

	t.Run("voltage only monitor", func(t *testing.T) {
		root := t.TempDir()
		writeHwmon(t, filepath.Join(root, "hwmon0"), map[string]string{"name": "ina219", "in1_input": "12000"})

		r, err := (&HwmonBatteryProvider{HwmonDir: root}).ReadMonitor(true)
		require.NoError(t, err)
		assert.True(t, r.Present)
		assert.False(t, r.HasCurrent)
		assert.False(t, r.HasPower)
		assert.False(t, r.HasEnergy)
		assert.False(t, r.HasTemp)
	})

	t.Run("no monitor", func(t *testing.T) {
		r, err := (&HwmonBatteryProvider{HwmonDir: t.TempDir()}).ReadMonitor(true)
		require.NoError(t, err)
		assert.False(t, r.Present)
	})

	t.Run("malformed voltage", func(t *testing.T) {
		root := t.TempDir()
		writeHwmon(t, filepath.Join(root, "hwmon0"), map[string]string{"name": "ina228", "in1_input": "x"})

		_, err := (&HwmonBatteryProvider{HwmonDir: root}).ReadMonitor(false)
		require.Error(t, err)
	})
}

func TestHwmonBatteryProvider_SeriesCells(t *testing.T) {
	assert.Equal(t, uint32(3), NewHwmonBatteryProviderForBoard(board.GhostnetV1).SeriesCells())
	assert.Zero(t, NewHwmonBatteryProviderForBoard("").SeriesCells())
}

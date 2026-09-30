package system

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeHwmon(t *testing.T, dir string, files map[string]string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(dir, 0o755))

	for name, value := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o644))
	}
}

func TestHwmonBatteryProvider_GetBatteryStatus(t *testing.T) {
	t.Run("ina219 present", func(t *testing.T) {
		root := t.TempDir()
		writeHwmon(t, filepath.Join(root, "hwmon0"), map[string]string{"name": "cpu_thermal", "temp1_input": "52000"})
		writeHwmon(t, filepath.Join(root, "hwmon1"), map[string]string{
			"name":         "ina219",
			"in1_input":    "11400",
			"curr1_input":  "-850",
			"power1_input": "9690000",
		})

		st, err := (&HwmonBatteryProvider{HwmonDir: root}).GetBatteryStatus()
		require.NoError(t, err)
		assert.True(t, st.Present)
		assert.InDelta(t, 11.4, st.VoltageVolts, 0.001)
		assert.InDelta(t, -0.85, st.CurrentAmps, 0.001)
		assert.InDelta(t, 9.69, st.PowerWatts, 0.001)
		assert.Equal(t, uint32(3), st.CellCount)
		assert.InDelta(t, 60, st.ChargePercent, 0.5)
	})

	t.Run("voltage only", func(t *testing.T) {
		root := t.TempDir()
		writeHwmon(t, filepath.Join(root, "hwmon0"), map[string]string{"name": "ina219", "in1_input": "12600"})

		st, err := (&HwmonBatteryProvider{HwmonDir: root}).GetBatteryStatus()
		require.NoError(t, err)
		assert.True(t, st.Present)
		assert.InDelta(t, 12.6, st.VoltageVolts, 0.001)
		assert.Zero(t, st.CurrentAmps)
		assert.Zero(t, st.PowerWatts)
		assert.InDelta(t, 100, st.ChargePercent, 0.001)
	})

	t.Run("no monitor", func(t *testing.T) {
		root := t.TempDir()
		writeHwmon(t, filepath.Join(root, "hwmon0"), map[string]string{"name": "cpu_thermal"})

		st, err := (&HwmonBatteryProvider{HwmonDir: root}).GetBatteryStatus()
		require.NoError(t, err)
		assert.False(t, st.Present)
		assert.Negative(t, st.ChargePercent)
	})

	t.Run("missing hwmon dir", func(t *testing.T) {
		st, err := (&HwmonBatteryProvider{HwmonDir: "/nonexistent/hwmon"}).GetBatteryStatus()
		require.NoError(t, err)
		assert.False(t, st.Present)
	})

	t.Run("malformed voltage", func(t *testing.T) {
		root := t.TempDir()
		writeHwmon(t, filepath.Join(root, "hwmon0"), map[string]string{"name": "ina219", "in1_input": "abc"})

		_, err := (&HwmonBatteryProvider{HwmonDir: root}).GetBatteryStatus()
		require.Error(t, err)
	})
}

func TestEstimateLiionCharge(t *testing.T) {
	tests := []struct {
		name      string
		volts     float32
		wantCells uint32
		wantPct   float32
	}{
		{name: "3S full", volts: 12.6, wantCells: 3, wantPct: 100},
		{name: "3S empty", volts: 9.0, wantCells: 3, wantPct: 0},
		{name: "3S nominal", volts: 11.1, wantCells: 3, wantPct: 40},
		{name: "3S sagging below empty", volts: 8.9, wantCells: 3, wantPct: 0},
		{name: "2S full", volts: 8.4, wantCells: 2, wantPct: 100},
		{name: "1S mid", volts: 3.75, wantCells: 1, wantPct: 50},
		{name: "zero", volts: 0, wantCells: 0, wantPct: -1},
		{name: "5V rail", volts: 5.1, wantCells: 0, wantPct: -1},
		{name: "too high", volts: 30, wantCells: 0, wantPct: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cells, pct := EstimateLiionCharge(tt.volts)
			assert.Equal(t, tt.wantCells, cells)
			assert.InDelta(t, tt.wantPct, pct, 0.5)
		})
	}
}

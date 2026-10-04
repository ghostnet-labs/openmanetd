package system_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openmanet/openmanetd/internal/system"
	"github.com/openmanet/openmanetd/internal/util/board"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHwmonBatteryProvider_v1Profile(t *testing.T) {
	for _, voltage := range []string{"8400", "8600", "9900", "11400", "12600"} {
		t.Run(voltage, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "hwmon0")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "name"), []byte("ina228\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "in1_input"), []byte(voltage+"\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "curr1_input"), []byte("850\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "power1_input"), []byte("9690000\n"), 0o644))

			provider := system.NewHwmonBatteryProviderForBoard(board.GhostnetV1)
			provider.HwmonDir = root
			st, err := provider.GetBatteryStatus()
			require.NoError(t, err)
			assert.True(t, st.Present)
			assert.Equal(t, uint32(3), st.CellCount)
			assert.Equal(t, float32(-1), st.ChargePercent)
			assert.InDelta(t, 0.85, st.CurrentAmps, 0.001)
			assert.InDelta(t, 9.69, st.PowerWatts, 0.001)
		})
	}
}

func TestHwmonBatteryProvider_profileWithoutMonitor(t *testing.T) {
	provider := system.NewHwmonBatteryProviderForBoard(board.GhostnetV1)
	provider.HwmonDir = t.TempDir()
	st, err := provider.GetBatteryStatus()
	require.NoError(t, err)
	assert.False(t, st.Present)
	assert.Zero(t, st.CellCount)
	assert.Negative(t, st.ChargePercent)
}

func TestHwmonBatteryProvider_legacyProfile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "hwmon0")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "name"), []byte("ina219\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "in1_input"), []byte("8400\n"), 0o644))

	provider := system.NewHwmonBatteryProviderForBoard(board.BCM2711_RAVEN_USB)
	provider.HwmonDir = root
	st, err := provider.GetBatteryStatus()
	require.NoError(t, err)
	assert.Equal(t, uint32(2), st.CellCount)
	assert.Equal(t, float32(100), st.ChargePercent)

	unknown := system.NewHwmonBatteryProviderForBoard("")
	unknown.HwmonDir = root
	st, err = unknown.GetBatteryStatus()
	require.NoError(t, err)
	assert.True(t, st.Present)
	assert.Zero(t, st.CellCount)
	assert.Equal(t, float32(-1), st.ChargePercent)
}

package system_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/openmanet/openmanetd/internal/system"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCachedBatteryProvider_freshness(t *testing.T) {
	tests := []struct {
		name  string
		age   time.Duration
		fresh bool
	}{
		{name: "immediate", fresh: true},
		{name: "exact boundary", age: 5 * time.Second, fresh: true},
		{name: "past boundary", age: 5*time.Second + time.Nanosecond},
		{name: "thirty seconds", age: 30 * time.Second},
		{name: "clock moved backward", age: -time.Nanosecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &fakeBatteryClock{now: time.Unix(100, 0)}
			provider := &fakeBatteryProvider{status: system.BatteryStatus{Present: true, VoltageVolts: 12, ChargePercent: 80}}
			cache := system.NewCachedBatteryProvider(provider, clock.Now)
			first, err := cache.GetBatteryStatus()
			require.NoError(t, err)

			first.VoltageVolts = 99 // callers must not mutate the retained sample

			provider.Set(system.BatteryStatus{}, errors.New("monitor unavailable"))
			clock.Advance(tt.age)

			st, err := cache.GetBatteryStatus()
			if !tt.fresh {
				require.Error(t, err)
				assert.Nil(t, st)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, float32(12), st.VoltageVolts)
			assert.True(t, st.Present)
		})
	}
}

func TestCachedBatteryProvider_absenceAndRecovery(t *testing.T) {
	clock := &fakeBatteryClock{now: time.Unix(100, 0)}
	provider := &fakeBatteryProvider{err: errors.New("not readable")}
	cache := system.NewCachedBatteryProvider(provider, clock.Now)
	_, err := cache.GetBatteryStatus()
	require.Error(t, err)
	provider.Set(system.BatteryStatus{Present: true, VoltageVolts: 11}, nil)

	st, err := cache.GetBatteryStatus()
	require.NoError(t, err)
	assert.True(t, st.Present)
	provider.Set(system.BatteryStatus{ChargePercent: -1}, nil)

	st, err = cache.GetBatteryStatus()
	require.NoError(t, err)
	assert.False(t, st.Present) // authoritative removal is immediate
	assert.Negative(t, st.ChargePercent)
	provider.Set(system.BatteryStatus{}, errors.New("removed"))

	_, err = cache.GetBatteryStatus()
	require.Error(t, err) // removal must invalidate the last-good value
	provider.Set(system.BatteryStatus{Present: true, VoltageVolts: 12}, nil)

	st, err = cache.GetBatteryStatus()
	require.NoError(t, err)
	assert.Equal(t, float32(12), st.VoltageVolts)
}

func TestCachedBatteryProvider_hwmonHotplug(t *testing.T) {
	root := t.TempDir()
	clock := &fakeBatteryClock{now: time.Unix(100, 0)}
	cache := system.NewCachedBatteryProvider(&system.HwmonBatteryProvider{HwmonDir: root}, clock.Now)
	st, err := cache.GetBatteryStatus()
	require.NoError(t, err)
	assert.False(t, st.Present)

	dir := filepath.Join(root, "hwmon3")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "name"), []byte("ina228\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "in1_input"), []byte("11400\n"), 0o644))

	st, err = cache.GetBatteryStatus()
	require.NoError(t, err)
	assert.True(t, st.Present)
	assert.InDelta(t, 11.4, st.VoltageVolts, 0.001)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "in1_input"), []byte("invalid\n"), 0o644))
	clock.Advance(6 * time.Second)

	_, err = cache.GetBatteryStatus()
	require.Error(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "in1_input"), []byte("12000\n"), 0o644))

	st, err = cache.GetBatteryStatus()
	require.NoError(t, err)
	assert.Equal(t, float32(12), st.VoltageVolts)
}

func TestCachedBatteryProvider_nilProvider(t *testing.T) {
	st, err := system.NewCachedBatteryProvider(nil, nil).GetBatteryStatus()
	require.NoError(t, err)
	assert.False(t, st.Present)
	assert.Negative(t, st.ChargePercent)
}

func TestCachedBatteryProvider_concurrentReadOrdering(t *testing.T) {
	clock := &fakeBatteryClock{now: time.Unix(100, 0)}
	block := make(chan struct{})
	read := make(chan struct{})
	done := make(chan struct{})

	var release sync.Once

	provider := &fakeBatteryProvider{
		status: system.BatteryStatus{Present: true, VoltageVolts: 10},
		block:  block,
		read:   read,
	}
	cache := system.NewCachedBatteryProvider(provider, clock.Now)

	t.Cleanup(func() {
		release.Do(func() { close(block) })
		<-done
	})

	go func() {
		defer close(done)

		_, _ = cache.GetBatteryStatus() // slow first read; result checked below
	}()

	<-read
	provider.UnblockNext(system.BatteryStatus{Present: true, VoltageVolts: 12})

	st, err := cache.GetBatteryStatus()
	require.NoError(t, err)
	assert.Equal(t, float32(12), st.VoltageVolts)
	release.Do(func() { close(block) })
	<-done
	provider.Set(system.BatteryStatus{}, errors.New("read failed"))

	st, err = cache.GetBatteryStatus()
	require.NoError(t, err)
	assert.Equal(t, float32(12), st.VoltageVolts) // older read did not overwrite
}

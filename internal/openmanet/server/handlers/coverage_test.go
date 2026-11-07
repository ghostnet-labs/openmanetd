package handlers_test

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coveragev1 "github.com/openmanet/openmanetd/internal/api/openmanet/coverage/v1"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
)

func coverageConfig(t *testing.T, enable bool) *config.Config {
	t.Helper()

	v := viper.New()
	v.Set("tak.coverage.enable", enable)

	return config.NewWithoutWatch(v)
}

func newCoverageFake() *fakeCoverageMap {
	return &fakeCoverageMap{
		local: &coveragev1.CoverageReport{Hostname: "manet01", CellMeters: 50, Cells: []*coveragev1.CoverageCell{{Row: 1}}},
		remote: []*coveragev1.CoverageReport{
			{Hostname: "manet02", CellMeters: 50},
		},
	}
}

func TestGetCoverageMap(t *testing.T) {
	fake := newCoverageFake()
	svc := &handlers.CoverageService{Log: zerolog.Nop(), Cfg: coverageConfig(t, true), Map: fake}

	res, err := svc.GetCoverageMap(context.Background(), &coveragev1.GetCoverageMapRequest{})
	require.NoError(t, err)

	assert.True(t, res.GetEnabled())
	assert.Equal(t, "manet01", res.GetLocal().GetHostname())
	assert.Len(t, res.GetLocal().GetCells(), 1)
	require.Len(t, res.GetRemote(), 1)
	assert.Equal(t, "manet02", res.GetRemote()[0].GetHostname())
}

func TestGetCoverageMap_disabledOrMissing(t *testing.T) {
	tests := []struct {
		name string
		svc  *handlers.CoverageService
	}{
		{"disabled in config", &handlers.CoverageService{Cfg: coverageConfig(t, false), Map: newCoverageFake()}},
		{"no map", &handlers.CoverageService{Cfg: coverageConfig(t, true)}},
		{"no config", &handlers.CoverageService{Map: newCoverageFake()}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tc.svc.GetCoverageMap(context.Background(), &coveragev1.GetCoverageMapRequest{})
			require.NoError(t, err)

			assert.False(t, res.GetEnabled())
			assert.Nil(t, res.GetLocal())
			assert.Empty(t, res.GetRemote())
		})
	}
}

func TestGetCoverageMap_disabledDoesNotReadTheMap(t *testing.T) {
	fake := newCoverageFake()
	svc := &handlers.CoverageService{Cfg: coverageConfig(t, false), Map: fake}

	_, err := svc.GetCoverageMap(context.Background(), &coveragev1.GetCoverageMapRequest{})
	require.NoError(t, err)
	assert.Zero(t, fake.getCalls())
}

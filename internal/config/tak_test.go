package config

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func takFromYAML(t *testing.T, doc string) TAKConfig {
	t.Helper()

	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(doc)))

	return NewWithoutWatch(v).GetTAK()
}

func TestGetTAK_defaults(t *testing.T) {
	assert.Equal(t, TAKConfig{
		Enable:           true,
		Role:             TAKRoleRadio,
		Interval:         10 * time.Second,
		Links:            true,
		Jamming:          true,
		Weather:          true,
		AircraftEnable:   true,
		AircraftMax:      32,
		AircraftRangeKm:  150,
		CoverageEnable:   true,
		CoverageCellM:    50,
		CoverageMaxCells: 1024,
	}, NewWithoutWatch(viper.New()).GetTAK(), "the feed is on with no config so ATAK works with zero setup")
}

func TestGetTAK_yamlBinding(t *testing.T) {
	tc := takFromYAML(t, `
tak:
  enable: false
  role: MAST
  interval: 30s
  links: false
  jamming: false
  weather: false
  aircraft:
    enable: false
    max: 0
    rangeKm: 0
  coverage:
    enable: false
    cellMeters: 25
    maxCells: 4096
`)

	assert.Equal(t, TAKConfig{
		Enable:           false,
		Role:             TAKRoleMast,
		Interval:         30 * time.Second,
		Links:            false,
		Jamming:          false,
		Weather:          false,
		AircraftEnable:   false,
		AircraftMax:      0,
		AircraftRangeKm:  0,
		CoverageEnable:   false,
		CoverageCellM:    25,
		CoverageMaxCells: 4096,
	}, tc)
}

func TestGetTAK_invalidValuesFallBack(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		problem string
		check   func(t *testing.T, tc TAKConfig)
	}{
		{"unknown role", "tak:\n  role: tower\n", "tak.role", func(t *testing.T, tc TAKConfig) {
			t.Helper()
			assert.Equal(t, TAKRoleRadio, tc.Role)
		}},
		{"interval too short", "tak:\n  interval: 1s\n", "tak.interval", func(t *testing.T, tc TAKConfig) {
			t.Helper()
			assert.Equal(t, DefaultTAKInterval, tc.Interval)
		}},
		{"interval too long", "tak:\n  interval: 6m\n", "tak.interval", func(t *testing.T, tc TAKConfig) {
			t.Helper()
			assert.Equal(t, DefaultTAKInterval, tc.Interval)
		}},
		{"interval garbage", "tak:\n  interval: soon\n", "tak.interval", func(t *testing.T, tc TAKConfig) {
			t.Helper()
			assert.Equal(t, DefaultTAKInterval, tc.Interval)
		}},
		{"too many aircraft", "tak:\n  aircraft:\n    max: 257\n", "tak.aircraft.max", func(t *testing.T, tc TAKConfig) {
			t.Helper()
			assert.Equal(t, DefaultTAKAircraftMax, tc.AircraftMax)
		}},
		{"negative aircraft", "tak:\n  aircraft:\n    max: -1\n", "tak.aircraft.max", func(t *testing.T, tc TAKConfig) {
			t.Helper()
			assert.Equal(t, DefaultTAKAircraftMax, tc.AircraftMax)
		}},
		{"range too far", "tak:\n  aircraft:\n    rangeKm: 1001\n", "tak.aircraft.rangeKm", func(t *testing.T, tc TAKConfig) {
			t.Helper()
			assert.InDelta(t, DefaultTAKAircraftRangeKm, tc.AircraftRangeKm, 1e-9)
		}},
		{"cell too small", "tak:\n  coverage:\n    cellMeters: 9\n", "tak.coverage.cellMeters", func(t *testing.T, tc TAKConfig) {
			t.Helper()
			assert.Equal(t, DefaultTAKCoverageCellM, tc.CoverageCellM)
		}},
		{"cell too big", "tak:\n  coverage:\n    cellMeters: 501\n", "tak.coverage.cellMeters", func(t *testing.T, tc TAKConfig) {
			t.Helper()
			assert.Equal(t, DefaultTAKCoverageCellM, tc.CoverageCellM)
		}},
		{"too few cells", "tak:\n  coverage:\n    maxCells: 63\n", "tak.coverage.maxCells", func(t *testing.T, tc TAKConfig) {
			t.Helper()
			assert.Equal(t, DefaultTAKCoverageMax, tc.CoverageMaxCells)
		}},
		{"too many cells", "tak:\n  coverage:\n    maxCells: 8193\n", "tak.coverage.maxCells", func(t *testing.T, tc TAKConfig) {
			t.Helper()
			assert.Equal(t, DefaultTAKCoverageMax, tc.CoverageMaxCells)
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := takFromYAML(t, tc.doc)

			require.Len(t, got.Problems, 1)
			assert.Contains(t, got.Problems[0], tc.problem)
			tc.check(t, got)
		})
	}
}

func TestGetTAK_boundaries(t *testing.T) {
	tc := takFromYAML(t, "tak:\n  interval: 2s\n  aircraft:\n    max: 256\n    rangeKm: 1000\n")

	assert.Empty(t, tc.Problems)

	lo := takFromYAML(t, "tak:\n  coverage:\n    cellMeters: 10\n    maxCells: 64\n")
	assert.Empty(t, lo.Problems)
	assert.Equal(t, 10, lo.CoverageCellM)
	assert.Equal(t, 64, lo.CoverageMaxCells)

	hi := takFromYAML(t, "tak:\n  coverage:\n    cellMeters: 500\n    maxCells: 8192\n")
	assert.Empty(t, hi.Problems)
	assert.Equal(t, 500, hi.CoverageCellM)
	assert.Equal(t, 8192, hi.CoverageMaxCells)
	assert.Equal(t, 2*time.Second, tc.Interval)
	assert.Equal(t, 256, tc.AircraftMax)
	assert.InDelta(t, 1000, tc.AircraftRangeKm, 1e-9)
}

func TestGetTAK_returnsCopies(t *testing.T) {
	c := NewWithoutWatch(viper.New())
	c.TAK.Problems = []string{"a"}

	got := c.GetTAK()
	got.Problems[0] = "b"

	assert.Equal(t, "a", c.GetTAK().Problems[0])
}

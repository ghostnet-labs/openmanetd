package tiles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTileXY_knownTiles(t *testing.T) {
	cases := []struct {
		name     string
		lat, lon float64
		z, x, y  int
	}{
		{"origin z1", 0.0001, 0.0001, 1, 1, 0},
		{"north-west corner", 85.05, -180, 4, 0, 0},
		{"south-east corner", -85.05, 179.999, 4, 15, 15},
		{"Berlin z10", 52.52, 13.405, 10, 550, 335},
		{"Eagle Mountain z16", 40.314, -112.006, 16, 12377, 24735},
		{"beyond the Mercator edge clamps", 89.9, 0, 3, 4, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.x, tileX(tc.lon, tc.z), "x")
			assert.Equal(t, tc.y, tileY(tc.lat, tc.z), "y")
		})
	}
}

func TestAreaRanges_coversCenterAndGrows(t *testing.T) {
	ranges := areaRanges(40.314, -112.006, 5, 10, 14)
	require.Len(t, ranges, 5)

	var prev int64

	for _, r := range ranges {
		x, y := tileX(-112.006, r.z), tileY(40.314, r.z)
		assert.True(t, x >= r.minX && x <= r.maxX && y >= r.minY && y <= r.maxY, "zoom %d covers the center", r.z)
		assert.GreaterOrEqual(t, r.count(), prev, "each zoom has at least as many tiles")
		prev = r.count()
	}

	// 5 km radius at zoom 14 (~1.8 km tiles at this latitude) is about
	// 6x7 tiles.
	assert.InDelta(t, 42, ranges[4].count(), 20)
}

func TestAreaRanges_clampsAtMapEdges(t *testing.T) {
	r := areaRanges(84, 179.9, 200, 2, 2)[0]
	assert.Equal(t, 3, r.maxX)
	assert.Equal(t, 0, r.minY)
}

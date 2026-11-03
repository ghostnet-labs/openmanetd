package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConventionalLines(t *testing.T) {
	row := func(state, cat, site, mhz, mode, use, lat, lon string) map[string]string {
		return map[string]string{
			colState: state, "category": cat, colSystem: "SYS", colSite: site, "freq_mhz": mhz,
			"mode": mode, "use": use, "lat": lat, "lon": lon,
		}
	}

	rows := []map[string]string{
		row("UT", "aviation", "Salt Lake City Tower", "118.3", "AM", "tower", "40.78", "-111.97"),
		row("UT", "aviation", "Salt Lake City Tower", "118.3", "AM", "tower", "40.78", "-111.97"), // duplicate
		row("US", "aviation", "VHF emergency / guard", "121.5", "AM", "guard", "", ""),
		row("UT", "conventional", "", "155.475", "NFM", "UT\tstatewide", "", ""),
		row("UT", "conventional", "", "154.43", "P25", "digital", "", ""),
		row("US", "interop", "8CALL90", "851.0125", "FM", "NPSPAC calling", "", ""),
		row("US", "interop", "WX1-WX7 NOAA Weather Radio", "162.4", "FM", "summary", "", ""),
		row("UT", "weather", "Salt Lake City", "162.55", "NFM", "NOAA", "40.6", "-112.0"),
		row("US", "aircraft-data", "UAT", "978", "digital", "", "", ""),
	}

	lines, err := conventionalLines(rows)
	require.NoError(t, err)
	require.Len(t, lines, 5)

	assert.Equal(t, "C\tUT\t2\t2\t25000\t118300000\t40.78\t-111.97\tSalt Lake City Tower", lines[0])
	assert.Equal(t, "C\tUS\t2\t2\t25000\t121500000\t\t\tVHF emergency / guard (guard)", lines[1])
	assert.Equal(t, "C\tUT\t1\t1\t12500\t155475000\t\t\tUT statewide", lines[2])
	assert.Equal(t, "C\tUS\t3\t1\t25000\t851012500\t\t\t8CALL90 - NPSPAC calling", lines[3])
	assert.Equal(t, "C\tUT\t4\t1\t25000\t162550000\t40.6\t-112.0\tSYS Salt Lake City", lines[4])

	long := row("UT", "conventional", "", "155.1", "NFM", strings.Repeat("x", 80), "", "")
	lines, err = conventionalLines([]map[string]string{long})
	require.NoError(t, err)
	assert.Equal(t, maxLabel, len([]rune(strings.Split(lines[0], "\t")[8])))

	_, err = conventionalLines([]map[string]string{row("UT", "conventional", "", "15x", "NFM", "bad", "", "")})
	require.Error(t, err)

	_, err = conventionalLines([]map[string]string{row("UT", "weather", "x", "162.4", "NFM", "", "north", "1")})
	require.ErrorContains(t, err, "position")
}

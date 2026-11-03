package sites_test

import (
	"strings"
	"testing"

	"github.com/openmanet/openmanetd/internal/scanner/sites"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func table(t *testing.T) []sites.Site {
	t.Helper()

	all, err := sites.Table()
	require.NoError(t, err)

	return all
}

func TestTable(t *testing.T) {
	all := table(t)
	require.Len(t, all, 260)

	states := map[string]int{}
	keys := make(map[string]bool, len(all))

	for i := range all {
		s := &all[i]
		states[s.State]++

		assert.False(t, keys[s.Key], "duplicate key %s", s.Key)
		keys[s.Key] = true

		assert.True(t, strings.HasPrefix(s.Key, s.State+"-"), s.Key)
		assert.NotEmpty(t, s.ControlHz, s.Name)
		assert.NotEmpty(t, s.System)
		assert.Equal(t, sites.Key(s.State, s.System, s.Name), s.Key)
		assert.NotEqual(t, sites.PhaseUnknown, s.Phase, s.Name)
		assert.NotEqual(t, sites.BandUnknown, s.Band, s.Name)
		assert.NotEqual(t, sites.EncryptionUnknown, s.Encryption, s.Name)
		// The region is roughly 31-49 N, 102-120 W.
		assert.True(t, s.Lat > 31 && s.Lat < 49.5 && s.Lon > -120.5 && s.Lon < -101.5, "%s %f,%f", s.Name, s.Lat, s.Lon)
		assert.LessOrEqual(t, s.LowHz, s.HighHz)
		assert.GreaterOrEqual(t, s.LowHz, uint64(136_000_000))
		assert.LessOrEqual(t, s.HighHz, uint64(870_000_000))
	}

	assert.Equal(t, map[string]int{"AZ": 32, "CO": 42, "ID": 66, "NM": 18, "NV": 36, "UT": 38, "WY": 28}, states)

	// Spot-check the first row of both CSVs: AZ WINS South Mountain.
	s, ok := sites.ByKey(all, sites.Key("AZ", "Arizona Wireless Integrated Network System (AZ WINS)", "South Mountain [2-001]"))
	require.True(t, ok)
	assert.Equal(t, "Maricopa", s.County)
	assert.InDelta(t, 33.333, s.Lat, 1e-9)
	assert.Equal(t, []uint64{774_068_750, 774_318_750}, s.ControlHz)
	assert.Contains(t, s.VoiceHz, uint64(773_068_750))
	assert.Equal(t, uint64(773_068_750), s.LowHz)
	assert.Equal(t, uint64(774_818_750), s.HighHz)
	assert.Equal(t, uint64(1_750_000), s.SpanHz())
	assert.Equal(t, sites.Band700, s.Band)
	assert.Equal(t, sites.Phase2, s.Phase)
	assert.Equal(t, sites.EncryptionPartial, s.Encryption)
	assert.Equal(t, uint8(1), s.RTLNeeded)
	assert.Equal(t, uint8(1), s.WideNeeded)
	assert.Len(t, s.Frequencies(), 8)

	_, ok = sites.ByKey(all, "XX-00000000")
	assert.False(t, ok)
}

func TestParse_errors(t *testing.T) {
	head := sites.Format + "\nY\tsys\nP\tP25 Phase I\n"
	good := "T\tUT\t0\tPeak\tSalt Lake\t40.7\t-111.9\t2\t1\t0\t2\t1\t1\t770000000\t771000000"

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "no header", in: "T\tx\n", want: "header"},
		{name: "unknown record", in: head + "Q\tx\n", want: "unknown record"},
		{name: "field count", in: head + "T\tUT\t0\n", want: "fields"},
		{name: "system index", in: head + strings.Replace(good, "\t0\tPeak", "\t9\tPeak", 1) + "\n", want: "dictionary"},
		{name: "coordinate", in: head + strings.Replace(good, "40.7", "north", 1) + "\n", want: "coordinate"},
		{name: "frequency", in: head + strings.Replace(good, "770000000", "77x", 1) + "\n", want: "frequency"},
		{name: "no control", in: head + strings.Replace(good, "\t770000000\t", "\t\t", 1) + "\n", want: "no control"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sites.Parse(strings.NewReader(tc.in))
			require.ErrorIs(t, err, sites.ErrFormat)
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	got, err := sites.Parse(strings.NewReader(head + good + "\n\n"))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "sys", got[0].System)
	assert.Equal(t, "P25 Phase I", got[0].PhaseDetail)
	assert.Equal(t, []uint64{771_000_000}, got[0].VoiceHz)
}

func TestDistanceKm(t *testing.T) {
	tests := []struct {
		name                   string
		lat1, lon1, lat2, lon2 float64
		want, tol              float64
	}{
		{name: "same point", lat1: 40.76, lon1: -111.89, lat2: 40.76, lon2: -111.89, want: 0, tol: 1e-9},
		{name: "Salt Lake City to Denver", lat1: 40.7608, lon1: -111.8910, lat2: 39.7392, lon2: -104.9903, want: 596, tol: 3},
		{name: "one degree of latitude", lat1: 40, lon1: -111, lat2: 41, lon2: -111, want: 111.2, tol: 0.2},
		{name: "antipodal", lat1: 0, lon1: 0, lat2: 0, lon2: 180, want: 20015, tol: 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.InDelta(t, tc.want, sites.DistanceKm(tc.lat1, tc.lon1, tc.lat2, tc.lon2), tc.tol)
		})
	}
}

func TestNearestAndRank(t *testing.T) {
	all := table(t)

	// Downtown Salt Lake City.
	best, ok := sites.Nearest(all, 40.7608, -111.8910, nil)
	require.True(t, ok)
	assert.Equal(t, "UT", best.Site.State)
	assert.Less(t, best.DistanceKm, 30.0)

	// Filtered to Wyoming the nearest site is far away.
	wy, ok := sites.Nearest(all, 40.7608, -111.8910, sites.StateFilter{"WY"})
	require.True(t, ok)
	assert.Equal(t, "WY", wy.Site.State)
	assert.Greater(t, wy.DistanceKm, best.DistanceKm)

	_, ok = sites.Nearest(all, 40.7608, -111.8910, sites.StateFilter{"TX"})
	assert.False(t, ok)

	ranked, total := sites.Rank(all, 40.7608, -111.8910, sites.StateFilter{"UT", "ID"}, 10)
	assert.Equal(t, 104, total)
	require.Len(t, ranked, 10)
	assert.Equal(t, best.Site, ranked[0].Site)

	for i := 1; i < len(ranked); i++ {
		assert.LessOrEqual(t, ranked[i-1].DistanceKm, ranked[i].DistanceKm)
	}

	all2, total2 := sites.Rank(all, 0, 0, nil, 0)
	assert.Len(t, all2, 260)
	assert.Equal(t, 260, total2)
}

package sites_test

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/scanner/sites"
)

func TestChannels_embedded(t *testing.T) {
	all, err := sites.Channels()
	require.NoError(t, err)
	require.Greater(t, len(all), 500)

	byCat := map[sites.ChannelCategory]int{}
	keys := map[string]struct{}{}

	var guard *sites.Channel

	for i := range all {
		c := &all[i]
		byCat[c.Category]++
		keys[c.Key] = struct{}{}

		assert.Contains(t, []sites.ChannelMode{sites.ModeNFM, sites.ModeAM}, c.Mode, c.Label)
		assert.LessOrEqual(t, len(c.Key), 32)

		if c.Category == sites.CategoryAviation {
			assert.Equal(t, sites.ModeAM, c.Mode, c.Label)
		}

		if c.Nationwide() && c.FrequencyHz == 121_500_000 {
			guard = c
		}
	}

	assert.Len(t, keys, len(all), "channel keys are unique")

	for _, cat := range []sites.ChannelCategory{
		sites.CategoryConventional, sites.CategoryAviation, sites.CategoryInterop, sites.CategoryWeather,
	} {
		assert.Positive(t, byCat[cat], "category %d", cat)
	}

	require.NotNil(t, guard, "121.5 MHz guard is in the table")
	assert.False(t, guard.HasPosition)
	assert.Equal(t, 25000, guard.SpacingHz)
}

func TestParseTable_channelErrors(t *testing.T) {
	head := sites.Format + "\n"
	good := "C\tUT\t2\t2\t25000\t118300000\t40.78\t-111.97\tSalt Lake City Tower"

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "field count", in: "C\tUT\t2", want: "fields"},
		{name: "category", in: strings.Replace(good, "\t2\t2\t", "\t9\t2\t", 1), want: "category"},
		{name: "mode", in: strings.Replace(good, "\t2\t2\t", "\t2\t7\t", 1), want: "mode"},
		{name: "frequency", in: strings.Replace(good, "118300000", "x", 1), want: "number"},
		{name: "position", in: strings.Replace(good, "40.78", "", 1), want: "position"},
		{name: "label", in: strings.TrimSuffix(good, "Salt Lake City Tower"), want: "label"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sites.ParseTable(strings.NewReader(head + tc.in + "\n"))
			require.ErrorIs(t, err, sites.ErrFormat)
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	got, err := sites.ParseTable(strings.NewReader(head + good + "\nC\tUS\t3\t1\t12500\t155752500\t\t\tVCALL10\n"))
	require.NoError(t, err)
	require.Len(t, got.Channels, 2)

	c := got.Channels[0]
	assert.Equal(t, sites.CategoryAviation, c.Category)
	assert.Equal(t, sites.ModeAM, c.Mode)
	assert.True(t, c.HasPosition)
	assert.InDelta(t, 40.78, c.Lat, 1e-9)
	assert.Equal(t, sites.ChannelKey("UT", sites.CategoryAviation, 118_300_000, "Salt Lake City Tower"), c.Key)
	assert.False(t, got.Channels[1].HasPosition)
	assert.True(t, got.Channels[1].Nationwide())
}

func TestNearbyChannels(t *testing.T) {
	ch := func(state string, cat sites.ChannelCategory, mhz float64, label string, pos ...float64) sites.Channel {
		c := sites.Channel{
			State: state, Category: cat, Mode: sites.ModeNFM, SpacingHz: 12500,
			FrequencyHz: uint64(math.Round(mhz * 1e6)), Label: label,
		}
		if len(pos) == 2 {
			c.Lat, c.Lon, c.HasPosition = pos[0], pos[1], true
		}

		c.Key = sites.ChannelKey(c.State, c.Category, c.FrequencyHz, c.Label)

		return c
	}

	all := []sites.Channel{
		ch("US", sites.CategoryAviation, 121.5, "guard"),
		ch("UT", sites.CategoryAviation, 121.5, "SLC RCAG", 40.78, -111.97),
		ch("UT", sites.CategoryAviation, 118.3, "SLC Tower", 40.78, -111.97),
		ch("UT", sites.CategoryAviation, 118.6, "Far Tower", 37.0, -113.5),
		ch("UT", sites.CategoryConventional, 155.475, "UT statewide"),
		ch("ID", sites.CategoryConventional, 155.4, "ID statewide"),
		ch("UT", sites.CategoryWeather, 162.55, "SLC WX", 40.6, -112.0),
		ch("UT", sites.CategoryWeather, 162.55, "Far WX", 41.5, -112.0),
	}

	labels := func(ns []sites.NearbyChannel) []string {
		out := make([]string, 0, len(ns))
		for _, n := range ns {
			out = append(out, n.Channel.Label)
		}

		return out
	}

	got := sites.NearbyChannels(all, sites.ChannelQuery{State: "UT", Lat: 40.76, Lon: -111.89, RadiusKm: 80, Valid: true})
	assert.Equal(t, []string{"SLC Tower", "guard", "UT statewide", "SLC WX"}, labels(got))
	assert.True(t, got[1].AlwaysOn)
	assert.True(t, math.IsNaN(got[1].DistanceKm))
	assert.Less(t, got[0].DistanceKm, 15.0)

	none := sites.NearbyChannels(all, sites.ChannelQuery{RadiusKm: 80})
	assert.Equal(t, []string{"guard"}, labels(none), "without a fix only nationwide channels")
}

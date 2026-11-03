package conventional_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/openmanet/openmanetd/internal/scanner/conventional"
	"github.com/openmanet/openmanetd/internal/scanner/sites"
)

func makeChannel(hz uint64, alwaysOn bool) scanner.ConventionalChannel {
	c := &sites.Channel{FrequencyHz: hz, Mode: sites.ModeNFM, SpacingHz: 12500, Label: "ch", Category: sites.CategoryConventional}
	c.Key = sites.ChannelKey("UT", c.Category, hz, c.Label)

	return scanner.ConventionalChannel{Channel: c, DistanceKm: math.NaN(), AlwaysOn: alwaysOn}
}

func TestPlanWindows(t *testing.T) {
	mhz := func(f float64) uint64 { return uint64(math.Round(f * 1e6)) }

	t.Run("packs nearby channels and keeps DC clear", func(t *testing.T) {
		chs := []scanner.ConventionalChannel{
			makeChannel(mhz(155.34), false), makeChannel(mhz(155.475), false), makeChannel(mhz(155.7525), false),
			makeChannel(mhz(462.5625), false),
		}

		ws := conventional.PlanWindowsForTest(chs, 1_638_400, 12, 8)
		require.Len(t, ws, 2)
		assert.Equal(t, []int{0, 1, 2}, ws[0].Chans)
		assert.Equal(t, []int{3}, ws[1].Chans)

		for _, w := range ws {
			for _, ci := range w.Chans {
				off := math.Abs(float64(chs[ci].Channel.FrequencyHz) - float64(w.CenterHz))
				assert.GreaterOrEqual(t, off, 20_000.0, "channel clear of DC")
				assert.LessOrEqual(t, off, 1_638_400/2-12_500.0, "channel inside the usable band")
			}
		}
	})

	t.Run("centered channel moves the center", func(t *testing.T) {
		chs := []scanner.ConventionalChannel{
			makeChannel(mhz(154.0), false), makeChannel(mhz(154.2), false), makeChannel(mhz(154.4), false),
		}

		ws := conventional.PlanWindowsForTest(chs, 1_638_400, 12, 8)
		require.Len(t, ws, 1)
		assert.NotEqual(t, mhz(154.2), ws[0].CenterHz)
		assert.InDelta(t, float64(mhz(154.2)), float64(ws[0].CenterHz), 25_000)
	})

	t.Run("always-on channels win the window budget", func(t *testing.T) {
		chs := []scanner.ConventionalChannel{
			makeChannel(mhz(118.3), false), makeChannel(mhz(118.6), false),
			makeChannel(mhz(121.5), true),
			makeChannel(mhz(155.0), false), makeChannel(mhz(155.1), false), makeChannel(mhz(155.2), false),
		}

		ws := conventional.PlanWindowsForTest(chs, 1_638_400, 2, 8)
		require.Len(t, ws, 2)
		assert.Equal(t, []int{2}, ws[0].Chans, "121.5 beats the two-channel 118 MHz cluster")
		assert.Equal(t, []int{3, 4, 5}, ws[1].Chans)
	})

	t.Run("per-window cap and tuning range", func(t *testing.T) {
		chs := make([]scanner.ConventionalChannel, 0, 12)
		for i := range 10 {
			chs = append(chs, makeChannel(mhz(460)+uint64(i)*25_000, false))
		}

		chs = append(chs, makeChannel(mhz(10), false), makeChannel(mhz(2400), false))

		ws := conventional.PlanWindowsForTest(chs, 1_638_400, 12, 4)
		require.Len(t, ws, 3)
		assert.Len(t, ws[0].Chans, 4)
		assert.Len(t, ws[1].Chans, 4)
		assert.Len(t, ws[2].Chans, 2)
	})

	t.Run("degenerate inputs", func(t *testing.T) {
		assert.Empty(t, conventional.PlanWindowsForTest(nil, 1_638_400, 12, 8))
		assert.Empty(t, conventional.PlanWindowsForTest([]scanner.ConventionalChannel{makeChannel(mhz(155), false)}, 10_000, 12, 8))
		assert.Empty(t, conventional.PlanWindowsForTest([]scanner.ConventionalChannel{makeChannel(mhz(155), false)}, 1_638_400, 0, 8))
	})
}

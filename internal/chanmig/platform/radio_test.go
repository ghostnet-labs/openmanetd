package platform_test

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/chanmig"
	"github.com/openmanet/openmanetd/internal/chanmig/platform"
)

func TestUCIBand(t *testing.T) {
	assert.Equal(t, "s1g", platform.UCIBand(chanmig.BandHaLow))
	assert.Equal(t, "2g", platform.UCIBand(chanmig.Band2G4))
	assert.Equal(t, "5g", platform.UCIBand(chanmig.Band5G))
	assert.Empty(t, platform.UCIBand(0))
}

func TestHTMode(t *testing.T) {
	halow := func(w uint16) chanmig.Assignment {
		return chanmig.Assignment{Band: chanmig.BandHaLow, Channel: 28, WidthMHz: w}
	}
	g2 := func(w uint16) chanmig.Assignment {
		return chanmig.Assignment{Band: chanmig.Band2G4, Channel: 6, WidthMHz: w}
	}
	g5 := func(w uint16) chanmig.Assignment {
		return chanmig.Assignment{Band: chanmig.Band5G, Channel: 36, WidthMHz: w}
	}

	cases := []struct {
		name    string
		current string
		want    string
		a       chanmig.Assignment
	}{
		{name: "halow 1", a: halow(1), current: "2 MHz", want: "1 MHz"},
		{name: "halow 8 ignores current", a: halow(8), current: "HT20", want: "8 MHz"},
		{name: "2g keeps HE", a: g2(40), current: "HE20", want: "HE40"},
		{name: "2g keeps HT40+ at 40", a: g2(40), current: "HT40+", want: "HT40+"},
		{name: "2g keeps HT40- at 40", a: g2(40), current: "ht40-", want: "HT40-"},
		{name: "2g HT40+ to 20", a: g2(20), current: "HT40+", want: "HT20"},
		{name: "2g NOHT", a: g2(20), current: "NOHT", want: "HT20"},
		{name: "2g empty", a: g2(20), current: "", want: "HT20"},
		{name: "5g keeps VHT", a: g5(160), current: "VHT80", want: "VHT160"},
		{name: "5g keeps EHT", a: g5(80), current: "EHT160", want: "EHT80"},
		{name: "5g HT promoted for 80", a: g5(80), current: "HT20", want: "VHT80"},
		{name: "5g HE lowercase", a: g5(20), current: " he80 ", want: "HE20"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, platform.HTMode(tc.a, tc.current))
		})
	}
}

func newRadioFixture(t *testing.T) (*fakeUCI, *fakeReloader, *platform.UCIRadio) {
	t.Helper()

	uci := newFakeUCI()
	uci.addDevice("radio0", "s1g", "28", "2 MHz")
	uci.addDevice("radio1", "2g", "1", "HE20")
	uci.addDevice("radio2", "5g", "36", "VHT80")

	rl := &fakeReloader{}

	return uci, rl, &platform.UCIRadio{Reader: uci, Reload: rl.reload, Log: zerolog.Nop()}
}

func TestUCIRadio_appliesAssignment(t *testing.T) {
	cases := []struct {
		name    string
		section string
		channel string
		htmode  string
		a       chanmig.Assignment
	}{
		{
			name: "halow", a: chanmig.Assignment{Band: chanmig.BandHaLow, Channel: 30, WidthMHz: 4},
			section: "radio0", channel: "30", htmode: "4 MHz",
		},
		{
			name: "2.4 GHz", a: chanmig.Assignment{Band: chanmig.Band2G4, Channel: 11, WidthMHz: 40},
			section: "radio1", channel: "11", htmode: "HE40",
		},
		{
			name: "5 GHz", a: chanmig.Assignment{Band: chanmig.Band5G, Channel: 149, WidthMHz: 40},
			section: "radio2", channel: "149", htmode: "VHT40",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uci, rl, radio := newRadioFixture(t)

			require.NoError(t, radio.SetChannel(context.Background(), tc.a))

			assert.Equal(t, tc.channel, uci.option(tc.section, "channel"))
			assert.Equal(t, tc.htmode, uci.option(tc.section, "htmode"))

			commits, reloads, sets := uci.counts()
			assert.Equal(t, 1, commits)
			assert.Equal(t, 1, reloads, "the UCI view is refreshed before the tune")
			assert.Equal(t, 2, sets, "only channel and htmode are written")
			assert.Equal(t, 1, rl.count())

			for _, other := range []string{"radio0", "radio1", "radio2"} {
				if other == tc.section {
					continue
				}

				assert.NotEqual(t, tc.channel, uci.option(other, "channel"), "other radios are untouched")
			}
		})
	}
}

func TestUCIRadio_noOpWhenAlreadyTuned(t *testing.T) {
	uci, rl, radio := newRadioFixture(t)

	require.NoError(t, radio.SetChannel(context.Background(),
		chanmig.Assignment{Band: chanmig.Band5G, Channel: 36, WidthMHz: 80}))

	commits, _, sets := uci.counts()
	assert.Zero(t, commits)
	assert.Zero(t, sets)
	assert.Zero(t, rl.count(), "a matching tune must not bounce the radio")
}

func TestUCIRadio_pinnedSection(t *testing.T) {
	uci, _, radio := newRadioFixture(t)
	uci.addDevice("radio9", "5g", "100", "HE80")

	radio.Radios = map[chanmig.Band]string{chanmig.Band5G: "radio9"}

	require.NoError(t, radio.SetChannel(context.Background(),
		chanmig.Assignment{Band: chanmig.Band5G, Channel: 52, WidthMHz: 80}))

	assert.Equal(t, "52", uci.option("radio9", "channel"))
	assert.Equal(t, "HE80", uci.option("radio9", "htmode"))
	assert.Equal(t, "36", uci.option("radio2", "channel"))
}

func TestUCIRadio_reloadDetachedFromCaller(t *testing.T) {
	_, rl, radio := newRadioFixture(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, radio.SetChannel(ctx, chanmig.Assignment{Band: chanmig.BandHaLow, Channel: 12, WidthMHz: 1}))
	assert.Equal(t, []error{nil}, rl.contextErrors(), "shutdown must not abort a reload in flight")
}

func TestUCIRadio_errors(t *testing.T) {
	valid := chanmig.Assignment{Band: chanmig.BandHaLow, Channel: 30, WidthMHz: 4}

	t.Run("invalid assignment", func(t *testing.T) {
		uci, rl, radio := newRadioFixture(t)

		require.ErrorIs(t, radio.SetChannel(context.Background(),
			chanmig.Assignment{Band: chanmig.BandHaLow, Channel: 30, WidthMHz: 3}), chanmig.ErrInvalidPlan)

		_, reloads, _ := uci.counts()
		assert.Zero(t, reloads, "UCI is not touched")
		assert.Zero(t, rl.count())
	})

	t.Run("no radio for band", func(t *testing.T) {
		uci := newFakeUCI()
		uci.addDevice("radio1", "2g", "1", "HE20")

		rl := &fakeReloader{}
		radio := &platform.UCIRadio{Reader: uci, Reload: rl.reload, Log: zerolog.Nop()}

		require.ErrorContains(t, radio.SetChannel(context.Background(), valid), `band "s1g"`)
		assert.Zero(t, rl.count())
	})

	t.Run("uci re-read fails", func(t *testing.T) {
		uci, rl, radio := newRadioFixture(t)
		uci.setErrors(nil, errInjected)

		require.ErrorIs(t, radio.SetChannel(context.Background(), valid), errInjected)
		assert.Zero(t, rl.count())
	})

	t.Run("commit fails", func(t *testing.T) {
		uci, rl, radio := newRadioFixture(t)
		uci.setErrors(errInjected, nil)

		require.ErrorIs(t, radio.SetChannel(context.Background(), valid), errInjected)
		assert.Zero(t, rl.count(), "nothing is reloaded after a failed commit")
	})

	t.Run("reload fails", func(t *testing.T) {
		_, rl, radio := newRadioFixture(t)
		rl.err = errInjected

		require.ErrorIs(t, radio.SetChannel(context.Background(), valid), errInjected)
	})
}

package jamdetect_test

import (
	"testing"

	"github.com/openmanet/openmanetd/internal/jamdetect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mt76Survey = `Survey data from phy1-mesh0
	frequency:			5180 MHz [in use]
	noise:				-91 dBm
	channel active time:		1234567 ms
	channel busy time:		234567 ms
	channel receive time:		200000 ms
	channel transmit time:		20000 ms
Survey data from phy1-mesh0
	frequency:			5200 MHz
	noise:				-93 dBm
	channel active time:		120 ms
	channel busy time:		10 ms
Survey data from phy1-mesh0
	frequency:			5220 MHz
`

const morseSurvey = `Survey data from wlh0
	frequency:			915.500 MHz [in use]
	noise:				-101 dBm
	channel active time:		50000 ms
	channel busy time:		4000 ms
	channel receive time:		3000 ms
	channel transmit time:		500 ms
	some new field:			7
`

func TestParseSurvey_mt76(t *testing.T) {
	got := jamdetect.ParseSurvey([]byte(mt76Survey))
	require.Len(t, got, 3)

	in, ok := jamdetect.InUse(got)
	require.True(t, ok)
	assert.InDelta(t, 5180, in.FreqMHz, 0.001)
	assert.True(t, in.HasNoise)
	assert.Equal(t, -91, in.NoiseDBm)
	assert.Equal(t, uint64(1234567), in.ActiveMs)
	assert.Equal(t, uint64(234567), in.BusyMs)
	assert.Equal(t, uint64(200000), in.RxMs)
	assert.Equal(t, uint64(20000), in.TxMs)
	assert.True(t, in.HasActive && in.HasBusy && in.HasRx && in.HasTx)

	assert.False(t, got[1].InUse)
	assert.False(t, got[1].HasRx)
	assert.False(t, got[2].HasNoise)
}

func TestParseSurvey_morseFractionalFrequency(t *testing.T) {
	got := jamdetect.ParseSurvey([]byte(morseSurvey))
	require.Len(t, got, 1)
	assert.InDelta(t, 915.5, got[0].FreqMHz, 0.001)
	assert.True(t, got[0].InUse)
	assert.Equal(t, -101, got[0].NoiseDBm)
}

func TestParseSurvey_emptyAndGarbage(t *testing.T) {
	assert.Empty(t, jamdetect.ParseSurvey(nil))
	assert.Empty(t, jamdetect.ParseSurvey([]byte("command failed: Operation not supported (-95)\n")))
	assert.Empty(t, jamdetect.ParseSurvey([]byte("Survey data from x\n\tfrequency: abc MHz\n")))

	_, ok := jamdetect.InUse(nil)
	assert.False(t, ok)
}

const iwDev = `phy#2
	Interface wlh0
		ifindex 9
		type mesh point
phy#1
	Interface phy1-ap0
		type AP
	Interface phy1-mesh0
		type mesh point
	Interface mon1
		type monitor
phy#0
	Interface mon0
		type monitor
	Interface phy0-sta0
		type managed
`

func TestParseIWDev_onePerPhyPreferMesh(t *testing.T) {
	got := jamdetect.ParseIWDev([]byte(iwDev), func(name string) bool { return name == "wlh0" })

	assert.Equal(t, []jamdetect.Radio{
		{Ifname: "phy0-sta0", PHY: "phy0"},
		{Ifname: "phy1-mesh0", PHY: "phy1"},
		{Ifname: "wlh0", PHY: "phy2", HaLow: true},
	}, got)
	assert.Empty(t, jamdetect.ParseIWDev(nil, nil))
}

func TestBandFor(t *testing.T) {
	tests := []struct {
		name  string
		freq  float64
		want  jamdetect.Band
		halow bool
	}{
		{name: "halow mapped 5 GHz", freq: 5180, halow: true, want: jamdetect.BandS1G},
		{name: "s1g real", freq: 915.5, want: jamdetect.BandS1G},
		{name: "2.4", freq: 2437, want: jamdetect.Band2G4},
		{name: "5", freq: 5745, want: jamdetect.Band5G},
		{name: "6", freq: 5955, want: jamdetect.Band6G},
		{name: "unknown", freq: 60480, want: jamdetect.BandUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, jamdetect.BandFor(tc.halow, tc.freq))
		})
	}
}

package radiocap_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/openmanet/openmanetd/internal/radiocap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixturePath locates testfixtures/radiocap/<name> from this file's location.
// The fixtures are synthetic (see testfixtures/radiocap/README.md).
func fixturePath(t *testing.T, name string) string {
	t.Helper()

	_, here, _, ok := runtime.Caller(0)
	require.True(t, ok)

	return filepath.Join(filepath.Dir(here), "..", "..", "testfixtures", "radiocap", name)
}

func parseFixture(t *testing.T, name string) []radiocap.PHY {
	t.Helper()

	f, err := os.Open(fixturePath(t, name))
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	phys, err := radiocap.ParseIWList(f)
	require.NoError(t, err)

	return phys
}

func TestParseIWList_mt7916DBDC(t *testing.T) {
	phys := parseFixture(t, "mt7916_dbdc.txt")
	require.Len(t, phys, 2)

	want := radiocap.Combination{
		Limits: []radiocap.Limit{
			{Types: []radiocap.IfType{"IBSS"}, Max: 1},
			{Types: []radiocap.IfType{radiocap.IfTypeAP, radiocap.IfTypeMesh}, Max: 16},
			{Types: []radiocap.IfType{radiocap.IfTypeManaged}, Max: 19},
		},
		MaxInterfaces: 19,
		MaxChannels:   1,
	}

	assert.Equal(t, "phy0", phys[0].Name)
	assert.Equal(t, []radiocap.Band{radiocap.Band2G4}, phys[0].Bands)
	assert.Equal(t, []radiocap.Combination{want}, phys[0].Combinations)

	assert.Equal(t, "phy1", phys[1].Name)
	assert.Equal(t, []radiocap.Band{radiocap.Band5G}, phys[1].Bands)
	assert.Equal(t, []radiocap.Combination{want}, phys[1].Combinations)
	assert.Equal(t, []radiocap.IfType{"IBSS", "managed", "AP", "AP/VLAN", "monitor", "mesh point"},
		phys[1].SupportedTypes)

	for _, p := range phys {
		assert.False(t, p.Qualified, "enumeration must not qualify %s", p.Name)
		assert.False(t, p.Available, "enumeration must not mark %s available", p.Name)
	}
}

func TestParseIWList_openWrtWiphyRadios(t *testing.T) {
	phys := parseFixture(t, "multiradio_owrt_iw.txt")
	require.Len(t, phys, 3, "each wiphy radio is its own PHY")

	perRadio := []radiocap.Combination{{
		Limits: []radiocap.Limit{
			{Types: []radiocap.IfType{radiocap.IfTypeManaged}, Max: 1},
			{Types: []radiocap.IfType{radiocap.IfTypeAP, radiocap.IfTypeMesh}, Max: 16},
		},
		MaxInterfaces: 17,
		MaxChannels:   1,
	}}

	want := []struct {
		name, radio string
		band        radiocap.Band
	}{
		{name: "phy0/radio0", radio: "0", band: radiocap.Band2G4},
		{name: "phy0/radio1", radio: "1", band: radiocap.Band5G},
		{name: "phy0/radio2", radio: "2", band: radiocap.Band6G},
	}

	for i, w := range want {
		assert.Equal(t, w.name, phys[i].Name)
		assert.Equal(t, "phy0", phys[i].Wiphy)
		assert.Equal(t, w.radio, phys[i].Radio)
		assert.Equal(t, []radiocap.Band{w.band}, phys[i].Bands)
		assert.Equal(t, perRadio, phys[i].Combinations, "wiphy-wide #channels <= 3 must not leak into a radio")
		assert.Contains(t, phys[i].SupportedTypes, radiocap.IfTypeMesh)
	}

	phys[0].SupportedTypes[0] = "changed"
	assert.Equal(t, radiocap.IfTypeManaged, phys[1].SupportedTypes[0], "radios must not share slices")
}

func TestParseIWList_stockFormatHasNoRadio(t *testing.T) {
	for _, p := range parseFixture(t, "mt7916_dbdc.txt") {
		assert.Equal(t, p.Name, p.Wiphy)
		assert.Empty(t, p.Radio)
	}
}

func TestParseIWList_badRadioRange(t *testing.T) {
	in := "Wiphy phy0\n\twiphy radio 0:\n\t\tfreq range: 2500.0 MHz - 2400.0 MHz\n"
	_, err := radiocap.ParseIWList(strings.NewReader(in))
	assert.Error(t, err)
}

func TestParseIWList_morseHaLow(t *testing.T) {
	phys := parseFixture(t, "morse_halow.txt")
	require.Len(t, phys, 1)

	p := phys[0]
	// The Morse driver reports S1G channels on 5 GHz numbers; iw alone says 5.
	assert.Equal(t, []radiocap.Band{radiocap.Band5G}, p.Bands)
	require.Len(t, p.Combinations, 1)
	assert.Equal(t, 3, p.Combinations[0].MaxInterfaces)
	assert.Equal(t, 1, p.Combinations[0].MaxChannels)

	p.SetHaLow()
	assert.Equal(t, []radiocap.Band{radiocap.BandS1G}, p.Bands)
}

func TestParseIWList_singlePHYTwoBandsTwoCombinations(t *testing.T) {
	phys := parseFixture(t, "ath9k_dualband_single_phy.txt")
	require.Len(t, phys, 1)

	p := phys[0]
	assert.Equal(t, []radiocap.Band{radiocap.Band2G4, radiocap.Band5G}, p.Bands)
	assert.Equal(t, []radiocap.IfType{"AP/VLAN", "monitor"}, p.SoftwareTypes)
	require.Len(t, p.Combinations, 2)
	assert.Equal(t, 1, p.Combinations[0].MaxChannels)
	assert.Equal(t, 2, p.Combinations[1].MaxChannels)
	assert.Equal(t, []radiocap.Limit{
		{Types: []radiocap.IfType{radiocap.IfTypeManaged}, Max: 2048},
		{Types: []radiocap.IfType{"P2P-client", "P2P-GO"}, Max: 1},
	}, p.Combinations[1].Limits)

	c := p.Capacity()
	assert.Equal(t, 2, c.MaxChannels)
	assert.Equal(t, 2048, c.MaxInterfaces)
	assert.Contains(t, c.MaxByType, radiocap.TypeLimit{Type: radiocap.IfTypeMesh, Max: 8})
}

func TestParseIWList_nativeS1GFrequencies(t *testing.T) {
	in := "Wiphy phy2\n" +
		"\tBand 5:\n" +
		"\t\tFrequencies:\n" +
		"\t\t\t* 902.5 MHz [1] (30.0 dBm)\n" +
		"\t\t\t* 904.5 MHz [5] (30.0 dBm)\n" +
		"\tvalid interface combinations:\n" +
		"\t\t * #{ mesh point } <= 1,\n" +
		"\t\t   total <= 1, #channels <= 1\n"

	phys, err := radiocap.ParseIWList(strings.NewReader(in))
	require.NoError(t, err)
	require.Len(t, phys, 1)
	assert.Equal(t, []radiocap.Band{radiocap.BandS1G}, phys[0].Bands)
}

func TestParseIWList_errors(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{name: "empty", in: ""},
		{name: "no wiphy", in: "\tBand 1:\n"},
		{name: "combination without total", in: "Wiphy phy0\n\tvalid interface combinations:\n\t\t * #{ AP } <= 1\n"},
		{name: "combination without limits", in: "Wiphy phy0\n\tvalid interface combinations:\n\t\t * total <= 1, #channels <= 1\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := radiocap.ParseIWList(strings.NewReader(tc.in))
			assert.Error(t, err)
		})
	}
}

func TestBandForMHz(t *testing.T) {
	tests := []struct {
		mhz  float64
		want radiocap.Band
	}{
		{mhz: 0, want: ""},
		{mhz: 902.5, want: radiocap.BandS1G},
		{mhz: 2412, want: radiocap.Band2G4},
		{mhz: 2484, want: radiocap.Band2G4},
		{mhz: 5180, want: radiocap.Band5G},
		{mhz: 5925, want: radiocap.Band5G},
		{mhz: 5955, want: radiocap.Band6G},
		{mhz: 60480, want: radiocap.Band60G},
		{mhz: 3600, want: ""},
	}

	for _, tc := range tests {
		assert.Equal(t, tc.want, radiocap.BandForMHz(tc.mhz), "%v MHz", tc.mhz)
	}
}

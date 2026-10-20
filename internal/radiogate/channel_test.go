package radiogate_test

import (
	"testing"

	"github.com/openmanet/openmanetd/internal/radiocap"
	"github.com/openmanet/openmanetd/internal/radiogate"
	"github.com/stretchr/testify/assert"
)

func TestUCIBand(t *testing.T) {
	tests := []struct {
		name    string
		uciBand string
		channel string
		want    radiocap.Band
		morse   bool
	}{
		{name: "morse wins over band option", uciBand: "5g", morse: true, want: radiocap.BandS1G},
		{name: "2g", uciBand: "2g", want: radiocap.Band2G4},
		{name: "5g", uciBand: "5G", want: radiocap.Band5G},
		{name: "6g", uciBand: "6g", want: radiocap.Band6G},
		{name: "60g", uciBand: "60g", want: radiocap.Band60G},
		{name: "s1g option", uciBand: "s1g", want: radiocap.BandS1G},
		{name: "no band, low channel", channel: "11", want: radiocap.Band2G4},
		{name: "no band, high channel", channel: "149", want: radiocap.Band5G},
		{name: "no band, auto", channel: "auto", want: ""},
		{name: "no band, zero", channel: "0", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, radiogate.UCIBand(tc.uciBand, tc.morse, tc.channel))
		})
	}
}

func TestUCIChannel(t *testing.T) {
	tests := []struct {
		name    string
		band    radiocap.Band
		channel string
		want    radiocap.Channel
		width   uint32
		slot    int
		wantOK  bool
	}{
		{
			name: "2.4 ch6", band: radiocap.Band2G4, channel: "6", width: 20, wantOK: true,
			want: radiocap.Channel{Band: radiocap.Band2G4, CenterKHz: 2437000, WidthKHz: 20000},
		},
		{
			name: "2.4 ch14", band: radiocap.Band2G4, channel: "14", width: 20, wantOK: true,
			want: radiocap.Channel{Band: radiocap.Band2G4, CenterKHz: 2484000, WidthKHz: 20000},
		},
		{
			name: "5 ch36 vht80", band: radiocap.Band5G, channel: "36", width: 80, wantOK: true,
			want: radiocap.Channel{Band: radiocap.Band5G, CenterKHz: 5180000, WidthKHz: 80000},
		},
		{
			name: "6 ch2", band: radiocap.Band6G, channel: "2", width: 20, wantOK: true,
			want: radiocap.Channel{Band: radiocap.Band6G, CenterKHz: 5935000, WidthKHz: 20000},
		},
		{
			name: "6 ch5", band: radiocap.Band6G, channel: "5", width: 20, wantOK: true,
			want: radiocap.Channel{Band: radiocap.Band6G, CenterKHz: 5975000, WidthKHz: 20000},
		},
		{
			name: "60 ch2", band: radiocap.Band60G, channel: "2", width: 2160, wantOK: true,
			want: radiocap.Channel{Band: radiocap.Band60G, CenterKHz: 60480000, WidthKHz: 2160000},
		},
		{
			name: "S1G ch28 8MHz", band: radiocap.BandS1G, channel: "28", width: 8, wantOK: true,
			want: radiocap.Channel{Band: radiocap.BandS1G, CenterKHz: 916000, WidthKHz: 8000},
		},
		{
			name: "S1G default width", band: radiocap.BandS1G, channel: "1", wantOK: true,
			want: radiocap.Channel{Band: radiocap.BandS1G, CenterKHz: 902500, WidthKHz: 1000},
		},
		{
			name: "default width", band: radiocap.Band5G, channel: "149", wantOK: true,
			want: radiocap.Channel{Band: radiocap.Band5G, CenterKHz: 5745000, WidthKHz: 20000},
		},
		{
			name: "auto uses slot", band: radiocap.Band5G, channel: "auto", width: 80, slot: 3, wantOK: true,
			want: radiocap.Channel{Band: radiocap.Band5G, CenterKHz: 4, WidthKHz: 80000},
		},
		{
			name: "empty is auto", band: radiocap.Band2G4, channel: "", width: 20, wantOK: true,
			want: radiocap.Channel{Band: radiocap.Band2G4, CenterKHz: 1, WidthKHz: 20000},
		},
		{name: "auto without band", band: "", channel: "auto"},
		{name: "unparsable", band: radiocap.Band5G, channel: "36+"},
		{name: "negative", band: radiocap.Band5G, channel: "-1"},
		{name: "unknown band", band: radiocap.Band("9"), channel: "1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := radiogate.UCIChannel(tc.band, tc.channel, tc.width, tc.slot)
			assert.Equal(t, tc.wantOK, ok)

			if tc.wantOK {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestUCIChannel_autoSlotsAreDistinct(t *testing.T) {
	a, okA := radiogate.UCIChannel(radiocap.Band5G, "auto", 20, 0)
	b, okB := radiogate.UCIChannel(radiocap.Band5G, "auto", 20, 1)

	assert.True(t, okA)
	assert.True(t, okB)
	assert.NotEqual(t, a, b, "two radios on auto pick their own channels")
}

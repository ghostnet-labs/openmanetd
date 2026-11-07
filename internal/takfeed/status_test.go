package takfeed_test

import (
	"context"
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/takfeed"
)

// remarksText decodes the <remarks> element of a detail blob, so tests
// read the text ATAK shows rather than its XML escaping.
func remarksText(t *testing.T, detail string) string {
	t.Helper()

	dec := xml.NewDecoder(strings.NewReader("<detail>" + detail + "</detail>"))

	for {
		tok, err := dec.Token()
		require.NoError(t, err, "no <remarks> in detail")

		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "remarks" {
			var text string
			require.NoError(t, dec.DecodeElement(&text, &se))

			return text
		}
	}
}

func fullStatus() *takfeed.Status {
	return &takfeed.Status{
		Health: &takfeed.Health{
			Model: "Raspberry Pi CM5", Firmware: "24.10.2", Uptime: 26*time.Hour + 14*time.Minute,
			CPUTempC: 61.4, HaveTemp: true, LoadPct: 23.2, HaveLoad: true, MemUsedPct: 41.6,
		},
		Radios: []takfeed.RadioState{
			{Name: "halow", Band: "s1g", State: "up", FreqMHz: 915.5, NoiseDBm: -101, BusyPct: 12.4, HaveAir: true, NoiseKnown: true},
			{Name: "wifi", State: "fault"},
		},
		GPS:         &takfeed.GPSQuality{Mode: 3, SatsUsed: 9, SatsSeen: 14, HDOP: 0.92},
		Comms:       &takfeed.CommsState{TalkGroup: 2, Transmitting: true},
		Scanner:     "P25 site Eagle Mountain, control channel locked",
		Uplink:      "BLOS connected, 2 of 3 peers online",
		ChannelMove: "HaLow moves to 920 MHz at 14:05Z",
		Alerts:      []string{"Wi-Fi radio fault", "battery critical"},
	}
}

func TestNodeEvent_statusRemarks(t *testing.T) {
	ev := takfeed.NodeEvent(&takfeed.NodeInfo{
		Callsign: "manet01",
		Role:     config.TAKRoleRadio,
		Status:   fullStatus(),
	}, testNow).GetCotEvent()

	text := remarksText(t, ev.GetDetail().GetXmlDetail())

	assert.True(t, strings.HasPrefix(text, "ALERT: Wi-Fi radio fault; battery critical.\nOpenMANET radio."), text)

	for _, want := range []string{
		"\nHealth: CPU 61 C, load 23%, memory 42% used, up 1d 2h, Raspberry Pi CM5, firmware 24.10.2.",
		"\nRadio HaLow (halow): up, 915.5 MHz, noise -101 dBm, channel busy 12%.",
		"\nRadio wifi: fault.",
		"\nGPS: 3D fix, 9 of 14 satellites, HDOP 0.9.",
		"\nVoice: talk group 2, TRANSMITTING now.",
		"\nScanner: P25 site Eagle Mountain, control channel locked.",
		"\nUplink: BLOS connected, 2 of 3 peers online.",
		"\nChannel change: HaLow moves to 920 MHz at 14:05Z.",
	} {
		assert.Contains(t, text, want)
	}
}

func TestNodeEvent_partialStatus(t *testing.T) {
	tests := []struct {
		name     string
		status   *takfeed.Status
		want     []string
		wantNone []string
	}{
		{
			name:     "nil status leaves only the summary",
			status:   nil,
			want:     []string{"OpenMANET radio."},
			wantNone: []string{"ALERT", "Health", "GPS", "Voice", "\n"},
		},
		{
			name:     "empty status adds nothing",
			status:   &takfeed.Status{},
			wantNone: []string{"ALERT", "Health", "GPS", "Voice", "Scanner", "Uplink", "\n"},
		},
		{
			name:     "health with nothing known says so",
			status:   &takfeed.Status{Health: &takfeed.Health{MemUsedPct: -1}},
			want:     []string{"\nHealth: unknown."},
			wantNone: []string{"memory"},
		},
		{
			name: "radio without air readings or noise",
			status: &takfeed.Status{Radios: []takfeed.RadioState{
				{Band: "2g4", FreqMHz: 2437, BusyPct: 40, HaveAir: true},
			}},
			want:     []string{"\nRadio 2.4 GHz: 2437 MHz, channel busy 40%."},
			wantNone: []string{"noise"},
		},
		{
			name:     "no fix and no satellites",
			status:   &takfeed.Status{GPS: &takfeed.GPSQuality{Mode: 1}},
			want:     []string{"\nGPS: no fix."},
			wantNone: []string{"satellites", "HDOP"},
		},
		{
			name:   "2D fix",
			status: &takfeed.Status{GPS: &takfeed.GPSQuality{Mode: 2, SatsUsed: 4}},
			want:   []string{"\nGPS: 2D fix, 4 of 4 satellites."},
		},
		{
			name:     "idle voice",
			status:   &takfeed.Status{Comms: &takfeed.CommsState{TalkGroup: 1}},
			want:     []string{"\nVoice: talk group 1."},
			wantNone: []string{"TRANSMITTING"},
		},
		{
			name:   "short uptime",
			status: &takfeed.Status{Health: &takfeed.Health{Uptime: 3*time.Hour + 7*time.Minute, MemUsedPct: -1}},
			want:   []string{"up 3h 7m."},
		},
		{
			name:   "minutes uptime",
			status: &takfeed.Status{Health: &takfeed.Health{Uptime: 9 * time.Minute, MemUsedPct: -1}},
			want:   []string{"up 9m."},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := takfeed.NodeEvent(&takfeed.NodeInfo{Callsign: "manet01", Status: tc.status}, testNow).GetCotEvent()
			text := remarksText(t, ev.GetDetail().GetXmlDetail())

			for _, w := range tc.want {
				assert.Contains(t, text, w)
			}

			for _, w := range tc.wantNone {
				assert.NotContains(t, text, w)
			}
		})
	}
}

func TestLinkEvent_signal(t *testing.T) {
	l := &takfeed.Link{
		UID: "manet-link-a-b", Algorithm: "BATMAN_V",
		A: takfeed.Endpoint{Name: "a", Lat: 40.4, Lon: -112}, B: takfeed.Endpoint{Name: "b", Lat: 40.5, Lon: -112.1},
		Metric: 30, SignalDBm: -63, TxBitrateKbps: 32500, HaveSignal: true,
	}

	text := remarksText(t, takfeed.LinkEvent(l, testNow, time.Minute).GetCotEvent().GetDetail().GetXmlDetail())
	assert.Contains(t, text, "strong (metric 30.00, BATMAN_V), signal -63 dBm, sending at 32.5 Mbit/s.")

	l.TxBitrateKbps = 0
	text = remarksText(t, takfeed.LinkEvent(l, testNow, time.Minute).GetCotEvent().GetDetail().GetXmlDetail())
	assert.Contains(t, text, "signal -63 dBm.")
	assert.NotContains(t, text, "sending")

	l.HaveSignal = false
	text = remarksText(t, takfeed.LinkEvent(l, testNow, time.Minute).GetCotEvent().GetDetail().GetXmlDetail())
	assert.NotContains(t, text, "signal")
}

func TestPublishOnce_nodeStatusAndLinkSignals(t *testing.T) {
	h := newHarness(t, "")
	status := &fakeStatus{st: *fullStatus()}
	h.opts.Status = status
	// bb is matched by its edge MAC; cc's link is a BLOS tunnel, so its
	// radio signal must not be shown.
	h.opts.Signals = &fakeSignals{signals: map[string]takfeed.StationSignal{
		"bb": {SignalDBm: -58, TxBitrateKbps: 6500},
		"cc": {SignalDBm: -80},
	}}
	p := takfeed.New(h.opts)

	p.PublishOnce(context.Background())

	nodes := byType(h.sender.events(t), takfeed.TypeRadio)
	require.Len(t, nodes, 1)
	assert.Contains(t, remarksText(t, nodes[0].GetDetail().GetXmlDetail()), "Voice: talk group 2")
	assert.Equal(t, 1, status.getCalls())

	links := byType(h.sender.events(t), takfeed.TypeLink)
	require.Len(t, links, 2)

	texts := map[string]string{}
	for _, l := range links {
		texts[l.GetUid()] = remarksText(t, l.GetDetail().GetXmlDetail())
	}

	assert.Contains(t, texts["manet-link-manet01-manet02"], "signal -58 dBm, sending at 6.5 Mbit/s")
	assert.NotContains(t, texts["manet-link-manet01-manet03"], "signal")
}

func TestPublishOnce_linkSignalBySecondaryMAC(t *testing.T) {
	h := newHarness(t, "")
	h.topo.topo.Nodes[1].SecondaryMacs = []string{"b2"}
	h.opts.Signals = &fakeSignals{signals: map[string]takfeed.StationSignal{"b2": {SignalDBm: -70}}}
	p := takfeed.New(h.opts)

	p.PublishOnce(context.Background())

	for _, l := range byType(h.sender.events(t), takfeed.TypeLink) {
		if l.GetUid() == "manet-link-manet01-manet02" {
			assert.Contains(t, remarksText(t, l.GetDetail().GetXmlDetail()), "signal -70 dBm")

			return
		}
	}

	t.Fatal("manet01-manet02 link not published")
}

package takfeed_test

import (
	"strings"
	"testing"
	"time"

	"github.com/coreywagehoft/go-tak/pkg/cot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/scanner/aircraft"
	"github.com/openmanet/openmanetd/internal/takfeed"
)

var testNow = time.Date(2026, 11, 7, 1, 30, 0, 0, time.UTC) //nolint:gochecknoglobals // test constant

func TestNodeEvent_roles(t *testing.T) {
	tests := []struct {
		name, role, wantType, wantWord string
	}{
		{name: "radio", role: config.TAKRoleRadio, wantType: takfeed.TypeRadio, wantWord: "OpenMANET radio"},
		{name: "mast", role: config.TAKRoleMast, wantType: takfeed.TypeMast, wantWord: "OpenMANET mast"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := takfeed.NodeEvent(&takfeed.NodeInfo{
				Callsign: "manet01",
				Role:     tc.role,
				Pos:      takfeed.Position{Lat: 40.4, Lon: -112.0, HAE: 1500, CE: 4},
			}, testNow).GetCotEvent()

			assert.Equal(t, tc.wantType, ev.GetType())
			assert.Equal(t, "manet01", ev.GetUid())
			assert.Equal(t, "manet01", ev.GetDetail().GetContact().GetCallsign())
			assert.InDelta(t, 4.0, ev.GetCe(), 1e-9)
			assert.InDelta(t, float64(cot.NotNum), ev.GetLe(), 1e-9, "unknown LE must be NotNum, not 0 m")
			assert.Contains(t, ev.GetDetail().GetXmlDetail(), tc.wantWord)
			assert.Equal(t, cot.TimeToMillis(testNow.Add(5*time.Minute)), ev.GetStaleTime())
		})
	}
}

func TestNodeEvent_healthRemarks(t *testing.T) {
	ev := takfeed.NodeEvent(&takfeed.NodeInfo{
		Callsign:  "manet01",
		Role:      config.TAKRoleRadio,
		Platform:  "Raspberry Pi CM5",
		IP:        "10.41.113.1",
		Battery:   &takfeed.Battery{Percent: 81.6, Volts: 11.84},
		Neighbors: 1,
		MeshNodes: 4,
		Gateway:   true,
		HaveMesh:  true,
	}, testNow).GetCotEvent()

	xml := ev.GetDetail().GetXmlDetail()
	assert.Contains(t, xml, "Battery 82% 11.8 V")
	assert.Contains(t, xml, "1 direct mesh link, 4 nodes on the mesh")
	assert.Contains(t, xml, "Internet gateway")
	assert.Contains(t, xml, "Web UI http://10.41.113.1")
	assert.Equal(t, uint32(82), ev.GetDetail().GetStatus().GetBattery())
	assert.Equal(t, "Raspberry Pi CM5 (OpenMANET)", ev.GetDetail().GetTakv().GetPlatform())
}

func TestNodeEvent_unknownBatteryAndMesh(t *testing.T) {
	ev := takfeed.NodeEvent(&takfeed.NodeInfo{
		Callsign: "manet01",
		Battery:  &takfeed.Battery{Percent: -1, Volts: 12.1},
	}, testNow).GetCotEvent()

	xml := ev.GetDetail().GetXmlDetail()
	assert.Contains(t, xml, "Battery 12.1 V")
	assert.NotContains(t, xml, "%")
	assert.NotContains(t, xml, "mesh link", "mesh counts are hidden until the first topology read")
	assert.Nil(t, ev.GetDetail().GetStatus(), "no battery status when the percent is unknown")
}

func TestNodeEvent_escapesRemarks(t *testing.T) {
	ev := takfeed.NodeEvent(&takfeed.NodeInfo{Callsign: "a", IP: `1.2.3.4"/><x>`}, testNow).GetCotEvent()

	assert.NotContains(t, ev.GetDetail().GetXmlDetail(), "<x>")
}

func TestLinkEvent_qualityColors(t *testing.T) {
	tests := []struct {
		name      string
		algorithm string
		metric    float64
		wantColor string
		wantWord  string
	}{
		{"IV strong", "BATMAN_IV", 1.1, "-16711936", "strong"},
		{"IV ok", "BATMAN_IV", 1.5, "-256", "ok"},
		{"IV weak", "BATMAN_IV", 2.5, "-65536", "weak"},
		{"V strong", "BATMAN_V", 25, "-16711936", "strong"},
		{"V ok", "BATMAN_V", 8, "-256", "ok"},
		{"V weak", "BATMAN_V", 2, "-65536", "weak"},
		{"unknown", "BATMAN_V", 0, "-8355712", "quality unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := takfeed.LinkEvent(&takfeed.Link{
				UID:       "manet-link-a-b",
				Algorithm: tc.algorithm,
				A:         takfeed.Endpoint{Name: "a", Lat: 40, Lon: -112},
				B:         takfeed.Endpoint{Name: "b", Lat: 41, Lon: -111},
				Metric:    tc.metric,
			}, testNow, 90*time.Second).GetCotEvent()

			xml := ev.GetDetail().GetXmlDetail()
			assert.Equal(t, takfeed.TypeLink, ev.GetType())
			assert.Contains(t, xml, `<strokeColor value="`+tc.wantColor+`"/>`)
			assert.Contains(t, xml, "Mesh link a - b: "+tc.wantWord)
			assert.Contains(t, xml, `<link point="40.0000000,-112.0000000"/><link point="41.0000000,-111.0000000"/>`)
			assert.Contains(t, xml, `<strokeStyle value="solid"/>`)
			assert.InDelta(t, 40.5, ev.GetLat(), 1e-9, "event point is the midpoint")
		})
	}
}

func TestLinkEvent_blosDashedAndMinimumStale(t *testing.T) {
	ev := takfeed.LinkEvent(&takfeed.Link{UID: "x", BLOS: true}, testNow, time.Second).GetCotEvent()

	assert.Contains(t, ev.GetDetail().GetXmlDetail(), `<strokeStyle value="dashed"/>`)
	assert.Contains(t, ev.GetDetail().GetXmlDetail(), "beyond line of sight")
	assert.Equal(t, cot.TimeToMillis(testNow.Add(time.Minute)), ev.GetStaleTime())
}

func TestAircraftEvent(t *testing.T) {
	a := aircraft.Aircraft{
		ICAO: "a1b2c3", Callsign: "N123AB  ", Squawk: "7700",
		Lat: 40.5, Lon: -112.1, GS: 100, Track: 270, AltFt: 4500,
		HasPos: true, HasAlt: true, HasGS: true, HasTrack: true, Sources: aircraft.SourceBoth,
	}

	ev := takfeed.AircraftEvent(&a, "manet01", testNow).GetCotEvent()

	assert.Equal(t, takfeed.TypeAircraft, ev.GetType())
	assert.Equal(t, "ICAO-A1B2C3", ev.GetUid(), "uid must match adsbcot so both feeds share one marker")
	assert.Equal(t, "N123AB", ev.GetDetail().GetContact().GetCallsign())
	assert.InDelta(t, 4500*0.3048, ev.GetHae(), 1e-6)
	assert.InDelta(t, 100*0.514444, ev.GetDetail().GetTrack().GetSpeed(), 1e-6)
	assert.InDelta(t, 270, ev.GetDetail().GetTrack().GetCourse(), 1e-9)

	xml := ev.GetDetail().GetXmlDetail()
	assert.True(t, strings.HasPrefix(xml, "<remarks>EMERGENCY (general emergency, squawk 7700)."))
	assert.Contains(t, xml, "4500 ft, 100 kt")
	assert.Contains(t, xml, "Heard by manet01 on 1090 MHz and 978 MHz.")
	assert.Equal(t, cot.TimeToMillis(testNow.Add(time.Minute)), ev.GetStaleTime())
}

func TestAircraftEvent_noCallsignOnGround(t *testing.T) {
	a := aircraft.Aircraft{ICAO: "abc123", HasPos: true, HasAlt: true, OnGround: true, AltFt: 0, Sources: aircraft.Source978}

	ev := takfeed.AircraftEvent(&a, "n", testNow).GetCotEvent()

	assert.Equal(t, "ABC123", ev.GetDetail().GetContact().GetCallsign())
	assert.InDelta(t, float64(cot.NotNum), ev.GetHae(), 1e-9)
	assert.Contains(t, ev.GetDetail().GetXmlDetail(), "on the ground")
	assert.Contains(t, ev.GetDetail().GetXmlDetail(), "978 MHz UAT")
}

func TestJamEvent_states(t *testing.T) {
	pos := takfeed.Position{Lat: 40.4, Lon: -112}
	jam := takfeed.Jam{
		ID: "morse0-915", State: takfeed.JamActive, Radio: "morse0", Band: "s1g", FreqMHz: 915,
		NoiseDBm: -72, BaselineNoiseDBm: -98, BusyPct: 95, UnexplainedBusyPct: 80,
		NoiseKnown: true, Started: testNow.Add(-time.Minute),
	}

	ev := takfeed.JamEvent(&jam, "manet01", pos, testNow, 30*time.Second).GetCotEvent()

	assert.Equal(t, takfeed.TypeJamActive, ev.GetType())
	assert.Equal(t, "manet01-jam-morse0-915", ev.GetUid())
	assert.Equal(t, "JAMMING 915 MHz", ev.GetDetail().GetContact().GetCallsign())
	assert.InDelta(t, 1000, ev.GetCe(), 1e-9, "the uncertainty ring shows the jammer is near, not at, the node")
	assert.Contains(t, ev.GetDetail().GetXmlDetail(), "detected by manet01 (HaLow morse0). Noise -72 dBm vs baseline -98 dBm, channel busy 95% (80% not explained by traffic), since 01:29Z")
	assert.Equal(t, cot.TimeToMillis(testNow.Add(time.Minute)), ev.GetStaleTime())

	jam.State = takfeed.JamSuspected
	ev = takfeed.JamEvent(&jam, "manet01", pos, testNow, 30*time.Second).GetCotEvent()

	assert.Equal(t, takfeed.TypeJamSuspected, ev.GetType())
	assert.Equal(t, "Possible jamming 915 MHz", ev.GetDetail().GetContact().GetCallsign())

	jam.NoiseKnown = false
	ev = takfeed.JamEvent(&jam, "manet01", pos, testNow, 30*time.Second).GetCotEvent()

	assert.NotContains(t, ev.GetDetail().GetXmlDetail(), "dBm", "no noise figures when the driver reports none")
}

func TestDeleteEvent(t *testing.T) {
	ev := takfeed.DeleteEvent("manet01-jam-1", testNow).GetCotEvent()

	require.Equal(t, takfeed.TypeDelete, ev.GetType())
	assert.Equal(t, `<link uid="manet01-jam-1" relation="none" type="none"/><__forcedelete/>`, ev.GetDetail().GetXmlDetail())
}

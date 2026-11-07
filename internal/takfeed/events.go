package takfeed

import (
	"encoding/xml"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/coreywagehoft/go-tak/pkg/cot"
	"github.com/coreywagehoft/go-tak/pkg/cotproto"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/scanner/aircraft"
)

// CoT types. Each maps to a MIL-STD-2525 symbol ATAK draws natively, so
// no plugin or icon pack is needed on the phone.
const (
	// TypeRadio is a friendly ground radio unit (person-worn node).
	TypeRadio = "a-f-G-U-U-S-R"
	// TypeMast is a friendly telecommunications tower (fixed mast).
	TypeMast = "a-f-G-I-U-T-com-tow"
	// TypeLink is a drawn polyline (ATAK "freehand" shape).
	TypeLink = "u-d-f"
	// TypeAircraft is a neutral civilian fixed-wing aircraft; ADS-B does
	// not say whether an aircraft is military.
	TypeAircraft = "a-n-A-C-F"
	// TypeJamSuspected is an unknown-affiliation ground jammer (yellow).
	TypeJamSuspected = "a-u-G-U-U-M-S-E-J"
	// TypeJamActive is a hostile ground jammer (red).
	TypeJamActive = "a-h-G-U-U-M-S-E-J"
	// TypeDelete removes a map item from every ATAK that has it.
	TypeDelete = "t-x-d-d"
)

// Stale times. ATAK hides an item once it goes stale, so each one is a
// few publish periods long: a lost node fades out on its own.
const (
	nodeStale     = 5 * time.Minute
	aircraftStale = 60 * time.Second
	minItemStale  = 60 * time.Second
	// jamCE is the circular error (m) put on a jamming marker. The marker
	// sits on the detecting node; the jammer is somewhere within radio
	// range of it, and ATAK draws CE as an uncertainty ring.
	jamCE = 1000.0
)

// ATAK colors are signed 32-bit ARGB.
const (
	colorGreen  = -16711936 // 0xFF00FF00
	colorYellow = -256      // 0xFFFFFF00
	colorRed    = -65536    // 0xFFFF0000
	colorGray   = -8355712  // 0xFF808080
)

const (
	knotsToMS = 0.514444
	feetToM   = 0.3048
)

// Position is a GNSS fix in WGS84. HAE is height above the ellipsoid in
// meters; CE and LE are horizontal and vertical error in meters.
type Position struct {
	Time   time.Time
	Lat    float64
	Lon    float64
	HAE    float64
	CE     float64
	LE     float64
	Speed  float64 // m/s
	Course float64 // degrees true
}

// Battery is the node's pack state. Percent is negative when unknown.
type Battery struct {
	Percent float32
	Volts   float32
}

// NodeInfo is everything this node shows about itself.
type NodeInfo struct {
	Battery *Battery
	// Status is the rest of the node's state; nil leaves it out.
	Status    *Status
	Callsign  string
	Role      string
	Platform  string
	IP        string
	Pos       Position
	Neighbors int
	MeshNodes int
	Gateway   bool
	// HaveMesh is false until the first topology read; the mesh counts
	// are left out of the remarks until then.
	HaveMesh bool
}

// Endpoint is one end of a mesh link.
type Endpoint struct {
	Name string
	Lat  float64
	Lon  float64
}

// Link is one direct mesh link between this node and a neighbor.
type Link struct {
	UID       string
	Algorithm string
	A         Endpoint
	B         Endpoint
	Metric    float64
	// SignalDBm and TxBitrateKbps come from this node's station list;
	// they are left out when HaveSignal is false (a BLOS link, or a
	// neighbor heard only through another interface).
	SignalDBm     int
	TxBitrateKbps int
	HaveSignal    bool
	BLOS          bool
}

// JamState is how sure a detector is about a jamming episode.
type JamState int

// Jam states.
const (
	JamSuspected JamState = iota + 1
	JamActive
)

// Jam is one active jamming episode seen by this node's radios. Band is
// "s1g", "2g4", "5g" or "6g". UnexplainedBusyPct is busy time the radio
// spent neither receiving nor sending, the jamming signature.
type Jam struct {
	Started            time.Time
	ID                 string
	Radio              string
	Band               string
	FreqMHz            float64
	NoiseDBm           float64
	BaselineNoiseDBm   float64
	BusyPct            float64
	UnexplainedBusyPct float64
	State              JamState
	// NoiseKnown is false when the driver reports no noise floor; the
	// noise figures are then left out.
	NoiseKnown bool
}

func baseEvent(typ, uid string, now time.Time, stale time.Duration) *cotproto.CotEvent {
	return &cotproto.CotEvent{
		Type:      typ,
		Uid:       uid,
		SendTime:  cot.TimeToMillis(now),
		StartTime: cot.TimeToMillis(now),
		StaleTime: cot.TimeToMillis(now.Add(stale)),
		How:       cot.HowDefault,
		Hae:       cot.NotNum,
		Ce:        cot.NotNum,
		Le:        cot.NotNum,
	}
}

// NodeEvent builds this node's own marker.
func NodeEvent(n *NodeInfo, now time.Time) *cotproto.TakMessage {
	typ := TypeRadio
	if n.Role == config.TAKRoleMast {
		typ = TypeMast
	}

	ev := baseEvent(typ, n.Callsign, now, nodeStale)
	ev.Lat, ev.Lon, ev.Hae = n.Pos.Lat, n.Pos.Lon, n.Pos.HAE
	ev.Ce, ev.Le = errOrNaN(n.Pos.CE), errOrNaN(n.Pos.LE)

	platform := "OpenMANET"
	if n.Platform != "" {
		platform = n.Platform + " (OpenMANET)"
	}

	ev.Detail = &cotproto.Detail{
		XmlDetail:         remarksXML(nodeRemarks(n)),
		Contact:           &cotproto.Contact{Callsign: n.Callsign},
		Group:             &cotproto.Group{Name: "Magenta", Role: "MANET Radio"},
		Takv:              &cotproto.Takv{Device: n.Callsign, Platform: platform},
		Track:             &cotproto.Track{Speed: n.Pos.Speed, Course: n.Pos.Course},
		PrecisionLocation: &cotproto.PrecisionLocation{Geopointsrc: "GPS", Altsrc: "GPS"},
	}

	if n.Battery != nil && n.Battery.Percent >= 0 {
		ev.Detail.Status = &cotproto.Status{Battery: uint32(math.Round(float64(min(n.Battery.Percent, 100))))}
	}

	return &cotproto.TakMessage{CotEvent: ev}
}

func nodeRemarks(n *NodeInfo) string {
	var b strings.Builder

	b.Grow(640)
	writeAlerts(&b, n.Status)

	if n.Role == config.TAKRoleMast {
		b.WriteString("OpenMANET mast")
	} else {
		b.WriteString("OpenMANET radio")
	}

	if n.Battery != nil {
		b.WriteString(". Battery ")

		if n.Battery.Percent >= 0 {
			b.WriteString(strconv.Itoa(int(math.Round(float64(n.Battery.Percent)))))
			b.WriteString("% ")
		}

		b.WriteString(strconv.FormatFloat(float64(n.Battery.Volts), 'f', 1, 32))
		b.WriteString(" V")
	}

	if n.HaveMesh {
		b.WriteString(". ")
		b.WriteString(plural(n.Neighbors, "direct mesh link"))
		b.WriteString(", ")
		b.WriteString(plural(n.MeshNodes, "node"))
		b.WriteString(" on the mesh")

		if n.Gateway {
			b.WriteString(". Internet gateway")
		}
	}

	if n.IP != "" {
		b.WriteString(". Web UI http://")
		b.WriteString(n.IP)
	}

	b.WriteString(".")
	writeStatus(&b, n.Status)

	return b.String()
}

// LinkEvent builds a line between the two ends of a mesh link, colored
// by link quality with the same thresholds as the web UI topology map.
// BLOS (tunneled) links are dashed.
func LinkEvent(l *Link, now time.Time, stale time.Duration) *cotproto.TakMessage {
	ev := baseEvent(TypeLink, l.UID, now, max(stale, minItemStale))
	ev.Lat = (l.A.Lat + l.B.Lat) / 2
	ev.Lon = (l.A.Lon + l.B.Lon) / 2

	quality, color := linkQuality(l.Metric, l.Algorithm)
	style := "solid"

	if l.BLOS {
		style = "dashed"
	}

	label := l.A.Name + " - " + l.B.Name

	var b strings.Builder

	b.Grow(384)
	writeLinkPoint(&b, l.A)
	writeLinkPoint(&b, l.B)
	b.WriteString(`<strokeColor value="`)
	b.WriteString(strconv.Itoa(color))
	b.WriteString(`"/><strokeWeight value="3.0"/><strokeStyle value="`)
	b.WriteString(style)
	b.WriteString(`"/><labels_on value="false"/><contact callsign="`)
	escape(&b, label)
	b.WriteString(`"/>`)

	remarks := "Mesh link " + label + ": " + quality
	if l.Metric > 0 {
		remarks += " (metric " + strconv.FormatFloat(l.Metric, 'f', 2, 64)
		if l.Algorithm != "" {
			remarks += ", " + l.Algorithm
		}

		remarks += ")"
	}

	if l.HaveSignal {
		remarks += ", signal " + strconv.Itoa(l.SignalDBm) + " dBm"
		if l.TxBitrateKbps > 0 {
			remarks += ", sending at " + strconv.FormatFloat(float64(l.TxBitrateKbps)/1000, 'f', 1, 64) + " Mbit/s"
		}
	}

	if l.BLOS {
		remarks += ", beyond line of sight tunnel"
	}

	b.WriteString(remarksXML(remarks + "."))

	ev.Detail = &cotproto.Detail{XmlDetail: b.String()}

	return &cotproto.TakMessage{CotEvent: ev}
}

func writeLinkPoint(b *strings.Builder, e Endpoint) {
	b.WriteString(`<link point="`)
	b.WriteString(strconv.FormatFloat(e.Lat, 'f', 7, 64))
	b.WriteByte(',')
	b.WriteString(strconv.FormatFloat(e.Lon, 'f', 7, 64))
	b.WriteString(`"/>`)
}

// algoBatmanV is batadv-vis's name for the BATMAN_V routing algorithm.
const algoBatmanV = "BATMAN_V"

// linkQuality buckets a batman-adv metric. BATMAN_V reports a
// throughput-derived value (higher is better); BATMAN_IV reports 255/TQ
// (lower is better). Thresholds match frontend TopologyMap qualityClass.
func linkQuality(metric float64, algorithm string) (string, int) {
	switch {
	case math.IsNaN(metric) || metric <= 0:
		return "quality unknown", colorGray
	case algorithm == algoBatmanV && metric >= 20, algorithm != algoBatmanV && metric <= 1.3:
		return "strong", colorGreen
	case algorithm == algoBatmanV && metric >= 5, algorithm != algoBatmanV && metric <= 1.8:
		return "ok", colorYellow
	default:
		return "weak", colorRed
	}
}

// AircraftUID is the ATAK uid for an aircraft. It matches the optional
// adsbcot package ("ICAO-<hex>"), so a node running both shows one
// marker per aircraft instead of two.
func AircraftUID(icao string) string {
	return "ICAO-" + strings.ToUpper(icao)
}

// AircraftEvent builds an aircraft marker. heardBy is this node's
// callsign. The caller only passes aircraft with a position.
func AircraftEvent(a *aircraft.Aircraft, heardBy string, now time.Time) *cotproto.TakMessage {
	ev := baseEvent(TypeAircraft, AircraftUID(a.ICAO), now, aircraftStale)
	ev.Lat, ev.Lon = a.Lat, a.Lon

	if a.HasAlt && !a.OnGround {
		ev.Hae = float64(a.AltFt) * feetToM
	}

	name := strings.TrimSpace(a.Callsign)
	if name == "" {
		name = strings.ToUpper(a.ICAO)
	}

	track := &cotproto.Track{}
	if a.HasGS {
		track.Speed = a.GS * knotsToMS
	}

	if a.HasTrack {
		track.Course = a.Track
	}

	var b strings.Builder

	b.Grow(160)

	if emergency := squawkEmergency(a.Squawk); emergency != "" {
		b.WriteString("EMERGENCY (")
		b.WriteString(emergency)
		b.WriteString("). ")
	}

	b.WriteString("Aircraft ")
	b.WriteString(name)
	b.WriteString(", ICAO ")
	b.WriteString(strings.ToUpper(a.ICAO))

	switch {
	case a.OnGround:
		b.WriteString(", on the ground")
	case a.HasAlt:
		b.WriteString(", ")
		b.WriteString(strconv.Itoa(int(a.AltFt)))
		b.WriteString(" ft")
	}

	if a.HasGS {
		b.WriteString(", ")
		b.WriteString(strconv.Itoa(int(math.Round(a.GS))))
		b.WriteString(" kt")
	}

	if a.Squawk != "" {
		b.WriteString(", squawk ")
		b.WriteString(a.Squawk)
	}

	b.WriteString(". Heard by ")
	b.WriteString(heardBy)
	b.WriteString(" on ")
	b.WriteString(sourceLabel(a.Sources))
	b.WriteString(".")

	ev.Detail = &cotproto.Detail{
		XmlDetail: remarksXML(b.String()),
		Contact:   &cotproto.Contact{Callsign: name},
		Track:     track,
	}

	return &cotproto.TakMessage{CotEvent: ev}
}

func squawkEmergency(squawk string) string {
	switch squawk {
	case "7500":
		return "hijack, squawk 7500"
	case "7600":
		return "radio failure, squawk 7600"
	case "7700":
		return "general emergency, squawk 7700"
	default:
		return ""
	}
}

func sourceLabel(s aircraft.Source) string {
	switch s {
	case aircraft.Source978:
		return "978 MHz UAT"
	case aircraft.SourceBoth:
		return "1090 MHz and 978 MHz"
	default:
		return "1090 MHz ADS-B"
	}
}

// JamUID is the ATAK uid of a jamming marker.
func JamUID(callsign, id string) string {
	return callsign + "-jam-" + id
}

// JamEvent builds a jamming marker at the detecting node's position.
func JamEvent(j *Jam, callsign string, pos Position, now time.Time, stale time.Duration) *cotproto.TakMessage {
	typ := TypeJamSuspected
	if j.State == JamActive {
		typ = TypeJamActive
	}

	ev := baseEvent(typ, JamUID(callsign, j.ID), now, max(stale, minItemStale))
	ev.Lat, ev.Lon, ev.Hae = pos.Lat, pos.Lon, pos.HAE
	ev.Ce = jamCE

	freq := strconv.FormatFloat(j.FreqMHz, 'f', -1, 64) + " MHz"
	title := "JAMMING " + freq

	if j.State != JamActive {
		title = "Possible jamming " + freq
	}

	var b strings.Builder

	b.Grow(256)
	b.WriteString(title)
	b.WriteString(" detected by ")
	b.WriteString(callsign)

	if label := bandLabel(j.Band); label != "" || j.Radio != "" {
		b.WriteString(" (")
		b.WriteString(strings.TrimSpace(label + " " + j.Radio))
		b.WriteString(")")
	}

	b.WriteString(".")

	if j.NoiseKnown {
		b.WriteString(" Noise ")
		b.WriteString(strconv.FormatFloat(j.NoiseDBm, 'f', 0, 64))
		b.WriteString(" dBm vs baseline ")
		b.WriteString(strconv.FormatFloat(j.BaselineNoiseDBm, 'f', 0, 64))
		b.WriteString(" dBm,")
	}

	b.WriteString(" channel busy ")
	b.WriteString(strconv.FormatFloat(j.BusyPct, 'f', 0, 64))
	b.WriteString("%")

	if j.UnexplainedBusyPct > 0 {
		b.WriteString(" (")
		b.WriteString(strconv.FormatFloat(j.UnexplainedBusyPct, 'f', 0, 64))
		b.WriteString("% not explained by traffic)")
	}

	if !j.Started.IsZero() {
		b.WriteString(", since ")
		b.WriteString(j.Started.UTC().Format("15:04Z"))
	}

	b.WriteString(". Marker is on the detecting node; the source is within radio range of it.")

	ev.Detail = &cotproto.Detail{
		XmlDetail: remarksXML(b.String()),
		Contact:   &cotproto.Contact{Callsign: title},
	}

	return &cotproto.TakMessage{CotEvent: ev}
}

func bandLabel(band string) string {
	switch band {
	case "s1g":
		return "HaLow"
	case "2g4":
		return "2.4 GHz"
	case "5g":
		return "5 GHz"
	case "6g":
		return "6 GHz"
	default:
		return band
	}
}

// DeleteEvent tells every ATAK to drop the item with uid now, instead of
// waiting for it to go stale.
func DeleteEvent(uid string, now time.Time) *cotproto.TakMessage {
	ev := baseEvent(TypeDelete, uid+"-delete", now, 20*time.Second)

	var b strings.Builder

	b.Grow(len(uid) + 64)
	b.WriteString(`<link uid="`)
	escape(&b, uid)
	b.WriteString(`" relation="none" type="none"/><__forcedelete/>`)

	ev.Detail = &cotproto.Detail{XmlDetail: b.String()}

	return &cotproto.TakMessage{CotEvent: ev}
}

func remarksXML(text string) string {
	var b strings.Builder

	b.Grow(len(text) + 20)
	b.WriteString("<remarks>")
	escape(&b, text)
	b.WriteString("</remarks>")

	return b.String()
}

func escape(b *strings.Builder, s string) {
	// xml.EscapeText only fails when the writer does; strings.Builder
	// never does.
	_ = xml.EscapeText(b, []byte(s))
}

func errOrNaN(v float64) float64 {
	if v <= 0 || math.IsNaN(v) {
		return cot.NotNum
	}

	return v
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}

	return strconv.Itoa(n) + " " + noun + "s"
}

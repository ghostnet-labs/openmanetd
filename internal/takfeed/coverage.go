package takfeed

import (
	"strconv"
	"strings"
	"time"

	"github.com/coreywagehoft/go-tak/pkg/cotproto"

	"github.com/openmanet/openmanetd/internal/coverage"
)

// TypeCoverage is a filled rectangle (ATAK "rectangle" shape).
const TypeCoverage = "u-d-r"

// Coverage publishing. A few cells go out per tick, changed cells first,
// so the feed's multicast load stays small however big the map grows.
const (
	coveragePerTick = 8
	// minCoverageStale keeps a cell on the map well past one full cycle.
	minCoverageStale = 30 * time.Minute
)

// Coverage cell signal thresholds (dBm of the best heard station).
const (
	coverageGoodDBm = -70
	coverageOkDBm   = -82
	// coverageDeadPct is the no-link share at which a cell counts as dead.
	coverageDeadPct = 50
)

// Half-transparent fills so the basemap shows through.
const (
	fillGreen  = -2147418368 // 0x8000FF00
	fillYellow = -2130706688 // 0x80FFFF00
	fillRed    = -2130771968 // 0x80FF0000
)

// CoverageMap is this node's coverage grid (*coverage.Grid).
type CoverageMap interface {
	Record(now time.Time, lat, lon float64, s coverage.Sample) coverage.Key
	NextToPublish(dst []coverage.Cell, n int) []coverage.Cell
	Len() int
	CellMeters() float64
}

// CoverageUID names one node's cell. Each node publishes its own cells,
// so two nodes that measured the same spot both show.
func CoverageUID(callsign string, k coverage.Key) string {
	return callsign + "-cov-" + strconv.FormatInt(int64(k.Row), 10) + "_" + strconv.FormatInt(int64(k.Col), 10)
}

// coverageGrade is a cell's color and a one-word verdict.
func coverageGrade(c *coverage.Cell) (int, string) {
	switch {
	case !c.HasLink() || c.NoLinkPct() >= coverageDeadPct:
		return fillRed, "no mesh link"
	case c.SignalDBm >= coverageGoodDBm && c.NoLinkSamples == 0:
		return fillGreen, "good"
	case c.SignalDBm >= coverageOkDBm:
		return fillYellow, "fair"
	default:
		return fillRed, "weak"
	}
}

// CoverageEvent builds one coverage heatmap cell measured by callsign.
func CoverageEvent(c *coverage.Cell, cellM float64, callsign string, now time.Time, stale time.Duration) *cotproto.TakMessage {
	south, west, north, east := coverage.Bounds(c.Key, cellM)

	ev := baseEvent(TypeCoverage, CoverageUID(callsign, c.Key), now, max(stale, minCoverageStale))
	ev.Lat, ev.Lon = (south+north)/2, (west+east)/2

	fill, grade := coverageGrade(c)

	var b strings.Builder

	b.Grow(512)

	for _, p := range [4][2]float64{{north, west}, {north, east}, {south, east}, {south, west}} {
		writeLinkPoint(&b, Endpoint{Lat: p[0], Lon: p[1]})
	}

	b.WriteString(`<strokeColor value="`)
	b.WriteString(strconv.Itoa(fill))
	b.WriteString(`"/><strokeWeight value="0.0"/><fillColor value="`)
	b.WriteString(strconv.Itoa(fill))
	b.WriteString(`"/><labels_on value="false"/><contact callsign="`)
	escape(&b, "Coverage "+callsign)
	b.WriteString(`"/>`)
	b.WriteString(remarksXML(coverageRemarks(c, callsign, grade)))

	ev.Detail = &cotproto.Detail{XmlDetail: b.String()}

	return &cotproto.TakMessage{CotEvent: ev}
}

func coverageRemarks(c *coverage.Cell, callsign, grade string) string {
	var b strings.Builder

	b.Grow(200)
	b.WriteString("Coverage here from ")
	b.WriteString(callsign)
	b.WriteString(": ")
	b.WriteString(grade)

	if c.HasLink() {
		b.WriteString(". Best signal ")
		b.WriteString(strconv.FormatFloat(c.SignalDBm, 'f', 0, 64))
		b.WriteString(" dBm, up to ")
		b.WriteString(plural(c.MaxLinks, "mesh station"))
		b.WriteString(" heard")

		if c.NoLinkSamples > 0 {
			b.WriteString(", no link ")
			b.WriteString(strconv.FormatFloat(c.NoLinkPct(), 'f', 0, 64))
			b.WriteString("% of the time")
		}
	}

	b.WriteString(". ")
	b.WriteString(plural(c.Samples, "reading"))
	b.WriteString(", last ")
	b.WriteString(c.LastSeen.UTC().Format("2006-01-02 15:04Z"))
	b.WriteByte('.')

	return b.String()
}

// coverageSample turns a station list into a coverage sample.
func coverageSample(signals map[string]StationSignal) coverage.Sample {
	s := coverage.Sample{Links: len(signals)}

	first := true
	for _, sig := range signals {
		if first || sig.SignalDBm > s.BestSignalDBm {
			s.BestSignalDBm = sig.SignalDBm
			first = false
		}
	}

	return s
}

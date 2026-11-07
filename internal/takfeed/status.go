package takfeed

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// StatusSource reports everything else the node knows about itself:
// health, radios, GNSS quality, comms, scanner and uplink state. It is
// read once per tick, so it must be cheap and must not block.
type StatusSource interface {
	NodeStatus(ctx context.Context) Status
}

// SignalSource reports the received signal of each directly heard mesh
// station, keyed by lowercase MAC address.
type SignalSource interface {
	StationSignals(ctx context.Context) map[string]StationSignal
}

// StationSignal is one directly heard mesh station.
type StationSignal struct {
	SignalDBm int
	// TxBitrateKbps is this node's current transmit rate to the station;
	// zero when unknown.
	TxBitrateKbps int
}

// Status is the node state shown in its marker beyond battery and mesh
// counts. Every part is optional: a nil pointer or empty string means
// the node does not know it, and the remarks leave it out.
type Status struct {
	Health  *Health
	GPS     *GPSQuality
	Comms   *CommsState
	Scanner string
	Uplink  string
	// ChannelMove describes a pending or running mesh channel change.
	ChannelMove string
	Radios      []RadioState
	// Alerts are problems worth a glance, such as a faulted radio or a
	// critical battery. They lead the remarks.
	Alerts []string
}

// Health is the node's system state.
type Health struct {
	Model    string
	Firmware string
	Uptime   time.Duration
	CPUTempC float64
	LoadPct  float64
	// MemUsedPct is negative when unknown.
	MemUsedPct float64
	HaveTemp   bool
	HaveLoad   bool
}

// RadioState is one radio's current state.
type RadioState struct {
	Name     string
	Band     string
	State    string
	FreqMHz  float64
	NoiseDBm float64
	BusyPct  float64
	// HaveAir is true when FreqMHz, NoiseDBm and BusyPct were measured.
	HaveAir    bool
	NoiseKnown bool
}

// GPSQuality is the GNSS fix quality.
type GPSQuality struct {
	// Mode is the gpsd fix mode: 2 for 2D, 3 for 3D, lower for no fix.
	Mode     int
	SatsUsed int
	SatsSeen int
	HDOP     float64
}

// CommsState is the node's push-to-talk state.
type CommsState struct {
	TalkGroup    int
	Transmitting bool
}

// writeStatus appends the status lines to a node's remarks. Each part
// goes on its own line so ATAK's remarks box stays readable.
func writeStatus(b *strings.Builder, s *Status) {
	if s == nil {
		return
	}

	if h := s.Health; h != nil {
		writeHealth(b, h)
	}

	for i := range s.Radios {
		writeRadio(b, &s.Radios[i])
	}

	if g := s.GPS; g != nil {
		writeGPS(b, g)
	}

	if c := s.Comms; c != nil {
		b.WriteString("\nVoice: talk group ")
		b.WriteString(strconv.Itoa(c.TalkGroup))

		if c.Transmitting {
			b.WriteString(", TRANSMITTING now")
		}

		b.WriteByte('.')
	}

	writeLine(b, "Scanner", s.Scanner)
	writeLine(b, "Uplink", s.Uplink)
	writeLine(b, "Channel change", s.ChannelMove)
}

// writeAlerts puts any alerts at the top of the remarks.
func writeAlerts(b *strings.Builder, s *Status) {
	if s == nil || len(s.Alerts) == 0 {
		return
	}

	b.WriteString("ALERT: ")
	b.WriteString(strings.Join(s.Alerts, "; "))
	b.WriteString(".\n")
}

func writeHealth(b *strings.Builder, h *Health) {
	b.WriteString("\nHealth:")

	sep := " "
	part := func(s string) {
		b.WriteString(sep)
		b.WriteString(s)

		sep = ", "
	}

	if h.HaveTemp {
		part("CPU " + strconv.FormatFloat(h.CPUTempC, 'f', 0, 64) + " C")
	}

	if h.HaveLoad {
		part("load " + strconv.FormatFloat(h.LoadPct, 'f', 0, 64) + "%")
	}

	if h.MemUsedPct >= 0 {
		part("memory " + strconv.FormatFloat(h.MemUsedPct, 'f', 0, 64) + "% used")
	}

	if h.Uptime > 0 {
		part("up " + shortDuration(h.Uptime))
	}

	if h.Model != "" {
		part(h.Model)
	}

	if h.Firmware != "" {
		part("firmware " + h.Firmware)
	}

	if sep == " " {
		b.WriteString(" unknown")
	}

	b.WriteByte('.')
}

func writeRadio(b *strings.Builder, r *RadioState) {
	b.WriteString("\nRadio ")

	switch {
	case r.Band != "" && r.Name != "":
		b.WriteString(bandLabel(r.Band))
		b.WriteString(" (")
		b.WriteString(r.Name)
		b.WriteByte(')')
	case r.Name != "":
		b.WriteString(r.Name)
	default:
		b.WriteString(bandLabel(r.Band))
	}

	b.WriteByte(':')

	sep := " "

	if r.State != "" {
		b.WriteString(sep)
		b.WriteString(r.State)

		sep = ", "
	}

	if r.HaveAir {
		b.WriteString(sep)
		b.WriteString(strconv.FormatFloat(r.FreqMHz, 'f', -1, 64))
		b.WriteString(" MHz")

		if r.NoiseKnown {
			b.WriteString(", noise ")
			b.WriteString(strconv.FormatFloat(r.NoiseDBm, 'f', 0, 64))
			b.WriteString(" dBm")
		}

		b.WriteString(", channel busy ")
		b.WriteString(strconv.FormatFloat(r.BusyPct, 'f', 0, 64))
		b.WriteByte('%')
	}

	b.WriteByte('.')
}

func writeGPS(b *strings.Builder, g *GPSQuality) {
	b.WriteString("\nGPS: ")

	switch {
	case g.Mode >= 3:
		b.WriteString("3D fix")
	case g.Mode == 2:
		b.WriteString("2D fix")
	default:
		b.WriteString("no fix")
	}

	if g.SatsSeen > 0 || g.SatsUsed > 0 {
		b.WriteString(", ")
		b.WriteString(strconv.Itoa(g.SatsUsed))
		b.WriteString(" of ")
		b.WriteString(strconv.Itoa(max(g.SatsSeen, g.SatsUsed)))
		b.WriteString(" satellites")
	}

	if g.HDOP > 0 {
		b.WriteString(", HDOP ")
		b.WriteString(strconv.FormatFloat(g.HDOP, 'f', 1, 64))
	}

	b.WriteByte('.')
}

func writeLine(b *strings.Builder, label, text string) {
	if text == "" {
		return
	}

	b.WriteByte('\n')
	b.WriteString(label)
	b.WriteString(": ")
	b.WriteString(text)

	if !strings.HasSuffix(text, ".") {
		b.WriteByte('.')
	}
}

// shortDuration renders an uptime as "3d 4h", "4h 12m" or "12m".
func shortDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	days := int(d / (24 * time.Hour))
	hours := int(d/time.Hour) % 24
	mins := int(d/time.Minute) % 60

	switch {
	case days > 0:
		return strconv.Itoa(days) + "d " + strconv.Itoa(hours) + "h"
	case hours > 0:
		return strconv.Itoa(hours) + "h " + strconv.Itoa(mins) + "m"
	default:
		return strconv.Itoa(mins) + "m"
	}
}

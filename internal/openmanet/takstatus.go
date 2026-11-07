package openmanet

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mdlayher/wifi"
	"tailscale.com/ipn/ipnstate"

	jamv1 "github.com/openmanet/openmanetd/internal/api/openmanet/jamdetect/v1"
	"github.com/openmanet/openmanetd/internal/chanmig"
	"github.com/openmanet/openmanetd/internal/gpsd"
	"github.com/openmanet/openmanetd/internal/hardware"
	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/openmanet/openmanetd/internal/system"
	"github.com/openmanet/openmanetd/internal/takfeed"
)

// hotCPUC is the CPU temperature (C) that raises an ATAK alert. The CM5
// starts throttling at 85 C.
const hotCPUC = 80.0

// grantWindow is how recent a P25 voice grant must be to be shown.
const grantWindow = 2 * time.Minute

// Narrow views of the subsystems the node marker reads. Each is optional;
// a nil field leaves its part out of the marker.
type (
	sysReader interface {
		GetUptime() (time.Duration, error)
		GetMemoryInfo() (*system.MemoryInfo, error)
		GetCPULoadPercent() (float32, error)
		GetCPUTempCelsius() (float32, error)
	}
	hardwareReader interface {
		Status() hardware.Status
	}
	jamReporter interface {
		Report() *jamv1.JamReport
	}
	gpsReader interface {
		GetPosition() gpsd.PositionReport
		GetSatelliteReport() gpsd.SatelliteReport
	}
	commsReader interface {
		ActiveTalkGroup() int
		Transmitting() bool
	}
	scannerReader interface {
		Status() scanner.Status
		Grants(limit int) ([]scanner.Grant, int)
		Conventional() scanner.ConventionalStatus
	}
	blosReader interface {
		IsRunning() bool
		Status() *ipnstate.Status
	}
	chanmigReader interface {
		Snapshot() chanmig.Snapshot
	}
)

// takStatus gathers the node's own state for its ATAK marker
// (takfeed.StatusSource). All reads are of cached or in-memory state, so
// a tick never blocks on hardware.
type takStatus struct {
	sys      sysReader
	hw       hardwareReader
	jam      jamReporter
	gps      gpsReader
	scanner  scannerReader
	blos     blosReader
	chanmig  chanmigReader
	now      func() time.Time
	comms    func() commsReader
	model    string
	firmware string
}

// NodeStatus implements takfeed.StatusSource.
func (s *takStatus) NodeStatus(_ context.Context) takfeed.Status {
	now := time.Now
	if s.now != nil {
		now = s.now
	}

	var st takfeed.Status

	st.Health = s.health()

	var hw *hardware.Status

	if s.hw != nil {
		h := s.hw.Status()
		hw = &h
	}

	st.Radios = s.radios(hw)
	st.GPS = s.gpsQuality()

	if s.comms != nil {
		if c := s.comms(); c != nil && c.ActiveTalkGroup() > 0 {
			st.Comms = &takfeed.CommsState{TalkGroup: c.ActiveTalkGroup(), Transmitting: c.Transmitting()}
		}
	}

	st.Scanner = s.scannerLine(now())
	st.Uplink = s.uplinkLine()
	st.ChannelMove = s.channelMoveLine(now())
	st.Alerts = alerts(hw, st.Health)

	return st
}

func (s *takStatus) health() *takfeed.Health {
	h := &takfeed.Health{Model: s.model, Firmware: s.firmware, MemUsedPct: -1}

	if s.sys != nil {
		if up, err := s.sys.GetUptime(); err == nil {
			h.Uptime = up
		}

		if m, err := s.sys.GetMemoryInfo(); err == nil && m != nil && m.TotalBytes > 0 {
			h.MemUsedPct = float64(m.UsedBytes()) * 100 / float64(m.TotalBytes)
		}

		if l, err := s.sys.GetCPULoadPercent(); err == nil {
			h.LoadPct, h.HaveLoad = float64(l), true
		}

		if c, err := s.sys.GetCPUTempCelsius(); err == nil && c > 0 {
			h.CPUTempC, h.HaveTemp = float64(c), true
		}
	}

	if h.Uptime == 0 && h.MemUsedPct < 0 && !h.HaveLoad && !h.HaveTemp && h.Model == "" && h.Firmware == "" {
		return nil
	}

	return h
}

// radios merges the hardware manager's power state with the jamming
// detector's air readings, matched by network interface name.
func (s *takStatus) radios(hw *hardware.Status) []takfeed.RadioState {
	var report []*jamv1.RadioJamStatus
	if s.jam != nil {
		report = s.jam.Report().GetRadios()
	}

	out := make([]takfeed.RadioState, 0, len(report)+2)
	byIf := make(map[string]int, len(report)+2)

	if hw != nil && hw.BoardDetected {
		for i := range hw.Radios {
			r := &hw.Radios[i]
			if r.Radio == hardware.RadioUnspecified || r.State == hardware.StateUnspecified || r.State == hardware.StateUnmanaged {
				continue
			}

			if r.Netdev != "" {
				byIf[r.Netdev] = len(out)
			}

			out = append(out, takfeed.RadioState{Name: hardwareRadioName(r.Radio), State: r.State.String()})
		}
	}

	for _, r := range report {
		rs := takfeed.RadioState{
			Band:       jamBand(r.GetBand()),
			FreqMHz:    r.GetFreqMhz(),
			NoiseDBm:   float64(r.GetNoiseDbm()),
			BusyPct:    r.GetBusyPct(),
			NoiseKnown: r.GetNoiseKnown(),
			HaveAir:    r.GetFreqMhz() > 0,
		}

		if i, ok := byIf[r.GetRadio()]; ok {
			rs.Name, rs.State = out[i].Name, out[i].State
			out[i] = rs

			continue
		}

		rs.Name = r.GetRadio()
		out = append(out, rs)
	}

	return out
}

func hardwareRadioName(r hardware.Radio) string {
	if r == hardware.RadioHaLow {
		return "HaLow"
	}

	if r == hardware.RadioWiFi {
		return "Wi-Fi"
	}

	return r.String()
}

func jamBand(b jamv1.RadioBand) string {
	switch b {
	case jamv1.RadioBand_RADIO_BAND_S1G:
		return "s1g"
	case jamv1.RadioBand_RADIO_BAND_2G4:
		return "2g4"
	case jamv1.RadioBand_RADIO_BAND_5G:
		return "5g"
	case jamv1.RadioBand_RADIO_BAND_6G:
		return "6g"
	default:
		return ""
	}
}

func (s *takStatus) gpsQuality() *takfeed.GPSQuality {
	if s.gps == nil {
		return nil
	}

	p := s.gps.GetPosition()
	sky := s.gps.GetSatelliteReport()

	if p.Mode == 0 && sky.NSat == 0 && p.SatellitesUsed == 0 {
		return nil
	}

	used := p.SatellitesUsed
	if sky.USat > used {
		used = sky.USat
	}

	return &takfeed.GPSQuality{Mode: p.Mode, SatsUsed: used, SatsSeen: sky.NSat, HDOP: sky.HDOP}
}

func (s *takStatus) scannerLine(now time.Time) string {
	if s.scanner == nil {
		return ""
	}

	st := s.scanner.Status()
	if !st.Enabled {
		return ""
	}

	parts := make([]string, 0, 3)

	if sel := st.Selection; sel != nil && sel.Site != nil {
		site := "P25 site " + sel.Site.Name
		if c := controlWord(st.Control.State); c != "" {
			site += ", control channel " + c
		}

		parts = append(parts, site)
	}

	if grants, _ := s.scanner.Grants(1); len(grants) > 0 && now.Sub(grants[0].Last) <= grantWindow {
		g := grants[0]
		v := "voice on talk group " + strconv.FormatUint(uint64(g.Talkgroup), 10) + " " + ago(now.Sub(g.Last))

		if g.Emergency {
			v += " (EMERGENCY)"
		}

		if g.Encrypted {
			v += " (encrypted)"
		}

		parts = append(parts, v)
	}

	conv := s.scanner.Conventional()
	active := 0

	for i := range conv.Channels {
		if conv.Channels[i].Active {
			active++
		}
	}

	if active > 0 {
		parts = append(parts, strconv.Itoa(active)+" conventional channel"+pluralS(active)+" active")
	}

	return strings.Join(parts, "; ")
}

func controlWord(c scanner.ControlState) string {
	switch c {
	case scanner.ControlLocked:
		return "locked"
	case scanner.ControlHunting:
		return "hunting"
	case scanner.ControlError:
		return "error"
	case scanner.ControlUnknown, scanner.ControlUnavailable, scanner.ControlIdle:
		return ""
	}

	return ""
}

func (s *takStatus) uplinkLine() string {
	if s.blos == nil || !s.blos.IsRunning() {
		return ""
	}

	st := s.blos.Status()
	if st == nil {
		return "BLOS starting"
	}

	if st.BackendState != "Running" {
		return "BLOS " + strings.ToLower(st.BackendState)
	}

	online := 0

	for _, p := range st.Peer {
		if p != nil && p.Online {
			online++
		}
	}

	return "BLOS connected, " + strconv.Itoa(online) + " of " + strconv.Itoa(len(st.Peer)) + " peers online"
}

func (s *takStatus) channelMoveLine(now time.Time) string {
	if s.chanmig == nil {
		return ""
	}

	snap := s.chanmig.Snapshot()
	parts := make([]string, 0, len(snap.Bands))

	for i := range snap.Bands {
		b := &snap.Bands[i]

		switch {
		case !b.SwitchAt.IsZero() && b.SwitchAt.After(now):
			parts = append(parts, chanmigBand(b.SwitchTo.Band)+" moves to channel "+
				strconv.Itoa(int(b.SwitchTo.Channel))+" at "+b.SwitchAt.UTC().Format("15:04Z"))
		case b.Mode == chanmig.BandSearching:
			parts = append(parts, chanmigBand(b.Operating.Band)+" searching for the mesh")
		case b.Mode == chanmig.BandFallback:
			parts = append(parts, chanmigBand(b.Operating.Band)+" fell back to channel "+strconv.Itoa(int(b.Operating.Channel)))
		case b.Mode == chanmig.BandSettled, b.Mode == chanmig.BandVerifying:
		}
	}

	return strings.Join(parts, "; ")
}

func chanmigBand(b chanmig.Band) string {
	switch b {
	case chanmig.BandHaLow:
		return "HaLow"
	case chanmig.Band2G4:
		return "2.4 GHz"
	case chanmig.Band5G:
		return "5 GHz"
	default:
		return b.String()
	}
}

// alerts lists the problems that lead the marker's remarks.
func alerts(hw *hardware.Status, h *takfeed.Health) []string {
	var out []string

	if hw != nil && hw.BoardDetected {
		for i := range hw.Radios {
			r := &hw.Radios[i]

			switch r.State {
			case hardware.StateFault, hardware.StateFailed, hardware.StateCooldown:
				out = append(out, hardwareRadioName(r.Radio)+" radio "+r.State.String())
			case hardware.StateUnspecified, hardware.StateUnmanaged, hardware.StateOff,
				hardware.StatePowering, hardware.StateEnumerating, hardware.StateUp:
			}
		}

		switch hw.Battery.Level {
		case hardware.BatteryLevelWarning:
			out = append(out, "battery low")
		case hardware.BatteryLevelCritical:
			out = append(out, "battery critical")
		case hardware.BatteryLevelShutdown:
			out = append(out, "battery empty, shutting down")
		case hardware.BatteryLevelUnspecified, hardware.BatteryLevelUnknown, hardware.BatteryLevelNormal:
		}

		if hw.PowerFault {
			out = append(out, "power fault")
		}

		if hw.ShutdownPending && hw.Battery.Level != hardware.BatteryLevelShutdown {
			out = append(out, "shutting down")
		}
	}

	if h != nil && h.HaveTemp && h.CPUTempC >= hotCPUC {
		out = append(out, "CPU hot ("+strconv.FormatFloat(h.CPUTempC, 'f', 0, 64)+" C)")
	}

	sort.Strings(out)

	return out
}

func ago(d time.Duration) string {
	if d < time.Minute {
		return strconv.Itoa(int(d/time.Second)) + " s ago"
	}

	return strconv.Itoa(int(d/time.Minute)) + " min ago"
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}

	return "s"
}

// stationLister is the part of the wireless provider the link signals
// read.
type stationLister interface {
	GetMeshInterfaces() ([]*wifi.Interface, error)
	StationInfo(iface *wifi.Interface) ([]*wifi.StationInfo, error)
}

// stationSignals reports each directly heard mesh station's signal
// (takfeed.SignalSource).
type stationSignals struct {
	wifi stationLister
}

// StationSignals implements takfeed.SignalSource. A failed read yields no
// signals; the link lines then just leave signal out.
func (s stationSignals) StationSignals(_ context.Context) map[string]takfeed.StationSignal {
	ifaces, err := s.wifi.GetMeshInterfaces()
	if err != nil {
		return nil
	}

	out := make(map[string]takfeed.StationSignal, 8)

	for _, iface := range ifaces {
		stations, err := s.wifi.StationInfo(iface)
		if err != nil {
			continue
		}

		for _, st := range stations {
			if st == nil || len(st.HardwareAddr) == 0 {
				continue
			}

			sig := st.Signal
			if st.SignalAverage != 0 {
				sig = st.SignalAverage
			}

			out[strings.ToLower(st.HardwareAddr.String())] = takfeed.StationSignal{
				SignalDBm:     sig,
				TxBitrateKbps: st.TransmitBitrate / 1000,
			}
		}
	}

	return out
}

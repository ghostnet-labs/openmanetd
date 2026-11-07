package weather

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	weatherv1 "github.com/openmanet/openmanetd/internal/api/openmanet/weather/v1"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Defaults for Options fields left zero.
const (
	DefaultPollInterval    = 5 * time.Second
	DefaultPublishInterval = 30 * time.Second
	DefaultStaleAfter      = 5 * time.Minute
	DefaultExpiry          = 30 * time.Minute
	DefaultGustWindow      = 10 * time.Minute
	// maxErrorLen bounds the poll error shared over the mesh.
	maxErrorLen = 120
)

// Sensor is a weather sensor the Monitor polls. Read must return when
// ctx is canceled.
type Sensor interface {
	Read(ctx context.Context) (Sample, error)
	Close() error
}

// Position is a GNSS fix in WGS84.
type Position struct {
	Latitude  float64
	Longitude float64
	AltitudeM float64
}

// Options wires a Monitor.
type Options struct {
	Log zerolog.Logger
	// Sensor is the sensor wired to this node; nil means this node has no
	// station and only lists the ones it hears.
	Sensor Sensor
	// Gossip shares reports over the mesh; nil keeps the station local.
	Gossip Gossip
	// Identity returns this node's batman MAC and hostname.
	Identity func() (primaryMAC, hostname string)
	// Position returns this node's fix; nil or ok=false omits position.
	Position func() (Position, bool)
	// Now defaults to time.Now.
	Now func() time.Time
	// Model names the sensor in reports, for example "sen0657".
	Model           string
	PollInterval    time.Duration
	PublishInterval time.Duration
	StaleAfter      time.Duration
	Expiry          time.Duration
	GustWindow      time.Duration
}

// Monitor polls the local sensor and tracks the stations other nodes
// share. Its methods are safe for concurrent use.
type Monitor struct { //nolint:govet // fieldalignment: mu must sit directly above the fields it guards (.claude/rules/concurrency.md).
	opts Options

	mu         sync.Mutex // protects the fields below
	state      weatherv1.SensorState
	lastErr    string
	sample     Sample
	observedAt time.Time
	winds      ring[windPoint]
	history    ring[historyPoint]
	remote     map[string]*remoteReport
	stats      Stats
}

// Stats counts Monitor activity.
type Stats struct {
	PollsTotal          uint64
	PollErrorsTotal     uint64
	PublishErrorsTotal  uint64
	FetchErrorsTotal    uint64
	DecodeErrorsTotal   uint64
	RemoteStationsTotal int
}

// New returns a Monitor. Zero durations take the package defaults.
func New(opts Options) *Monitor {
	if opts.Now == nil {
		opts.Now = time.Now
	}

	setDefault(&opts.PollInterval, DefaultPollInterval)
	setDefault(&opts.PublishInterval, DefaultPublishInterval)
	setDefault(&opts.StaleAfter, DefaultStaleAfter)
	setDefault(&opts.Expiry, DefaultExpiry)
	setDefault(&opts.GustWindow, DefaultGustWindow)

	windEntries := min(int(opts.GustWindow/opts.PollInterval)+2, maxWindEntries)

	return &Monitor{
		opts:    opts,
		state:   weatherv1.SensorState_SENSOR_STATE_STARTING,
		winds:   newRing[windPoint](windEntries),
		history: newRing[historyPoint](historyEntries),
		remote:  make(map[string]*remoteReport, 4),
	}
}

func setDefault(d *time.Duration, def time.Duration) {
	if *d <= 0 {
		*d = def
	}
}

// SensorEnabled reports whether this node has a weather sensor.
func (m *Monitor) SensorEnabled() bool {
	return m.opts.Sensor != nil
}

// Run polls and gossips until ctx is canceled, then closes the sensor.
func (m *Monitor) Run(ctx context.Context) {
	var pollC <-chan time.Time

	if m.opts.Sensor != nil {
		defer func() {
			if err := m.opts.Sensor.Close(); err != nil {
				m.opts.Log.Warn().Err(err).Msg("weather: close sensor")
			}
		}()

		poll := time.NewTicker(m.opts.PollInterval)
		defer poll.Stop()

		pollC = poll.C

		m.PollOnce(ctx)
	}

	publish := time.NewTicker(m.opts.PublishInterval)
	defer publish.Stop()

	m.GossipOnce()

	for {
		select {
		case <-ctx.Done():
			return
		case <-pollC:
			m.PollOnce(ctx)
		case <-publish.C:
			m.GossipOnce()
		}
	}
}

// PollOnce reads the sensor once. A failed read keeps the last good
// values and marks the sensor in error.
func (m *Monitor) PollOnce(ctx context.Context) {
	if m.opts.Sensor == nil {
		return
	}

	s, err := m.opts.Sensor.Read(ctx)
	now := m.opts.Now()

	m.mu.Lock()
	defer m.mu.Unlock()

	m.stats.PollsTotal++

	if err != nil {
		m.stats.PollErrorsTotal++

		if ctx.Err() != nil {
			return
		}

		if m.state != weatherv1.SensorState_SENSOR_STATE_ERROR {
			m.opts.Log.Warn().Err(err).Msg("weather: sensor read failed; further failures logged at debug")
		} else {
			m.opts.Log.Debug().Err(err).Msg("weather: sensor read failed")
		}

		m.state = weatherv1.SensorState_SENSOR_STATE_ERROR
		m.lastErr = truncate(err.Error(), maxErrorLen)

		return
	}

	if m.state == weatherv1.SensorState_SENSOR_STATE_ERROR {
		m.opts.Log.Info().Msg("weather: sensor reading again")
	}

	m.state = weatherv1.SensorState_SENSOR_STATE_OK
	m.lastErr = ""
	m.sample = s
	m.observedAt = now

	if s.Has&FieldWindSpeed != 0 {
		m.winds.push(windPoint{t: now, speed: s.WindSpeedMS})
	}

	if last, ok := m.history.last(); !ok || now.Sub(last.t) >= historyStep {
		m.history.push(historyPoint{t: now, pressure: s.PressureHPa, rain: s.RainTotalMM, has: s.Has})
	}
}

// Local returns this node's report, or nil without a sensor.
func (m *Monitor) Local() *weatherv1.WeatherReport {
	if m.opts.Sensor == nil {
		return nil
	}

	mac, host := m.identity()
	pos, havePos := m.position()
	now := m.opts.Now()

	rep := &weatherv1.WeatherReport{
		PrimaryMac:  mac,
		Hostname:    host,
		CollectedAt: timestamppb.New(now),
		Model:       m.opts.Model,
		HasPosition: havePos,
	}

	if havePos {
		rep.Latitude, rep.Longitude, rep.AltitudeM = pos.Latitude, pos.Longitude, pos.AltitudeM
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	rep.State = m.state
	rep.Error = m.lastErr

	if !m.observedAt.IsZero() {
		rep.Reading = m.readingLocked(now)
	}

	return rep
}

// readingLocked builds the reading from the latest sample and history.
// The caller holds m.mu.
func (m *Monitor) readingLocked(now time.Time) *weatherv1.WeatherReading {
	s := m.sample
	r := &weatherv1.WeatherReading{ObservedAt: timestamppb.New(m.observedAt)}

	if s.Has&FieldTemperature != 0 {
		r.TemperatureC = proto.Float64(s.TemperatureC)
	}

	if s.Has&FieldHumidity != 0 {
		r.HumidityPct = proto.Float64(s.HumidityPct)
	}

	if s.Has&FieldPressure != 0 {
		r.PressureHpa = proto.Float64(s.PressureHPa)

		if d, ok := pressureChange(&m.history, m.observedAt, s.PressureHPa); ok {
			r.PressureChange_3HHpa = proto.Float64(d)
		}
	}

	if s.Has&FieldWindSpeed != 0 {
		r.WindSpeedMs = proto.Float64(s.WindSpeedMS)

		if g, ok := gust(&m.winds, now.Add(-m.opts.GustWindow)); ok {
			r.WindGustMs = proto.Float64(max(g, s.WindSpeedMS))
		}
	}

	if s.Has&FieldWindDirection != 0 {
		r.WindDirectionDeg = proto.Float64(s.WindDirectionDeg)
	}

	if s.Has&FieldRainTotal != 0 {
		r.RainTotalMm = proto.Float64(s.RainTotalMM)

		if v, ok := rainLastHour(&m.history, m.observedAt, s.RainTotalMM); ok {
			r.RainLastHourMm = proto.Float64(v)
		}
	}

	if s.Has&FieldLight != 0 {
		r.LightLux = proto.Float64(s.LightLux)
	}

	return r
}

// Stations returns this node's station first (when it has a sensor),
// then every remote station sorted by hostname then MAC. The reports are
// copies.
func (m *Monitor) Stations() []*weatherv1.WeatherStation {
	local := m.Local()
	now := m.opts.Now()

	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]*weatherv1.WeatherStation, 0, len(m.remote)+1)

	if local != nil {
		age := int32(-1)
		if !m.observedAt.IsZero() {
			age = seconds(now.Sub(m.observedAt))
		}

		out = append(out, m.station(local, true, age))
	}

	start := len(out)

	for _, r := range m.remote {
		cp, _ := proto.Clone(r.report).(*weatherv1.WeatherReport) //nolint:forcetypeassert // Clone keeps the type
		out = append(out, m.station(cp, false, r.age(now)))
	}

	slices.SortFunc(out[start:], func(a, b *weatherv1.WeatherStation) int {
		if c := strings.Compare(a.GetReport().GetHostname(), b.GetReport().GetHostname()); c != 0 {
			return c
		}

		return strings.Compare(a.GetReport().GetPrimaryMac(), b.GetReport().GetPrimaryMac())
	})

	return out
}

func (m *Monitor) station(rep *weatherv1.WeatherReport, local bool, age int32) *weatherv1.WeatherStation {
	return &weatherv1.WeatherStation{
		Report:     rep,
		Local:      local,
		AgeSeconds: age,
		Stale:      age < 0 || time.Duration(age)*time.Second > m.opts.StaleAfter,
	}
}

// Stats returns a copy of the counters.
func (m *Monitor) Stats() Stats {
	m.mu.Lock()
	defer m.mu.Unlock()

	s := m.stats
	s.RemoteStationsTotal = len(m.remote)

	return s
}

func (m *Monitor) identity() (primaryMAC, hostname string) {
	if m.opts.Identity == nil {
		return "", ""
	}

	mac, host := m.opts.Identity()

	return strings.ToLower(mac), host
}

func (m *Monitor) position() (Position, bool) {
	if m.opts.Position == nil {
		return Position{}, false
	}

	return m.opts.Position()
}

func seconds(d time.Duration) int32 {
	if d < 0 {
		return 0
	}

	return int32(min(d/time.Second, time.Duration(1<<31-1))) //nolint:gosec // clamped to int32 range
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n]
}

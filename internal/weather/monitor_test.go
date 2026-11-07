package weather_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	weatherv1 "github.com/openmanet/openmanetd/internal/api/openmanet/weather/v1"
	"github.com/openmanet/openmanetd/internal/weather"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const selfMAC = "02:00:00:00:00:01"

func fullSample() weather.Sample {
	return weather.Sample{
		TemperatureC:     5.2,
		HumidityPct:      40,
		PressureHPa:      850,
		WindSpeedMS:      4,
		WindDirectionDeg: 315,
		RainTotalMM:      1.5,
		LightLux:         20000,
		Has: weather.FieldTemperature | weather.FieldHumidity | weather.FieldPressure | weather.FieldWindSpeed |
			weather.FieldWindDirection | weather.FieldRainTotal | weather.FieldLight,
	}
}

type monitorOpts struct {
	sensor *fakeSensor
	gossip *fakeGossip
	clock  *fakeClock
}

func newMonitor(t *testing.T, mo monitorOpts) *weather.Monitor {
	t.Helper()

	opts := weather.Options{
		Log:             zerolog.Nop(),
		Model:           "sen0657",
		Now:             mo.clock.Now,
		PollInterval:    5 * time.Second,
		PublishInterval: 30 * time.Second,
		StaleAfter:      5 * time.Minute,
		Expiry:          30 * time.Minute,
		GustWindow:      10 * time.Minute,
		Identity:        func() (string, string) { return strings.ToUpper(selfMAC), "mast-1" },
		Position: func() (weather.Position, bool) {
			return weather.Position{Latitude: 40.31, Longitude: -112.01, AltitudeM: 1490}, true
		},
	}

	if mo.sensor != nil {
		opts.Sensor = mo.sensor
	}

	if mo.gossip != nil {
		opts.Gossip = mo.gossip
	}

	return weather.New(opts)
}

func TestMonitor_Local_noSensor(t *testing.T) {
	m := newMonitor(t, monitorOpts{clock: newFakeClock()})

	assert.False(t, m.SensorEnabled())
	assert.Nil(t, m.Local())
	assert.Empty(t, m.Stations())
}

func TestMonitor_Local_startingBeforeFirstPoll(t *testing.T) {
	m := newMonitor(t, monitorOpts{clock: newFakeClock(), sensor: &fakeSensor{}})

	rep := m.Local()
	require.NotNil(t, rep)
	assert.Equal(t, weatherv1.SensorState_SENSOR_STATE_STARTING, rep.GetState())
	assert.Nil(t, rep.GetReading())

	st := m.Stations()
	require.Len(t, st, 1)
	assert.Equal(t, int32(-1), st[0].GetAgeSeconds())
	assert.True(t, st[0].GetStale(), "no reading yet must not look current")
}

func TestMonitor_PollOnce_buildsReport(t *testing.T) {
	clock := newFakeClock()
	sensor := &fakeSensor{sample: fullSample()}
	m := newMonitor(t, monitorOpts{clock: clock, sensor: sensor})

	m.PollOnce(context.Background())

	rep := m.Local()
	require.NotNil(t, rep)
	assert.Equal(t, selfMAC, rep.GetPrimaryMac(), "MAC is lowercased")
	assert.Equal(t, "mast-1", rep.GetHostname())
	assert.Equal(t, "sen0657", rep.GetModel())
	assert.Equal(t, weatherv1.SensorState_SENSOR_STATE_OK, rep.GetState())
	assert.True(t, rep.GetHasPosition())
	assert.InDelta(t, 40.31, rep.GetLatitude(), 1e-9)

	r := rep.GetReading()
	require.NotNil(t, r)
	assert.InDelta(t, 5.2, r.GetTemperatureC(), 1e-9)
	assert.InDelta(t, 40, r.GetHumidityPct(), 1e-9)
	assert.InDelta(t, 850, r.GetPressureHpa(), 1e-9)
	assert.InDelta(t, 4, r.GetWindSpeedMs(), 1e-9)
	assert.InDelta(t, 4, r.GetWindGustMs(), 1e-9)
	assert.InDelta(t, 315, r.GetWindDirectionDeg(), 1e-9)
	assert.InDelta(t, 1.5, r.GetRainTotalMm(), 1e-9)
	assert.InDelta(t, 20000, r.GetLightLux(), 1e-9)
	assert.Nil(t, r.PressureChange_3HHpa, "no 3-hour history yet")
	assert.Nil(t, r.RainLastHourMm, "no hour of history yet")
}

func TestMonitor_PollOnce_missingFieldsStayUnset(t *testing.T) {
	sensor := &fakeSensor{sample: weather.Sample{TemperatureC: 0, Has: weather.FieldTemperature}}
	m := newMonitor(t, monitorOpts{clock: newFakeClock(), sensor: sensor})

	m.PollOnce(context.Background())

	r := m.Local().GetReading()
	require.NotNil(t, r.TemperatureC, "0 degC is a real reading")
	assert.Nil(t, r.HumidityPct)
	assert.Nil(t, r.WindSpeedMs)
	assert.Nil(t, r.WindGustMs)
}

func TestMonitor_PollOnce_errorKeepsLastValues(t *testing.T) {
	clock := newFakeClock()
	sensor := &fakeSensor{sample: fullSample()}
	m := newMonitor(t, monitorOpts{clock: clock, sensor: sensor})

	m.PollOnce(context.Background())
	observed := m.Local().GetReading().GetObservedAt().AsTime()

	clock.Advance(time.Minute)
	sensor.set(weather.Sample{}, errInjected)
	m.PollOnce(context.Background())

	rep := m.Local()
	assert.Equal(t, weatherv1.SensorState_SENSOR_STATE_ERROR, rep.GetState())
	assert.Contains(t, rep.GetError(), "injected")
	assert.InDelta(t, 5.2, rep.GetReading().GetTemperatureC(), 1e-9)
	assert.Equal(t, observed, rep.GetReading().GetObservedAt().AsTime(), "observed_at says how old the values are")
	assert.Equal(t, uint64(1), m.Stats().PollErrorsTotal)

	clock.Advance(5 * time.Minute)

	st := m.Stations()
	require.Len(t, st, 1)
	assert.True(t, st[0].GetStale())
	assert.Equal(t, int32(360), st[0].GetAgeSeconds())

	sensor.set(fullSample(), nil)
	m.PollOnce(context.Background())
	assert.Equal(t, weatherv1.SensorState_SENSOR_STATE_OK, m.Local().GetState())
	assert.Empty(t, m.Local().GetError())
}

func TestMonitor_PollOnce_longErrorTruncated(t *testing.T) {
	sensor := &fakeSensor{err: &longErr{}}
	m := newMonitor(t, monitorOpts{clock: newFakeClock(), sensor: sensor})

	m.PollOnce(context.Background())
	assert.Len(t, m.Local().GetError(), 120)
}

type longErr struct{}

func (*longErr) Error() string { return strings.Repeat("x", 500) }

func TestMonitor_gustIsMaxOverWindow(t *testing.T) {
	clock := newFakeClock()
	sensor := &fakeSensor{}
	m := newMonitor(t, monitorOpts{clock: clock, sensor: sensor})

	poll := func(speed float64) {
		s := fullSample()
		s.WindSpeedMS = speed
		sensor.set(s, nil)
		m.PollOnce(context.Background())
		clock.Advance(5 * time.Second)
	}

	poll(15) // falls out of the 10-minute window below
	clock.Advance(10 * time.Minute)
	poll(9)
	poll(3)

	r := m.Local().GetReading()
	assert.InDelta(t, 3, r.GetWindSpeedMs(), 1e-9)
	assert.InDelta(t, 9, r.GetWindGustMs(), 1e-9)
}

func TestMonitor_pressureChangeAfterThreeHours(t *testing.T) {
	clock := newFakeClock()
	sensor := &fakeSensor{}
	m := newMonitor(t, monitorOpts{clock: clock, sensor: sensor})

	s := fullSample()
	for i := range 181 {
		s.PressureHPa = 850 - float64(i)*0.02 // 3.6 hPa fall over 3 h
		sensor.set(s, nil)
		m.PollOnce(context.Background())

		if i < 180 {
			clock.Advance(time.Minute)
		}
	}

	r := m.Local().GetReading()
	require.NotNil(t, r.PressureChange_3HHpa)
	assert.InDelta(t, -3.6, r.GetPressureChange_3HHpa(), 1e-9)
}

func TestMonitor_rainLastHourHandlesCounterReset(t *testing.T) {
	clock := newFakeClock()
	sensor := &fakeSensor{}
	m := newMonitor(t, monitorOpts{clock: clock, sensor: sensor})

	totals := []float64{10, 10, 10.5, 11, 0.2, 0.4}
	s := fullSample()

	// One hour of quiet history first, then the shower and a reset.
	for range 61 {
		s.RainTotalMM = 10
		sensor.set(s, nil)
		m.PollOnce(context.Background())
		clock.Advance(time.Minute)
	}

	for _, v := range totals {
		s.RainTotalMM = v
		sensor.set(s, nil)
		m.PollOnce(context.Background())
		clock.Advance(time.Minute)
	}

	r := m.Local().GetReading()
	require.NotNil(t, r.RainLastHourMm)
	assert.InDelta(t, 1.4, r.GetRainLastHourMm(), 1e-9, "0.5 + 0.5 before the reset, 0.2 + 0.2 after")
}

func TestMonitor_GossipOnce_publishesAndIngests(t *testing.T) {
	clock := newFakeClock()
	g := &fakeGossip{}
	sensor := &fakeSensor{sample: fullSample()}
	m := newMonitor(t, monitorOpts{clock: clock, sensor: sensor, gossip: g})

	m.PollOnce(context.Background())

	own := m.Local()
	ownBytes, err := own.MarshalVT()
	require.NoError(t, err)

	remoteRep := func(mac, host string, observed time.Time) []byte {
		t.Helper()

		rep := &weatherv1.WeatherReport{
			PrimaryMac:  mac,
			Hostname:    host,
			CollectedAt: timestamppb.New(observed.Add(10 * time.Second)),
			State:       weatherv1.SensorState_SENSOR_STATE_OK,
			Reading:     &weatherv1.WeatherReading{ObservedAt: timestamppb.New(observed)},
		}
		b, mErr := rep.MarshalVT()
		require.NoError(t, mErr)

		return b
	}

	srcMAC, err := net.ParseMAC("02:00:00:00:00:09")
	require.NoError(t, err)

	now := clock.Now()
	g.setRecords(
		weather.GossipRecord{Data: ownBytes},
		weather.GossipRecord{Data: remoteRep("02:00:00:00:00:0B", "mast-3", now)},
		weather.GossipRecord{Data: remoteRep("02:00:00:00:00:0A", "mast-2", now)},
		weather.GossipRecord{Source: srcMAC, Data: remoteRep("", "mast-0", now)},
		weather.GossipRecord{Data: []byte{0xFF, 0xFF}},
	)

	m.GossipOnce()

	pub := g.getPublished()
	require.Len(t, pub, 1)

	got := &weatherv1.WeatherReport{}
	require.NoError(t, got.UnmarshalVT(pub[0]))
	assert.Equal(t, "mast-1", got.GetHostname())

	st := m.Stations()
	require.Len(t, st, 4, "own report is not listed twice; the bad record is dropped")
	assert.True(t, st[0].GetLocal())
	assert.Equal(t, "mast-0", st[1].GetReport().GetHostname())
	assert.Equal(t, "02:00:00:00:00:09", st[1].GetReport().GetPrimaryMac(), "falls back to the alfred source MAC")
	assert.Equal(t, "mast-2", st[2].GetReport().GetHostname())
	assert.Equal(t, "02:00:00:00:00:0a", st[2].GetReport().GetPrimaryMac())
	assert.Equal(t, "mast-3", st[3].GetReport().GetHostname())
	assert.Equal(t, int32(10), st[2].GetAgeSeconds(), "age includes how old the reading was when published")
	assert.False(t, st[2].GetStale())
	assert.Equal(t, uint64(1), m.Stats().DecodeErrorsTotal)
	assert.Equal(t, 3, m.Stats().RemoteStationsTotal)
}

func TestMonitor_GossipOnce_staleThenExpired(t *testing.T) {
	clock := newFakeClock()
	g := &fakeGossip{}
	m := newMonitor(t, monitorOpts{clock: clock, gossip: g})

	rep := &weatherv1.WeatherReport{
		PrimaryMac:  "02:00:00:00:00:0a",
		Hostname:    "mast-2",
		CollectedAt: timestamppb.New(clock.Now()),
		Reading:     &weatherv1.WeatherReading{ObservedAt: timestamppb.New(clock.Now())},
	}
	b, err := rep.MarshalVT()
	require.NoError(t, err)
	g.setRecords(weather.GossipRecord{Data: b})

	m.GossipOnce()
	require.Len(t, m.Stations(), 1)
	assert.Empty(t, g.getPublished(), "a node without a sensor publishes nothing")

	clock.Advance(6 * time.Minute)
	m.GossipOnce()

	st := m.Stations()
	require.Len(t, st, 1)
	assert.True(t, st[0].GetStale(), "the same report repeated does not refresh the age")

	clock.Advance(25 * time.Minute)
	m.GossipOnce()
	assert.Empty(t, m.Stations(), "expired after weather.expiry of silence")
}

func TestMonitor_GossipOnce_errorsCounted(t *testing.T) {
	g := &fakeGossip{publishErr: errInjected, fetchErr: errInjected}
	m := newMonitor(t, monitorOpts{clock: newFakeClock(), gossip: g, sensor: &fakeSensor{sample: fullSample()}})

	m.GossipOnce()

	s := m.Stats()
	assert.Equal(t, uint64(1), s.PublishErrorsTotal)
	assert.Equal(t, uint64(1), s.FetchErrorsTotal)
}

func TestMonitor_Run_pollsAndClosesSensor(t *testing.T) {
	sensor := &fakeSensor{sample: fullSample(), readsCh: make(chan struct{}, 1)}
	m := newMonitor(t, monitorOpts{clock: newFakeClock(), sensor: sensor, gossip: &fakeGossip{}})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)

		m.Run(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		<-done
	})

	<-sensor.readsCh
	cancel()
	<-done

	assert.True(t, sensor.isClosed())
	assert.Equal(t, weatherv1.SensorState_SENSOR_STATE_OK, m.Local().GetState())
}

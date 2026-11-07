package openmanet

import (
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/gpsd"
	"github.com/openmanet/openmanetd/internal/jamdetect"
	"github.com/openmanet/openmanetd/internal/takfeed"
)

func TestGPSPosition_nilServiceHasNoFix(t *testing.T) {
	_, ok := gpsPosition{}.Position()

	assert.False(t, ok)
}

func TestTAKFeedOptions_optionalSourcesStayNilInterfaces(t *testing.T) {
	cfg := config.NewWithoutWatch(viper.New())

	opts := takFeedOptions(cfg, nil, nil, nil, nil, nil, zerolog.Nop())

	// A typed nil pointer in an interface would make takfeed call through
	// it and panic; every absent source must be a true nil interface.
	assert.Nil(t, opts.Nodes)
	assert.Nil(t, opts.Topology)
	assert.Nil(t, opts.Battery)
	assert.Nil(t, opts.Aircraft)
	assert.Nil(t, opts.Jams)
	assert.Nil(t, opts.Signals)
	assert.NotNil(t, opts.Status, "the status lines are always on; each part checks its own source")
	assert.False(t, opts.CameraOwnsMarker)
	assert.NotNil(t, opts.Sender)
	assert.Equal(t, gpsd.NodeCallsign(), opts.Callsign)
}

func TestTAKFeedOptions_wiresJamDetector(t *testing.T) {
	cfg := config.NewWithoutWatch(viper.New())

	opts := takFeedOptions(cfg, nil, nil, nil, nil, jamdetect.New(jamdetect.Options{}), zerolog.Nop())

	assert.NotNil(t, opts.Jams)
	assert.Empty(t, opts.Jams.ActiveJams(), "a fresh detector has no episodes")
}

func TestToTAKJam(t *testing.T) {
	started := time.Date(2026, 11, 7, 1, 0, 0, 0, time.UTC)
	ev := jamdetect.Event{
		Started: started, ID: "morse0-915", Radio: "morse0", Band: jamdetect.BandS1G,
		FreqMHz: 915.5, NoiseDBm: -70, BaselineNoiseDBm: -97.5, BusyPct: 90,
		UnexplainedBusyPct: 75, State: jamdetect.StateJammed, NoiseKnown: true,
	}

	got, ok := toTAKJam(&ev)

	assert.True(t, ok)
	assert.Equal(t, takfeed.Jam{
		Started: started, ID: "morse0-915", Radio: "morse0", Band: "s1g",
		FreqMHz: 915.5, NoiseDBm: -70, BaselineNoiseDBm: -97.5, BusyPct: 90,
		UnexplainedBusyPct: 75, State: takfeed.JamActive, NoiseKnown: true,
	}, got)

	ev.State = jamdetect.StateSuspected
	got, ok = toTAKJam(&ev)
	assert.True(t, ok)
	assert.Equal(t, takfeed.JamSuspected, got.State)

	ev.State = jamdetect.State(0)
	_, ok = toTAKJam(&ev)
	assert.False(t, ok, "a closed episode is not a marker")
}

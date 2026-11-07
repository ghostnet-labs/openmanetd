package openmanet

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/gpsd"
)

func TestGPSPosition_nilServiceHasNoFix(t *testing.T) {
	_, ok := gpsPosition{}.Position()

	assert.False(t, ok)
}

func TestTAKFeedOptions_optionalSourcesStayNilInterfaces(t *testing.T) {
	cfg := config.NewWithoutWatch(viper.New())

	opts := takFeedOptions(cfg, nil, nil, nil, nil, zerolog.Nop())

	// A typed nil pointer in an interface would make takfeed call through
	// it and panic; every absent source must be a true nil interface.
	assert.Nil(t, opts.Nodes)
	assert.Nil(t, opts.Topology)
	assert.Nil(t, opts.Battery)
	assert.Nil(t, opts.Aircraft)
	assert.Nil(t, opts.Jams)
	assert.False(t, opts.CameraOwnsMarker)
	assert.NotNil(t, opts.Sender)
	assert.Equal(t, gpsd.NodeCallsign(), opts.Callsign)
}

package openmanet

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/openmanet/go-alfred"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/jamdetect"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJamGossip(t *testing.T) {
	src := net.HardwareAddr{2, 0, 0, 0, 0, 9}
	client := &fakeAlfredClient{records: []alfred.Record{
		{Source: src, Version: jamAlfredDataVersion, Data: []byte("report")},
		{Source: src, Version: jamAlfredDataVersion + 1, Data: []byte("future format")},
	}}
	g := jamGossip{client: client}

	require.NoError(t, g.Publish([]byte("mine")))
	assert.Equal(t, []fakeAlfredSet{{dataType: 105, version: jamAlfredDataVersion, payload: []byte("mine")}}, client.setCalls())

	recs, err := g.Fetch()
	require.NoError(t, err)
	assert.Equal(t, []jamdetect.GossipRecord{{Source: src, Data: []byte("report")}}, recs)
	assert.Equal(t, []uint8{105}, client.requestCalls())

	client.setErr = errFakeAlfred
	require.ErrorIs(t, g.Publish([]byte("x")), errFakeAlfred)

	client.requestErr = errFakeAlfred
	_, err = g.Fetch()
	require.ErrorIs(t, err, errFakeAlfred)
}

func TestJamdetectOptions(t *testing.T) {
	got := jamdetectOptions(config.JamdetectConfig{
		Interval: time.Second, GossipInterval: 2 * time.Second, GossipExpiry: 3 * time.Second,
		NoiseRiseDB: 1, NoiseCeilingDBm: -2, BusyRisePct: 3, BusyFloorPct: 4, BusyCeilingPct: 5,
		WarmupSamples: 6, SuspectSamples: 7, JamSamples: 8, ClearSamples: 9,
	})

	assert.Equal(t, time.Second, got.Interval)
	assert.Equal(t, 2*time.Second, got.GossipInterval)
	assert.Equal(t, 3*time.Second, got.GossipExpiry)
	assert.Equal(t, jamdetect.Thresholds{
		NoiseRiseDB: 1, NoiseCeilingDBm: -2, BusyRisePct: 3, BusyFloorPct: 4, BusyCeilingPct: 5,
		WarmupSamples: 6, SuspectSamples: 7, JamSamples: 8, ClearSamples: 9,
	}, got.Thresholds)
}

func TestStartJamdetect_disabled(t *testing.T) {
	v := viper.New()
	v.Set("jamdetect.enable", false)

	assert.Nil(t, startJamdetect(context.Background(), config.NewWithoutWatch(v), nil, nil, zerolog.Nop()))
}

func TestJamDetectorAPI_nilStaysNilInterface(t *testing.T) {
	assert.Nil(t, jamDetectorAPI(nil))
	assert.NotNil(t, jamDetectorAPI(jamdetect.New(jamdetect.Options{})))
}

func TestNodeIdentity_unknownInterface(t *testing.T) {
	mac, host := nodeIdentity("does-not-exist0")
	assert.Empty(t, mac)
	assert.NotEmpty(t, host)
}

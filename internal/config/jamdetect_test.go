package config

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetJamdetect_defaults(t *testing.T) {
	jc := NewWithoutWatch(viper.New()).GetJamdetect()

	assert.True(t, jc.Enable, "detection is on by default on every node")
	assert.True(t, jc.Gossip)
	assert.Equal(t, DefaultJamdetectInterval, jc.Interval)
	assert.Equal(t, DefaultJamdetectGossipInterval, jc.GossipInterval)
	assert.Equal(t, DefaultJamdetectGossipExpiry, jc.GossipExpiry)
	assert.Zero(t, jc.NoiseRiseDB)
	assert.Zero(t, jc.NoiseCeilingDBm)
	assert.Zero(t, jc.JamSamples)
}

func TestGetJamdetect_yamlBinding(t *testing.T) {
	const doc = `
jamdetect:
  enable: false
  gossip: false
  interval: 2s
  gossipInterval: 15s
  gossipExpiry: 5m
  noiseRiseDb: 8
  noiseCeilingDbm: -65
  busyRisePct: 25
  busyFloorPct: 35
  busyCeilingPct: 90
  warmupSamples: 30
  suspectSamples: 3
  jamSamples: 6
  clearSamples: 10
`

	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(doc)))

	jc := NewWithoutWatch(v).GetJamdetect()

	assert.Equal(t, JamdetectConfig{
		Enable:          false,
		Gossip:          false,
		Interval:        2 * time.Second,
		GossipInterval:  15 * time.Second,
		GossipExpiry:    5 * time.Minute,
		NoiseRiseDB:     8,
		NoiseCeilingDBm: -65,
		BusyRisePct:     25,
		BusyFloorPct:    35,
		BusyCeilingPct:  90,
		WarmupSamples:   30,
		SuspectSamples:  3,
		JamSamples:      6,
		ClearSamples:    10,
	}, jc)
}

func TestGetJamdetect_invalidValuesFallBack(t *testing.T) {
	const doc = `
jamdetect:
  interval: -1s
  gossipExpiry: banana
  noiseRiseDb: -3
  noiseCeilingDbm: 20
  busyCeilingPct: 150
  jamSamples: -2
`

	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(doc)))

	jc := NewWithoutWatch(v).GetJamdetect()

	assert.Equal(t, DefaultJamdetectInterval, jc.Interval)
	assert.Equal(t, DefaultJamdetectGossipExpiry, jc.GossipExpiry)
	assert.Zero(t, jc.NoiseRiseDB)
	assert.Zero(t, jc.NoiseCeilingDBm)
	assert.Zero(t, jc.BusyCeilingPct)
	assert.Zero(t, jc.JamSamples)
}

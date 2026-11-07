package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetOnboard_defaults(t *testing.T) {
	oc := NewWithoutWatch(viper.New()).GetOnboard()

	assert.True(t, oc.Enable, "onboarding is on by default")
	assert.Equal(t, DefaultOnboardDir, oc.Dir)
	assert.Equal(t, DefaultOnboardMaxAPKBytes, oc.MaxAPKBytes)
	assert.Equal(t, DefaultOnboardMapSources(), oc.MapSources)
}

func TestGetOnboard_yamlBinding(t *testing.T) {
	const doc = `
onboard:
  enable: false
  dir: /mnt/data/onboard
  maxApkBytes: 1048576
  mapSources:
    - name: Local Ortho
      layer: ortho
      tileType: PNG
      minZoom: 5
      maxZoom: 19
    - name: ""
      layer: dropped
      tileType: png
    - name: Bad Layer
      layer: "../x"
      tileType: png
    - name: Bad Type
      layer: gif
      tileType: gif
    - name: Bad Zoom
      layer: z
      tileType: jpg
      minZoom: 9
      maxZoom: 3
`

	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(doc)))

	oc := NewWithoutWatch(v).GetOnboard()

	assert.False(t, oc.Enable)
	assert.Equal(t, "/mnt/data/onboard", oc.Dir)
	assert.Equal(t, int64(1048576), oc.MaxAPKBytes)
	assert.Equal(t, []OnboardMapSource{
		{Name: "Local Ortho", Layer: "ortho", TileType: "png", MinZoom: 5, MaxZoom: 19},
	}, oc.MapSources, "invalid entries are dropped, tile type is lower-cased")
}

func TestGetOnboard_emptyMapSourcesDisablesMaps(t *testing.T) {
	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader("onboard:\n  mapSources: []\n")))

	assert.Empty(t, NewWithoutWatch(v).GetOnboard().MapSources)
}

func TestGetOnboard_returnsCopy(t *testing.T) {
	c := NewWithoutWatch(viper.New())

	oc := c.GetOnboard()
	oc.MapSources[0].Name = "changed"

	assert.Equal(t, "Manet Satellite", c.GetOnboard().MapSources[0].Name)
}

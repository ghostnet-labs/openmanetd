package config

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetChanmig_defaults(t *testing.T) {
	cc := NewWithoutWatch(viper.New()).GetChanmig()

	assert.False(t, cc.Enable, "channel migration must be off by default")
	assert.Equal(t, DefaultChanmigPublicKeyFile, cc.PublicKeyFile)
	assert.Equal(t, DefaultChanmigPrivateKeyFile, cc.PrivateKeyFile)
	assert.Equal(t, DefaultChanmigStoreFile, cc.StoreFile)
	assert.Zero(t, cc.MeshID)
	assert.Zero(t, cc.NodeID)
	assert.Zero(t, cc.HistoryLen)
	assert.Equal(t, ChanmigBandConfig{}, cc.HaLow)
	assert.Equal(t, ChanmigBandConfig{}, cc.WiFi2G)
	assert.Equal(t, ChanmigBandConfig{}, cc.WiFi5G)
	assert.Equal(t, ChanmigTiming{}, cc.Timing)
}

// TestGetChanmig_yamlBinding reads the keys from YAML, as the daemon does,
// so the nested band names ("2g", "5g") and camelCase keys are pinned.
func TestGetChanmig_yamlBinding(t *testing.T) {
	const doc = `
chanmig:
  enable: true
  meshId: 18446744073709551615
  nodeId: 42
  publicKeyFile: /tmp/a.pub
  privateKeyFile: /tmp/a.key
  storeFile: /tmp/plans.bin
  historyLen: 6
  bands:
    halow:
      radio: radio0
      initial: "28/2"
      rendezvous: ["28/2", "12/1"]
      allowed: ["28/2", "30/4"]
    2g:
      initial: "6/20"
    5g:
      radio: radio2
      initial: "36/80"
      allowed: ["36/80", "149/80"]
  timing:
    advertInterval: 15s
    peerWindow: 1m
    verifyBound: 70s
    fallbackBound: 71s
    peerLossTimeout: 3m
    coldBootGrace: 61s
    dwell: 31s
    abortMargin: 32s
    maxLead: 20m
    radioRetry: 33s
    stagger: 50s
`

	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(doc)))

	cc := NewWithoutWatch(v).GetChanmig()

	assert.True(t, cc.Enable)
	assert.Equal(t, uint64(18446744073709551615), cc.MeshID)
	assert.Equal(t, uint64(42), cc.NodeID)
	assert.Equal(t, "/tmp/a.pub", cc.PublicKeyFile)
	assert.Equal(t, "/tmp/a.key", cc.PrivateKeyFile)
	assert.Equal(t, "/tmp/plans.bin", cc.StoreFile)
	assert.Equal(t, 6, cc.HistoryLen)
	assert.Equal(t, ChanmigBandConfig{
		Radio: "radio0", Initial: "28/2",
		Rendezvous: []string{"28/2", "12/1"}, Allowed: []string{"28/2", "30/4"},
	}, cc.HaLow)
	assert.Equal(t, "6/20", cc.WiFi2G.Initial)
	assert.Empty(t, cc.WiFi2G.Radio)
	assert.Equal(t, ChanmigBandConfig{Radio: "radio2", Initial: "36/80", Allowed: []string{"36/80", "149/80"}}, cc.WiFi5G)
	assert.Equal(t, ChanmigTiming{
		AdvertInterval:  15 * time.Second,
		PeerWindow:      time.Minute,
		VerifyBound:     70 * time.Second,
		FallbackBound:   71 * time.Second,
		PeerLossTimeout: 3 * time.Minute,
		ColdBootGrace:   61 * time.Second,
		Dwell:           31 * time.Second,
		AbortMargin:     32 * time.Second,
		MaxLead:         20 * time.Minute,
		RadioRetry:      33 * time.Second,
		Stagger:         50 * time.Second,
	}, cc.Timing)
}

func TestGetChanmig_invalidValuesFallBack(t *testing.T) {
	v := viper.New()
	v.Set("chanmig.enable", false)
	v.Set("chanmig.historyLen", -3)
	v.Set("chanmig.timing.dwell", "soon")
	v.Set("chanmig.timing.stagger", "-5s")
	v.Set("chanmig.publicKeyFile", "")

	cc := NewWithoutWatch(v).GetChanmig()
	assert.False(t, cc.Enable)
	assert.Zero(t, cc.HistoryLen)
	assert.Zero(t, cc.Timing.Dwell, "an unparsable duration keeps the wiring default")
	assert.Zero(t, cc.Timing.Stagger, "a negative duration keeps the wiring default")
	assert.Equal(t, DefaultChanmigPublicKeyFile, cc.PublicKeyFile)
}

func TestGetChanmig_returnsCopies(t *testing.T) {
	v := viper.New()
	v.Set("chanmig.bands.halow.allowed", []string{"28/2"})
	v.Set("chanmig.bands.halow.rendezvous", []string{"12/1"})

	cfg := NewWithoutWatch(v)
	cc := cfg.GetChanmig()
	cc.HaLow.Allowed[0] = "changed"
	cc.HaLow.Rendezvous[0] = "changed"

	again := cfg.GetChanmig()
	assert.Equal(t, "28/2", again.HaLow.Allowed[0])
	assert.Equal(t, "12/1", again.HaLow.Rendezvous[0])
}

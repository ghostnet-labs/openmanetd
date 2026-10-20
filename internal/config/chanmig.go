package config

import (
	"slices"
	"time"
)

// Channel-migration (chanmig) defaults. The subsystem is off by default:
// with chanmig.enable unset or false nothing is started and no radio is
// touched.
const (
	DefaultChanmigEnable         bool   = false
	DefaultChanmigPublicKeyFile  string = "/etc/openmanetd/chanmig/authority.pub"
	DefaultChanmigPrivateKeyFile string = "/etc/openmanetd/chanmig/authority.key"
	DefaultChanmigStoreFile      string = "/etc/openmanetd/chanmig/plans.bin"
)

// ChanmigBandConfig is the provisioned knowledge for one band. Assignments
// are "CHANNEL/WIDTH_MHZ" strings such as "28/2" (HaLow) or "36/80"
// (5 GHz); the chanmig wiring parses and validates them. A band with an
// empty Initial is not managed.
type ChanmigBandConfig struct {
	// Radio pins the UCI wifi-device section. Empty selects the first
	// section whose band option matches.
	Radio      string
	Initial    string
	Rendezvous []string
	Allowed    []string
}

// ChanmigTiming overrides protocol timers. A zero field keeps the
// transport-appropriate default chosen by the chanmig wiring.
type ChanmigTiming struct {
	AdvertInterval  time.Duration
	PeerWindow      time.Duration
	VerifyBound     time.Duration
	FallbackBound   time.Duration
	PeerLossTimeout time.Duration
	ColdBootGrace   time.Duration
	Dwell           time.Duration
	AbortMargin     time.Duration
	MaxLead         time.Duration
	RadioRetry      time.Duration
	Stagger         time.Duration
}

// ChanmigConfig holds the chanmig settings. It is returned by value from
// GetChanmig with every slice copied.
type ChanmigConfig struct {
	// PublicKeyFile holds the trusted plan-authority public key(s).
	// Required on every node when Enable is true.
	PublicKeyFile string
	// PrivateKeyFile holds the plan-authority private key. Present only
	// on coordinators; a missing file means "not a coordinator".
	PrivateKeyFile string
	// StoreFile persists accepted plans across reboots.
	StoreFile string
	HaLow     ChanmigBandConfig
	WiFi2G    ChanmigBandConfig
	WiFi5G    ChanmigBandConfig
	Timing    ChanmigTiming
	// MeshID binds plans to this mesh; every node of a mesh must agree.
	MeshID uint64
	// NodeID overrides the node identity; 0 derives it from the batman
	// interface MAC.
	NodeID uint64
	// HistoryLen bounds the superseded plans kept for rediscovery; 0
	// keeps the package default.
	HistoryLen int
	// Enable starts the channel-migration node.
	Enable bool
}

// loadChanmig reads the chanmig section, applying defaults. The caller
// holds c.mu.
func (c *Config) loadChanmig() ChanmigConfig {
	cc := ChanmigConfig{
		Enable:         DefaultChanmigEnable,
		PublicKeyFile:  DefaultChanmigPublicKeyFile,
		PrivateKeyFile: DefaultChanmigPrivateKeyFile,
		StoreFile:      DefaultChanmigStoreFile,
		MeshID:         c.v.GetUint64("chanmig.meshId"),
		NodeID:         c.v.GetUint64("chanmig.nodeId"),
		HaLow:          c.loadChanmigBand("chanmig.bands.halow"),
		WiFi2G:         c.loadChanmigBand("chanmig.bands.2g"),
		WiFi5G:         c.loadChanmigBand("chanmig.bands.5g"),
		Timing:         c.loadChanmigTiming(),
	}

	if c.v.IsSet("chanmig.enable") {
		cc.Enable = c.v.GetBool("chanmig.enable")
	}

	if val := c.v.GetString("chanmig.publicKeyFile"); val != "" {
		cc.PublicKeyFile = val
	}

	if val := c.v.GetString("chanmig.privateKeyFile"); val != "" {
		cc.PrivateKeyFile = val
	}

	if val := c.v.GetString("chanmig.storeFile"); val != "" {
		cc.StoreFile = val
	}

	if val := c.v.GetInt("chanmig.historyLen"); val > 0 {
		cc.HistoryLen = val
	}

	return cc
}

func (c *Config) loadChanmigBand(prefix string) ChanmigBandConfig {
	return ChanmigBandConfig{
		Radio:      c.v.GetString(prefix + ".radio"),
		Initial:    c.v.GetString(prefix + ".initial"),
		Rendezvous: c.v.GetStringSlice(prefix + ".rendezvous"),
		Allowed:    c.v.GetStringSlice(prefix + ".allowed"),
	}
}

func (c *Config) loadChanmigTiming() ChanmigTiming {
	d := func(key string) time.Duration {
		return parseDurationOrDefault(c.v.GetString("chanmig.timing."+key), 0)
	}

	return ChanmigTiming{
		AdvertInterval:  d("advertInterval"),
		PeerWindow:      d("peerWindow"),
		VerifyBound:     d("verifyBound"),
		FallbackBound:   d("fallbackBound"),
		PeerLossTimeout: d("peerLossTimeout"),
		ColdBootGrace:   d("coldBootGrace"),
		Dwell:           d("dwell"),
		AbortMargin:     d("abortMargin"),
		MaxLead:         d("maxLead"),
		RadioRetry:      d("radioRetry"),
		Stagger:         d("stagger"),
	}
}

// GetChanmig returns the chanmig settings.
func (c *Config) GetChanmig() ChanmigConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cc := c.Chanmig
	for _, b := range []*ChanmigBandConfig{&cc.HaLow, &cc.WiFi2G, &cc.WiFi5G} {
		b.Rendezvous = slices.Clone(b.Rendezvous)
		b.Allowed = slices.Clone(b.Allowed)
	}

	return cc
}

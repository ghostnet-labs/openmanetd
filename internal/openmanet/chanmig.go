package openmanet

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"time"

	"github.com/openmanet/go-alfred"
	"github.com/rs/zerolog"

	"github.com/openmanet/openmanetd/internal/chanmig"
	"github.com/openmanet/openmanetd/internal/chanmig/platform"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/mgmt"
	"github.com/openmanet/openmanetd/internal/network"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/openmanet/openmanetd/internal/util/logger"
)

// chanmigStopTimeout bounds the shutdown wait. A radio reload in flight
// runs detached with its own 30 s bound, so the daemon does not wait for it.
const chanmigStopTimeout = 5 * time.Second

// alfredGossip binds an alfred client to the chanmig data type.
type alfredGossip struct {
	client alfred.ReadWriteClient
}

func (g alfredGossip) Publish(payload []byte) error {
	if err := g.client.Set(platform.AlfredDataType, platform.AlfredDataVersion, payload); err != nil {
		return fmt.Errorf("alfred set: %w", err)
	}

	return nil
}

// Fetch returns every chanmig record alfred holds. Records of another
// format version are skipped so a mixed-firmware mesh degrades to "peer
// not heard" instead of decode noise.
func (g alfredGossip) Fetch() ([]platform.GossipRecord, error) {
	recs, err := g.client.Request(platform.AlfredDataType)
	if err != nil {
		return nil, fmt.Errorf("alfred request: %w", err)
	}

	out := make([]platform.GossipRecord, 0, len(recs))

	for _, r := range recs {
		if r.Version != platform.AlfredDataVersion {
			continue
		}

		out = append(out, platform.GossipRecord{Source: r.Source, Data: r.Data})
	}

	return out, nil
}

// chanmigDeps are the host-specific inputs to buildChanmig, injectable in
// tests.
type chanmigDeps struct {
	gossip platform.Gossip
	reader network.ConfigReader
	// reload applies committed UCI; nil runs reload_config.
	reload func(ctx context.Context) error
	// mac returns the MAC the node ID is derived from when
	// chanmig.nodeId is unset.
	mac   func() (net.HardwareAddr, error)
	clock chanmig.Clock
	log   zerolog.Logger
	boot  uint32
}

// startChanmig builds the channel-migration node and runs it until ctx is
// canceled. It returns nil, starting nothing, when chanmig.enable is false
// (the default), when alfred is disabled, or when provisioning is
// incomplete; the last two are logged.
func startChanmig(ctx context.Context, cfg *config.Config, manager *mgmt.ManagementConfig, log zerolog.Logger) *platform.Service {
	cc := cfg.GetChanmig()
	if !cc.Enable {
		return nil
	}

	if manager == nil || manager.Client() == nil {
		log.Warn().Msg("chanmig: enabled but alfred is disabled; channel migration not started")

		return nil
	}

	batIface := cfg.GetAlfredBatInterface()

	svc, err := buildChanmig(cc, chanmigDeps{
		gossip: alfredGossip{client: manager.Client()},
		reader: network.NewUCIWirelessConfigReader(),
		mac: func() (net.HardwareAddr, error) {
			ifc, err := net.InterfaceByName(batIface)
			if err != nil {
				return nil, fmt.Errorf("look up %s: %w", batIface, err)
			}

			return ifc.HardwareAddr, nil
		},
		clock: platform.SystemClock{},
		log:   logger.GetLogger("chanmig"),
		boot:  rand.Uint32(),
	})
	if err != nil {
		log.Error().Err(err).Msg("chanmig: not started")

		return nil
	}

	go svc.Run(ctx)

	return svc
}

// buildChanmig assembles the node and its adapters from config.
func buildChanmig(cc config.ChanmigConfig, deps chanmigDeps) (*platform.Service, error) {
	bands, radios, err := chanmigBands(cc)
	if err != nil {
		return nil, err
	}

	keys, err := platform.LoadPublicKeys(cc.PublicKeyFile)
	if err != nil {
		return nil, fmt.Errorf("plan-authority public key: %w", err)
	}

	verifier, err := chanmig.NewVerifier(cc.MeshID, keys...)
	if err != nil {
		return nil, fmt.Errorf("verifier: %w", err)
	}

	id, err := chanmigNodeID(cc, deps.mac)
	if err != nil {
		return nil, err
	}

	signer, err := chanmigSigner(cc.PrivateKeyFile, id, keys)
	if err != nil {
		return nil, err
	}

	transport := platform.NewAlfredTransport(deps.gossip, deps.boot, platform.DefaultPollInterval, deps.log)

	node, err := chanmig.New(chanmig.Config{
		Clock:     deps.clock,
		Transport: transport,
		Radio:     &platform.UCIRadio{Reader: deps.reader, Reload: deps.reload, Radios: radios, Log: deps.log},
		Store:     platform.NewFileStore(cc.StoreFile),
		Verifier:  verifier,
		Signer:    signer,
		Log:       deps.log,
		Bands:     bands,
		Timing:    chanmigTiming(platform.AlfredTiming(), cc.Timing),
		// HistoryLen 0 keeps the package default.
		HistoryLen: cc.HistoryLen,
		ID:         id,
	})
	if err != nil {
		return nil, fmt.Errorf("chanmig node: %w", err)
	}

	deps.log.Info().Uint64("node_id", uint64(id)).Uint64("mesh_id", cc.MeshID).Int("bands", len(bands)).
		Bool("coordinator", signer != nil).Dur("recovery_bound", node.RecoveryBound()).
		Msg("chanmig: channel migration enabled")

	return platform.NewService(node, transport, platform.DefaultTickInterval, deps.log), nil
}

// chanmigBands parses the configured bands. A band with no initial
// assignment is not managed.
func chanmigBands(cc config.ChanmigConfig) ([]chanmig.BandConfig, map[chanmig.Band]string, error) {
	specs := []struct {
		cfg  config.ChanmigBandConfig
		band chanmig.Band
	}{
		{cfg: cc.HaLow, band: chanmig.BandHaLow},
		{cfg: cc.WiFi2G, band: chanmig.Band2G4},
		{cfg: cc.WiFi5G, band: chanmig.Band5G},
	}

	bands := make([]chanmig.BandConfig, 0, len(specs))
	radios := make(map[chanmig.Band]string, len(specs))

	for _, s := range specs {
		if s.cfg.Initial == "" {
			continue
		}

		bc, err := platform.BandSpec{
			Band:       s.band,
			Initial:    s.cfg.Initial,
			Rendezvous: s.cfg.Rendezvous,
			Allowed:    s.cfg.Allowed,
		}.BandConfig()
		if err != nil {
			return nil, nil, fmt.Errorf("chanmig band config: %w", err)
		}

		bands = append(bands, bc)

		if s.cfg.Radio != "" {
			radios[s.band] = s.cfg.Radio
		}
	}

	if len(bands) == 0 {
		return nil, nil, errors.New("chanmig: no band has an initial assignment (chanmig.bands.<halow|2g|5g>.initial)")
	}

	return bands, radios, nil
}

func chanmigNodeID(cc config.ChanmigConfig, mac func() (net.HardwareAddr, error)) (chanmig.NodeID, error) {
	if cc.NodeID != 0 {
		return chanmig.NodeID(cc.NodeID), nil
	}

	hw, err := mac()
	if err != nil {
		return 0, fmt.Errorf("chanmig node id: %w", err)
	}

	id, err := platform.NodeIDFromMAC(hw)
	if err != nil {
		return 0, fmt.Errorf("chanmig node id: %w", err)
	}

	return id, nil
}

// chanmigSigner loads the coordinator signing key. No key file means the
// node is not a coordinator. A key whose public half is not trusted is
// refused: plans it signed would be rejected by every node.
func chanmigSigner(path string, id chanmig.NodeID, trusted []ed25519.PublicKey) (*chanmig.Signer, error) {
	priv, err := platform.LoadPrivateKey(path)
	if errors.Is(err, platform.ErrNoPrivateKey) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("plan-authority private key: %w", err)
	}

	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok || !platform.TrustsKey(trusted, pub) {
		return nil, errors.New("chanmig: plan-authority private key does not match any trusted public key")
	}

	signer, err := chanmig.NewSigner(id, priv)
	if err != nil {
		return nil, fmt.Errorf("chanmig signer: %w", err)
	}

	return signer, nil
}

// chanmigTiming overlays non-zero config overrides on base.
func chanmigTiming(base chanmig.Timing, o config.ChanmigTiming) chanmig.Timing {
	set := func(dst *time.Duration, v time.Duration) {
		if v > 0 {
			*dst = v
		}
	}

	set(&base.AdvertInterval, o.AdvertInterval)
	set(&base.PeerWindow, o.PeerWindow)
	set(&base.VerifyBound, o.VerifyBound)
	set(&base.FallbackBound, o.FallbackBound)
	set(&base.PeerLossTimeout, o.PeerLossTimeout)
	set(&base.ColdBootGrace, o.ColdBootGrace)
	set(&base.Dwell, o.Dwell)
	set(&base.AbortMargin, o.AbortMargin)
	set(&base.MaxLead, o.MaxLead)
	set(&base.RadioRetry, o.RadioRetry)
	set(&base.Stagger, o.Stagger)

	return base
}

// chanmigAPI returns the running node as the handler interface, or a true
// nil interface (not a typed nil pointer) when chanmig is not running, so
// the status RPC reports enabled=false.
func chanmigAPI(svc *platform.Service) handlers.ChannelMigrator {
	if svc == nil {
		return nil
	}

	return svc.Node()
}

// waitChanmig waits, bounded, for the service's loops to exit after ctx is
// canceled.
func waitChanmig(svc *platform.Service, log zerolog.Logger) {
	if svc == nil {
		return
	}

	select {
	case <-svc.Done():
	case <-time.After(chanmigStopTimeout):
		log.Warn().Dur("timeout", chanmigStopTimeout).Msg("chanmig did not stop in time")
	}
}

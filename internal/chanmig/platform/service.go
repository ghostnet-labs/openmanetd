package platform

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/openmanet/openmanetd/internal/chanmig"
)

// DefaultTickInterval drives Node.Tick. It must stay well below the advert
// interval.
const DefaultTickInterval = time.Second

// AlfredTiming returns protocol timers sized for alfred gossip. alfred
// primaries synchronize roughly every 10 s, so a peer's fresh bundle can
// take one to two sync rounds to arrive; the chanmig defaults (1 s adverts,
// 5 s peer window) assume a direct per-band datagram path and would make
// every band look silent. Each bound here keeps the ratios the chanmig
// validator requires.
func AlfredTiming() chanmig.Timing {
	return chanmig.Timing{
		AdvertInterval:  10 * time.Second,
		PeerWindow:      45 * time.Second,
		VerifyBound:     60 * time.Second,
		FallbackBound:   60 * time.Second,
		PeerLossTimeout: 150 * time.Second,
		ColdBootGrace:   60 * time.Second,
		Dwell:           30 * time.Second,
		AbortMargin:     30 * time.Second,
		MaxLead:         10 * time.Minute,
		RadioRetry:      30 * time.Second,
		Stagger:         time.Minute,
	}
}

// ParseAssignment parses "CHANNEL/WIDTH_MHZ" (for example "36/80") for
// band b and validates it.
func ParseAssignment(b chanmig.Band, s string) (chanmig.Assignment, error) {
	chStr, wStr, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok {
		return chanmig.Assignment{}, fmt.Errorf("chanmig/platform: assignment %q is not CHANNEL/WIDTH", s)
	}

	ch, err := strconv.ParseUint(strings.TrimSpace(chStr), 10, 16)
	if err != nil {
		return chanmig.Assignment{}, fmt.Errorf("chanmig/platform: assignment %q channel: %w", s, err)
	}

	w, err := strconv.ParseUint(strings.TrimSpace(wStr), 10, 16)
	if err != nil {
		return chanmig.Assignment{}, fmt.Errorf("chanmig/platform: assignment %q width: %w", s, err)
	}

	a := chanmig.Assignment{Band: b, Channel: uint16(ch), WidthMHz: uint16(w)}
	if err := a.Validate(); err != nil {
		return chanmig.Assignment{}, fmt.Errorf("assignment %q: %w", s, err)
	}

	return a, nil
}

// BandSpec is one band's provisioning in string form, as read from config.
type BandSpec struct {
	Initial    string
	Rendezvous []string
	Allowed    []string
	Band       chanmig.Band
}

// BandConfig parses spec into a chanmig.BandConfig.
func (spec BandSpec) BandConfig() (chanmig.BandConfig, error) {
	initial, err := ParseAssignment(spec.Band, spec.Initial)
	if err != nil {
		return chanmig.BandConfig{}, fmt.Errorf("%s initial: %w", spec.Band, err)
	}

	rendezvous, err := parseAssignments(spec.Band, spec.Rendezvous)
	if err != nil {
		return chanmig.BandConfig{}, fmt.Errorf("%s rendezvous: %w", spec.Band, err)
	}

	allowed, err := parseAssignments(spec.Band, spec.Allowed)
	if err != nil {
		return chanmig.BandConfig{}, fmt.Errorf("%s allowed: %w", spec.Band, err)
	}

	return chanmig.BandConfig{Initial: initial, Rendezvous: rendezvous, Allowed: allowed}, nil
}

func parseAssignments(b chanmig.Band, in []string) ([]chanmig.Assignment, error) {
	if len(in) == 0 {
		return nil, nil
	}

	out := make([]chanmig.Assignment, 0, len(in))

	for _, s := range in {
		a, err := ParseAssignment(b, s)
		if err != nil {
			return nil, err
		}

		out = append(out, a)
	}

	return out, nil
}

// NodeIDFromMAC derives a NodeID from a 6-byte MAC address.
func NodeIDFromMAC(mac net.HardwareAddr) (chanmig.NodeID, error) {
	if len(mac) != 6 {
		return 0, fmt.Errorf("chanmig/platform: MAC %q is not 6 bytes", mac)
	}

	var b [8]byte

	copy(b[2:], mac)

	id := chanmig.NodeID(binary.BigEndian.Uint64(b[:]))
	if id == 0 {
		return 0, errors.New("chanmig/platform: all-zero MAC")
	}

	return id, nil
}

// Service runs one chanmig.Node with its alfred transport. Run starts two
// goroutines' worth of work (the tick loop on the caller's goroutine and
// the alfred poll loop) and returns only after both have stopped.
type Service struct {
	node      *chanmig.Node
	transport *AlfredTransport
	done      chan struct{}
	log       zerolog.Logger
	tick      time.Duration
}

// NewService wraps node and transport. tick <= 0 selects
// DefaultTickInterval.
func NewService(node *chanmig.Node, transport *AlfredTransport, tick time.Duration, log zerolog.Logger) *Service {
	if tick <= 0 {
		tick = DefaultTickInterval
	}

	return &Service{node: node, transport: transport, tick: tick, log: log, done: make(chan struct{})}
}

// Node returns the running node, for status and Propose.
func (s *Service) Node() *chanmig.Node { return s.node }

// Done is closed once Run has returned.
func (s *Service) Done() <-chan struct{} { return s.done }

// Run loads persisted plans, tunes the radios, then ticks the node and
// polls alfred until ctx is done. Call it exactly once.
func (s *Service) Run(ctx context.Context) {
	defer close(s.done)

	if err := s.node.Start(ctx); err != nil {
		s.log.Error().Err(err).Msg("chanmig: node failed to start")

		return
	}

	var wg sync.WaitGroup

	wg.Go(func() { s.transport.Run(ctx, s.node.HandleMessage) })

	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()

	if err := s.node.Run(ctx, ticker.C); err != nil {
		s.log.Error().Err(err).Msg("chanmig: node loop stopped")
	}

	wg.Wait()
	s.log.Info().Msg("chanmig: stopped")
}

package chanmig

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// Clock supplies time. Synced reports whether the clock is disciplined
// (GNSS or NTP over the mesh); an unsynced node times a plan's switch from
// the moment it received the plan instead of from the absolute ActivateAt.
type Clock interface {
	Now() time.Time
	Synced() bool
}

// Transport broadcasts a payload on one band's mesh interface. It must not
// retain payload after returning and must not call back into the Node
// synchronously. In production it rides the alfred payload codec, which
// already authenticates mesh membership.
type Transport interface {
	Broadcast(ctx context.Context, band Band, payload []byte) error
}

// RadioSetter tunes one band's radio. An error marks the radio failed until
// a retry succeeds.
type RadioSetter interface {
	SetChannel(ctx context.Context, a Assignment) error
}

// Store persists accepted plans across power cycles. Records are opaque
// byte strings, newest first.
type Store interface {
	Load(ctx context.Context) ([][]byte, error)
	Save(ctx context.Context, records [][]byte) error
}

// BandConfig is the offline knowledge a node keeps for one radio band:
// the provisioned initial assignment, the rendezvous list and the allowed
// channel/width search list. Every plan must stay inside Allowed so a
// returning node can always find the mesh by sweeping it.
type BandConfig struct {
	Rendezvous []Assignment
	Allowed    []Assignment
	Initial    Assignment
}

// Timing holds the protocol timers. DefaultTiming documents the defaults.
type Timing struct {
	// AdvertInterval is the period between adverts on each band.
	AdvertInterval time.Duration
	// PeerWindow is how long a heard peer counts as present.
	PeerWindow time.Duration
	// VerifyBound is how long a switched band waits for a peer before
	// falling back to its previous assignment.
	VerifyBound time.Duration
	// FallbackBound is how long a band waits for a peer on its previous
	// assignment before returning to the plan's assignment.
	FallbackBound time.Duration
	// PeerLossTimeout is how long a node may hear no peer on any band
	// before it starts rediscovery.
	PeerLossTimeout time.Duration
	// ColdBootGrace is how long a booting node listens on its last-known
	// plan before it starts rediscovery.
	ColdBootGrace time.Duration
	// Dwell is how long a searching radio listens on each candidate.
	Dwell time.Duration
	// AbortMargin is how long before activation the coordinator checks
	// its acknowledgements.
	AbortMargin time.Duration
	// MaxLead bounds ActivateAt-IssuedAt in accepted plans.
	MaxLead time.Duration
	// RadioRetry is the delay before re-tuning a failed radio.
	RadioRetry time.Duration
	// Stagger separates successive changed bands in plans this node issues.
	Stagger time.Duration
}

// DefaultTiming returns the default timers from the design record.
func DefaultTiming() Timing {
	return Timing{
		AdvertInterval:  time.Second,
		PeerWindow:      5 * time.Second,
		VerifyBound:     8 * time.Second,
		FallbackBound:   8 * time.Second,
		PeerLossTimeout: 20 * time.Second,
		ColdBootGrace:   10 * time.Second,
		Dwell:           3 * time.Second,
		AbortMargin:     5 * time.Second,
		MaxLead:         10 * time.Minute,
		RadioRetry:      10 * time.Second,
		Stagger:         5 * time.Second,
	}
}

func (t *Timing) validate() error {
	for _, d := range []time.Duration{
		t.AdvertInterval, t.PeerWindow, t.VerifyBound, t.FallbackBound, t.PeerLossTimeout,
		t.ColdBootGrace, t.Dwell, t.AbortMargin, t.MaxLead, t.RadioRetry,
	} {
		if d <= 0 {
			return errors.New("chanmig: every timer must be positive")
		}
	}

	switch {
	case t.Stagger < 0 || t.Stagger > maxStagger:
		return fmt.Errorf("chanmig: stagger %s out of range", t.Stagger)
	case t.Dwell < 2*t.AdvertInterval:
		return errors.New("chanmig: dwell must cover at least two advert intervals")
	case t.PeerWindow < 2*t.AdvertInterval:
		return errors.New("chanmig: peer window must cover at least two advert intervals")
	case t.PeerLossTimeout <= t.VerifyBound+t.FallbackBound:
		return errors.New("chanmig: peer-loss timeout must exceed verify plus fallback bounds")
	}

	return nil
}

// Config wires a Node.
type Config struct {
	Clock     Clock
	Transport Transport
	Radio     RadioSetter
	Store     Store
	Verifier  *Verifier
	// Signer is set only on nodes provisioned as coordinators.
	Signer *Signer
	Log    zerolog.Logger
	Bands  []BandConfig
	Timing Timing
	// HistoryLen bounds how many superseded plans are kept for search.
	HistoryLen int
	ID         NodeID
}

// Limits that bound memory on constrained targets.
const (
	maxPeers          = 256
	maxAcks           = 256
	maxSearchList     = 64
	defaultHistoryLen = 4
	maxHistoryLen     = 16
)

// Phase is the node-level migration state.
type Phase uint8

// Phases reported by Status.
const (
	PhaseBooting Phase = iota
	PhaseStable
	PhasePending
	PhaseMigrating
	PhaseSearching
)

// String returns the phase name.
func (p Phase) String() string {
	switch p {
	case PhaseBooting:
		return "booting"
	case PhaseStable:
		return "stable"
	case PhasePending:
		return "pending"
	case PhaseMigrating:
		return "migrating"
	case PhaseSearching:
		return "searching"
	default:
		return "unknown"
	}
}

// Stats counts protocol events.
type Stats struct {
	Accepted    uint64
	Rejected    uint64
	Stale       uint64
	Malformed   uint64
	Fallbacks   uint64
	Searches    uint64
	Recoveries  uint64
	Aborts      uint64
	RadioErrors uint64
	SendErrors  uint64
}

// Status is a point-in-time view of a Node.
type Status struct {
	Operating []Assignment
	// LastSearch is how long the most recent completed search took.
	LastSearch time.Duration
	Stats      Stats
	Version    uint64
	SignerID   NodeID
	Peers      int
	Acks       int
	Phase      Phase
}

type bandMode uint8

const (
	modeSettled bandMode = iota
	modeVerifying
	modeFallback
	modeSearching
)

type bandState struct {
	modeSince  time.Time
	switchAt   time.Time
	deadline   time.Time
	lastPeer   time.Time
	retryAt    time.Time
	candidates []Assignment
	cfg        BandConfig
	op         Assignment
	prev       Assignment
	switchTo   Assignment
	mode       bandMode
	failed     bool
}

type searchState struct {
	started   time.Time
	stepStart time.Time
	epoch     int
	step      int
	steps     int
	parkCount int
	parking   bool
}

type tuneReq struct {
	bs *bandState
	a  Assignment
}

type sendReq struct {
	payload []byte
	band    Band
}

// effects collects I/O decided under the state lock and executed after it
// is released.
type effects struct {
	tunes []tuneReq
	sends []sendReq
	save  [][]byte
}

// Node runs the migration and rediscovery state machines for one mesh node.
// All methods are safe for concurrent use.
type Node struct { //nolint:govet // fieldalignment: mu stays above the fields it guards per .claude/rules/concurrency.md
	cfg Config

	// ioMu serializes decide-then-act sequences so radio and transport
	// calls happen in the order the state machine decided them. It is
	// never taken while mu is held.
	ioMu sync.Mutex

	mu           sync.Mutex // protects the fields below
	bootDeadline time.Time
	lastPeerAny  time.Time
	lastAdvert   time.Time
	current      *SignedPlan
	peers        map[NodeID]time.Time
	acks         map[NodeID]struct{}
	bands        []*bandState
	history      []SignedPlan
	search       searchState
	stats        Stats
	lastSearch   time.Duration
	phase        Phase
	advertNow    bool
	coordinating bool
	abortChecked bool
}

// New validates cfg and returns a Node. Call Start before use.
func New(cfg Config) (*Node, error) {
	if cfg.Clock == nil || cfg.Transport == nil || cfg.Radio == nil || cfg.Store == nil || cfg.Verifier == nil {
		return nil, errors.New("chanmig: clock, transport, radio, store and verifier are required")
	}

	if err := cfg.Timing.validate(); err != nil {
		return nil, err
	}

	if cfg.HistoryLen == 0 {
		cfg.HistoryLen = defaultHistoryLen
	}

	if cfg.HistoryLen < 0 || cfg.HistoryLen > maxHistoryLen {
		return nil, fmt.Errorf("chanmig: history length %d out of range", cfg.HistoryLen)
	}

	bands, err := newBandStates(cfg.Bands)
	if err != nil {
		return nil, err
	}

	return &Node{
		cfg:   cfg,
		bands: bands,
		peers: make(map[NodeID]time.Time, maxPeers),
		acks:  make(map[NodeID]struct{}, maxAcks),
	}, nil
}

func newBandStates(cfgs []BandConfig) ([]*bandState, error) {
	if len(cfgs) == 0 || len(cfgs) > maxBands {
		return nil, fmt.Errorf("chanmig: %d bands configured", len(cfgs))
	}

	out := make([]*bandState, 0, len(cfgs))

	for _, bc := range cfgs {
		if err := validateBandConfig(bc); err != nil {
			return nil, err
		}

		for _, other := range out {
			if other.cfg.Initial.Band == bc.Initial.Band {
				return nil, fmt.Errorf("chanmig: band %s configured twice", bc.Initial.Band)
			}
		}

		out = append(out, &bandState{cfg: bc, op: bc.Initial})
	}

	slices.SortFunc(out, func(a, b *bandState) int { return int(a.cfg.Initial.Band) - int(b.cfg.Initial.Band) })

	return out, nil
}

func validateBandConfig(bc BandConfig) error {
	if err := bc.Initial.Validate(); err != nil {
		return fmt.Errorf("initial assignment: %w", err)
	}

	if len(bc.Rendezvous)+len(bc.Allowed) > maxSearchList {
		return fmt.Errorf("chanmig: %s search list longer than %d", bc.Initial.Band, maxSearchList)
	}

	for _, list := range [][]Assignment{bc.Rendezvous, bc.Allowed} {
		for _, a := range list {
			if err := a.Validate(); err != nil {
				return err
			}

			if a.Band != bc.Initial.Band {
				return fmt.Errorf("chanmig: %s list contains %s entry", bc.Initial.Band, a.Band)
			}
		}
	}

	return nil
}

// RecoveryBound returns the worst-case time from RF coverage returning to
// the node re-joining the mesh, for a mesh that is itself stable on a
// channel in this node's search list:
//
//	PeerLossTimeout + 4 x E + Dwell,  E = longest search list x Dwell
//
// One seek epoch (E) can just miss the right channel, a park lasts 2E and
// the next seek finds it within E. The extra Dwell absorbs advert phase.
func (n *Node) RecoveryBound() time.Duration {
	longest := 0

	for _, bs := range n.bands {
		l := min(2+n.cfg.HistoryLen+len(bs.cfg.Rendezvous)+len(bs.cfg.Allowed), maxSearchList)
		longest = max(longest, l)
	}

	t := n.cfg.Timing
	epoch := time.Duration(longest) * t.Dwell

	return t.PeerLossTimeout + 4*epoch + t.Dwell
}

// Start loads persisted plans, tunes radios to the last-known plan and
// enters the cold-boot listening phase.
func (n *Node) Start(ctx context.Context) error {
	records, err := n.cfg.Store.Load(ctx)
	if err != nil {
		n.cfg.Log.Warn().Err(err).Msg("chanmig: plan store unreadable, starting from provisioned plan")

		records = nil
	}

	n.ioMu.Lock()
	defer n.ioMu.Unlock()

	n.mu.Lock()

	now := n.cfg.Clock.Now()
	n.loadRecords(records)

	var eff effects

	for _, bs := range n.bands {
		bs.mode = modeSettled
		bs.modeSince = now
		n.tuneTo(&eff, bs, n.target(bs))
	}

	n.phase = PhaseBooting
	n.bootDeadline = now.Add(n.cfg.Timing.ColdBootGrace)
	n.advertNow = true
	n.mu.Unlock()

	n.apply(ctx, &eff)

	return nil
}

func (n *Node) loadRecords(records [][]byte) {
	n.current = nil
	n.history = n.history[:0]

	for _, rec := range records {
		raw, sig, ok := decodeRecord(rec)
		if !ok {
			continue
		}

		sp, err := n.cfg.Verifier.Verify(raw, sig)
		if err != nil {
			continue
		}

		if n.current == nil {
			n.current = &sp

			continue
		}

		if len(n.history) < n.cfg.HistoryLen {
			n.history = append(n.history, sp)
		}
	}
}

// HandleMessage processes one payload received on band.
func (n *Node) HandleMessage(ctx context.Context, band Band, payload []byte) {
	m, err := decodeMessage(payload)

	n.ioMu.Lock()
	defer n.ioMu.Unlock()

	n.mu.Lock()

	if err != nil {
		n.stats.Malformed++
		n.mu.Unlock()

		return
	}

	bs := n.band(band)
	if m.from == n.cfg.ID || bs == nil || bs.failed {
		n.mu.Unlock()

		return
	}

	now := n.cfg.Clock.Now()

	var eff effects

	n.notePeer(bs, m.from, now)

	peerBehind := false

	switch m.kind {
	case msgAdvert:
		peerBehind = n.onAdvert(&eff, m, now)
	case msgAck:
		n.onAck(&eff, m.ack, now)
	}

	switch n.phase {
	case PhaseSearching:
		n.exitSearch(&eff, bs, peerBehind, now)
	case PhaseBooting:
		n.phase = PhaseStable
	default:
	}

	n.mu.Unlock()
	n.apply(ctx, &eff)
}

func (n *Node) band(b Band) *bandState {
	for _, bs := range n.bands {
		if bs.cfg.Initial.Band == b {
			return bs
		}
	}

	return nil
}

func (n *Node) notePeer(bs *bandState, id NodeID, now time.Time) {
	bs.lastPeer = now
	n.lastPeerAny = now

	if _, ok := n.peers[id]; ok || len(n.peers) < maxPeers {
		n.peers[id] = now
	}
}

// onAdvert handles an advert and reports whether the sender holds an
// older plan than this node.
func (n *Node) onAdvert(eff *effects, m decodedMessage, now time.Time) bool {
	if !m.hasPlan {
		if n.current != nil {
			n.advertNow = true

			return true
		}

		return false
	}

	// Most adverts repeat the plan this node already holds; skip the
	// signature check for byte-identical copies.
	if n.current != nil && bytes.Equal(m.raw, n.current.Raw) && bytes.Equal(m.sig, n.current.Sig) {
		return false
	}

	sp, err := n.cfg.Verifier.Verify(bytes.Clone(m.raw), bytes.Clone(m.sig))
	if err != nil {
		n.stats.Rejected++
		n.cfg.Log.Debug().Err(err).Uint64("from", uint64(m.from)).Msg("chanmig: rejected plan")

		return false
	}

	return n.offer(eff, sp, now) < 0
}

// offer considers a verified plan. It returns >0 when the plan was
// accepted, 0 when it is the current plan and <0 when it is older.
func (n *Node) offer(eff *effects, sp SignedPlan, now time.Time) int {
	if n.current != nil {
		c := compareSigned(&sp, n.current)
		if c <= 0 {
			if c < 0 {
				n.stats.Stale++
				n.advertNow = true
			}

			return c
		}
	}

	if err := n.checkPolicy(&sp.Plan); err != nil {
		n.stats.Rejected++
		n.cfg.Log.Warn().Err(err).Uint64("version", sp.Plan.Version).Msg("chanmig: plan violates local policy")

		return 0
	}

	if n.current != nil {
		n.history = slices.Insert(n.history, 0, *n.current)
		if len(n.history) > n.cfg.HistoryLen {
			n.history = n.history[:n.cfg.HistoryLen]
		}
	}

	n.current = &sp
	n.stats.Accepted++
	n.advertNow = true
	n.coordinating = sp.Plan.SignerID == n.cfg.ID && n.cfg.Signer != nil
	n.abortChecked = false
	clear(n.acks)

	eff.save = n.records()
	start := n.schedule(now)

	if start.After(now) {
		payload := appendAck(make([]byte, 0, ackSize), ack{from: n.cfg.ID, version: sp.Plan.Version, signer: sp.Plan.SignerID})
		n.broadcast(eff, payload)
	}

	n.cfg.Log.Info().Uint64("version", sp.Plan.Version).Uint64("signer", uint64(sp.Plan.SignerID)).
		Time("activate_at", sp.Plan.ActivateAt).Msg("chanmig: accepted channel plan")

	return 1
}

// checkPolicy applies node-local rules: bounded lead time and every
// assignment for a configured band inside that band's allowed list.
func (n *Node) checkPolicy(p *Plan) error {
	if p.ActivateAt.Sub(p.IssuedAt) > n.cfg.Timing.MaxLead {
		return fmt.Errorf("%w: lead %s exceeds %s", ErrInvalidPlan, p.ActivateAt.Sub(p.IssuedAt), n.cfg.Timing.MaxLead)
	}

	for _, a := range p.Assignments {
		bs := n.band(a.Band)
		if bs == nil || len(bs.cfg.Allowed) == 0 {
			continue
		}

		if !slices.Contains(bs.cfg.Allowed, a) {
			return fmt.Errorf("%w: %s not in allowed list", ErrInvalidPlan, a)
		}
	}

	return nil
}

// schedule computes per-band switch times for the current plan and returns
// the activation start. Changed bands switch one Stagger apart so a moved
// band is verified while the others still hold the old paths.
func (n *Node) schedule(now time.Time) time.Time {
	p := &n.current.Plan

	start := p.ActivateAt
	if !n.cfg.Clock.Synced() {
		start = now.Add(p.ActivateAt.Sub(p.IssuedAt))
	}

	if start.Before(now) {
		start = now
	}

	i := 0

	for _, bs := range n.bands {
		a, ok := p.Assignment(bs.cfg.Initial.Band)
		if !ok || bs.mode == modeSearching {
			continue
		}

		if a == bs.op {
			bs.switchAt = time.Time{}

			continue
		}

		bs.switchTo = a
		bs.switchAt = start.Add(time.Duration(i) * p.Stagger)
		i++
	}

	return start
}

func (n *Node) onAck(eff *effects, a ack, now time.Time) {
	if n.current == nil || a.version != n.current.Plan.Version || a.signer != n.current.Plan.SignerID {
		return
	}

	if !now.Before(n.current.Plan.ActivateAt) && n.cfg.Clock.Synced() {
		return
	}

	if _, seen := n.acks[a.from]; seen || len(n.acks) >= maxAcks {
		return
	}

	n.acks[a.from] = struct{}{}
	// Relay so acknowledgements reach a coordinator several hops away.
	n.broadcast(eff, appendAck(make([]byte, 0, ackSize), a))
}

// Tick advances timers. Call it periodically (Run does) at a period well
// below AdvertInterval.
func (n *Node) Tick(ctx context.Context) {
	n.ioMu.Lock()
	defer n.ioMu.Unlock()

	n.mu.Lock()

	now := n.cfg.Clock.Now()

	var eff effects

	n.prunePeers(now)
	n.retryRadios(&eff, now)

	switch n.phase {
	case PhaseBooting:
		if !now.Before(n.bootDeadline) {
			n.enterSearch(&eff, now)
		}
	case PhaseSearching:
		n.searchTick(&eff, now)
	default:
		for _, bs := range n.bands {
			n.tickBand(&eff, bs, now)
		}

		if !n.lastPeerAny.IsZero() && now.Sub(n.lastPeerAny) >= n.cfg.Timing.PeerLossTimeout {
			n.enterSearch(&eff, now)
		}
	}

	n.checkAbort(&eff, now)

	if n.advertNow || now.Sub(n.lastAdvert) >= n.cfg.Timing.AdvertInterval {
		n.advertNow = false
		n.lastAdvert = now
		n.broadcast(&eff, appendAdvert(make([]byte, 0, maxMessageSize), n.cfg.ID, n.current))
	}

	n.mu.Unlock()
	n.apply(ctx, &eff)
}

func (n *Node) prunePeers(now time.Time) {
	for id, seen := range n.peers {
		if now.Sub(seen) > n.cfg.Timing.PeerWindow {
			delete(n.peers, id)
		}
	}
}

func (n *Node) retryRadios(eff *effects, now time.Time) {
	for _, bs := range n.bands {
		if !bs.failed || now.Before(bs.retryAt) {
			continue
		}

		bs.retryAt = now.Add(n.cfg.Timing.RadioRetry)

		if bs.mode != modeSearching {
			bs.mode = modeSettled
			bs.modeSince = now
		}

		n.tuneTo(eff, bs, bs.op)
	}
}

func (n *Node) tickBand(eff *effects, bs *bandState, now time.Time) {
	if bs.failed {
		return
	}

	t := n.cfg.Timing

	if !bs.switchAt.IsZero() && !now.Before(bs.switchAt) {
		hadPeers := !bs.lastPeer.IsZero() && now.Sub(bs.lastPeer) < t.PeerWindow
		bs.prev = bs.op
		bs.switchAt = time.Time{}
		bs.modeSince = now
		bs.mode = modeSettled

		if hadPeers {
			bs.mode = modeVerifying
			bs.deadline = now.Add(t.VerifyBound)
		}

		n.tuneTo(eff, bs, bs.switchTo)

		return
	}

	heard := !bs.lastPeer.Before(bs.modeSince)

	switch bs.mode {
	case modeVerifying:
		n.tickVerifying(eff, bs, heard, now)
	case modeFallback:
		if heard {
			bs.mode = modeSettled
		} else if !now.Before(bs.deadline) {
			bs.mode = modeSettled
			bs.modeSince = now
			n.tuneTo(eff, bs, n.target(bs))
		}
	case modeSettled:
		n.correctDrift(eff, bs, now)
	case modeSearching:
	}
}

func (n *Node) tickVerifying(eff *effects, bs *bandState, heard bool, now time.Time) {
	switch {
	case heard:
		bs.mode = modeSettled
	case !now.Before(bs.deadline):
		n.stats.Fallbacks++
		bs.mode = modeFallback
		bs.modeSince = now
		bs.deadline = now.Add(n.cfg.Timing.FallbackBound)
		n.tuneTo(eff, bs, bs.prev)
		n.cfg.Log.Warn().Str("band", bs.cfg.Initial.Band.String()).Msg("chanmig: no peers on new channel, falling back")
	}
}

// correctDrift returns a settled band that sits off its plan assignment
// (after a fallback or after following a lagging peer) to the plan once the
// band has heard nobody for PeerLossTimeout.
func (n *Node) correctDrift(eff *effects, bs *bandState, now time.Time) {
	t := n.target(bs)
	if bs.op == t || !bs.switchAt.IsZero() {
		return
	}

	quiet := n.cfg.Timing.PeerLossTimeout
	if now.Sub(bs.modeSince) >= quiet && (bs.lastPeer.IsZero() || now.Sub(bs.lastPeer) >= quiet) {
		bs.modeSince = now
		n.tuneTo(eff, bs, t)
	}
}

func (n *Node) checkAbort(eff *effects, now time.Time) {
	if !n.coordinating || n.abortChecked || n.current == nil {
		return
	}

	p := &n.current.Plan
	if now.Before(p.ActivateAt.Add(-n.cfg.Timing.AbortMargin)) {
		return
	}

	n.abortChecked = true

	if !now.Before(p.ActivateAt) || len(n.peers) == 0 || 2*len(n.acks) > len(n.peers) {
		return
	}

	revert := Plan{
		MeshID:      p.MeshID,
		Version:     p.Version + 1,
		IssuedAt:    now,
		ActivateAt:  p.ActivateAt,
		Assignments: n.revertAssignments(),
	}

	sp, err := n.cfg.Signer.Sign(revert)
	if err != nil {
		n.cfg.Log.Error().Err(err).Msg("chanmig: cannot sign revert plan")

		return
	}

	n.stats.Aborts++
	n.cfg.Log.Warn().Int("acks", len(n.acks)).Int("peers", len(n.peers)).Msg("chanmig: too few acks, reverting plan")
	n.offer(eff, sp, now)
	// The revert carries no new information to wait for.
	n.abortChecked = true
}

// revertAssignments returns the assignments the mesh used before the
// current plan: the previous plan if one is held, else the provisioned
// initial assignments.
func (n *Node) revertAssignments() []Assignment {
	if len(n.history) > 0 {
		return slices.Clone(n.history[0].Plan.Assignments)
	}

	out := make([]Assignment, 0, len(n.bands))
	for _, bs := range n.bands {
		out = append(out, bs.cfg.Initial)
	}

	return out
}

// target is the assignment band bs should use under the current plan.
func (n *Node) target(bs *bandState) Assignment {
	if n.current != nil {
		if a, ok := n.current.Plan.Assignment(bs.cfg.Initial.Band); ok {
			return a
		}
	}

	return bs.cfg.Initial
}

func (n *Node) tuneTo(eff *effects, bs *bandState, a Assignment) {
	bs.op = a
	eff.tunes = append(eff.tunes, tuneReq{bs: bs, a: a})
}

func (n *Node) broadcast(eff *effects, payload []byte) {
	for _, bs := range n.bands {
		if !bs.failed {
			eff.sends = append(eff.sends, sendReq{band: bs.cfg.Initial.Band, payload: payload})
		}
	}
}

// apply executes effects outside mu. Callers hold ioMu.
func (n *Node) apply(ctx context.Context, eff *effects) {
	for _, t := range eff.tunes {
		err := n.cfg.Radio.SetChannel(ctx, t.a)

		n.mu.Lock()
		n.radioResult(t, err)
		n.mu.Unlock()
	}

	for _, s := range eff.sends {
		if err := n.cfg.Transport.Broadcast(ctx, s.band, s.payload); err != nil {
			n.mu.Lock()
			n.stats.SendErrors++
			n.mu.Unlock()
			n.cfg.Log.Debug().Err(err).Str("band", s.band.String()).Msg("chanmig: broadcast failed")
		}
	}

	if eff.save != nil {
		if err := n.cfg.Store.Save(ctx, eff.save); err != nil {
			n.cfg.Log.Warn().Err(err).Msg("chanmig: cannot persist plans")
		}
	}
}

func (n *Node) radioResult(t tuneReq, err error) {
	if err == nil {
		t.bs.failed = false

		return
	}

	n.stats.RadioErrors++

	if !t.bs.failed {
		t.bs.failed = true
		t.bs.retryAt = n.cfg.Clock.Now().Add(n.cfg.Timing.RadioRetry)
		n.cfg.Log.Warn().Err(err).Str("assignment", t.a.String()).Msg("chanmig: radio tune failed")
	}
}

// records encodes current and history plans for the Store, newest first.
func (n *Node) records() [][]byte {
	out := make([][]byte, 0, 1+len(n.history))
	if n.current != nil {
		out = append(out, encodeRecord(n.current))
	}

	for i := range n.history {
		out = append(out, encodeRecord(&n.history[i]))
	}

	return out
}

func encodeRecord(sp *SignedPlan) []byte {
	buf := make([]byte, 0, 2+len(sp.Raw)+len(sp.Sig))
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(sp.Raw)))
	buf = append(buf, sp.Raw...)

	return append(buf, sp.Sig...)
}

func decodeRecord(rec []byte) (raw, sig []byte, ok bool) {
	if len(rec) < 2 {
		return nil, nil, false
	}

	n := int(binary.BigEndian.Uint16(rec))
	if len(rec) < 2+n {
		return nil, nil, false
	}

	return rec[2 : 2+n], rec[2+n:], true
}

// Propose signs and starts distributing a new plan that switches to
// assignments lead from now. Only nodes holding a Signer may propose.
func (n *Node) Propose(ctx context.Context, assignments []Assignment, lead time.Duration) (SignedPlan, error) {
	if n.cfg.Signer == nil {
		return SignedPlan{}, ErrNoSigner
	}

	n.ioMu.Lock()
	defer n.ioMu.Unlock()

	n.mu.Lock()

	now := n.cfg.Clock.Now()
	version := uint64(1)

	if n.current != nil {
		version = n.current.Plan.Version + 1
	}

	sorted := slices.Clone(assignments)
	slices.SortFunc(sorted, func(a, b Assignment) int { return int(a.Band) - int(b.Band) })

	sp, err := n.cfg.Signer.Sign(Plan{
		MeshID:      n.cfg.Verifier.MeshID(),
		Version:     version,
		IssuedAt:    now,
		ActivateAt:  now.Add(lead),
		Stagger:     n.cfg.Timing.Stagger,
		Assignments: sorted,
	})
	if err == nil {
		err = n.checkPolicy(&sp.Plan)
	}

	if err != nil {
		n.mu.Unlock()

		return SignedPlan{}, err
	}

	var eff effects

	n.offer(&eff, sp, now)
	n.mu.Unlock()
	n.apply(ctx, &eff)

	return sp, nil
}

// Status returns a snapshot of the node.
func (n *Node) Status() Status {
	n.mu.Lock()
	defer n.mu.Unlock()

	st := Status{
		Operating:  make([]Assignment, 0, len(n.bands)),
		Phase:      n.derivedPhase(),
		Peers:      len(n.peers),
		Acks:       len(n.acks),
		Stats:      n.stats,
		LastSearch: n.lastSearch,
	}

	for _, bs := range n.bands {
		if !bs.failed {
			st.Operating = append(st.Operating, bs.op)
		}
	}

	if n.current != nil {
		st.Version = n.current.Plan.Version
		st.SignerID = n.current.Plan.SignerID
	}

	return st
}

func (n *Node) derivedPhase() Phase {
	if n.phase != PhaseStable {
		return n.phase
	}

	pending := false

	for _, bs := range n.bands {
		if bs.mode == modeVerifying || bs.mode == modeFallback {
			return PhaseMigrating
		}

		if !bs.switchAt.IsZero() {
			pending = true
		}
	}

	if pending {
		return PhasePending
	}

	return PhaseStable
}

// Run calls Tick for every value received from ticks until ctx is done or
// ticks is closed. Pass time.NewTicker(d).C in production.
func (n *Node) Run(ctx context.Context, ticks <-chan time.Time) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-ticks:
			if !ok {
				return nil
			}

			n.Tick(ctx)
		}
	}
}

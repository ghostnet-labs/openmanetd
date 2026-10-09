package chanmig_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/chanmig"
)

var errRadio = errors.New("fake radio failure")

// simTime is the shared simulated wall clock.
type simTime struct {
	now time.Time
	mu  sync.Mutex
}

func (s *simTime) get() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.now
}

func (s *simTime) advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.now = s.now.Add(d)
}

// fakeClock is one node's view of simTime, optionally offset and unsynced.
type fakeClock struct {
	base   *simTime
	mu     sync.Mutex
	offset time.Duration
	synced bool
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.base.get().Add(c.offset)
}

func (c *fakeClock) Synced() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.synced
}

func (c *fakeClock) set(offset time.Duration, synced bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.offset, c.synced = offset, synced
}

// fakeRadio records the assignment each band is tuned to. A band in
// failing returns errRadio and is left untuned.
type fakeRadio struct {
	tuned   map[chanmig.Band]chanmig.Assignment
	failing map[chanmig.Band]bool
	mu      sync.Mutex
	calls   int
}

func newFakeRadio() *fakeRadio {
	return &fakeRadio{tuned: map[chanmig.Band]chanmig.Assignment{}, failing: map[chanmig.Band]bool{}}
}

func (r *fakeRadio) SetChannel(_ context.Context, a chanmig.Assignment) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls++

	if r.failing[a.Band] {
		delete(r.tuned, a.Band)

		return errRadio
	}

	r.tuned[a.Band] = a

	return nil
}

func (r *fakeRadio) on(b chanmig.Band) (chanmig.Assignment, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	a, ok := r.tuned[b]

	return a, ok
}

func (r *fakeRadio) setFailing(b chanmig.Band, fail bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.failing[b] = fail
	if fail {
		delete(r.tuned, b)
	}
}

// fakeStore keeps records in memory.
type fakeStore struct {
	loadErr error
	saveErr error
	records [][]byte
	mu      sync.Mutex
	saves   int
}

func (s *fakeStore) Load(context.Context) ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.loadErr != nil {
		return nil, s.loadErr
	}

	out := make([][]byte, len(s.records))
	for i, r := range s.records {
		out[i] = append([]byte(nil), r...)
	}

	return out, nil
}

func (s *fakeStore) Save(_ context.Context, records [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.saves++
	if s.saveErr != nil {
		return s.saveErr
	}

	s.records = make([][]byte, len(records))
	for i, r := range records {
		s.records[i] = append([]byte(nil), r...)
	}

	return nil
}

func (s *fakeStore) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.saves
}

type delivery struct {
	payload []byte
	from    chanmig.NodeID
	a       chanmig.Assignment
	band    chanmig.Band
}

type linkKey struct {
	from, to chanmig.NodeID
	band     chanmig.Band
}

// fakeMedium models one-hop broadcast per band. A frame reaches a node
// whose radio is tuned to the sender's exact assignment, unless the
// assignment is jammed, the link is cut or either node is powered off.
type fakeMedium struct {
	nodes  map[chanmig.NodeID]*simNode
	cut    map[linkKey]bool
	jammed map[chanmig.Assignment]bool
	queue  []delivery
	order  []chanmig.NodeID
	mu     sync.Mutex
}

func newFakeMedium() *fakeMedium {
	return &fakeMedium{
		nodes:  map[chanmig.NodeID]*simNode{},
		cut:    map[linkKey]bool{},
		jammed: map[chanmig.Assignment]bool{},
	}
}

// fakeTransport is one node's handle on the medium.
type fakeTransport struct {
	m    *fakeMedium
	id   chanmig.NodeID
	fail bool
}

func (t *fakeTransport) Broadcast(_ context.Context, band chanmig.Band, payload []byte) error {
	if t.fail {
		return errors.New("fake transport failure")
	}

	t.m.mu.Lock()
	defer t.m.mu.Unlock()

	sn := t.m.nodes[t.id]
	if sn == nil || !sn.alive {
		return nil
	}

	a, ok := sn.radio.on(band)
	if !ok {
		return nil
	}

	p := append([]byte(nil), payload...)
	t.m.queue = append(t.m.queue, delivery{from: t.id, band: band, a: a, payload: p})

	return nil
}

func (m *fakeMedium) setCut(from, to chanmig.NodeID, band chanmig.Band, cut bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cut[linkKey{from: from, to: to, band: band}] = cut
}

func (m *fakeMedium) jam(a chanmig.Assignment) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.jammed[a] = true
}

type handoff struct {
	to      *simNode
	payload []byte
	band    chanmig.Band
}

// pump delivers queued frames until the medium is quiet.
func (m *fakeMedium) pump(ctx context.Context) {
	for range 64 {
		m.mu.Lock()
		q := m.queue
		m.queue = nil

		var out []handoff

		for _, d := range q {
			if m.jammed[d.a] {
				continue
			}

			for _, id := range m.order {
				sn := m.nodes[id]
				if id == d.from || !sn.alive || m.cut[linkKey{from: d.from, to: id, band: d.band}] {
					continue
				}

				if a, ok := sn.radio.on(d.band); ok && a == d.a {
					out = append(out, handoff{to: sn, band: d.band, payload: d.payload})
				}
			}
		}
		m.mu.Unlock()

		if len(out) == 0 && len(q) == 0 {
			return
		}

		for _, h := range out {
			h.to.node.HandleMessage(ctx, h.band, h.payload)
		}
	}
}

// Test topology: three bands with small search lists.
func assign(b chanmig.Band, ch, w uint16) chanmig.Assignment {
	return chanmig.Assignment{Band: b, Channel: ch, WidthMHz: w}
}

func halow(ch uint16) chanmig.Assignment  { return assign(chanmig.BandHaLow, ch, 1) }
func wifi24(ch uint16) chanmig.Assignment { return assign(chanmig.Band2G4, ch, 20) }
func wifi5(ch uint16) chanmig.Assignment  { return assign(chanmig.Band5G, ch, 80) }

func testBands() []chanmig.BandConfig {
	return []chanmig.BandConfig{
		{
			Initial:    halow(1),
			Rendezvous: []chanmig.Assignment{halow(9), halow(10)},
			Allowed:    []chanmig.Assignment{halow(1), halow(2), halow(3), halow(4), halow(5), halow(6), halow(7), halow(8)},
		},
		{
			Initial: wifi24(1),
			Allowed: []chanmig.Assignment{wifi24(1), wifi24(6), wifi24(11)},
		},
		{
			Initial:    wifi5(36),
			Rendezvous: []chanmig.Assignment{assign(chanmig.Band5G, 165, 20)},
			Allowed:    []chanmig.Assignment{wifi5(36), wifi5(44), wifi5(149), wifi5(157)},
		},
	}
}

const testMeshID = 0x0E1A

func testKey(seed byte) ed25519.PrivateKey {
	s := make([]byte, ed25519.SeedSize)
	for i := range s {
		s[i] = seed
	}

	return ed25519.NewKeyFromSeed(s)
}

func testVerifier(t *testing.T) *chanmig.Verifier {
	t.Helper()

	v, err := chanmig.NewVerifier(testMeshID, testKey(1).Public().(ed25519.PublicKey))
	require.NoError(t, err)

	return v
}

// simNode is one simulated node and its fakes, which survive power cycles.
type simNode struct {
	node      *chanmig.Node
	radio     *fakeRadio
	store     *fakeStore
	clock     *fakeClock
	transport *fakeTransport
	id        chanmig.NodeID
	coord     bool
	alive     bool
}

type sim struct {
	t      *testing.T
	time   *simTime
	medium *fakeMedium
	nodes  map[chanmig.NodeID]*simNode
	order  []chanmig.NodeID
	step   time.Duration
}

func newSim(t *testing.T) *sim {
	t.Helper()

	return &sim{
		t:      t,
		time:   &simTime{now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)},
		medium: newFakeMedium(),
		nodes:  map[chanmig.NodeID]*simNode{},
		step:   100 * time.Millisecond,
	}
}

// add creates and starts a node. Coordinators hold the plan-authority key.
func (s *sim) add(id chanmig.NodeID, coord bool) *simNode {
	s.t.Helper()

	sn := &simNode{
		id:    id,
		coord: coord,
		radio: newFakeRadio(),
		store: &fakeStore{},
		clock: &fakeClock{base: s.time, synced: true},
	}
	sn.transport = &fakeTransport{m: s.medium, id: id}

	s.medium.mu.Lock()
	s.medium.nodes[id] = sn
	s.medium.order = append(s.medium.order, id)
	s.medium.mu.Unlock()

	s.nodes[id] = sn
	s.order = append(s.order, id)
	s.powerOn(id)

	return sn
}

func (s *sim) newNode(sn *simNode) *chanmig.Node {
	s.t.Helper()

	cfg := chanmig.Config{
		ID:        sn.id,
		Log:       zerolog.Nop(),
		Clock:     sn.clock,
		Transport: sn.transport,
		Radio:     sn.radio,
		Store:     sn.store,
		Verifier:  testVerifier(s.t),
		Bands:     testBands(),
		Timing:    chanmig.DefaultTiming(),
	}

	if sn.coord {
		signer, err := chanmig.NewSigner(sn.id, testKey(1))
		require.NoError(s.t, err)

		cfg.Signer = signer
	}

	n, err := chanmig.New(cfg)
	require.NoError(s.t, err)

	return n
}

// powerOn boots a fresh Node instance over the node's persistent fakes.
func (s *sim) powerOn(id chanmig.NodeID) {
	s.t.Helper()

	sn := s.nodes[id]
	n := s.newNode(sn)

	s.medium.mu.Lock()
	sn.node = n
	sn.alive = true
	s.medium.mu.Unlock()

	require.NoError(s.t, n.Start(s.t.Context()))
}

func (s *sim) powerOff(id chanmig.NodeID) {
	s.medium.mu.Lock()
	defer s.medium.mu.Unlock()

	s.nodes[id].alive = false
}

// isolate cuts (or restores) every link between id and all other nodes on
// the given bands.
func (s *sim) isolate(id chanmig.NodeID, cut bool, bands ...chanmig.Band) {
	for _, other := range s.order {
		if other == id {
			continue
		}

		for _, b := range bands {
			s.medium.setCut(id, other, b, cut)
			s.medium.setCut(other, id, b, cut)
		}
	}
}

// partition cuts (or restores) every link between groups a and b.
func (s *sim) partition(a, b []chanmig.NodeID, cut bool) {
	for _, x := range a {
		for _, y := range b {
			for _, band := range allBands() {
				s.medium.setCut(x, y, band, cut)
				s.medium.setCut(y, x, band, cut)
			}
		}
	}
}

func allBands() []chanmig.Band {
	return []chanmig.Band{chanmig.BandHaLow, chanmig.Band2G4, chanmig.Band5G}
}

func (s *sim) tick() {
	s.time.advance(s.step)

	for _, id := range s.order {
		sn := s.nodes[id]
		if sn.alive {
			sn.node.Tick(s.t.Context())
		}
	}

	s.medium.pump(s.t.Context())
}

func (s *sim) run(d time.Duration) {
	for end := s.time.get().Add(d); s.time.get().Before(end); {
		s.tick()
	}
}

// runUntil ticks until cond holds or limit elapses and returns the elapsed
// simulated time.
func (s *sim) runUntil(limit time.Duration, cond func() bool) (time.Duration, bool) {
	start := s.time.get()

	for s.time.get().Sub(start) <= limit {
		if cond() {
			return s.time.get().Sub(start), true
		}

		s.tick()
	}

	return s.time.get().Sub(start), cond()
}

func (s *sim) now() time.Time { return s.time.get() }

func (s *sim) status(id chanmig.NodeID) chanmig.Status { return s.nodes[id].node.Status() }

// onPlan reports whether node id holds version and every working radio is
// tuned to plan's assignment for its band.
func (s *sim) onPlan(id chanmig.NodeID, sp chanmig.SignedPlan) bool {
	sn := s.nodes[id]
	st := sn.node.Status()

	if st.Version != sp.Plan.Version || st.SignerID != sp.Plan.SignerID || st.Phase != chanmig.PhaseStable {
		return false
	}

	for _, a := range sp.Plan.Assignments {
		if got, ok := sn.radio.on(a.Band); !ok || got != a {
			return false
		}
	}

	return true
}

func (s *sim) allOnPlan(sp chanmig.SignedPlan, ids ...chanmig.NodeID) bool {
	for _, id := range ids {
		if !s.onPlan(id, sp) {
			return false
		}
	}

	return true
}

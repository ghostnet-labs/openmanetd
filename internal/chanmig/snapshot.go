package chanmig

import (
	"crypto/ed25519"
	"slices"
	"time"
)

// BandSnapshot is the migration state of one managed band.
type BandSnapshot struct {
	// ModeSince is when the band entered Mode (or was last re-tuned in it).
	ModeSince time.Time
	// Deadline ends the verify or fallback window; zero unless Mode is
	// BandVerifying or BandFallback.
	Deadline time.Time
	// SwitchAt is when the band switches to SwitchTo; zero when no switch
	// is scheduled.
	SwitchAt time.Time
	// LastPeer is when a peer was last heard on the band; zero when none
	// was heard since start.
	LastPeer time.Time
	// Operating is the assignment the radio is tuned to (or, while Failed,
	// the one it will be retried on).
	Operating Assignment
	// Target is the current plan's assignment for the band, or the
	// provisioned initial assignment when no plan covers it.
	Target Assignment
	// SwitchTo is the scheduled assignment; zero when SwitchAt is zero.
	SwitchTo Assignment
	// Previous is the assignment used before the last switch; zero (Band
	// 0) before the first switch.
	Previous Assignment
	// SearchCandidates is the length of the band's rediscovery search
	// list; zero before the first search.
	SearchCandidates int
	Mode             BandMode
	// Failed is true when the last tune failed.
	Failed bool
}

// SearchSnapshot is the current or most recent rediscovery schedule.
type SearchSnapshot struct {
	// Started is when the search started; zero when none ran.
	Started time.Time
	Epoch   int
	Step    int
	Steps   int
	Parks   int
	Parking bool
}

// Snapshot is a detailed point-in-time view of a Node for operators. Every
// slice, map and plan in it is a copy the caller owns, except the trusted
// keys' bytes, which are shared and must not be modified.
type Snapshot struct {
	// At is the node clock reading the snapshot was taken at.
	At time.Time
	// Peers maps every peer heard within the peer window to when it was
	// last heard on any band.
	Peers map[NodeID]time.Time
	// CurrentPlan is the accepted plan with the highest version; nil while
	// the node runs on its provisioned initial assignments.
	CurrentPlan *Plan
	// PreviousPlan is the plan CurrentPlan superseded; nil when none is
	// held.
	PreviousPlan *Plan
	// Bands has one entry per managed band, sorted by band.
	Bands []BandSnapshot
	// TrustedKeys are the plan-authority keys plans are verified against.
	TrustedKeys []ed25519.PublicKey
	Search      SearchSnapshot
	// LastSearch is how long the most recent completed search took.
	LastSearch time.Duration
	Stats      Stats
	MeshID     uint64
	ID         NodeID
	// Acks counts distinct nodes that acknowledged CurrentPlan.
	Acks  int
	Phase Phase
	// ClockSynced reports whether the clock is disciplined.
	ClockSynced bool
	// Coordinator is true when the node holds a plan-authority signer.
	Coordinator bool
	// Coordinating is true when the node signed CurrentPlan and collects
	// acknowledgements for it.
	Coordinating bool
}

// Snapshot returns a detailed copy of the node state. It is meant for
// status RPCs, not for hot paths: it allocates the copies it returns and
// nothing else.
func (n *Node) Snapshot() Snapshot {
	// Clock.Synced may make a system call; query it before taking mu.
	synced := n.cfg.Clock.Synced()

	n.mu.Lock()
	defer n.mu.Unlock()

	now := n.cfg.Clock.Now()

	s := Snapshot{
		At:           now,
		Peers:        make(map[NodeID]time.Time, len(n.peers)),
		Bands:        make([]BandSnapshot, 0, len(n.bands)),
		TrustedKeys:  n.cfg.Verifier.Keys(),
		LastSearch:   n.lastSearch,
		Stats:        n.stats,
		MeshID:       n.cfg.Verifier.MeshID(),
		ID:           n.cfg.ID,
		Acks:         len(n.acks),
		Phase:        n.derivedPhase(),
		ClockSynced:  synced,
		Coordinator:  n.cfg.Signer != nil,
		Coordinating: n.coordinating,
		Search: SearchSnapshot{
			Started: n.search.started,
			Epoch:   n.search.epoch,
			Step:    n.search.step,
			Steps:   n.search.steps,
			Parks:   n.search.parkCount,
			Parking: n.search.parking,
		},
	}

	// Peers older than the window are pruned only on Tick; leave them out
	// here so the view matches the protocol's notion of "present".
	for id, seen := range n.peers {
		if now.Sub(seen) <= n.cfg.Timing.PeerWindow {
			s.Peers[id] = seen
		}
	}

	if n.current != nil {
		s.CurrentPlan = clonePlan(&n.current.Plan)
	}

	if len(n.history) > 0 {
		s.PreviousPlan = clonePlan(&n.history[0].Plan)
	}

	for _, bs := range n.bands {
		s.Bands = append(s.Bands, n.bandSnapshot(bs))
	}

	return s
}

func (n *Node) bandSnapshot(bs *bandState) BandSnapshot {
	b := BandSnapshot{
		ModeSince:        bs.modeSince,
		LastPeer:         bs.lastPeer,
		Operating:        bs.op,
		Target:           n.target(bs),
		Previous:         bs.prev,
		SearchCandidates: len(bs.candidates),
		Mode:             bs.mode,
		Failed:           bs.failed,
	}

	if bs.mode == BandVerifying || bs.mode == BandFallback {
		b.Deadline = bs.deadline
	}

	if !bs.switchAt.IsZero() {
		b.SwitchAt = bs.switchAt
		b.SwitchTo = bs.switchTo
	}

	return b
}

func clonePlan(p *Plan) *Plan {
	c := *p
	c.Assignments = slices.Clone(p.Assignments)

	return &c
}

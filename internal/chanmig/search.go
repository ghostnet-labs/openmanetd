package chanmig

import (
	"slices"
	"time"
)

// enterSearch starts rediscovery. Every working radio walks its own
// candidate list in parallel, so a band that is still reachable (for
// example HaLow while Wi-Fi is out of range) finds the mesh without waiting
// for the others.
func (n *Node) enterSearch(eff *effects, now time.Time) {
	n.phase = PhaseSearching
	n.stats.Searches++
	n.search = searchState{started: now, stepStart: now}

	for _, bs := range n.bands {
		bs.candidates = n.candidates(bs, bs.candidates[:0])
		bs.mode = modeSearching
		bs.switchAt = time.Time{}
		n.search.steps = max(n.search.steps, len(bs.candidates))
		n.tuneTo(eff, bs, bs.candidates[0])
	}

	n.advertNow = true
	n.cfg.Log.Info().Int("steps", n.search.steps).Msg("chanmig: no peers, starting rediscovery")
}

// candidates builds band bs's ordered, de-duplicated search list:
// last-known plan, older plans newest first, the provisioned initial
// assignment, the rendezvous list, then the allowed sweep.
func (n *Node) candidates(bs *bandState, out []Assignment) []Assignment {
	band := bs.cfg.Initial.Band
	add := func(a Assignment) {
		if len(out) < maxSearchList && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}

	add(n.target(bs))

	for i := range n.history {
		if a, ok := n.history[i].Plan.Assignment(band); ok {
			add(a)
		}
	}

	add(bs.cfg.Initial)

	for _, a := range bs.cfg.Rendezvous {
		add(a)
	}

	for _, a := range bs.cfg.Allowed {
		add(a)
	}

	return out
}

// searchTick advances the seek/park schedule. A seek epoch visits each
// candidate for one Dwell. After a seek epoch the node may park on a
// rendezvous channel for two epochs so that two searching nodes (for
// example after a mesh-wide power loss) are guaranteed an overlap; it never
// parks twice in a row, which keeps the bound for a stable mesh at four
// epochs.
func (n *Node) searchTick(eff *effects, now time.Time) {
	s := &n.search
	dwell := n.cfg.Timing.Dwell

	if s.parking {
		if now.Sub(s.stepStart) >= 2*time.Duration(s.steps)*dwell {
			s.parking = false
			s.epoch++

			n.seekStep(eff, 0, now)
		}

		return
	}

	if now.Sub(s.stepStart) < dwell {
		return
	}

	if s.step+1 < s.steps {
		n.seekStep(eff, s.step+1, now)

		return
	}

	s.epoch++

	if !n.parkThisEpoch(s.epoch) {
		n.seekStep(eff, 0, now)

		return
	}

	s.parking = true
	s.parkCount++
	s.stepStart = now

	for _, bs := range n.bands {
		park := bs.candidates[0]
		if r := bs.cfg.Rendezvous; len(r) > 0 {
			park = r[(s.parkCount-1)%len(r)]
		}

		n.tuneTo(eff, bs, park)
	}
}

func (n *Node) seekStep(eff *effects, step int, now time.Time) {
	n.search.step = step
	n.search.stepStart = now

	for _, bs := range n.bands {
		if bs.failed {
			bs.op = bs.candidates[step%len(bs.candidates)]

			continue
		}

		n.tuneTo(eff, bs, bs.candidates[step%len(bs.candidates)])
	}
}

// parkThisEpoch is a deterministic per-node coin flip so that nodes which
// entered search together do not stay in lockstep.
func (n *Node) parkThisEpoch(epoch int) bool {
	x := uint64(n.cfg.ID) ^ (uint64(epoch) * 0x9e3779b97f4a7c15)
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31

	return x&1 == 1
}

// exitSearch ends rediscovery after a peer was heard on band found. The
// found band stays where the peer is unless the peer holds an older plan
// (it is about to move to ours); every other band goes to the current
// plan's assignment.
func (n *Node) exitSearch(eff *effects, found *bandState, peerBehind bool, now time.Time) {
	n.phase = PhaseStable
	n.stats.Recoveries++
	n.lastSearch = now.Sub(n.search.started)

	for _, bs := range n.bands {
		bs.mode = modeSettled
		bs.modeSince = now
		bs.switchAt = time.Time{}

		if bs == found && !peerBehind {
			continue
		}

		if t := n.target(bs); t != bs.op {
			n.tuneTo(eff, bs, t)
		}
	}

	n.cfg.Log.Info().Dur("took", n.lastSearch).Str("band", found.cfg.Initial.Band.String()).
		Msg("chanmig: rediscovered mesh")
}

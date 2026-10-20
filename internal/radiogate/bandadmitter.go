package radiogate

import (
	"context"
	"sync"

	"github.com/openmanet/openmanetd/internal/pathsel"
	"github.com/openmanet/openmanetd/internal/radiocap"
)

// Reasons BandAdmitter reports besides radiocap.Reason values.
const (
	// ReasonUnknownBand: the pathsel band has no radiocap equivalent.
	ReasonUnknownBand = "unknown_band"
)

// probeDemandID names the single synthetic demand AdmitBand evaluates.
const probeDemandID = "pathsel-probe"

// BandAdmitter adapts the capacity model to pathsel.BandAdmitter. It asks
// whether one mesh interface on band could run on an eligible PHY at all
// (radiocap.Model.Admit with no current demands), so it answers "does
// this node have usable radio capacity on the band", not "is there spare
// room beside everything already running"; per-change admission is the
// Gate's job.
//
// Outside enforce mode, and whenever the model is unavailable, every band
// is admitted: the capacity input must never take a path away until real
// qualification data exists. Answers are cached per model and policy, so
// a pathsel decision costs a mutex and a map lookup.
type BandAdmitter struct { //nolint:govet // fieldalignment: mu must sit directly above the fields it guards (.claude/rules/concurrency.md); the pointer-scan-optimal layout would separate them.
	// Source supplies the model.
	Source ModelSource
	// Mode returns the current gate mode. nil uses DefaultMode.
	Mode func() Mode
	// Policy returns the admission policy. nil uses radiocap.DefaultPolicy.
	Policy func() radiocap.Policy

	mu       sync.Mutex // protects the fields below
	model    *radiocap.Model
	cache    map[pathsel.Band]bandAnswer
	unqualOK bool
}

var _ pathsel.BandAdmitter = (*BandAdmitter)(nil)

type bandAnswer struct {
	reason string
	ok     bool
}

// NewBandAdmitter returns a BandAdmitter sharing g's model source, mode
// and policy.
func (g *Gate) NewBandAdmitter() *BandAdmitter {
	return &BandAdmitter{Source: g.Source, Mode: g.Mode, Policy: g.Policy}
}

// AdmitBand implements pathsel.BandAdmitter.
func (a *BandAdmitter) AdmitBand(band pathsel.Band) (ok bool, reason string) {
	mode := DefaultMode
	if a.Mode != nil {
		mode = a.Mode()
	}

	if mode != ModeEnforce || a.Source == nil {
		return true, ""
	}

	rb, known := radiocapBand(band)
	if !known {
		return false, ReasonUnknownBand
	}

	// pathsel.BandAdmitter carries no context; the probe bounds itself
	// (iwListTimeout) and is cached for the provider's TTL.
	m, err := a.Source.Model(context.Background())
	if err != nil {
		return true, ""
	}

	pol := radiocap.DefaultPolicy()
	if a.Policy != nil {
		pol = a.Policy()
	}

	a.mu.Lock()
	if a.model != m || a.unqualOK != pol.AllowUnqualified {
		a.model = m
		a.unqualOK = pol.AllowUnqualified
		a.cache = make(map[pathsel.Band]bandAnswer, 3)
	}

	if ans, hit := a.cache[band]; hit {
		a.mu.Unlock()

		return ans.ok, ans.reason
	}
	a.mu.Unlock()

	ans := admitOne(m, rb, pol.AllowUnqualified)

	a.mu.Lock()
	defer a.mu.Unlock()

	// The model may have moved on while Admit ran; only cache an answer
	// for the model it was computed from. The map holds at most one
	// entry per pathsel band.
	if a.model == m {
		a.cache[band] = ans
	}

	return ans.ok, ans.reason
}

func admitOne(m *radiocap.Model, b radiocap.Band, allowUnqualified bool) bandAnswer {
	ch, _ := UCIChannel(b, "", 0, 0)
	probe := []radiocap.Demand{{
		ID:      probeDemandID,
		Purpose: radiocap.PurposeMesh,
		Role:    radiocap.RoleMesh,
		Channel: ch,
	}}

	// Bounded interruption is meaningless with no current demands.
	dec, err := m.Admit(nil, probe, radiocap.Policy{AllowUnqualified: allowUnqualified})
	if err != nil {
		return bandAnswer{ok: true}
	}

	if dec.Outcome == radiocap.OutcomeAdmit {
		return bandAnswer{ok: true}
	}

	return bandAnswer{reason: string(dec.Reason)}
}

func radiocapBand(b pathsel.Band) (radiocap.Band, bool) {
	switch b {
	case pathsel.BandHaLow:
		return radiocap.BandS1G, true
	case pathsel.Band2G4:
		return radiocap.Band2G4, true
	case pathsel.Band5G:
		return radiocap.Band5G, true
	default:
		return "", false
	}
}

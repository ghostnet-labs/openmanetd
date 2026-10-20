package radiogate

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/openmanet/openmanetd/internal/radiocap"
	"github.com/rs/zerolog"
)

// Mode is how the gate treats a capacity decision.
type Mode string

// Modes. The values match config.RadioCapacityGate*.
const (
	// ModeOff skips the check: nothing is probed or recorded.
	ModeOff Mode = "off"
	// ModeAdvisory evaluates and records every change, warns on
	// defer/reject, and never blocks.
	ModeAdvisory Mode = "advisory"
	// ModeEnforce blocks a change the model defers or rejects.
	ModeEnforce Mode = "enforce"
	// DefaultMode is used for an unknown or unset mode.
	DefaultMode = ModeAdvisory
)

// ParseMode maps a configured string to a Mode. ok is false for an
// unknown value, which yields DefaultMode.
func ParseMode(s string) (m Mode, ok bool) {
	switch m := Mode(strings.ToLower(strings.TrimSpace(s))); m {
	case ModeOff, ModeAdvisory, ModeEnforce:
		return m, true
	default:
		return DefaultMode, false
	}
}

// ModelSource supplies the current capacity model. *Provider implements
// it.
type ModelSource interface {
	Model(ctx context.Context) (*radiocap.Model, error)
}

// Request is one radio configuration change to admit.
type Request struct {
	// Origin names the code path asking (for the decision history).
	Origin string
	// Radios are the UCI wifi-device sections being changed.
	Radios []string
	// Current are the demands that keep running unchanged.
	Current []radiocap.Demand
	// Requested are the demands the change brings up, in their
	// post-change state.
	Requested []radiocap.Demand
}

// DeniedError is returned by Gate.Evaluate in enforce mode when the model
// defers or rejects the change.
type DeniedError struct {
	Decision radiocap.Decision
}

func (e *DeniedError) Error() string {
	var b strings.Builder

	b.WriteString("radio capacity ")
	b.WriteString(string(e.Decision.Outcome))
	b.WriteString(" (")
	b.WriteString(string(e.Decision.Reason))
	b.WriteString("): ")
	b.WriteString(e.Decision.Explanation)

	return b.String()
}

// Gate checks radio configuration changes against the capacity model.
// Fields must be set before first use; Evaluate and Recent are safe for
// concurrent use.
type Gate struct {
	Log zerolog.Logger
	// Source supplies the model. nil behaves like a failed probe.
	Source ModelSource
	// Mode returns the current mode. nil uses DefaultMode.
	Mode func() Mode
	// Policy returns the admission policy. nil uses
	// radiocap.DefaultPolicy (reject on shortage, never interrupt).
	Policy func() radiocap.Policy
	// Now is the clock for history records. nil uses time.Now.
	Now func() time.Time

	history Ring
}

// errNoSource is recorded when the gate has no model source.
var errNoSource = errors.New("radiogate: no capacity model source")

func (g *Gate) mode() Mode {
	if g.Mode == nil {
		return DefaultMode
	}

	return g.Mode()
}

func (g *Gate) policy() radiocap.Policy {
	if g.Policy == nil {
		return radiocap.DefaultPolicy()
	}

	return g.Policy()
}

func (g *Gate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}

	return time.Now()
}

// Recent returns up to RingSize recent decisions, newest first.
func (g *Gate) Recent() []Record {
	return g.history.Recent()
}

// Evaluate decides whether req may proceed. It returns nil unless the
// mode is enforce and the model defers or rejects the change, in which
// case it returns a *DeniedError. A probe failure, an unidentified HaLow
// PHY or a request the model cannot evaluate never blocks: it is logged
// and recorded. Off mode does nothing.
func (g *Gate) Evaluate(ctx context.Context, req Request) error {
	mode := g.mode()
	if mode == ModeOff {
		return nil
	}

	if len(req.Requested) == 0 {
		// Nothing comes up (for example the change disables the radio's
		// only interface); there is nothing to admit.
		return nil
	}

	rec := Record{
		Time:      g.now(),
		Origin:    req.Origin,
		Mode:      mode,
		Radios:    slices.Clone(req.Radios),
		Current:   len(req.Current),
		Requested: len(req.Requested),
	}

	dec, err := g.decide(ctx, &req)
	if err != nil {
		rec.Error = err.Error()
		g.history.Add(rec)
		g.Log.Warn().Err(err).Str("origin", req.Origin).Strs("radios", req.Radios).
			Msg("Radio capacity check unavailable; proceeding without it")

		return nil
	}

	rec.Decision = &dec
	rec.Blocked = mode == ModeEnforce && dec.Outcome != radiocap.OutcomeAdmit
	g.history.Add(rec)

	if dec.Outcome == radiocap.OutcomeAdmit {
		g.Log.Debug().Str("origin", req.Origin).Strs("radios", req.Radios).
			Str("reason", string(dec.Reason)).Msg("Radio capacity admitted change")

		return nil
	}

	g.Log.Warn().Str("origin", req.Origin).Strs("radios", req.Radios).Str("mode", string(mode)).
		Str("outcome", string(dec.Outcome)).Str("reason", string(dec.Reason)).
		Str("explanation", dec.Explanation).Bool("blocked", rec.Blocked).
		Msg("Radio capacity does not admit change")

	if rec.Blocked {
		return &DeniedError{Decision: dec}
	}

	return nil
}

// ErrHaLowUnidentified is recorded when a request needs HaLow but no PHY
// was identified as HaLow (for example the Morse interface is down, so
// its wiphy could not be resolved). Evaluating anyway would reject every
// HaLow change for a reason that is not capacity.
var ErrHaLowUnidentified = errors.New("radiogate: HaLow demand but no PHY identified as HaLow")

func (g *Gate) decide(ctx context.Context, req *Request) (radiocap.Decision, error) {
	if g.Source == nil {
		return radiocap.Decision{}, errNoSource
	}

	m, err := g.Source.Model(ctx)
	if err != nil {
		return radiocap.Decision{}, err //nolint:wrapcheck // the source already names the probe step
	}

	if needsBand(req, radiocap.BandS1G) && !modelHasBand(m, radiocap.BandS1G) {
		return radiocap.Decision{}, ErrHaLowUnidentified
	}

	dec, err := m.Admit(req.Current, req.Requested, g.policy())
	if err != nil {
		return radiocap.Decision{}, err //nolint:wrapcheck // radiocap errors carry their own prefix
	}

	return dec, nil
}

func needsBand(req *Request, b radiocap.Band) bool {
	has := func(d radiocap.Demand) bool { return d.Channel.Band == b }

	return slices.ContainsFunc(req.Requested, has) || slices.ContainsFunc(req.Current, has)
}

func modelHasBand(m *radiocap.Model, b radiocap.Band) bool {
	return slices.ContainsFunc(m.Capacities(), func(c radiocap.Capacity) bool {
		return slices.Contains(c.Bands, b)
	})
}

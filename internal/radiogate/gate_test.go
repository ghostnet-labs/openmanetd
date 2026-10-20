package radiogate_test

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/openmanet/openmanetd/internal/radiocap"
	"github.com/openmanet/openmanetd/internal/radiogate"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allEvidence qualifies every PHY of the node fixture for sharing.
func allEvidence() radiogate.Settings {
	all := []string{"phy0", "phy1", "phy2"}

	return radiogate.Settings{QualifiedPHYs: all, SharingQualifiedPHYs: all}
}

func nodeModel(t *testing.T, s radiogate.Settings) *radiocap.Model {
	t.Helper()

	p, _, _ := newProvider(t, s)

	m, err := p.Model(context.Background())
	require.NoError(t, err)

	return m
}

func mustChannel(t *testing.T, band radiocap.Band, ch string, width uint32) radiocap.Channel {
	t.Helper()

	c, ok := radiogate.UCIChannel(band, ch, width, 0)
	require.True(t, ok)

	return c
}

// halowJoin is a HaLow mesh change beside a running 2.4 GHz mesh.
func halowJoin(t *testing.T) radiogate.Request {
	t.Helper()

	return radiogate.Request{
		Origin: "test",
		Radios: []string{"radio3"},
		Current: []radiocap.Demand{{
			ID: "batmesh1_radio0", Purpose: radiocap.PurposeMesh, Role: radiocap.RoleMesh,
			Channel: mustChannel(t, radiocap.Band2G4, "6", 20),
		}},
		Requested: []radiocap.Demand{{
			ID: "default_radio3", Purpose: radiocap.PurposeMesh, Role: radiocap.RoleMesh,
			Channel: mustChannel(t, radiocap.BandS1G, "28", 8),
		}},
	}
}

// shortRequest puts two different 2.4 GHz channels on the one 2.4 GHz
// PHY (#channels <= 1): capacity_short even with full evidence.
func shortRequest(t *testing.T) radiogate.Request {
	t.Helper()

	return radiogate.Request{
		Origin: "test",
		Radios: []string{"radio2"},
		Current: []radiocap.Demand{{
			ID: "mesh", Purpose: radiocap.PurposeMesh, Role: radiocap.RoleMesh,
			Channel: mustChannel(t, radiocap.Band2G4, "1", 20),
		}},
		Requested: []radiocap.Demand{{
			ID: "ap", Purpose: radiocap.PurposeEUD, Role: radiocap.RoleAP,
			Channel: mustChannel(t, radiocap.Band2G4, "11", 20),
		}},
	}
}

func newGate(src radiogate.ModelSource, mode radiogate.Mode, log zerolog.Logger) *radiogate.Gate {
	return &radiogate.Gate{
		Log:    log,
		Source: src,
		Mode:   func() radiogate.Mode { return mode },
	}
}

func TestParseMode(t *testing.T) {
	tests := []struct {
		in     string
		want   radiogate.Mode
		wantOK bool
	}{
		{in: "off", want: radiogate.ModeOff, wantOK: true},
		{in: "advisory", want: radiogate.ModeAdvisory, wantOK: true},
		{in: " ENFORCE ", want: radiogate.ModeEnforce, wantOK: true},
		{in: "", want: radiogate.DefaultMode},
		{in: "block", want: radiogate.DefaultMode},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := radiogate.ParseMode(tc.in)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantOK, ok)
		})
	}

	assert.Equal(t, radiogate.ModeAdvisory, radiogate.DefaultMode, "advisory is the shipped default")
}

func TestGate_Evaluate_decisions(t *testing.T) {
	unavailHaLow := allEvidence()
	unavailHaLow.UnavailablePHYs = []string{"phy2"}

	tests := []struct {
		name        string
		settings    radiogate.Settings
		req         func(*testing.T) radiogate.Request
		mode        radiogate.Mode
		wantOutcome radiocap.Outcome
		wantReason  radiocap.Reason
		wantBlocked bool
		wantWarn    bool
	}{
		{
			name: "advisory admit", settings: allEvidence(), req: halowJoin, mode: radiogate.ModeAdvisory,
			wantOutcome: radiocap.OutcomeAdmit, wantReason: radiocap.ReasonFits,
		},
		{
			name: "advisory reject never blocks", settings: radiogate.Settings{}, req: halowJoin, mode: radiogate.ModeAdvisory,
			wantOutcome: radiocap.OutcomeReject, wantReason: radiocap.ReasonUnqualifiedOnly, wantWarn: true,
		},
		{
			name: "advisory reject no eligible PHY", settings: radiogate.Settings{}, req: shortRequest, mode: radiogate.ModeAdvisory,
			wantOutcome: radiocap.OutcomeReject, wantReason: radiocap.ReasonNoEligiblePHY, wantWarn: true,
		},
		{
			name: "advisory defer never blocks", settings: unavailHaLow, req: halowJoin, mode: radiogate.ModeAdvisory,
			wantOutcome: radiocap.OutcomeDefer, wantReason: radiocap.ReasonPHYUnavailable, wantWarn: true,
		},
		{
			name: "enforce admit", settings: allEvidence(), req: halowJoin, mode: radiogate.ModeEnforce,
			wantOutcome: radiocap.OutcomeAdmit, wantReason: radiocap.ReasonFits,
		},
		{
			name: "enforce reject unqualified", settings: radiogate.Settings{SharingQualifiedPHYs: []string{"phy0", "phy2"}},
			req: halowJoin, mode: radiogate.ModeEnforce,
			wantOutcome: radiocap.OutcomeReject, wantReason: radiocap.ReasonUnqualifiedOnly, wantBlocked: true, wantWarn: true,
		},
		{
			name: "enforce reject capacity short", settings: allEvidence(), req: shortRequest, mode: radiogate.ModeEnforce,
			wantOutcome: radiocap.OutcomeReject, wantReason: radiocap.ReasonCapacityShort, wantBlocked: true, wantWarn: true,
		},
		{
			name: "enforce defer blocks", settings: unavailHaLow, req: halowJoin, mode: radiogate.ModeEnforce,
			wantOutcome: radiocap.OutcomeDefer, wantReason: radiocap.ReasonPHYUnavailable, wantBlocked: true, wantWarn: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer

			src := &fakeSource{model: nodeModel(t, tc.settings)}
			g := newGate(src, tc.mode, zerolog.New(&logs).Level(zerolog.InfoLevel))

			err := g.Evaluate(context.Background(), tc.req(t))

			if tc.wantBlocked {
				var denied *radiogate.DeniedError
				require.ErrorAs(t, err, &denied)
				assert.Equal(t, tc.wantOutcome, denied.Decision.Outcome)
				assert.Equal(t, tc.wantReason, denied.Decision.Reason)
				assert.Contains(t, err.Error(), string(tc.wantReason))
				assert.Contains(t, err.Error(), denied.Decision.Explanation)
			} else {
				require.NoError(t, err)
			}

			recs := g.Recent()
			require.Len(t, recs, 1)
			rec := recs[0]
			require.NotNil(t, rec.Decision)
			assert.Empty(t, rec.Error)
			assert.Equal(t, tc.wantOutcome, rec.Decision.Outcome)
			assert.Equal(t, tc.wantReason, rec.Decision.Reason)
			assert.Equal(t, tc.wantBlocked, rec.Blocked)
			assert.Equal(t, tc.mode, rec.Mode)
			assert.Equal(t, "test", rec.Origin)
			assert.Equal(t, radiocap.NotEvaluated, rec.Decision.PhysicalQualification)

			if tc.wantWarn {
				assert.Contains(t, logs.String(), `"level":"warn"`)
				assert.Contains(t, logs.String(), string(tc.wantReason))
			} else {
				assert.Empty(t, logs.String(), "an admission logs at debug only")
			}
		})
	}
}

func TestGate_Evaluate_offDoesNothing(t *testing.T) {
	src := &fakeSource{err: errors.New("must not be called")}
	g := newGate(src, radiogate.ModeOff, zerolog.Nop())

	require.NoError(t, g.Evaluate(context.Background(), halowJoin(t)))
	assert.Equal(t, 0, src.getCalls(), "off mode never probes")
	assert.Empty(t, g.Recent())
}

func TestGate_Evaluate_nothingRequested(t *testing.T) {
	src := &fakeSource{model: nodeModel(t, radiogate.Settings{})}
	g := newGate(src, radiogate.ModeEnforce, zerolog.Nop())

	req := halowJoin(t)
	req.Requested = nil

	require.NoError(t, g.Evaluate(context.Background(), req))
	assert.Equal(t, 0, src.getCalls())
	assert.Empty(t, g.Recent())
}

func TestGate_Evaluate_neverBlocksWhenCheckCannotRun(t *testing.T) {
	noHaLow := allEvidence()

	tests := []struct {
		name    string
		src     radiogate.ModelSource
		req     func(*testing.T) radiogate.Request
		wantErr string
	}{
		{
			name:    "probe failure",
			src:     &fakeSource{err: errors.New("radiogate: iw list: exec: not found")},
			req:     halowJoin,
			wantErr: "iw list",
		},
		{name: "no source", src: nil, req: halowJoin, wantErr: "no capacity model source"},
		{
			name: "HaLow PHY not identified",
			src: func() radiogate.ModelSource {
				p := &radiogate.Provider{
					Exec:     func(context.Context) ([]byte, error) { return fixture(t, nodeFixture), nil },
					Settings: func() radiogate.Settings { return noHaLow },
				}

				return p
			}(),
			req:     halowJoin,
			wantErr: "HaLow",
		},
		{
			name: "invalid demands",
			src:  &fakeSource{model: nodeModel(t, allEvidence())},
			req: func(t *testing.T) radiogate.Request {
				t.Helper()

				r := halowJoin(t)
				r.Requested = append(r.Requested, r.Requested[0])

				return r
			},
			wantErr: "duplicate demand",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer

			g := newGate(tc.src, radiogate.ModeEnforce, zerolog.New(&logs))

			require.NoError(t, g.Evaluate(context.Background(), tc.req(t)), "enforce never blocks on a failed check")

			recs := g.Recent()
			require.Len(t, recs, 1)
			assert.Nil(t, recs[0].Decision)
			assert.Nil(t, recs[0].RequestedDemands, "demands are kept only with a decision")
			assert.Nil(t, recs[0].CurrentDemands)
			assert.False(t, recs[0].Blocked)
			assert.Contains(t, recs[0].Error, tc.wantErr)
			assert.Contains(t, logs.String(), "proceeding without it")
		})
	}
}

func TestGate_Evaluate_defaultsWithNilFuncs(t *testing.T) {
	g := &radiogate.Gate{Log: zerolog.Nop(), Source: &fakeSource{model: nodeModel(t, radiogate.Settings{})}}

	require.NoError(t, g.Evaluate(context.Background(), halowJoin(t)), "nil Mode is advisory")

	recs := g.Recent()
	require.Len(t, recs, 1)
	assert.Equal(t, radiogate.ModeAdvisory, recs[0].Mode)
	assert.Equal(t, radiocap.OutcomeReject, recs[0].Decision.Outcome, "nil Policy is the default reject-on-shortage policy")
}

func TestGate_CurrentMode(t *testing.T) {
	assert.Equal(t, radiogate.DefaultMode, (&radiogate.Gate{}).CurrentMode(), "nil Mode is the default")

	for _, m := range []radiogate.Mode{radiogate.ModeOff, radiogate.ModeAdvisory, radiogate.ModeEnforce} {
		assert.Equal(t, m, newGate(nil, m, zerolog.Nop()).CurrentMode())
	}
}

func TestGate_Evaluate_recordsDemands(t *testing.T) {
	g := newGate(&fakeSource{model: nodeModel(t, allEvidence())}, radiogate.ModeAdvisory, zerolog.Nop())

	req := halowJoin(t)
	require.NoError(t, g.Evaluate(context.Background(), req))

	recs := g.Recent()
	require.Len(t, recs, 1)
	require.NotNil(t, recs[0].Decision)
	assert.Equal(t, req.Current, recs[0].CurrentDemands)
	assert.Equal(t, req.Requested, recs[0].RequestedDemands)

	req.Requested[0].ID = "mutated"
	assert.Equal(t, "default_radio3", g.Recent()[0].RequestedDemands[0].ID, "the record owns a copy")
}

func TestGate_Evaluate_policyAllowUnqualified(t *testing.T) {
	g := newGate(&fakeSource{model: nodeModel(t, radiogate.Settings{SharingQualifiedPHYs: []string{"phy0", "phy2"}})},
		radiogate.ModeEnforce, zerolog.Nop())
	g.Policy = func() radiocap.Policy { return radiocap.Policy{AllowUnqualified: true} }

	require.NoError(t, g.Evaluate(context.Background(), halowJoin(t)))
	assert.True(t, g.Recent()[0].Decision.UsesUnqualified)
}

func TestGate_Recent_boundedNewestFirst(t *testing.T) {
	g := newGate(&fakeSource{model: nodeModel(t, allEvidence())}, radiogate.ModeAdvisory, zerolog.Nop())

	total := radiogate.RingSize + 8
	for i := range total {
		req := halowJoin(t)
		req.Origin = strconv.Itoa(i)
		require.NoError(t, g.Evaluate(context.Background(), req))
	}

	recs := g.Recent()
	require.Len(t, recs, radiogate.RingSize)
	assert.Equal(t, strconv.Itoa(total-1), recs[0].Origin, "newest first")
	assert.Equal(t, strconv.Itoa(total-radiogate.RingSize), recs[len(recs)-1].Origin, "oldest kept")
}

func TestGate_Evaluate_concurrent(t *testing.T) {
	g := newGate(&fakeSource{model: nodeModel(t, allEvidence())}, radiogate.ModeEnforce, zerolog.Nop())

	var wg sync.WaitGroup

	for range 16 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			assert.NoError(t, g.Evaluate(context.Background(), halowJoin(t)))
			_ = g.Recent()
		}()
	}

	wg.Wait()
	assert.Len(t, g.Recent(), 16)
}

func TestRing_zeroValue(t *testing.T) {
	var r radiogate.Ring

	assert.Empty(t, r.Recent())
	assert.Equal(t, 0, r.Len())

	r.Add(radiogate.Record{Origin: "a"})
	r.Add(radiogate.Record{Origin: "b"})

	assert.Equal(t, 2, r.Len())
	assert.Equal(t, []string{"b", "a"}, []string{r.Recent()[0].Origin, r.Recent()[1].Origin})
}

func TestDeniedError_message(t *testing.T) {
	err := &radiogate.DeniedError{Decision: radiocap.Decision{
		Outcome: radiocap.OutcomeReject, Reason: radiocap.ReasonCapacityShort, Explanation: "Rejected: short",
	}}

	assert.Equal(t, "radio capacity reject (capacity_short): Rejected: short", err.Error())
}

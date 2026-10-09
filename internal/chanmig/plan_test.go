package chanmig_test

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/chanmig"
)

func planEpoch() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) }

func makePlan(version uint64, as ...chanmig.Assignment) chanmig.Plan {
	return chanmig.Plan{
		MeshID:      testMeshID,
		Version:     version,
		IssuedAt:    planEpoch(),
		ActivateAt:  planEpoch().Add(30 * time.Second),
		Stagger:     5 * time.Second,
		Assignments: as,
	}
}

func TestAssignmentValidate_widthsAndChannels(t *testing.T) {
	tests := []struct {
		name string
		a    chanmig.Assignment
		ok   bool
	}{
		{name: "halow 1 MHz", a: halow(1), ok: true},
		{name: "halow 8 MHz", a: assign(chanmig.BandHaLow, 3, 8), ok: true},
		{name: "halow 3 MHz", a: assign(chanmig.BandHaLow, 3, 3)},
		{name: "halow 16 MHz", a: assign(chanmig.BandHaLow, 3, 16)},
		{name: "2.4 GHz 20 MHz", a: wifi24(11), ok: true},
		{name: "2.4 GHz 40 MHz", a: assign(chanmig.Band2G4, 6, 40), ok: true},
		{name: "2.4 GHz 80 MHz", a: assign(chanmig.Band2G4, 6, 80)},
		{name: "5 GHz 160 MHz", a: assign(chanmig.Band5G, 36, 160), ok: true},
		{name: "5 GHz 320 MHz", a: assign(chanmig.Band5G, 36, 320)},
		{name: "5 GHz zero width", a: assign(chanmig.Band5G, 36, 0)},
		{name: "channel zero", a: assign(chanmig.Band5G, 0, 20)},
		{name: "unknown band zero", a: assign(0, 1, 20)},
		{name: "unknown band four", a: assign(4, 1, 20)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.a.Validate()
			if tc.ok {
				assert.NoError(t, err)

				return
			}

			assert.ErrorIs(t, err, chanmig.ErrInvalidPlan)
		})
	}
}

func TestPlanValidate_structure(t *testing.T) {
	tests := []struct {
		mutate func(p *chanmig.Plan)
		name   string
		ok     bool
	}{
		{name: "valid", mutate: func(*chanmig.Plan) {}, ok: true},
		{name: "no assignments", mutate: func(p *chanmig.Plan) { p.Assignments = nil }},
		{name: "four assignments", mutate: func(p *chanmig.Plan) { p.Assignments = append(p.Assignments, wifi5(44)) }},
		{name: "unsorted", mutate: func(p *chanmig.Plan) { p.Assignments = []chanmig.Assignment{wifi5(36), halow(1)} }},
		{name: "duplicate band", mutate: func(p *chanmig.Plan) { p.Assignments = []chanmig.Assignment{halow(1), halow(2)} }},
		{name: "version zero", mutate: func(p *chanmig.Plan) { p.Version = 0 }},
		{name: "activation before issue", mutate: func(p *chanmig.Plan) { p.ActivateAt = p.IssuedAt.Add(-time.Millisecond) }},
		{name: "activation equals issue", mutate: func(p *chanmig.Plan) { p.ActivateAt = p.IssuedAt }, ok: true},
		{name: "negative stagger", mutate: func(p *chanmig.Plan) { p.Stagger = -time.Second }},
		{name: "maximum stagger", mutate: func(p *chanmig.Plan) { p.Stagger = time.Minute }, ok: true},
		{name: "stagger above maximum", mutate: func(p *chanmig.Plan) { p.Stagger = time.Minute + time.Millisecond }},
		{name: "invalid assignment", mutate: func(p *chanmig.Plan) { p.Assignments[0].WidthMHz = 3 }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := makePlan(1, halow(1), wifi24(1), wifi5(36))
			tc.mutate(&p)

			err := p.Validate()
			if tc.ok {
				assert.NoError(t, err)

				return
			}

			assert.ErrorIs(t, err, chanmig.ErrInvalidPlan)
		})
	}
}

func TestMarshalCanonical_roundTripAndLayout(t *testing.T) {
	p := makePlan(7, halow(3), wifi5(149))
	p.SignerID = 0x0102030405060708

	raw, err := p.MarshalCanonical()
	require.NoError(t, err)
	require.Len(t, raw, 50+2*5)

	// Pin the documented layout so encoder changes cannot slip by.
	assert.Equal(t, "OMCP", string(raw[:4]))
	assert.Equal(t, byte(1), raw[4], "format")
	assert.Equal(t, []byte{0, 0, 0, 0, 0, 0, 0x0E, 0x1A}, raw[5:13], "mesh id")
	assert.Equal(t, []byte{0, 0, 0, 0, 0, 0, 0, 7}, raw[13:21], "version")
	assert.Equal(t, []byte{1, 2, 3, 4, 5, 6, 7, 8}, raw[21:29], "signer")
	assert.Equal(t, []byte{0, 0, 0x13, 0x88}, raw[45:49], "stagger ms")
	assert.Equal(t, byte(2), raw[49], "count")
	assert.Equal(t, []byte{1, 0, 3, 0, 1}, raw[50:55], "halow assignment")
	assert.Equal(t, []byte{3, 0, 149, 0, 80}, raw[55:60], "5 GHz assignment")

	got, err := chanmig.UnmarshalCanonical(raw)
	require.NoError(t, err)
	assert.Equal(t, p.Version, got.Version)
	assert.Equal(t, p.SignerID, got.SignerID)
	assert.True(t, p.IssuedAt.Equal(got.IssuedAt))
	assert.True(t, p.ActivateAt.Equal(got.ActivateAt))
	assert.Equal(t, p.Stagger, got.Stagger)
	assert.Equal(t, p.Assignments, got.Assignments)

	a, ok := got.Assignment(chanmig.Band5G)
	assert.True(t, ok)
	assert.Equal(t, wifi5(149), a)

	_, ok = got.Assignment(chanmig.Band2G4)
	assert.False(t, ok)
}

func TestUnmarshalCanonical_rejectsNonCanonical(t *testing.T) {
	p := makePlan(1, halow(1))
	raw, err := p.MarshalCanonical()
	require.NoError(t, err)

	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "empty", raw: nil},
		{name: "truncated header", raw: raw[:20]},
		{name: "trailing byte", raw: append(append([]byte(nil), raw...), 0)},
		{name: "bad magic", raw: withByte(raw, 0, 'X')},
		{name: "unknown format", raw: withByte(raw, 4, 2)},
		{name: "count mismatch", raw: withByte(raw, 49, 2)},
		{name: "invalid width", raw: withByte(raw, 54, 3)},
		{name: "oversized", raw: make([]byte, 100)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := chanmig.UnmarshalCanonical(tc.raw)
			assert.Error(t, err)
		})
	}
}

func withByte(b []byte, i int, v byte) []byte {
	out := append([]byte(nil), b...)
	out[i] = v

	return out
}

func TestBandAndAssignmentString(t *testing.T) {
	assert.Equal(t, "halow", chanmig.BandHaLow.String())
	assert.Equal(t, "2.4ghz", chanmig.Band2G4.String())
	assert.Equal(t, "5ghz", chanmig.Band5G.String())
	assert.Equal(t, "band(7)", chanmig.Band(7).String())
	assert.Equal(t, "5ghz/ch149/80MHz", wifi5(149).String())
}

func TestSignVerify(t *testing.T) {
	signer, err := chanmig.NewSigner(42, testKey(1))
	require.NoError(t, err)
	assert.Equal(t, chanmig.NodeID(42), signer.ID())

	v := testVerifier(t)
	assert.Equal(t, uint64(testMeshID), v.MeshID())

	sp, err := signer.Sign(makePlan(3, halow(2)))
	require.NoError(t, err)
	assert.Equal(t, chanmig.NodeID(42), sp.Plan.SignerID, "signer stamps its own ID")

	got, err := v.Verify(sp.Raw, sp.Sig)
	require.NoError(t, err)
	assert.Equal(t, sp.Plan, got.Plan)

	t.Run("tampered bytes", func(t *testing.T) {
		_, err := v.Verify(withByte(sp.Raw, 20, 4), sp.Sig)
		assert.ErrorIs(t, err, chanmig.ErrBadSignature)
	})

	t.Run("tampered signature", func(t *testing.T) {
		_, err := v.Verify(sp.Raw, withByte(sp.Sig, 0, sp.Sig[0]^1))
		assert.ErrorIs(t, err, chanmig.ErrBadSignature)
	})

	t.Run("short signature", func(t *testing.T) {
		_, err := v.Verify(sp.Raw, sp.Sig[:10])
		assert.ErrorIs(t, err, chanmig.ErrBadSignature)
	})

	t.Run("untrusted key", func(t *testing.T) {
		forger, err := chanmig.NewSigner(42, testKey(2))
		require.NoError(t, err)

		forged, err := forger.Sign(makePlan(99, halow(2)))
		require.NoError(t, err)

		_, err = v.Verify(forged.Raw, forged.Sig)
		assert.ErrorIs(t, err, chanmig.ErrBadSignature)
	})

	t.Run("other mesh", func(t *testing.T) {
		p := makePlan(3, halow(2))
		p.MeshID = 0xBEEF

		other, err := signer.Sign(p)
		require.NoError(t, err)

		_, err = v.Verify(other.Raw, other.Sig)
		assert.ErrorIs(t, err, chanmig.ErrWrongMesh)
	})

	t.Run("invalid plan is not signed", func(t *testing.T) {
		_, err := signer.Sign(makePlan(0, halow(2)))
		assert.ErrorIs(t, err, chanmig.ErrInvalidPlan)
	})
}

func TestVerifier_keyRotation(t *testing.T) {
	oldKey, newKey := testKey(1), testKey(3)

	v, err := chanmig.NewVerifier(testMeshID, oldKey.Public().(ed25519.PublicKey), newKey.Public().(ed25519.PublicKey))
	require.NoError(t, err)

	for _, k := range []ed25519.PrivateKey{oldKey, newKey} {
		s, err := chanmig.NewSigner(1, k)
		require.NoError(t, err)

		sp, err := s.Sign(makePlan(1, halow(1)))
		require.NoError(t, err)

		_, err = v.Verify(sp.Raw, sp.Sig)
		assert.NoError(t, err)
	}
}

func TestNewSignerAndVerifier_rejectBadKeys(t *testing.T) {
	_, err := chanmig.NewSigner(1, ed25519.PrivateKey(make([]byte, 10)))
	assert.Error(t, err)

	_, err = chanmig.NewVerifier(testMeshID)
	assert.Error(t, err)

	_, err = chanmig.NewVerifier(testMeshID, ed25519.PublicKey(make([]byte, 31)))
	assert.Error(t, err)
}

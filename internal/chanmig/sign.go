package chanmig

import (
	"crypto/ed25519"
	"errors"
	"fmt"
)

// NodeID identifies a mesh node (for example derived from its primary MAC).
type NodeID uint64

// SignedPlan is a verified or freshly signed plan together with the exact
// canonical bytes and the Ed25519 signature over them.
type SignedPlan struct {
	Raw  []byte
	Sig  []byte
	Plan Plan
}

// Errors returned by signing and verification.
var (
	// ErrBadSignature reports a signature that no trusted key verifies.
	ErrBadSignature = errors.New("chanmig: plan signature invalid")
	// ErrWrongMesh reports a plan bound to a different mesh.
	ErrWrongMesh = errors.New("chanmig: plan is for a different mesh")
	// ErrNoSigner reports a Propose call on a node without a signing key.
	ErrNoSigner = errors.New("chanmig: node holds no plan-authority key")
)

// Signer signs plans with the mesh plan-authority private key. Only nodes
// provisioned as coordinators hold one.
type Signer struct {
	key ed25519.PrivateKey
	id  NodeID
}

// NewSigner returns a Signer that signs as node id.
func NewSigner(id NodeID, key ed25519.PrivateKey) (*Signer, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("chanmig: signing key has %d bytes, want %d", len(key), ed25519.PrivateKeySize)
	}

	return &Signer{id: id, key: key}, nil
}

// ID returns the signer's node ID.
func (s *Signer) ID() NodeID { return s.id }

// Sign stamps p with the signer's ID, encodes it canonically and signs the
// bytes.
func (s *Signer) Sign(p Plan) (SignedPlan, error) {
	p.SignerID = s.id
	p.IssuedAt = p.IssuedAt.Truncate(0)
	p.ActivateAt = p.ActivateAt.Truncate(0)

	raw, err := p.MarshalCanonical()
	if err != nil {
		return SignedPlan{}, err
	}

	// Re-parse so the returned Plan has exactly the precision of the wire
	// form (milliseconds), keeping signer and verifier views identical.
	parsed, err := UnmarshalCanonical(raw)
	if err != nil {
		return SignedPlan{}, err
	}

	return SignedPlan{Plan: parsed, Raw: raw, Sig: ed25519.Sign(s.key, raw)}, nil
}

// Verifier checks plan signatures against the trusted plan-authority public
// keys provisioned at setup. The default deployment provisions exactly one
// mesh-wide key; several keys allow rotation without a flag day.
type Verifier struct {
	keys   []ed25519.PublicKey
	meshID uint64
}

// NewVerifier returns a Verifier for meshID trusting keys.
func NewVerifier(meshID uint64, keys ...ed25519.PublicKey) (*Verifier, error) {
	if len(keys) == 0 {
		return nil, errors.New("chanmig: at least one plan-authority key is required")
	}

	trusted := make([]ed25519.PublicKey, 0, len(keys))

	for i, k := range keys {
		if len(k) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("chanmig: key %d has %d bytes, want %d", i, len(k), ed25519.PublicKeySize)
		}

		trusted = append(trusted, k)
	}

	return &Verifier{meshID: meshID, keys: trusted}, nil
}

// MeshID returns the mesh the verifier accepts plans for.
func (v *Verifier) MeshID() uint64 { return v.meshID }

// Verify authenticates raw against sig, then parses and validates it. The
// signature is checked before parsing so unauthenticated bytes never reach
// the decoder's validation logic.
func (v *Verifier) Verify(raw, sig []byte) (SignedPlan, error) {
	if len(sig) != ed25519.SignatureSize || len(raw) > maxPlanSize {
		return SignedPlan{}, ErrBadSignature
	}

	if !v.signedByTrustedKey(raw, sig) {
		return SignedPlan{}, ErrBadSignature
	}

	p, err := UnmarshalCanonical(raw)
	if err != nil {
		return SignedPlan{}, err
	}

	if p.MeshID != v.meshID {
		return SignedPlan{}, ErrWrongMesh
	}

	return SignedPlan{Plan: p, Raw: raw, Sig: sig}, nil
}

func (v *Verifier) signedByTrustedKey(raw, sig []byte) bool {
	for _, k := range v.keys {
		if ed25519.Verify(k, raw, sig) {
			return true
		}
	}

	return false
}

package chanmig

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Band identifies a radio band. The numeric values are part of the
// canonical plan encoding and must never be renumbered.
type Band uint8

// Supported bands.
const (
	BandHaLow Band = 1
	Band2G4   Band = 2
	Band5G    Band = 3
)

// maxBands is the number of defined bands; plans hold at most one
// assignment per band.
const maxBands = 3

// String returns a short human-readable band name.
func (b Band) String() string {
	switch b {
	case BandHaLow:
		return "halow"
	case Band2G4:
		return "2.4ghz"
	case Band5G:
		return "5ghz"
	default:
		return "band(" + strconv.Itoa(int(b)) + ")"
	}
}

func (b Band) valid() bool { return b >= BandHaLow && b <= Band5G }

// validWidth reports whether w MHz is a legal channel width on band b.
func (b Band) validWidth(w uint16) bool {
	switch b {
	case BandHaLow:
		return w == 1 || w == 2 || w == 4 || w == 8
	case Band2G4:
		return w == 20 || w == 40
	case Band5G:
		return w == 20 || w == 40 || w == 80 || w == 160
	default:
		return false
	}
}

// Assignment is one band's channel number and width.
type Assignment struct {
	Channel  uint16
	WidthMHz uint16
	Band     Band
}

// Validate checks the assignment's band, channel and width.
func (a Assignment) Validate() error {
	if !a.Band.valid() {
		return fmt.Errorf("%w: unknown band %d", ErrInvalidPlan, a.Band)
	}

	if a.Channel == 0 {
		return fmt.Errorf("%w: %s channel is zero", ErrInvalidPlan, a.Band)
	}

	if !a.Band.validWidth(a.WidthMHz) {
		return fmt.Errorf("%w: %s width %d MHz not allowed", ErrInvalidPlan, a.Band, a.WidthMHz)
	}

	return nil
}

// String renders the assignment as band/channel/width.
func (a Assignment) String() string {
	return a.Band.String() + "/ch" + strconv.Itoa(int(a.Channel)) + "/" + strconv.Itoa(int(a.WidthMHz)) + "MHz"
}

// Plan is a versioned band/channel/width plan. A Plan is only trusted once
// it has been signed (see Signer) or verified (see Verifier).
type Plan struct {
	// IssuedAt is when the signer created the plan (millisecond precision).
	IssuedAt time.Time
	// ActivateAt is the not-before switch time (millisecond precision).
	ActivateAt time.Time
	// Assignments holds at most one entry per band, sorted by band.
	Assignments []Assignment
	// MeshID binds the plan to one mesh so it cannot be replayed into
	// another mesh that happens to share a key.
	MeshID uint64
	// Version increases monotonically across the mesh.
	Version uint64
	// SignerID is the node ID of the issuing coordinator.
	SignerID NodeID
	// Stagger separates the switch times of successive changed bands so
	// that one band is verified before the next one moves.
	Stagger time.Duration
}

// Canonical encoding constants. The layout is fixed:
//
//	magic(4) "OMCP" | format(1) | meshID(8) | version(8) | signerID(8) |
//	issuedAt ms(8) | activateAt ms(8) | stagger ms(4) | count(1) |
//	count x { band(1) | channel(2) | width(2) }
//
// All integers are big-endian. The signature covers these exact bytes.
const (
	planMagic          = "OMCP"
	planFormat         = 1
	planHeaderSize     = 4 + 1 + 8 + 8 + 8 + 8 + 8 + 4 + 1
	planAssignmentSize = 5
	maxPlanSize        = planHeaderSize + maxBands*planAssignmentSize
	maxStagger         = time.Minute
)

// Errors returned by plan encoding and validation.
var (
	// ErrInvalidPlan reports a structurally invalid plan.
	ErrInvalidPlan = errors.New("chanmig: invalid plan")
	// ErrNotCanonical reports bytes that are not the canonical encoding.
	ErrNotCanonical = errors.New("chanmig: non-canonical plan encoding")
)

// Validate checks structural rules: at least one assignment, one per band,
// sorted by band, each assignment valid, ActivateAt not before IssuedAt and
// a bounded stagger. Policy rules that depend on node configuration (allowed
// channels, maximum lead) are checked by the Node.
func (p *Plan) Validate() error {
	if len(p.Assignments) == 0 || len(p.Assignments) > maxBands {
		return fmt.Errorf("%w: %d assignments", ErrInvalidPlan, len(p.Assignments))
	}

	var prev Band

	for _, a := range p.Assignments {
		if err := a.Validate(); err != nil {
			return err
		}

		if a.Band <= prev {
			return fmt.Errorf("%w: assignments not sorted by band or duplicated", ErrInvalidPlan)
		}

		prev = a.Band
	}

	if p.Version == 0 {
		return fmt.Errorf("%w: version 0 is reserved for the provisioned initial plan", ErrInvalidPlan)
	}

	if p.ActivateAt.Before(p.IssuedAt) {
		return fmt.Errorf("%w: activation precedes issue time", ErrInvalidPlan)
	}

	if p.Stagger < 0 || p.Stagger > maxStagger {
		return fmt.Errorf("%w: stagger %s out of range", ErrInvalidPlan, p.Stagger)
	}

	return nil
}

// Assignment returns the plan's assignment for band b.
func (p *Plan) Assignment(b Band) (Assignment, bool) {
	for _, a := range p.Assignments {
		if a.Band == b {
			return a, true
		}
	}

	return Assignment{}, false
}

// MarshalCanonical validates p and returns its canonical encoding.
func (p *Plan) MarshalCanonical() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}

	buf := make([]byte, 0, planHeaderSize+len(p.Assignments)*planAssignmentSize)
	buf = append(buf, planMagic...)
	buf = append(buf, planFormat)
	buf = binary.BigEndian.AppendUint64(buf, p.MeshID)
	buf = binary.BigEndian.AppendUint64(buf, p.Version)
	buf = binary.BigEndian.AppendUint64(buf, uint64(p.SignerID))
	buf = binary.BigEndian.AppendUint64(buf, uint64(p.IssuedAt.UnixMilli()))
	buf = binary.BigEndian.AppendUint64(buf, uint64(p.ActivateAt.UnixMilli()))
	buf = binary.BigEndian.AppendUint32(buf, uint32(p.Stagger.Milliseconds()))
	buf = append(buf, byte(len(p.Assignments)))

	for _, a := range p.Assignments {
		buf = append(buf, byte(a.Band))
		buf = binary.BigEndian.AppendUint16(buf, a.Channel)
		buf = binary.BigEndian.AppendUint16(buf, a.WidthMHz)
	}

	return buf, nil
}

// UnmarshalCanonical parses canonical plan bytes. It rejects trailing
// bytes, unknown formats and any encoding that would not re-encode to the
// same bytes, so a signature over the bytes is a signature over the plan.
func UnmarshalCanonical(raw []byte) (Plan, error) {
	if len(raw) < planHeaderSize || len(raw) > maxPlanSize {
		return Plan{}, fmt.Errorf("%w: length %d", ErrNotCanonical, len(raw))
	}

	if string(raw[:4]) != planMagic || raw[4] != planFormat {
		return Plan{}, fmt.Errorf("%w: bad magic or format", ErrNotCanonical)
	}

	be := binary.BigEndian
	p := Plan{
		MeshID:     be.Uint64(raw[5:]),
		Version:    be.Uint64(raw[13:]),
		SignerID:   NodeID(be.Uint64(raw[21:])),
		IssuedAt:   time.UnixMilli(int64(be.Uint64(raw[29:]))),
		ActivateAt: time.UnixMilli(int64(be.Uint64(raw[37:]))),
		Stagger:    time.Duration(be.Uint32(raw[45:])) * time.Millisecond,
	}

	n := int(raw[49])
	if len(raw) != planHeaderSize+n*planAssignmentSize {
		return Plan{}, fmt.Errorf("%w: length does not match %d assignments", ErrNotCanonical, n)
	}

	p.Assignments = make([]Assignment, 0, n)

	for i := range n {
		off := planHeaderSize + i*planAssignmentSize
		p.Assignments = append(p.Assignments, Assignment{
			Band:     Band(raw[off]),
			Channel:  be.Uint16(raw[off+1:]),
			WidthMHz: be.Uint16(raw[off+3:]),
		})
	}

	if err := p.Validate(); err != nil {
		return Plan{}, err
	}

	return p, nil
}

// compareSigned orders two verified plans for partition/merge resolution:
// higher version wins, then higher signer ID, then the lexicographically
// larger canonical encoding. It returns >0 when a wins, <0 when b wins and
// 0 only for identical bytes.
func compareSigned(a, b *SignedPlan) int {
	switch {
	case a.Plan.Version != b.Plan.Version:
		return cmpUint(a.Plan.Version, b.Plan.Version)
	case a.Plan.SignerID != b.Plan.SignerID:
		return cmpUint(uint64(a.Plan.SignerID), uint64(b.Plan.SignerID))
	default:
		return bytes.Compare(a.Raw, b.Raw)
	}
}

func cmpUint(a, b uint64) int {
	if a > b {
		return 1
	}

	return -1
}

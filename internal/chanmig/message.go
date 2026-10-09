package chanmig

import (
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
)

// Message types on the wire.
const (
	msgAdvert byte = 1
	msgAck    byte = 2
)

// Wire sizes.
const (
	advertHeaderSize = 1 + 8 + 1     // type, from, hasPlan
	advertPlanPrefix = 2             // raw length
	ackSize          = 1 + 8 + 8 + 8 // type, from, version, signer
	maxMessageSize   = advertHeaderSize + advertPlanPrefix + maxPlanSize + ed25519.SignatureSize
)

// ErrMalformed reports a message that cannot be decoded.
var ErrMalformed = errors.New("chanmig: malformed message")

// ack acknowledges receipt of a pending plan.
type ack struct {
	from    NodeID
	version uint64
	signer  NodeID
}

func appendAdvert(buf []byte, from NodeID, sp *SignedPlan) []byte {
	buf = append(buf, msgAdvert)
	buf = binary.BigEndian.AppendUint64(buf, uint64(from))

	if sp == nil {
		return append(buf, 0)
	}

	buf = append(buf, 1)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(sp.Raw)))
	buf = append(buf, sp.Raw...)

	return append(buf, sp.Sig...)
}

func appendAck(buf []byte, a ack) []byte {
	buf = append(buf, msgAck)
	buf = binary.BigEndian.AppendUint64(buf, uint64(a.from))
	buf = binary.BigEndian.AppendUint64(buf, a.version)

	return binary.BigEndian.AppendUint64(buf, uint64(a.signer))
}

// decodedMessage is the parsed form of one wire message. For adverts the
// raw/sig slices alias the input and are only valid until the caller copies
// them.
type decodedMessage struct {
	raw     []byte
	sig     []byte
	ack     ack
	from    NodeID
	kind    byte
	hasPlan bool
}

func decodeMessage(b []byte) (decodedMessage, error) {
	if len(b) == 0 || len(b) > maxMessageSize {
		return decodedMessage{}, fmt.Errorf("%w: length %d", ErrMalformed, len(b))
	}

	switch b[0] {
	case msgAdvert:
		return decodeAdvert(b)
	case msgAck:
		if len(b) != ackSize {
			return decodedMessage{}, fmt.Errorf("%w: ack length %d", ErrMalformed, len(b))
		}

		a := ack{
			from:    NodeID(binary.BigEndian.Uint64(b[1:])),
			version: binary.BigEndian.Uint64(b[9:]),
			signer:  NodeID(binary.BigEndian.Uint64(b[17:])),
		}

		return decodedMessage{kind: msgAck, from: a.from, ack: a}, nil
	default:
		return decodedMessage{}, fmt.Errorf("%w: type %d", ErrMalformed, b[0])
	}
}

func decodeAdvert(b []byte) (decodedMessage, error) {
	if len(b) < advertHeaderSize {
		return decodedMessage{}, fmt.Errorf("%w: short advert", ErrMalformed)
	}

	m := decodedMessage{kind: msgAdvert, from: NodeID(binary.BigEndian.Uint64(b[1:]))}

	switch b[9] {
	case 0:
		if len(b) != advertHeaderSize {
			return decodedMessage{}, fmt.Errorf("%w: trailing bytes", ErrMalformed)
		}

		return m, nil
	case 1:
	default:
		return decodedMessage{}, fmt.Errorf("%w: plan flag %d", ErrMalformed, b[9])
	}

	rest := b[advertHeaderSize:]
	if len(rest) < advertPlanPrefix {
		return decodedMessage{}, fmt.Errorf("%w: missing plan length", ErrMalformed)
	}

	n := int(binary.BigEndian.Uint16(rest))
	rest = rest[advertPlanPrefix:]

	if len(rest) != n+ed25519.SignatureSize {
		return decodedMessage{}, fmt.Errorf("%w: plan length %d", ErrMalformed, n)
	}

	m.hasPlan = true
	m.raw = rest[:n]
	m.sig = rest[n:]

	return m, nil
}

package p25

import "math/bits"

// DUID is the P25 Phase 1 data unit identifier carried in the NID.
type DUID uint8

// Data unit identifiers (TIA-102.BAAA table 7-1).
const (
	DUIDHeader      DUID = 0x0 // HDU
	DUIDTerminator  DUID = 0x3 // TDU (simple terminator)
	DUIDLDU1        DUID = 0x5
	DUIDTSDU        DUID = 0x7 // trunking signaling data unit
	DUIDLDU2        DUID = 0xA
	DUIDPDU         DUID = 0xC // packet data unit
	DUIDTerminatorL DUID = 0xF // TDU with link control
)

// Valid reports whether d is one of the defined data unit identifiers.
func (d DUID) Valid() bool {
	switch d {
	case DUIDHeader, DUIDTerminator, DUIDLDU1, DUIDTSDU, DUIDLDU2, DUIDPDU, DUIDTerminatorL:
		return true
	default:
		return false
	}
}

// BCH(63,16,23) over GF(2^6) protecting the 16-bit NAC||DUID field. The
// generator is the least common multiple of the minimal polynomials of
// alpha^1..alpha^22 with GF(64) built on the primitive polynomial
// x^6 + x + 1. Its octal value 6331 1413 6723 5453 matches the published
// BCH code tables (Lin & Costello, Appendix C, n=63 k=16 t=11); the test
// suite re-derives it from the field to pin the constant.
const (
	bchGenerator = 0o6331141367235453
	bchParity    = 47 // n - k
	bchN         = 63
	bchT         = 11
	gfPrimitive  = 0b1000011 // x^6 + x + 1
)

// EncodeNID returns the 64-bit network identifier for nac and duid, most
// significant bit first on air: 16 information bits (12-bit NAC, 4-bit
// DUID), 47 systematic BCH parity bits and one trailing even-parity bit.
func EncodeNID(nac uint16, duid DUID) uint64 {
	info := uint64(nac&0xFFF)<<4 | uint64(duid&0xF)
	cw := info<<bchParity | bchRemainder(info<<bchParity)

	return cw<<1 | uint64(bits.OnesCount64(cw)&1)
}

// bchRemainder returns v mod g(x) for a 63-bit polynomial v.
func bchRemainder(v uint64) uint64 {
	for i := bchN - 1; i >= bchParity; i-- {
		if v&(1<<i) != 0 {
			v ^= bchGenerator << (i - bchParity)
		}
	}

	return v
}

// nidCodec decodes BCH(63,16) NID words using Berlekamp-Massey and a Chien
// search over precomputed GF(64) tables. It holds no per-call state and
// performs no allocation.
type nidCodec struct {
	exp [2 * 63]uint8
	log [64]uint8
}

func newNIDCodec() *nidCodec {
	c := &nidCodec{}

	x := uint8(1)
	for i := range 63 {
		c.exp[i] = x
		c.exp[i+63] = x
		c.log[x] = uint8(i)

		x <<= 1
		if x&0x40 != 0 {
			x ^= gfPrimitive
		}
	}

	return c
}

func (c *nidCodec) mul(a, b uint8) uint8 {
	if a == 0 || b == 0 {
		return 0
	}

	return c.exp[int(c.log[a])+int(c.log[b])]
}

func (c *nidCodec) div(a, b uint8) uint8 {
	if a == 0 {
		return 0
	}

	return c.exp[int(c.log[a])+63-int(c.log[b])]
}

// decode corrects up to 11 bit errors in the 64-bit NID word. It returns
// the NAC, DUID, number of corrected bits and whether decoding succeeded.
// The trailing parity bit is not used for correction.
func (c *nidCodec) decode(word uint64) (nac uint16, duid DUID, corrected int, ok bool) {
	cw := word >> 1

	var syn [2*bchT + 1]uint8

	nonzero := false

	for j := 1; j <= 2*bchT; j++ {
		var s uint8

		for v := cw; v != 0; v &= v - 1 {
			i := bits.TrailingZeros64(v)
			s ^= c.exp[(i*j)%63]
		}

		syn[j] = s
		nonzero = nonzero || s != 0
	}

	if nonzero {
		var n int

		cw, n, ok = c.correct(cw, &syn)
		if !ok {
			return 0, 0, 0, false
		}

		corrected = n
	}

	info := cw >> bchParity

	return uint16(info >> 4), DUID(info & 0xF), corrected, true
}

// correct runs Berlekamp-Massey on the syndromes and flips the located
// error bits. It fails when the locator degree exceeds t or the Chien search
// does not find exactly that many roots.
func (c *nidCodec) correct(cw uint64, syn *[2*bchT + 1]uint8) (fixed uint64, nerr int, ok bool) {
	var locator, prev, tmp [2*bchT + 2]uint8

	locator[0], prev[0] = 1, 1
	degree, shift := 0, 1
	lastDisc := uint8(1)

	for n := range 2 * bchT {
		d := syn[n+1]
		for i := 1; i <= degree; i++ {
			d ^= c.mul(locator[i], syn[n+1-i])
		}

		if d == 0 {
			shift++

			continue
		}

		coef := c.div(d, lastDisc)
		tmp = locator

		for i := 0; i+shift < len(locator); i++ {
			locator[i+shift] ^= c.mul(coef, prev[i])
		}

		if 2*degree <= n {
			degree = n + 1 - degree
			prev = tmp
			lastDisc = d
			shift = 1
		} else {
			shift++
		}
	}

	if degree > bchT {
		return 0, 0, false
	}

	// Chien search: position i is in error when locator(alpha^-i) == 0.
	roots := 0

	for i := range bchN {
		var sum uint8

		for k := 0; k <= degree; k++ {
			if locator[k] == 0 {
				continue
			}

			sum ^= c.exp[(int(c.log[locator[k]])+(63-i)*k%63)%63]
		}

		if sum == 0 {
			cw ^= 1 << i
			roots++
		}
	}

	if roots != degree {
		return 0, 0, false
	}

	return cw, roots, true
}

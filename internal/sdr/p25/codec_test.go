package p25_test

import (
	"math/bits"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/sdr/p25"
	"github.com/openmanet/openmanetd/internal/sdr/p25/p25test"
)

// TestCRCCCITT_knownAnswer pins the CRC engine to the published check value
// of CRC-16/XMODEM (poly 0x1021, init 0, no reflection, no xorout) for the
// ASCII string "123456789", independent of any P25 code.
func TestCRCCCITT_knownAnswer(t *testing.T) {
	assert.Equal(t, uint16(0x31C3), p25.CRCCCITT([]byte("123456789")))
	assert.Equal(t, uint16(0), p25.CRCCCITT(nil))
}

// TestTSBKCRC_isInvertedCCITT checks the TIA-102.BAAA convention: the TSBK
// CRC field is the inverted CCITT remainder of the first 80 bits. Running
// the register over all 96 bits therefore leaves the CRC of 0xFFFF, the
// well-known residue 0x1D0F (OP25 checks the equivalent crc^0xFFFF form).
func TestTSBKCRC_isInvertedCCITT(t *testing.T) {
	b := p25test.Grant(0, p25test.Channel(1, 0x2AB), 8001, 1234567)

	assert.Equal(t, p25.CRCCCITT(b[:10])^0xFFFF, uint16(b[10])<<8|uint16(b[11]))
	assert.Equal(t, uint16(0x1D0F), p25.CRCCCITT(b[:]))
}

// gfTables builds GF(64) with x^6+x+1 independently of the package.
func gfTables() (exp [63]uint8, log [64]int) {
	x := uint8(1)
	for i := range 63 {
		exp[i] = x
		log[x] = i

		x <<= 1
		if x&0x40 != 0 {
			x ^= 0x43
		}
	}

	return exp, log
}

// TestBCHGenerator_derivedFromField re-derives the BCH(63,16,23) generator
// as the product of (x - alpha^i) over the union of the cyclotomic cosets
// of 1..22 and compares it with the package constant (octal
// 6331141367235453 from the standard BCH tables).
func TestBCHGenerator_derivedFromField(t *testing.T) {
	exp, log := gfTables()
	mul := func(a, b uint8) uint8 {
		if a == 0 || b == 0 {
			return 0
		}

		return exp[(log[a]+log[b])%63]
	}

	roots := map[int]bool{}

	for i := 1; i <= 22; i++ {
		for j := i; !roots[j]; j = j * 2 % 63 {
			roots[j] = true
		}
	}

	require.Len(t, roots, 47)

	poly := []uint8{1} // coefficients, poly[k] multiplies x^k
	for r := range roots {
		next := make([]uint8, len(poly)+1)
		for k, c := range poly {
			next[k+1] ^= c
			next[k] ^= mul(c, exp[r])
		}

		poly = next
	}

	var g uint64

	for k, c := range poly {
		require.LessOrEqual(t, c, uint8(1), "binary coefficient")

		g |= uint64(c) << k
	}

	assert.Equal(t, uint64(0o6331141367235453), g)
	assert.Equal(t, g, uint64(p25.BCHGenerator))
	assert.Equal(t, 47, 63-bits.LeadingZeros64(g))
}

// TestEncodeNID_codewordProperties checks systematic layout, divisibility
// by g(x) and the trailing even parity bit.
func TestEncodeNID_codewordProperties(t *testing.T) {
	for _, tc := range []struct {
		nac  uint16
		duid p25.DUID
	}{{0x293, p25.DUIDTSDU}, {0xF7E, p25.DUIDLDU1}, {0x000, p25.DUIDHeader}, {0xFFF, p25.DUIDTerminatorL}} {
		w := p25.EncodeNID(tc.nac, tc.duid)
		assert.Equal(t, uint64(tc.nac)<<4|uint64(tc.duid), w>>48, "info bits first")
		assert.Zero(t, bits.OnesCount64(w)&1, "even parity")

		// Polynomial division of the 63-bit codeword by g leaves zero.
		cw := w >> 1
		for i := 62; i >= 47; i-- {
			if cw&(1<<i) != 0 {
				cw ^= uint64(p25.BCHGenerator) << (i - 47)
			}
		}

		assert.Zero(t, cw)
	}

	assert.Equal(t, uint64(0), p25.EncodeNID(0, p25.DUIDHeader), "all-zero info is the zero codeword")
}

func TestNIDDecode_correctsUpToElevenErrors(t *testing.T) {
	dec := p25.NewNIDDecoder()
	rng := rand.New(rand.NewPCG(1, 2))

	for range 2000 {
		nac := uint16(rng.IntN(4096))
		duid := []p25.DUID{p25.DUIDTSDU, p25.DUIDLDU1, p25.DUIDLDU2, p25.DUIDHeader}[rng.IntN(4)]
		w := p25.EncodeNID(nac, duid)
		nerr := rng.IntN(12)

		for _, pos := range rng.Perm(64)[:nerr] {
			w ^= 1 << pos
		}

		gotNAC, gotDUID, fixed, ok := dec.Decode(w)
		require.True(t, ok, "nac %03x errors %d", nac, nerr)
		assert.Equal(t, nac, gotNAC)
		assert.Equal(t, duid, gotDUID)
		assert.LessOrEqual(t, fixed, nerr)
	}
}

func TestNIDDecode_rejectsMostNoise(t *testing.T) {
	dec := p25.NewNIDDecoder()
	rng := rand.New(rand.NewPCG(3, 4))
	accepted := 0

	for range 5000 {
		_, duid, _, ok := dec.Decode(rng.Uint64())
		if ok && duid.Valid() {
			accepted++
		}
	}

	// Bounded-distance decoding accepts ~2^16*V(63,11)/2^63 ~= 0.2% of
	// random words; DUID validity filters a further 9/16.
	assert.Less(t, accepted, 25)
}

// TestTrellis_knownAnswer pins the encoder output for a hand-traced input
// against the TIA-102.BAAA state transition table.
func TestTrellis_knownAnswer(t *testing.T) {
	words := p25.TrellisWords()

	for s := range 4 {
		seen := map[uint8]bool{}
		for in := range 4 {
			seen[words[s*4+in]] = true
		}

		assert.Len(t, seen, 4, "row %d is a permutation of distinct symbols", s)
	}

	// Input dibits 1,2,3,0 then zeros: state 0 --1--> 0xC, state 1 --2-->
	// 0xD, state 2 --3--> 0x4, state 3 --0--> 0x5, then 0x2 forever.
	var block [12]byte

	block[0] = 0b01_10_11_00
	enc := p25.EncodeTSBK(&block)
	table := p25.InterleaveTable()

	var coded [98]uint8
	for d, v := range enc {
		coded[table[d]] = v
	}

	want := []uint8{0x3, 0x0, 0x3, 0x1, 0x1, 0x0, 0x1, 0x1}
	assert.Equal(t, want, coded[:8])

	for i := 8; i < 98; i += 2 {
		assert.Equal(t, []uint8{0, 2}, coded[i:i+2], "zero input in state 0 emits 0x2 at %d", i)
	}
}

func TestInterleave_symbolOrder(t *testing.T) {
	table := p25.InterleaveTable()
	seen := map[uint8]bool{}

	for _, v := range table {
		seen[v] = true
	}

	assert.Len(t, seen, 98)

	// Transmitted symbol k (dibits 2k, 2k+1) is coded symbol order[k].
	var order []int

	for start := range 4 {
		for s := start; s < 49; s += 4 {
			order = append(order, s)
		}
	}

	for k, s := range order {
		assert.Equal(t, uint8(2*s), table[2*k])
		assert.Equal(t, uint8(2*s+1), table[2*k+1])
	}
}

func TestTSBKDecode_correctsScatteredErrors(t *testing.T) {
	dec := p25.NewTSBKDecoder()
	rng := rand.New(rand.NewPCG(5, 6))

	for range 200 {
		var b [12]byte
		for i := range b {
			b[i] = byte(rng.IntN(256))
		}

		enc := p25.EncodeTSBK(&b)

		// Flip one bit in each of four widely separated dibits.
		for _, pos := range []int{3, 30, 60, 90} {
			enc[pos] ^= 1 << rng.IntN(2)
		}

		got, metric := dec.Decode(enc[:])
		assert.Equal(t, b, got)
		assert.Equal(t, 4, metric)
	}
}

func TestSyncDibits_pattern(t *testing.T) {
	s := p25.SyncDibits()

	var w uint64
	for _, d := range s {
		w = w<<2 | uint64(d)
	}

	assert.Equal(t, uint64(0x5575F5FF77FF), w)

	for _, d := range s {
		assert.Contains(t, []float64{3, -3}, p25.DibitToSymbol(d), "sync uses only outer symbols")
	}
}

func TestStatusDibitPositions(t *testing.T) {
	frame := p25test.TSDU(0x293, p25test.Grant(0, 1, 2, 3))
	assert.Zero(t, len(frame)%36)

	n := 0

	for i := range frame {
		if p25.StatusDibit(i) {
			assert.Equal(t, 35, i%36)

			n++
		}
	}

	assert.Equal(t, len(frame)/36, n)
}

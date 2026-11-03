// Package p25test synthesizes P25 Phase 1 control channel IQ for tests and
// fixtures: it builds TSBKs with CRCs, trellis-encodes and interleaves
// them, assembles TSDU frames with frame sync, NID and status symbols, and
// C4FM-modulates the dibits to complex baseband with configurable channel
// offset, frequency error, clock error, polarity and noise.
//
// The output is synthetic: it exercises the decoder against the encoding
// rules as implemented here, plus independent known-answer tests in the
// p25 package. It is not an over-the-air recording.
package p25test

import (
	"encoding/binary"
	"math"
	"math/cmplx"
	"math/rand/v2"

	"github.com/openmanet/openmanetd/internal/sdr/p25"
)

// TSBK returns a 12-byte trunking signaling block for opcode/mfid and
// the 64 argument bits, with the CRC filled in and the last-block flag
// clear.
func TSBK(opcode, mfid uint8, args uint64) [12]byte {
	var b [12]byte

	b[0] = opcode & 0x3F
	b[1] = mfid
	binary.BigEndian.PutUint64(b[2:10], args)
	binary.BigEndian.PutUint16(b[10:], p25.TSBKCRC(b[:]))

	return b
}

// SetLastBlock sets the LB flag and refreshes the CRC.
func SetLastBlock(b *[12]byte) {
	b[0] |= 0x80
	binary.BigEndian.PutUint16(b[10:], p25.TSBKCRC(b[:]))
}

// Service option bits for grants.
const (
	SvcEmergency = 0x80
	SvcEncrypted = 0x40
)

// Channel packs a 4-bit identifier and 12-bit channel number.
func Channel(iden uint8, number uint16) uint16 { return uint16(iden&0xF)<<12 | number&0xFFF }

// Grant builds GRP_V_CH_GRANT.
func Grant(svc uint8, ch, group uint16, src uint32) [12]byte {
	return TSBK(0x00, 0, uint64(svc)<<56|uint64(ch)<<40|uint64(group)<<24|uint64(src&0xFFFFFF))
}

// GrantUpdate builds GRP_V_CH_GRANT_UPDT.
func GrantUpdate(chA, grpA, chB, grpB uint16) [12]byte {
	return TSBK(0x02, 0, uint64(chA)<<48|uint64(grpA)<<32|uint64(chB)<<16|uint64(grpB))
}

// GrantUpdateExplicit builds GRP_V_CH_GRANT_UPDT_EXP.
func GrantUpdateExplicit(svc uint8, chT, chR, group uint16) [12]byte {
	return TSBK(0x03, 0, uint64(svc)<<56|uint64(chT)<<32|uint64(chR)<<16|uint64(group))
}

// IdenUp builds IDEN_UP. txOffsetHz must be a multiple of 250 kHz.
func IdenUp(iden uint8, bandwidthHz uint32, txOffsetHz int64, spacingHz uint32, baseHz uint64) [12]byte {
	sign, off := offsetBits(txOffsetHz, 250_000)

	return TSBK(0x3D, 0, uint64(iden&0xF)<<60|uint64(bandwidthHz/125&0x1FF)<<51|
		sign<<50|(off&0xFF)<<42|uint64(spacingHz/125&0x3FF)<<32|baseHz/5&0xFFFFFFFF)
}

// IdenUpVU builds IDEN_UP_VU; txOffsetHz must be a multiple of spacingHz.
// bwCode is the 4-bit bandwidth code (0x4 = 6.25 kHz, 0x5 = 12.5 kHz).
func IdenUpVU(iden, bwCode uint8, txOffsetHz int64, spacingHz uint32, baseHz uint64) [12]byte {
	return idenVU(0x34, iden, bwCode, txOffsetHz, spacingHz, baseHz)
}

// IdenUpTDMA builds IDEN_UP_TDMA with the given channel type (3 = 2-slot
// 12.5 kHz TDMA).
func IdenUpTDMA(iden, channelType uint8, txOffsetHz int64, spacingHz uint32, baseHz uint64) [12]byte {
	return idenVU(0x33, iden, channelType, txOffsetHz, spacingHz, baseHz)
}

func idenVU(op, iden, code uint8, txOffsetHz int64, spacingHz uint32, baseHz uint64) [12]byte {
	sign, off := offsetBits(txOffsetHz, int64(spacingHz))

	return TSBK(op, 0, uint64(iden&0xF)<<60|uint64(code&0xF)<<56|sign<<55|(off&0x1FFF)<<42|
		uint64(spacingHz/125&0x3FF)<<32|baseHz/5&0xFFFFFFFF)
}

func offsetBits(offHz, unit int64) (sign, mag uint64) {
	if offHz >= 0 {
		sign = 1
	}

	if offHz < 0 {
		offHz = -offHz
	}

	return sign, uint64(offHz / unit)
}

// RFSSStatus builds RFSS_STS_BCST.
func RFSSStatus(lra uint8, sysID uint16, rfss, site uint8, ch uint16, ssc uint8) [12]byte {
	return TSBK(0x3A, 0, uint64(lra)<<56|uint64(sysID&0xFFF)<<40|uint64(rfss)<<32|uint64(site)<<24|
		uint64(ch)<<8|uint64(ssc))
}

// NetStatus builds NET_STS_BCST.
func NetStatus(lra uint8, wacn uint32, sysID, ch uint16, ssc uint8) [12]byte {
	return TSBK(0x3B, 0, uint64(lra)<<56|uint64(wacn&0xFFFFF)<<36|uint64(sysID&0xFFF)<<24|
		uint64(ch)<<8|uint64(ssc))
}

// AdjacentStatus builds ADJ_STS_BCST.
func AdjacentStatus(lra, flags uint8, sysID uint16, rfss, site uint8, ch uint16, ssc uint8) [12]byte {
	return TSBK(0x3C, 0, uint64(lra)<<56|uint64(flags&0xF)<<52|uint64(sysID&0xFFF)<<40|
		uint64(rfss)<<32|uint64(site)<<24|uint64(ch)<<8|uint64(ssc))
}

// StatusIdle is the outbound status symbol used for inserted status dibits.
const StatusIdle = 0b01

// TSDU assembles a trunking signaling data unit (1..3 TSBKs) into on-air
// dibits: frame sync, NID, TSBKs, status symbols every 36th dibit and null
// padding to the next status symbol boundary. The last block's LB flag is
// set automatically.
func TSDU(nac uint16, blocks ...[12]byte) []uint8 {
	if len(blocks) == 0 || len(blocks) > 3 {
		panic("p25test: a TSDU carries 1 to 3 TSBKs")
	}

	data := make([]uint8, 0, 56+98*len(blocks))
	syncD := p25.SyncDibits()
	data = append(data, syncD[:]...)

	nid := p25.EncodeNID(nac, p25.DUIDTSDU)
	for i := range 32 {
		data = append(data, uint8(nid>>(62-2*i))&3)
	}

	for i, b := range blocks {
		if i == len(blocks)-1 {
			SetLastBlock(&b)
		}

		enc := p25.EncodeTSBK(&b)
		data = append(data, enc[:]...)
	}

	return InsertStatus(data)
}

// InsertStatus interleaves status symbols into frame data dibits and pads
// with null dibits so the frame ends on a status symbol.
func InsertStatus(data []uint8) []uint8 {
	out := make([]uint8, 0, len(data)+len(data)/35+36)

	for _, v := range data {
		if p25.StatusDibit(len(out)) {
			out = append(out, StatusIdle)
		}

		out = append(out, v)
	}

	for len(out)%36 != 0 {
		if p25.StatusDibit(len(out)) {
			out = append(out, StatusIdle)
		} else {
			out = append(out, 0)
		}
	}

	return out
}

// RandomDibits returns n uniformly random dibits (idle filler between or
// before frames).
func RandomDibits(rng *rand.Rand, n int) []uint8 {
	out := make([]uint8, n)
	for i := range out {
		out[i] = uint8(rng.IntN(4))
	}

	return out
}

// Modulator turns dibits into C4FM complex baseband.
type Modulator struct {
	// SampleRate of the generated IQ in samples per second.
	SampleRate float64
	// OffsetHz places the channel away from 0 Hz (tuner offset).
	OffsetHz float64
	// FreqErrorHz is an additional carrier error (transmitter or tuner).
	FreqErrorHz float64
	// ClockPPM scales the symbol period (transmitter/receiver clock error).
	ClockPPM float64
	// Amplitude of the carrier (0..1 full scale).
	Amplitude float64
	// CNRdB is the carrier-to-noise ratio measured in a 12.5 kHz
	// bandwidth. Zero or negative disables noise.
	CNRdB float64
	// TimingOffset delays the symbol grid by this fraction of a symbol.
	TimingOffset float64
	// Invert flips deviation polarity.
	Invert bool
	// Seed makes the noise reproducible.
	Seed uint64
}

const (
	symbolRate = 4800.0
	devUnitHz  = 600.0
	pulseSpan  = 8  // symbols each side of the pulse center
	pulseOver  = 64 // table points per symbol
	rcAlpha    = 0.2
)

// Modulate C4FM-modulates dibits: impulses through the TIA-102.BAAA
// raised-cosine (alpha 0.2) and inverse-sinc shaping filters, then an FM
// modulator. The pulse is normalized so a one-symbol integrate-and-dump
// receiver sees exactly +/-600 and +/-1800 Hz at symbol centers.
func (m Modulator) Modulate(dibits []uint8) []complex64 {
	pulse := c4fmPulse()
	period := m.SampleRate / symbolRate * (1 + m.ClockPPM*1e-6) // samples per symbol
	t0 := (pulseSpan + m.TimingOffset) * period
	n := int(float64(len(dibits)+2*pulseSpan) * period)
	out := make([]complex64, n)
	rng := rand.New(rand.NewPCG(m.Seed, m.Seed^0x9e3779b97f4a7c15)) //nolint:gosec // reproducible test noise

	sigma := 0.0
	if m.CNRdB > 0 {
		// Noise power in 12.5 kHz = A^2 / CNR; spread across the full rate.
		sigma = math.Sqrt(m.Amplitude * m.Amplitude / math.Pow(10, m.CNRdB/10) * m.SampleRate / 12500 / 2)
	}

	syms := make([]float64, len(dibits))
	for i, d := range dibits {
		syms[i] = p25.DibitToSymbol(d)
		if m.Invert {
			syms[i] = -syms[i]
		}
	}

	phase := 0.0

	for i := range out {
		t := (float64(i) - t0) / period // in symbols
		k0 := int(math.Ceil(t - pulseSpan))
		f := 0.0

		for k := max(k0, 0); k < len(syms) && float64(k) <= t+pulseSpan; k++ {
			f += syms[k] * lookup(pulse, t-float64(k))
		}

		f = f*devUnitHz + m.OffsetHz + m.FreqErrorHz
		phase += 2 * math.Pi * f / m.SampleRate
		phase = math.Mod(phase, 2*math.Pi)

		v := cmplx.Rect(m.Amplitude, phase)
		if sigma > 0 {
			v += complex(rng.NormFloat64()*sigma, rng.NormFloat64()*sigma)
		}

		out[i] = complex64(v)
	}

	return out
}

func lookup(pulse []float64, t float64) float64 {
	x := (t + pulseSpan) * pulseOver
	i := int(x)

	if i < 0 || i+1 >= len(pulse) {
		return 0
	}

	frac := x - float64(i)

	return pulse[i] + frac*(pulse[i+1]-pulse[i])
}

// c4fmPulse tabulates the impulse response of raised cosine times the
// inverse-sinc shaping filter, h(t) = T * 2 * int_0^fmax H(f) cos(2 pi f t) df,
// with t in symbols and a mild window at the span edges.
func c4fmPulse() []float64 {
	const steps = 2000

	n := 2*pulseSpan*pulseOver + 1
	out := make([]float64, n)
	fmax := (1 + rcAlpha) / 2 // cycles per symbol
	df := fmax / steps

	h := make([]float64, steps)
	for j := range h {
		f := (float64(j) + 0.5) * df
		h[j] = raisedCosine(f) * math.Pi * f / math.Sin(math.Pi*f)
	}

	for i := range out {
		t := float64(i)/pulseOver - pulseSpan

		var acc float64
		for j, v := range h {
			acc += v * math.Cos(2*math.Pi*(float64(j)+0.5)*df*t)
		}

		w := 0.5 + 0.5*math.Cos(math.Pi*t/pulseSpan)
		out[i] = 2 * acc * df * w
	}

	return out
}

func raisedCosine(f float64) float64 {
	lo, hi := (1-rcAlpha)/2, (1+rcAlpha)/2

	switch {
	case f <= lo:
		return 1
	case f >= hi:
		return 0
	default:
		return 0.5 * (1 + math.Cos(math.Pi/rcAlpha*(f-lo)))
	}
}

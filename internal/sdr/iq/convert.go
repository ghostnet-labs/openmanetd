// Package iq holds the sample-level DSP building blocks shared by the SDR
// decoders: raw IQ byte-stream converters, FIR design, decimating filters,
// a numerically controlled oscillator for channel shifting and a fractional
// resampler. Every streaming type reuses caller- or self-owned buffers so the
// per-sample hot paths do not allocate in steady state.
package iq

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Format names a raw interleaved IQ byte layout produced by a capture tool.
type Format int

const (
	// FormatCU8 is unsigned 8-bit interleaved I/Q, offset 127.5, as written
	// by rtl_sdr.
	FormatCU8 Format = iota + 1
	// FormatCS16 is signed 16-bit little-endian interleaved I/Q, as written
	// by airspy_rx -t 2 (INT16_IQ).
	FormatCS16
	// FormatCF32 is 32-bit little-endian IEEE float interleaved I/Q.
	FormatCF32
)

// BytesPerSample returns the number of bytes one complex sample occupies.
func (f Format) BytesPerSample() int {
	switch f {
	case FormatCU8:
		return 2
	case FormatCS16:
		return 4
	case FormatCF32:
		return 8
	default:
		return 0
	}
}

// String returns the conventional file-extension name of the format.
func (f Format) String() string {
	switch f {
	case FormatCU8:
		return "cu8"
	case FormatCS16:
		return "cs16"
	case FormatCF32:
		return "cf32"
	default:
		return "unknown"
	}
}

// ParseFormat maps a format name ("cu8", "cs16", "cf32") to a Format.
func ParseFormat(s string) (Format, error) {
	switch s {
	case "cu8":
		return FormatCU8, nil
	case "cs16":
		return FormatCS16, nil
	case "cf32":
		return FormatCF32, nil
	default:
		return 0, fmt.Errorf("iq: unknown sample format %q", s)
	}
}

// Converter turns a raw IQ byte stream into complex64 samples scaled to
// roughly [-1, 1]. It is streaming: a read that ends mid-sample is carried
// over to the next call, so callers can pass arbitrary pipe reads.
// A Converter is not safe for concurrent use.
type Converter struct {
	lut    [256]float32
	carry  [8]byte
	format Format
	ncarry int
}

// NewConverter returns a Converter for format f.
func NewConverter(f Format) (*Converter, error) {
	if f.BytesPerSample() == 0 {
		return nil, fmt.Errorf("iq: unsupported format %d", int(f))
	}

	c := &Converter{format: f}
	for i := range c.lut {
		c.lut[i] = (float32(i) - 127.5) / 127.5
	}

	return c, nil
}

// Format returns the converter's input format.
func (c *Converter) Format() Format { return c.format }

// Reset discards any carried partial sample.
func (c *Converter) Reset() { c.ncarry = 0 }

// Convert appends the samples decoded from src to dst[:0] and returns the
// result. It allocates only when cap(dst) is too small for the output.
func (c *Converter) Convert(dst []complex64, src []byte) []complex64 {
	dst = dst[:0]
	bps := c.format.BytesPerSample()

	if c.ncarry > 0 {
		n := copy(c.carry[c.ncarry:bps], src)
		c.ncarry += n

		src = src[n:]

		if c.ncarry < bps {
			return dst
		}

		dst = c.convertFull(dst, c.carry[:bps])
		c.ncarry = 0
	}

	full := len(src) / bps * bps
	dst = c.convertFull(dst, src[:full])
	c.ncarry = copy(c.carry[:], src[full:])

	return dst
}

func (c *Converter) convertFull(dst []complex64, src []byte) []complex64 {
	bps := c.format.BytesPerSample()
	need := len(dst) + len(src)/bps

	if cap(dst) < need {
		grown := make([]complex64, len(dst), need)
		copy(grown, dst)
		dst = grown
	}

	switch c.format {
	case FormatCU8:
		for i := 0; i+1 < len(src); i += 2 {
			dst = append(dst, complex(c.lut[src[i]], c.lut[src[i+1]]))
		}
	case FormatCS16:
		const scale = 1.0 / 32768.0

		for i := 0; i+3 < len(src); i += 4 {
			re := int16(binary.LittleEndian.Uint16(src[i:]))
			im := int16(binary.LittleEndian.Uint16(src[i+2:]))
			dst = append(dst, complex(float32(re)*scale, float32(im)*scale))
		}
	case FormatCF32:
		for i := 0; i+7 < len(src); i += 8 {
			re := math.Float32frombits(binary.LittleEndian.Uint32(src[i:]))
			im := math.Float32frombits(binary.LittleEndian.Uint32(src[i+4:]))
			dst = append(dst, complex(re, im))
		}
	}

	return dst
}

// AppendCU8 quantizes samples to rtl_sdr-style unsigned 8-bit IQ and appends
// them to dst. Values outside [-1, 1] are clipped. It is the inverse of a
// FormatCU8 Converter and is used to synthesize fixtures.
func AppendCU8(dst []byte, src []complex64) []byte {
	for _, s := range src {
		dst = append(dst, quantU8(real(s)), quantU8(imag(s)))
	}

	return dst
}

// AppendCS16 quantizes samples to signed 16-bit little-endian IQ.
func AppendCS16(dst []byte, src []complex64) []byte {
	for _, s := range src {
		dst = binary.LittleEndian.AppendUint16(dst, uint16(quantS16(real(s))))
		dst = binary.LittleEndian.AppendUint16(dst, uint16(quantS16(imag(s))))
	}

	return dst
}

func quantU8(v float32) byte {
	x := math.Round(float64(v)*127.5 + 127.5)

	return byte(min(max(x, 0), 255))
}

func quantS16(v float32) int16 {
	x := math.Round(float64(v) * 32768)

	return int16(min(max(x, -32768), 32767))
}

package p25

// tsbkDibits is the on-air length of one trellis-coded, interleaved data
// block: 49 rate-1/2 constellation symbols of two dibits each.
const tsbkDibits = 98

// trellisWords returns the rate-1/2 trellis state transition table: entry
// [state*4+input] is the 4-bit symbol (two dibits, high dibit first) emitted
// when input dibit `input` arrives in state `state`; the next state is the
// input. This is the TIA-102.BAAA constellation table already composed with
// the constellation-to-dibit-pair mapping.
func trellisWords() [16]uint8 {
	return [16]uint8{
		0x2, 0xC, 0x1, 0xF,
		0xE, 0x0, 0xD, 0x3,
		0x9, 0x7, 0xA, 0x4,
		0x5, 0xB, 0x6, 0x8,
	}
}

// interleaveTable returns the TIA-102.BAAA data block interleave: received
// dibit d belongs at position table[d] of the de-interleaved block. In
// constellation-symbol terms it sends symbols 0,4,8,...,48 first, then
// 1,5,...,45, then 2,6,...,46 and finally 3,7,...,47.
func interleaveTable() [tsbkDibits]uint8 {
	return [tsbkDibits]uint8{
		0, 1, 8, 9, 16, 17, 24, 25, 32, 33, 40, 41, 48, 49, 56, 57, 64, 65, 72, 73, 80, 81, 88, 89, 96, 97,
		2, 3, 10, 11, 18, 19, 26, 27, 34, 35, 42, 43, 50, 51, 58, 59, 66, 67, 74, 75, 82, 83, 90, 91,
		4, 5, 12, 13, 20, 21, 28, 29, 36, 37, 44, 45, 52, 53, 60, 61, 68, 69, 76, 77, 84, 85, 92, 93,
		6, 7, 14, 15, 22, 23, 30, 31, 38, 39, 46, 47, 54, 55, 62, 63, 70, 71, 78, 79, 86, 87, 94, 95,
	}
}

// EncodeTSBK trellis-encodes and interleaves a 12-byte trunking signaling
// block into the 98 dibits that are transmitted (before status symbol
// insertion).
func EncodeTSBK(block *[12]byte) [tsbkDibits]uint8 {
	words := trellisWords()
	table := interleaveTable()

	var coded, out [tsbkDibits]uint8

	state := uint8(0)

	for i := range 49 {
		var in uint8
		if i < 48 {
			in = block[i/4] >> (6 - 2*(i%4)) & 3
		}

		w := words[state*4+in]
		coded[2*i] = w >> 2
		coded[2*i+1] = w & 3
		state = in
	}

	for d := range out {
		out[d] = coded[table[d]]
	}

	return out
}

// trellisDecoder is a hard-decision Viterbi decoder for the rate-1/2 P25
// trellis. All working storage is fixed-size so decoding never allocates.
type trellisDecoder struct {
	survivor [49][4]uint8
	words    [16]uint8
	table    [tsbkDibits]uint8
}

func newTrellisDecoder() *trellisDecoder {
	return &trellisDecoder{words: trellisWords(), table: interleaveTable()}
}

// decode de-interleaves and Viterbi-decodes 98 received dibits into dst.
// It returns the path metric (number of bit errors along the best path).
func (t *trellisDecoder) decode(dst *[12]byte, rx []uint8) int {
	var coded [tsbkDibits]uint8

	for d := range tsbkDibits {
		coded[t.table[d]] = rx[d]
	}

	const inf = 1 << 20

	metric := [4]int{0, inf, inf, inf}

	for i := range 49 {
		got := coded[2*i]<<2 | coded[2*i+1]

		var next [4]int

		for in := range uint8(4) {
			best, from := inf*2, uint8(0)

			for s := range uint8(4) {
				m := metric[s] + popcount4(got^t.words[s*4+in])
				if m < best {
					best, from = m, s
				}
			}

			next[in] = best
			t.survivor[i][in] = from
		}

		metric = next
	}

	// The 49th input is the zero flush dibit, so the path ends in state 0.
	state := uint8(0)
	*dst = [12]byte{}

	for i := 48; i >= 0; i-- {
		if i < 48 {
			dst[i/4] |= state << (6 - 2*(i%4))
		}

		state = t.survivor[i][state]
	}

	return metric[0]
}

func popcount4(v uint8) int {
	v = v&0x5 + v>>1&0x5

	return int(v&0x3 + v>>2&0x3)
}

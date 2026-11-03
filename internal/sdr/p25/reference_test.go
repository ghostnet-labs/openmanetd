package p25_test

import (
	"encoding/hex"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/sdr/p25"
)

// Known answers taken from independent reference implementations
// (SDRTrunk v0.6.1, read for facts only; nothing is copied from it). These
// pin our tables to code that decodes real P25 sites, not just to our own
// encoder.

// sdrtrunkNID is the 64-bit NID test vector in SDRTrunk's
// edac/BCH_63_16_11.java (NAC 0x260, DUID 0x3), with a variant carrying
// bit errors.
const (
	sdrtrunkNID      = "0010011000000011010010100000000110000111110011101010001010110000"
	sdrtrunkNIDError = "0001010100000011010010100000000110000111100011001010001010110000"
)

func parseBits(t *testing.T, s string) uint64 {
	t.Helper()

	v, err := strconv.ParseUint(s, 2, 64)
	require.NoError(t, err)

	return v
}

func TestReference_NIDVector(t *testing.T) {
	want := parseBits(t, sdrtrunkNID)
	assert.Equal(t, want, p25.EncodeNID(0x260, p25.DUIDTerminator), "bit-exact including parity bit")

	nac, duid, fixed, ok := p25.NewNIDDecoder().Decode(parseBits(t, sdrtrunkNIDError))
	require.True(t, ok)
	assert.Equal(t, uint16(0x260), nac)
	assert.Equal(t, p25.DUIDTerminator, duid)
	assert.Equal(t, 6, fixed)
}

// TestReference_Interleave pins entries of SDRTrunk's P25P1Interleave
// DATA_DEINTERLEAVE (bit-level: received bit i moves to pattern[i]):
// bits 4..7 -> 16..19, bits 52..55 -> 4..7, bits 100..103 -> 8..11,
// bits 148..151 -> 12..15, bits 192..195 -> 188..191.
func TestReference_Interleave(t *testing.T) {
	table := p25.InterleaveTable()
	bitMap := map[int]int{4: 16, 52: 4, 100: 8, 148: 12, 194: 190, 50: 194}

	for rxBit, deintBit := range bitMap {
		assert.Equal(t, uint8(deintBit/2), table[rxBit/2], "received bit %d", rxBit)
	}
}

// TestReference_TrellisMatrix pins SDRTrunk's P25_1_2_Node
// TRANSITION_MATRIX[previous input][input], whose rows are annotated with
// the TIA-102.BAAA constellation points {0,15,12,3}, {4,11,8,7},
// {13,2,1,14}, {9,6,5,10}.
func TestReference_TrellisMatrix(t *testing.T) {
	want := [16]uint8{2, 12, 1, 15, 14, 0, 13, 3, 9, 7, 10, 4, 5, 11, 6, 8}
	assert.Equal(t, want, p25.TrellisWords())
}

// The TSBK vectors below were assembled bit by bit in Python from
// SDRTrunk's field offset tables (FrequencyBandUpdate,
// GroupVoiceChannelGrant) with an independent CRC-CCITT implementation
// (init 0, inverted output), so they do not depend on p25test.
func decodeHex(t *testing.T, s string) [12]byte {
	t.Helper()

	raw, err := hex.DecodeString(s)
	require.NoError(t, err)
	require.Len(t, raw, 12)

	return [12]byte(raw)
}

func TestReference_IdenUpVector(t *testing.T) {
	var got []p25.ChannelIdentifier

	ok := p25.ParseTSBK(decodeHex(t, "bd001322d0320a2510a2ba20"), func(e p25.Event) {
		if id, isID := e.(*p25.ChannelIdentifier); isID {
			got = append(got, *id)
		}
	})
	require.True(t, ok, "CRC")
	require.Len(t, got, 1)
	assert.Equal(t, p25.ChannelIdentifier{
		BaseHz: 851_006_250, TxOffsetHz: -45_000_000, SpacingHz: 6250, BandwidthHz: 12500,
		Identifier: 1, Slots: 1,
	}, got[0])
}

func TestReference_GrantVector(t *testing.T) {
	var got []p25.GroupVoiceGrant

	ok := p25.ParseTSBK(decodeHex(t, "8000c312ab1f4112d687a79b"), func(e p25.Event) {
		if g, isGrant := e.(*p25.GroupVoiceGrant); isGrant {
			got = append(got, *g)
		}
	})
	require.True(t, ok, "CRC")
	require.Len(t, got, 1)
	assert.Equal(t, p25.GroupVoiceGrant{
		Source: 1234567, Talkgroup: 8001, Channel: 0x12AB, Priority: 3, Encrypted: true, Emergency: true,
	}, got[0])

	bad := decodeHex(t, "8000c312ab1f4112d687a79b")
	bad[5] ^= 1
	assert.False(t, p25.ParseTSBK(bad, func(p25.Event) {}), "CRC rejects a flipped bit")
}

// TestReference_TDMAChannelTypes pins SDRTrunk's ChannelType table: types
// 0-2 are FDMA (12.5, 12.5, 6.25 kHz), 3 and 5 are 2-slot TDMA, 4 is
// 4-slot 25 kHz TDMA.
func TestReference_TDMAChannelTypes(t *testing.T) {
	cases := []struct {
		code  uint8
		slots uint8
		bw    uint32
		tdma  bool
	}{
		{0, 1, 12500, false}, {1, 1, 12500, false}, {2, 1, 6250, false},
		{3, 2, 12500, true}, {4, 4, 25000, true}, {5, 2, 12500, true}, {9, 1, 0, false},
	}

	for _, tc := range cases {
		// IDEN_UP_TDMA, identifier 3, offset 0, spacing 12.5 kHz, base 851.00625 MHz.
		args := uint64(3)<<60 | uint64(tc.code)<<56 | uint64(1)<<55 | uint64(100)<<32 | 851_006_250/5
		b := tsbkWithArgs(0x33, args)

		var got p25.ChannelIdentifier

		require.True(t, p25.ParseTSBK(b, func(e p25.Event) { got = *e.(*p25.ChannelIdentifier) }))
		assert.Equal(t, tc.slots, got.Slots, "type %d", tc.code)
		assert.Equal(t, tc.bw, got.BandwidthHz, "type %d", tc.code)
		assert.Equal(t, tc.tdma, got.TDMA, "type %d", tc.code)
	}
}

func tsbkWithArgs(op uint8, args uint64) [12]byte {
	var b [12]byte

	b[0] = 0x80 | op
	for i := range 8 {
		b[2+i] = byte(args >> (56 - 8*i))
	}

	crc := p25.TSBKCRC(b[:])
	b[10], b[11] = byte(crc>>8), byte(crc)

	return b
}

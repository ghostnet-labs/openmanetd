package p25

// crcCCITTPoly is the CRC-CCITT generator x^16 + x^12 + x^5 + 1 used by the
// TIA-102.BAAA trunking signaling block.
const crcCCITTPoly = 0x1021

// crcCCITT computes the non-reflected CRC-CCITT of data with a zero initial
// register and no final inversion (the "XMODEM" parameterization).
func crcCCITT(data []byte) uint16 {
	var crc uint16

	for _, b := range data {
		crc ^= uint16(b) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ crcCCITTPoly
			} else {
				crc <<= 1
			}
		}
	}

	return crc
}

// TSBKCRC returns the CRC field for the first 10 bytes (80 bits) of a TSBK:
// CRC-CCITT with a zero initial register, inverted (TIA-102.BAAA).
func TSBKCRC(block []byte) uint16 {
	return crcCCITT(block[:10]) ^ 0xFFFF
}

// tsbkCRCValid reports whether the 12-byte block carries a valid CRC.
func tsbkCRCValid(block *[12]byte) bool {
	return TSBKCRC(block[:]) == uint16(block[10])<<8|uint16(block[11])
}

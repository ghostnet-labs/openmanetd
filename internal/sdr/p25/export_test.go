package p25

// Test-only access to unexported codec internals.

const BCHGenerator = bchGenerator

func CRCCCITT(b []byte) uint16 { return crcCCITT(b) }

func TrellisWords() [16]uint8 { return trellisWords() }

func InterleaveTable() [98]uint8 { return interleaveTable() }

type NIDDecoder struct{ c *nidCodec }

func NewNIDDecoder() NIDDecoder { return NIDDecoder{c: newNIDCodec()} }

func (n NIDDecoder) Decode(word uint64) (uint16, DUID, int, bool) { return n.c.decode(word) }

type TSBKDecoder struct{ t *trellisDecoder }

func NewTSBKDecoder() TSBKDecoder { return TSBKDecoder{t: newTrellisDecoder()} }

func (t TSBKDecoder) Decode(rx []uint8) ([12]byte, int) {
	var b [12]byte

	m := t.t.decode(&b, rx)

	return b, m
}

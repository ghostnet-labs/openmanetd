// Package p25 decodes P25 Phase 1 (TIA-102.BAAA FDMA) control channels from
// complex baseband IQ: C4FM demodulation, frame sync, NID BCH decoding,
// status symbol removal, TSBK de-interleaving, Viterbi decoding of the
// rate-1/2 trellis, CRC-CCITT checking and parsing of the trunking
// messages a scanner needs to follow voice grants.
//
// Phase II TDMA voice is out of scope; Phase II systems still run their
// control channel as Phase 1 FDMA, so this decoder follows them as well.
// Simulcast (LSM/CQPSK) control channels are received with the same C4FM
// discriminator path, which usually locks with some frame loss.
package p25

import (
	"math"

	"github.com/openmanet/openmanetd/internal/sdr/iq"
)

const (
	symbolRate = 4800.0

	// channelRate is the target demodulation rate; the decimation factor
	// is floor(sampleRate/channelRate) and timing recovery absorbs the
	// fractional samples per symbol that result.
	channelRate = 48000.0

	// C4FM channel filter edges. Occupied bandwidth is about +/-4.7 kHz;
	// the extra room tolerates ~1.5 kHz of tuning error.
	channelPassHz = 6000.0
	channelStopHz = 8500.0
	channelAttenD = 60.0

	// writeBlock bounds the per-call scratch memory; longer writes are
	// processed in pieces of this size.
	writeBlock = 4096

	syncDibits  = 24
	nidDibits   = 32
	headerData  = syncDibits + nidDibits // data dibits before the first TSBK
	maxTSBKs    = 3
	frameDibits = headerData + maxTSBKs*tsbkDibits

	// statusPeriod: one status dibit follows every 35 data dibits,
	// counted from the start of the frame sync.
	statusPeriod = 36

	// syncThreshold is the minimum normalized correlation against the
	// sync pattern; maxSyncErrors the dibit errors tolerated after
	// slicing the candidate with its own level estimate.
	syncThreshold = 0.80
	maxSyncErrors = 3

	// unlockSymbols: no valid NID for this many symbols declares loss.
	unlockSymbols = 4800

	// trackGain is the Gardner loop gain in samples per unit of
	// normalized timing error, scaled by samples per symbol; acquireGain
	// is used until the first frame sync so the loop pulls in quickly.
	trackGain   = 0.03
	acquireGain = 0.08
	// trackAlpha/acquireAlpha smooth the DC and amplitude estimates used
	// to normalize the timing error (per symbol).
	trackAlpha   = 1.0 / 128
	acquireAlpha = 1.0 / 24
	// histLen is the discriminator history ring (power of two, > 2 sps).
	histLen = 64
)

// syncWord is the 48-bit P25 Phase 1 frame synchronization sequence.
const syncWord = 0x5575F5FF77FF

// dibitToSymbol maps a dibit to its C4FM deviation in units of 600 Hz.
func dibitToSymbol(d uint8) float64 {
	switch d {
	case 0b01:
		return 3
	case 0b00:
		return 1
	case 0b10:
		return -1
	default:
		return -3
	}
}

// DibitToSymbol is the exported C4FM mapping: 01 -> +3, 00 -> +1,
// 10 -> -1, 11 -> -3 (units of 600 Hz deviation).
func DibitToSymbol(d uint8) float64 { return dibitToSymbol(d) }

// SyncDibits returns the 24 dibits of the frame sync, first on air first.
func SyncDibits() [syncDibits]uint8 {
	var out [syncDibits]uint8
	for i := range out {
		out[i] = uint8(uint64(syncWord)>>(46-2*i)) & 3
	}

	return out
}

// StatusDibit reports whether on-air dibit index i (counted from the first
// frame sync dibit) is a status symbol rather than data.
func StatusDibit(i int) bool { return i%statusPeriod == statusPeriod-1 }

// Stats are running counters for diagnostics. Read them with Decoder.Stats.
type Stats struct {
	Symbols     uint64 `json:"symbols"`
	Syncs       uint64 `json:"syncs"`
	NIDErrors   uint64 `json:"nid_errors"`
	NIDFixed    uint64 `json:"nid_corrected_bits"`
	TSBKs       uint64 `json:"tsbks"`
	TSBKBadCRC  uint64 `json:"tsbk_bad_crc"`
	TrellisBits uint64 `json:"trellis_corrected_bits"`
}

// Decoder is a streaming P25 Phase 1 control channel decoder. Feed it
// complex baseband samples with Write; decoded events are delivered
// synchronously to the emit callback from within Write. Not safe for
// concurrent use.
type Decoder struct {
	emit        func(Event)
	chain       *iq.Chain
	shifter     *iq.Shifter
	nid         *nidCodec
	trellis     *trellisDecoder
	mixBuf      []complex64
	decBuf      []complex64
	box         []float64 // integrate-and-dump window (one symbol)
	idens       [16]ChannelIdentifier
	hist        [histLen]float64    // matched filter output ring for interpolation
	syncPat     [syncDibits]float64 // sync pattern as +/-1
	win         [syncDibits]float64 // last 24 soft symbols
	stats       Stats
	adjacent    AdjacentSite
	site        SiteStatus
	grant       GroupVoiceGrant
	gain        float64 // Gardner loop gain (acquire or track)
	winPos      int
	boxPos      int
	head        int
	fmScale     float64
	alpha       float64
	sps         float64
	tau         float64 // samples until the next symbol strobe
	prevSym     float64
	dc          float64 // running symbol DC (frequency error), Hz
	absAvg      float64
	prev        complex64
	sinceLock   int
	boxSum      float64
	patNorm     float64
	levelScale  float64 // outer-symbol deviation from the last sync; negative when inverted
	level0      float64 // symbol DC from the last sync
	nextTSBK    int     // data dibit index where the next TSBK ends
	pos         int     // on-air dibit index within the frame
	ndata       int     // data dibits collected (status symbols removed)
	sync        SyncState
	nac         uint16
	frame       [frameDibits]uint8
	block       [12]byte
	inFrame     bool
	invertedPol bool
}

// NewDecoder returns a decoder for complex baseband at sampleRate samples
// per second (any rate >= 24 kS/s; typical captures are 1.024-2.4 MS/s).
// The control channel must be centered at 0 Hz unless SetFrequencyOffset
// is used. emit receives every decoded Event; see Event for ownership.
func NewDecoder(sampleRate int, emit func(Event)) *Decoder {
	if sampleRate < 2*int(channelStopHz)+4000 {
		panic("p25: sample rate too low for a 12.5 kHz channel")
	}

	factor := max(1, int(float64(sampleRate)/channelRate))

	chain, err := iq.NewChain(float64(sampleRate), factor, channelPassHz, channelStopHz, channelAttenD, writeBlock)
	if err != nil {
		// The rate check above guarantees a valid design.
		panic("p25: " + err.Error())
	}

	if emit == nil {
		emit = func(Event) {}
	}

	d := &Decoder{
		emit:    emit,
		chain:   chain,
		nid:     newNIDCodec(),
		trellis: newTrellisDecoder(),
		mixBuf:  make([]complex64, writeBlock),
		decBuf:  make([]complex64, 0, writeBlock/factor+1),
	}

	rate := chain.OutRate()
	d.sps = rate / symbolRate
	d.fmScale = rate / (2 * math.Pi)
	d.box = make([]float64, max(1, int(math.Round(d.sps))))
	d.tau = d.sps
	d.gain, d.alpha = acquireGain, acquireAlpha
	d.absAvg = 2 * 600 // mean |symbol| for equiprobable +/-1, +/-3

	pat := SyncDibits()
	for i, p := range pat {
		d.syncPat[i] = dibitToSymbol(p) / 3
	}

	var mean float64
	for _, p := range d.syncPat {
		mean += p
	}

	mean /= syncDibits

	for _, p := range d.syncPat {
		d.patNorm += (p - mean) * (p - mean)
	}

	d.patNorm = math.Sqrt(d.patNorm)

	return d
}

// SetFrequencyOffset tunes the decoder to a control channel offsetHz away
// from the capture center (e.g. +250e3 for a channel 250 kHz above the
// tuner frequency). Zero disables the mixer.
func (d *Decoder) SetFrequencyOffset(offsetHz float64) {
	if offsetHz == 0 {
		d.shifter = nil

		return
	}

	if d.shifter == nil {
		d.shifter = iq.NewShifter(offsetHz, d.chain.OutRate()*float64(d.chain.Factor()))

		return
	}

	d.shifter.SetOffset(offsetHz)
}

// Stats returns a copy of the running counters.
func (d *Decoder) Stats() Stats { return d.stats }

// ChannelRate returns the internal demodulation sample rate.
func (d *Decoder) ChannelRate() float64 { return d.chain.OutRate() }

// Identifiers returns the channel plan learned so far, indexed by the 4-bit
// channel identifier. Entries with SpacingHz == 0 are unknown.
func (d *Decoder) Identifiers() [16]ChannelIdentifier { return d.idens }

// Write consumes a block of complex baseband samples of any length. It does
// not allocate in steady state.
func (d *Decoder) Write(samples []complex64) {
	for len(samples) > 0 {
		n := min(len(samples), writeBlock)
		blk := samples[:n]
		samples = samples[n:]

		if d.shifter != nil {
			blk = d.shifter.Mix(d.mixBuf, blk)
		}

		d.decBuf = d.chain.Process(d.decBuf, blk)
		for _, s := range d.decBuf {
			d.demodSample(s)
		}
	}
}

// demodSample runs the FM discriminator, the one-symbol integrate-and-dump
// matched filter (C4FM's transmit shaping includes the inverse-sinc
// pre-compensation for exactly this receiver) and the interpolating
// Gardner timing loop.
func (d *Decoder) demodSample(s complex64) {
	p := s * complex(real(d.prev), -imag(d.prev))
	d.prev = s
	f := math.Atan2(float64(imag(p)), float64(real(p))) * d.fmScale

	d.boxSum += f - d.box[d.boxPos]
	d.box[d.boxPos] = f

	d.boxPos++
	if d.boxPos == len(d.box) {
		d.boxPos = 0
		// Re-sum once per window so float rounding cannot accumulate.
		var sum float64
		for _, v := range d.box {
			sum += v
		}

		d.boxSum = sum
	}

	d.head = (d.head + 1) & (histLen - 1)
	d.hist[d.head] = d.boxSum / float64(len(d.box))

	d.tau--
	if d.tau > 0 {
		return
	}

	sym := d.interp(d.tau)
	mid := d.interp(d.tau-d.sps/2) - d.dc

	outer := 1.5 * d.absAvg
	e := (sym - d.prevSym) * mid / (outer * outer)
	adj := min(max(d.gain*d.sps*e, -1), 1)
	d.tau += d.sps - adj
	d.prevSym = sym

	d.dc += (sym - d.dc) * d.alpha
	d.absAvg += (math.Abs(sym-d.dc) - d.absAvg) * d.alpha

	d.symbol(sym)
}

// interp linearly interpolates the matched filter output at pos samples
// relative to the newest sample (pos <= 0).
func (d *Decoder) interp(pos float64) float64 {
	i := math.Floor(pos)
	frac := pos - i
	a := d.hist[(d.head+int(i))&(histLen-1)]

	if frac == 0 {
		return a
	}

	b := d.hist[(d.head+int(i)+1)&(histLen-1)]

	return a + frac*(b-a)
}

// symbol handles one recovered symbol: frame assembly and sync search.
func (d *Decoder) symbol(sym float64) {
	d.stats.Symbols++
	d.win[d.winPos] = sym

	d.winPos++
	if d.winPos == syncDibits {
		d.winPos = 0
	}

	if d.inFrame {
		d.frameSymbol(sym)
	}

	if d.sync.Locked {
		d.sinceLock++
		if d.sinceLock > unlockSymbols {
			d.sync.Locked = false
			d.gain, d.alpha = acquireGain, acquireAlpha
			d.emit(&d.sync)
		}
	}

	if d.stats.Symbols >= syncDibits {
		d.checkSync()
	}
}

// checkSync correlates the last 24 symbols against the sync pattern (mean
// removed, so frequency error does not matter) and starts a frame on a hit.
func (d *Decoder) checkSync() {
	var mean float64
	for _, v := range d.win {
		mean += v
	}

	mean /= syncDibits

	var corr, energy float64

	for i := range syncDibits {
		v := d.win[(d.winPos+i)%syncDibits] - mean
		corr += v * d.syncPat[i]
		energy += v * v
	}

	if energy == 0 || math.Abs(corr) < syncThreshold*math.Sqrt(energy)*d.patNorm {
		return
	}

	// Level estimate from the sync itself: mean of the +3 and -3 symbols.
	var pos, neg float64

	var npos, nneg int

	for i := range syncDibits {
		v := d.win[(d.winPos+i)%syncDibits]
		if d.syncPat[i] > 0 {
			pos += v
			npos++
		} else {
			neg += v
			nneg++
		}
	}

	pos /= float64(npos)
	neg /= float64(nneg)
	level0 := (pos + neg) / 2
	scale := (pos - neg) / 2 // signed: negative for inverted polarity

	if scale == 0 {
		return
	}

	pat := SyncDibits()
	errs := 0

	for i := range syncDibits {
		if slice(d.win[(d.winPos+i)%syncDibits], level0, scale) != pat[i] {
			errs++
		}
	}

	if errs > maxSyncErrors {
		return
	}

	d.stats.Syncs++
	d.gain, d.alpha = trackGain, trackAlpha
	d.dc = level0
	d.level0, d.levelScale = level0, scale
	d.invertedPol = scale < 0
	d.inFrame = true
	d.pos = syncDibits
	d.ndata = syncDibits
	copy(d.frame[:syncDibits], pat[:])
}

// slice converts a soft symbol into a dibit using the frame's levels.
func slice(v, level0, scale float64) uint8 {
	x := (v - level0) / scale * 3

	switch {
	case x >= 2:
		return 0b01
	case x >= 0:
		return 0b00
	case x >= -2:
		return 0b10
	default:
		return 0b11
	}
}

// frameSymbol appends one on-air symbol to the current frame, dropping
// status symbols, and decodes the NID and TSBKs as they complete.
func (d *Decoder) frameSymbol(sym float64) {
	isStatus := StatusDibit(d.pos)
	d.pos++

	if isStatus {
		return
	}

	d.frame[d.ndata] = slice(sym, d.level0, d.levelScale)
	d.ndata++

	switch d.ndata {
	case headerData:
		d.decodeNID()
	case d.nextTSBK:
		d.decodeTSBK()
	}
}

func (d *Decoder) decodeNID() {
	var word uint64
	for _, v := range d.frame[syncDibits:headerData] {
		word = word<<2 | uint64(v)
	}

	nac, duid, fixed, ok := d.nid.decode(word)
	if !ok || !duid.Valid() {
		d.stats.NIDErrors++
		d.inFrame = false

		return
	}

	d.stats.NIDFixed += uint64(fixed)
	d.nac = nac
	d.sinceLock = 0

	if !d.sync.Locked || d.sync.NAC != nac || d.sync.Inverted != d.invertedPol {
		d.sync = SyncState{Locked: true, NAC: nac, Inverted: d.invertedPol}
		d.emit(&d.sync)
	}

	if duid != DUIDTSDU {
		d.inFrame = false

		return
	}

	d.nextTSBK = headerData + tsbkDibits
}

func (d *Decoder) decodeTSBK() {
	start := d.nextTSBK - tsbkDibits
	fixed := d.trellis.decode(&d.block, d.frame[start:d.nextTSBK])
	last := d.nextTSBK+tsbkDibits > frameDibits

	if !tsbkCRCValid(&d.block) {
		d.stats.TSBKBadCRC++
	} else {
		d.stats.TSBKs++
		d.stats.TrellisBits += uint64(fixed)
		last = last || d.block[0]&0x80 != 0
		d.handleTSBK(&d.block)
	}

	if last {
		d.inFrame = false

		return
	}

	d.nextTSBK += tsbkDibits
}

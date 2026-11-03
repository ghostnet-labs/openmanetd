# internal/sdr

Native Go SDR decoding for the GHO-53 scanner work. It uses no CGO and no
vocoder: control channel and analog voice only. Everything runs on recorded
or synthesized IQ, so CI needs no hardware.

| Package | Purpose |
|---------|---------|
| `iq` | cu8 (rtl_sdr), cs16 (airspy_rx `-t 2`) and cf32 converters; Kaiser FIR design; decimating FIR; multi-stage decimation `Chain`; NCO `Shifter`; cubic `Resampler`. |
| `p25` | P25 Phase 1 control channel decoder: C4FM demod, frame sync (both polarities), BCH(63,16) NID, status symbol removal, TSBK de-interleave, Viterbi, CRC-CCITT, trunking message parsing. |
| `p25/p25test` | Test-vector generator: TSBK builders, TSDU assembly, C4FM modulator with offset, carrier error, clock error, noise and polarity. |
| `analog` | NFM with a CTCSS-free noise squelch (12.5/25 kHz) and AM envelope detection (8.33/25 kHz aviation). Both produce 8 kHz int16 PCM blocks with the squelch state. |
| `source` | `Source` runs `rtl_sdr`/`airspy_rx` through an injectable `Runner` (it claims devices by serial and restarts on `Retune`). `FileSource` replays a capture. |

Fixtures live in `testfixtures/sdr/p25/`. They are synthesized, not recorded
over the air (see that directory's README).

## Pipeline

```
rtl_sdr / airspy_rx stdout --source.Source--> []complex64 @ 1-10 MS/s
  -> Shifter (channel offset) -> Chain (decimate to ~48 kHz, 6 kHz channel filter)
  -> FM discriminator -> 1-symbol integrate-and-dump -> Gardner timing (4800 Bd)
  -> sync correlator (0x5575F5FF77FF, either polarity) -> level estimate from sync
  -> slicer -> drop status dibits -> NID BCH -> TSBK deinterleave -> Viterbi -> CRC
  -> events: *GroupVoiceGrant, *SiteStatus, *AdjacentSite, *ChannelIdentifier, *SyncState
```

```go
d := p25.NewDecoder(1_024_000, func(e p25.Event) {
	if g, ok := e.(*p25.GroupVoiceGrant); ok && !g.Encrypted {
		follow(*g) // copy: events are reused after the callback returns
	}
})
d.SetFrequencyOffset(+250e3) // control channel 250 kHz above the tuner center
src, _ := source.New(source.Config{Driver: source.DriverAirspy, Serial: "0x...",
	FrequencyHz: 851_000_000, SampleRate: 2_500_000}, nil)
_ = src.Run(ctx, d.Write)
```

## Performance

These figures are from the amd64 dev container (4 vCPU). A CM5 should be
within about 2x. Every hot path allocates nothing in steady state, and
tests assert 0 allocs.

| Benchmark | Time | Allocs |
|-----------|------|--------|
| `p25.Decoder.Write`, 16384 samples at 1.024 MS/s (16 ms of signal) | ~290 us | 0 |
| `iq.Chain`, 1.024 MS/s / 21, 16384 samples | ~190 us | 0 |
| `analog` NFM, 16384 samples at 1.024 MS/s | ~270 us | 0 |
| `iq.Converter` cu8, 64 KiB | ~31 us | 0 |

On the synthetic signals, the decoder gets every TSBK through at 12 dB or
more CNR (in 12.5 kHz) with carrier errors tested from -0.9 to +2 kHz and an 80 ppm clock
error. At 10 dB, about 97% of TSBKs pass. The first frame after start can
be missed while the loops pull in (about 100 symbols).

## Bench questions (unverified against real P25 sites)

The codecs are pinned against independent known answers where one exists:
the CRC-16/XMODEM check value; the BCH generator re-derived from GF(64) and
matched to the published BCH table entry; and a hand-traced trellis output.
The items below match how other decoders work (OP25, SDRTrunk) but were
written without the TIA documents at hand. Each one needs a single
over-the-air capture to confirm.

1. **NID bit order.** The code assumes the codeword is systematic: the 16
   information bits (NAC then DUID) go out first as the high-order
   coefficients, then 47 parity bits, then an even-parity bit. The 64th bit
   is not checked. Capture a known NAC and compare.
2. **Trellis table orientation and interleave direction.** The code assumes
   the `[state][input]` word table `2C1F E0D3 97A4 5B68` with the high dibit
   first, and that received dibit `d` lands at position `table[d]` of the
   de-interleaved block. Swapping either one would fail every CRC on
   hardware while the synthetic tests still pass.
3. **TSBK field offsets and units.** These need checking against
   TIA-102.AABC: RFSS/NET/ADJ_STS_BCST bit positions; the IDEN_UP tx-offset
   sign (1 = positive) and its 250 kHz unit; the IDEN_UP_VU/TDMA offset
   counted in channel spacings; the IDEN_UP_TDMA channel-type to slots table
   (`1,1,1,2,4,2`); and the GRP_V_CH_GRANT_UPDT_EXP field order (T before R).
   `FrequencyHz` is taken from channel T.
4. **Status symbol phase.** The code puts status dibits at on-air index
   35 mod 36, counted from the first frame sync dibit.
5. **Simulcast (LSM/CQPSK).** Many Utah sites are simulcast. The C4FM
   discriminator usually locks on CQPSK control channels, with some frame
   loss. Measure the TSBK CRC pass rate on a UCA simulcast site. If it is
   poor, add a CQPSK path (differential phase detector plus a Costas or AFC
   loop) in front of the same framer.
6. **Carrier error.** The decoder has no AFC; the slicer calibrates DC from
   each sync. It is reliable to about +/-1.5-2 kHz, which is under 2 ppm at
   800 MHz. Use TCXO dongles or `source.Config.PPM`. Adding AFC between
   frames is a follow-up.
7. **rtl_sdr `-d` index fallback.** librtlsdr parses `-d` as an index first
   (strtol base 0), then as a serial. `Validate` rejects numeric rtl serials
   below 256, such as the factory default `00000001`. Confirm that the
   planned `1090` and `978` serials open the right device with several
   dongles attached.
8. **airspy_rx flags.** Confirm the `-s 0x<serial>` format, that `-a` takes
   Hz, and the `-t 2` int16 scaling (12-bit samples). The scaling only
   affects absolute dBFS, which matters for the AM carrier squelch.
9. **Squelch defaults.** `analog.DefaultNoiseSquelch` (0.5 of peak
   deviation, measured as noise RMS above the voice band) and
   `DefaultCarrierSquelchDBFS` (-45 dBFS) were set on synthetic noise. Set
   them against real noise floors at the gains used in the field.

Out of scope: Phase II TDMA voice, IMBE/AMBE audio (patent-encumbered),
inbound (ISP) messages, and vendor (non-zero MFID) TSBKs, which are ignored.
Encrypted TSBKs (P bit) are ignored too. Encrypted calls are still reported,
with `Encrypted=true`, from the grant's service options.

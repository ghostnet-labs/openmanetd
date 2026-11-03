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
d, err := p25.NewDecoder(1_024_000, func(e p25.Event) {
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
## Conventional scan (internal/scanner/conventional)

The scan RTL-SDR (`scanner.receivers.scanSerial`, default `scan`) sweeps
analog channels. Those are the GHO-60 conventional, interop and weather rows
plus aviation, all embedded in `sites.dat.gz` as `C` records. The manager
selects every channel within `scanner.conventional.radiusKm` of the fix
(default 80 km, at most 500). It also always includes the nationwide `US`
channels and 121.5 MHz, keeping one entry per frequency.

```
plan (channels, sorted) -> greedy weighted window cover (always-on x3, <=12 windows, <=8 ch each)
  per window: rtl_sdr -f center -s 2048000 --source.Source--> []complex64
    -> settle 50 ms -> per channel: Shifter (offset) -> analog.Demodulator
       (AM 108-137 MHz, NFM elsewhere) -> 8 kHz int16 PCM, 160-sample blocks
    -> dwell 1 s; hold while squelch open + 2 s, at most 30 s -> next window
  squelch-open blocks -> AudioFrame -> StreamAudio (PCM s16le or Opus)
```

- **Ownership.** The scan RTL-SDR belongs to either the P25 decoder or the
  conventional scanner, never to both. With no wide receiver
  (`scanner.receivers.wideSerial`), P25 keeps the scan tuner and the panel
  shows `RECEIVER_BUSY`. Before tuning the scan tuner for P25, the manager
  stops the conventional capture and waits for `WaitIdle`. Before handing
  the tuner back, it waits for `p25mon.WaitReleased`.
- **Listening** pins the scan to the windows that hold the subscribed
  channels. Streams are capped at 4, and further requests get
  `ResourceExhausted`. Subscriber channels hold 10 frames (200 ms), and a
  slow client loses frames rather than blocking the capture. Only 8 kHz
  audio leaves the node, never IQ.
- **Wide tuner rate.** `scanner.tuner.wideSampleRate` is optional and must
  be one of 2500000, 3000000, 6000000 or 10000000. Airspy Mini supports
  6/3 MS/s and Airspy R2 supports 10/2.5 MS/s. A set rate caps the usable
  P25 span at 80% of the rate. Any other value is ignored and logged as a
  config problem when the scanner is enabled.

| Benchmark (amd64, 4 vCPU) | Time | Allocs |
|-----------|------|--------|
| `conventional` consume, 8 channels, 32768 samples at 2.048 MS/s (16 ms) | ~4.1 ms | 0 |
| `iq.Shifter.Mix`, 32768 samples | ~110 us | 0 |

Eight channels therefore take about 25% of one amd64 core. Fixtures are in
`testfixtures/sdr/conventional/`: one synthesized AM aviation capture and one
NFM capture, each with a live channel and a quiet one. They run end to end
through the real scanner, the manager and the `StreamAudio` handler,
including an Opus decode.

## Verified against reference source

These were checked against SDRTrunk v0.6.1, a decoder used on live sites,
and against airspyone_host `fc61ab6` (`airspy_rx.c`). The reference code was
read for facts only; nothing was copied. `p25/reference_test.go` holds the
known answers derived from it.

- **NID.** `EncodeNID(0x260, TDU)` reproduces the 64-bit NID vector in
  SDRTrunk's `BCH_63_16_11` bit for bit, parity bit included: 16 info bits
  first, then 47 BCH parity bits and an even-parity bit. Its
  errored variant decodes back to NAC 0x260 with 6 bits corrected. Further
  independent pins: the generator is re-derived from GF(64) and matches
  the published BCH table, and the CRC matches the CRC-16/XMODEM check
  value.
- **Trellis.** The `[previous input][input]` word table equals SDRTrunk's
  `P25_1_2_Node.TRANSITION_MATRIX`, whose rows are annotated with the
  BAAA constellation points. Symbols are read MSB first and decoded bits
  are emitted MSB first, as here.
- **Interleave.** All 196 entries of SDRTrunk's bit-level
  `DATA_DEINTERLEAVE` match our dibit table, and the direction matches
  (received position i goes to pattern[i]).
- **CRC.** SDRTrunk generates its TSBK checksums as CCITT over 80 bits with
  output inversion, the same as `TSBKCRC`.
- **Status symbols.** SDRTrunk's framer drops one dibit after every 35 data
  dibits. It sets its counter to 21 after the 56 FS+NID data dibits, which
  is on-air index 35 mod 36 from the start of the sync, as here.
- **TSBK fields.** These all match SDRTrunk's offset tables, units and
  sign conventions: IDEN_UP (sign bit set = positive, 250 kHz unit),
  IDEN_UP_VU and IDEN_UP_TDMA (offset in channel spacings), GRP_V_CH_GRANT,
  GRANT_UPDT, GRANT_UPDT_EXP (downlink channel first, so `FrequencyHz`
  comes from the downlink), RFSS/NET/ADJ_STS_BCST, and the service option
  bits (0x80 emergency, 0x40 encrypted). TDMA downlink is
  base + spacing * floor(n / slots).
  - *Fixed:* in IDEN_UP_TDMA, channel types 0-2 are FDMA and the bandwidth
    depends on the type (12.5, 12.5, 6.25, 12.5, 25, 12.5 kHz). Unknown
    types keep one slot. The previous code marked every IDEN_UP_TDMA entry
    TDMA at 12.5 kHz.
- **airspy_rx.** `-s` is parsed with strtoull (decimal, or hex after `0x`),
  `-t 2` is INT16_IQ, `-r -` writes stdout, and `-f` is MHz via strtod.
  *Fixed:* `-a` values up to 1,000,000 are read as a rate index, not Hz, so
  `Validate` now requires an airspy rate above 1 MS/s and a numeric serial.

## Bench questions (still need real hardware)

The numbering follows the earlier list: items 1-4 and the airspy flags are
now verified against reference source (above). "Verified" means it agrees
with a decoder that works on air. It is not the same as decoding a capture
here; the first over-the-air recording should still become a fixture.

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
8. **airspy int16 scaling.** The flags are source-verified (see above). The
   absolute level of `-t 2` samples (12-bit ADC in int16) is not. It only
   affects dBFS, which matters for the AM carrier squelch.
9. **Squelch defaults.** `analog.DefaultNoiseSquelch` (0.5 of peak
   deviation, measured as noise RMS above the voice band) and
   `DefaultCarrierSquelchDBFS` (-45 dBFS) were set on synthetic noise. Set
   them against real noise floors at the gains used in the field.

10. **Conventional retune cost.** Every window visit restarts `rtl_sdr`, so
   USB open, tuner init and PLL lock cost roughly 100-300 ms on top of the
   50 ms settle. Measure the time between visits on a CM5. If it eats too
   much of the 1 s dwell, keep one `rtl_tcp` process and retune it over
   its control socket instead (follow-up).
11. **Conventional squelch levels.** The NFM noise squelch and the AM
   -45 dBFS carrier squelch (item 9) also gate conventional audio and the
   LIVE / last-heard state. RTL-SDR AGC and gain change the AM dBFS
   reference, so set the level per receiver with real traffic.
12. **Conventional CPU on target.** The 8-channel figure above is amd64.
   `Shifter.Mix` and the decimation chains use float64 and float32
   arithmetic, which is very slow on soft-float mipsle. Measure on CM5
   (arm64) and lower `MaxPerWindow` (or the sample rate) if a window cannot
   keep up in real time. Real-time limit: a 32768-sample block must finish
   in under 16 ms.
13. **80% usable window.** Window planning uses `scanner.tuner.rtlUsableHz`
   (default 2.0 MHz), clamped to 80% of 2.048 MS/s (about 1.64 MHz), and
   assumes that span is flat enough. The edge
   guard is 12.5 kHz and the DC guard 20 kHz. Check the R820T roll-off and
   the DC spike at the gains used; widen the guards if edge channels
   measure worse.

Out of scope: Phase II TDMA voice, IMBE/AMBE audio (patent-encumbered),
inbound (ISP) messages, and vendor (non-zero MFID) TSBKs, which are ignored.
Encrypted TSBKs (P bit) are ignored too. Encrypted calls are still reported,
with `Encrypted=true`, from the grant's service options.

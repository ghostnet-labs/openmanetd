# P25 Phase 1 control channel IQ fixtures

**These files are synthesized, not over-the-air recordings.** They come from
`internal/sdr/p25/p25test`, which builds TSBKs (with CRC-CCITT), trellis-encodes
and interleaves them, assembles TSDU frames (frame sync, BCH NID, status
symbols), C4FM-modulates them and adds a channel offset, carrier error, clock
error and Gaussian noise. They pin decoder behavior for CI; they do not prove
interoperability with real P25 sites (see the bench questions in
`internal/sdr/README.md`).

Format: rtl_sdr-style unsigned 8-bit interleaved I/Q (`cu8`) at 1.024 MS/s.
Each file is about 140 ms (under 300 KB).

| File | Channel offset | Carrier error | CNR (12.5 kHz) | Other | Content |
|------|----------------|---------------|----------------|-------|---------|
| `c4fm_1024k_site.cu8` | +100 kHz | +400 Hz | 22 dB | timing offset 0.37 symbol | IDEN_UP, NET_STS_BCST, RFSS_STS_BCST; GRP_V_CH_GRANT TG 8001 |
| `c4fm_1024k_inverted_encrypted.cu8` | -150 kHz | -700 Hz | 16 dB | inverted polarity, +40 ppm clock | IDEN_UP_VU, encrypted emergency grant TG 100, GRP_V_CH_GRANT_UPDT; ADJ_STS_BCST |
| `c4fm_1024k_tdma_lowsnr.cu8` | +250 kHz | +1000 Hz | 14 dB | timing offset 0.6 symbol | ADJ_STS_BCST, GRP_V_CH_GRANT_UPDT_EXP (encrypted), IDEN_UP_TDMA; grant on a TDMA slot |

NAC 0x293, WACN 0xBEE00, system 0x3A1 throughout. Expected decodes live in
`internal/sdr/p25/fixtures_test.go`.

To regenerate (deterministic, seeded noise):

```sh
OPENMANET_UPDATE_FIXTURES=1 go test ./internal/sdr/p25 -run TestGenerateFixtures
```

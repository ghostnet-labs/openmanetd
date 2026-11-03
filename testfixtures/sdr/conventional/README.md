# Conventional (analog) scanner IQ fixtures

**These files are synthesized, not over-the-air recordings.** They come from
`internal/scanner/conventional/conventionaltest.Synthesize`: one modulated
carrier plus complex Gaussian noise at -42 dBFS total power, quantized to
rtl_sdr's unsigned 8-bit interleaved I/Q (`cu8`) at 256 kS/s. Each file is
250 ms (128000 bytes) and loops without a phase step (whole numbers of
carrier and tone cycles).

| File | Capture center | Carrier | Quiet channel |
|------|----------------|---------|---------------|
| `am_256k_aviation.cu8` | 120.000 MHz | 120.050 MHz, AM, 1 kHz tone, 60% depth, amplitude 0.3 | 119.950 MHz |
| `nfm_256k_vhf.cu8` | 155.425 MHz | 155.450 MHz, NFM, 1 kHz tone, 2.5 kHz deviation, amplitude 0.3 | 155.400 MHz |

The window planner centers the scan list {119.95, 120.05} on 120.000 MHz and
{155.40, 155.45} on 155.425 MHz, so a test that scans those four channels at
256 kS/s replays these captures in turn (see
`internal/scanner/conventional/scanner_test.go` and the ScannerService
integration test). The squelch must open on the carrier and stay closed on
the quiet channel.

To regenerate (deterministic, seeded noise):

```sh
OPENMANET_UPDATE_FIXTURES=1 go test ./internal/scanner/conventional -run TestGenerateFixtures
```

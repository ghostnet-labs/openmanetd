package source

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/openmanet/openmanetd/internal/sdr/iq"
)

// FileSource replays a raw IQ capture (e.g. a testfixtures/sdr fixture or
// an rtl_sdr recording) into a Consumer.
type FileSource struct {
	Path   string
	Format iq.Format
	// SampleRate is used for Realtime pacing.
	SampleRate int
	// BlockSamples per Consumer call (0 selects 16384).
	BlockSamples int
	// Loop restarts from the beginning at EOF until ctx is canceled.
	Loop bool
	// Realtime paces delivery at SampleRate instead of as fast as possible.
	Realtime bool
}

// Run replays the file. It returns nil at EOF (without Loop) or when ctx
// is canceled.
func (f *FileSource) Run(ctx context.Context, consume Consumer) error {
	conv, err := iq.NewConverter(f.Format)
	if err != nil {
		return err
	}

	fh, err := os.Open(f.Path)
	if err != nil {
		return fmt.Errorf("source: open replay file: %w", err)
	}
	defer fh.Close()

	block := f.BlockSamples
	if block <= 0 {
		block = 16384
	}

	var tick *time.Ticker

	if f.Realtime && f.SampleRate > 0 {
		tick = time.NewTicker(time.Duration(float64(block) / float64(f.SampleRate) * float64(time.Second)))
		defer tick.Stop()
	}

	r := bufio.NewReaderSize(fh, readSize)
	buf := make([]byte, block*f.Format.BytesPerSample())
	samples := make([]complex64, 0, block)

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		n, err := io.ReadFull(r, buf)
		if n > 0 {
			samples = conv.Convert(samples, buf[:n])
			consume(samples)
		}

		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			if !f.Loop {
				return nil
			}

			if _, seekErr := fh.Seek(0, io.SeekStart); seekErr != nil {
				return fmt.Errorf("source: rewind: %w", seekErr)
			}

			r.Reset(fh)
			conv.Reset()

			continue
		}

		if err != nil {
			return fmt.Errorf("source: read replay file: %w", err)
		}

		if tick != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
			}
		}
	}
}

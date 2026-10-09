package hardware

import (
	"context"
	"fmt"
	"time"
)

// Clock is the manager's time source. Tests inject a fake whose Sleep
// advances virtual time without blocking.
type Clock interface {
	Now() time.Time
	// Sleep blocks for d or until ctx is done, returning ctx.Err() in the
	// latter case.
	Sleep(ctx context.Context, d time.Duration) error
}

// realClock is the wall-clock Clock.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("sleep: %w", ctx.Err())
	case <-t.C:
		return nil
	}
}

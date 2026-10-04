package control

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestOpenVLMSource_disconnectReleasesActivePTT(t *testing.T) {
	tests := []struct {
		name    string
		reports []bool
		want    []PTTEvent
	}{
		{name: "pressed", reports: []bool{true}, want: []PTTEvent{PTTDown, PTTUp}},
		{name: "idle", reports: []bool{false}},
		{name: "released", reports: []bool{true, false}, want: []PTTEvent{PTTDown, PTTUp}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reports := make(chan []byte, len(tt.reports))
			for _, pressed := range tt.reports {
				reports <- makeOpenVLMReport(pressed)
			}

			close(reports)

			src := NewOpenVLMSourceWithOpener(openerReturning(&fakeDisconnectHIDDevice{reports: reports}), zerolog.Nop())
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			t.Cleanup(cancel)

			events := src.Events(ctx)

			var got []PTTEvent

			for {
				select {
				case ev, ok := <-events:
					if !ok {
						require.Equal(t, tt.want, got)

						return
					}

					got = append(got, ev)
				case <-ctx.Done():
					// Cancel before joining; the event sender must always have
					// a shutdown path even if a regression fills its buffer.
					cancel()

					for range events {
					}

					t.Fatal("source did not close after disconnect")
				}
			}
		})
	}
}

package control

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// TestOpenVLMSource_disconnectReleasesActivePTT pins that a HID loss while
// PTT is held emits PTTUp before the binding is released, and that the
// source then keeps running (rediscovery) instead of closing its channel.
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

			// First open returns the device that disconnects; later opens
			// (rediscovery) get a device that blocks until closed.
			first := true
			opener := func(string) (HIDDevice, error) {
				if first {
					first = false

					return &fakeDisconnectHIDDevice{reports: reports}, nil
				}

				return newMockHIDDevice(), nil
			}

			binder := newFakeDeviceBinder("/dev/hidraw0", 1)
			src := NewOpenVLMSourceWithOptions(zerolog.Nop(), OpenVLMOptions{
				Opener: opener, Binder: binder, RetryMin: time.Millisecond, RetryMax: time.Millisecond,
			})

			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			events := src.Events(ctx)

			var got []PTTEvent

		collect:
			for {
				select {
				case ev, ok := <-events:
					require.True(t, ok, "source must not close its channel on device loss")

					got = append(got, ev)
				case <-binder.released:
					break collect
				case <-time.After(time.Second):
					t.Fatal("binding was not released after disconnect")
				}
			}

			cancel()

			// Release happens after the balancing PTTUp was queued; drain
			// anything still buffered until the source shuts down.
			for ev := range events {
				got = append(got, ev)
			}

			require.Equal(t, tt.want, got)
		})
	}
}

package scanner

import (
	"context"
	"time"
)

// TickForTest runs one poll cycle synchronously.
func (m *Manager) TickForTest(ctx context.Context, now time.Time) { m.tick(ctx, now) }

// HandleEventForTest applies one decoder event synchronously.
func (m *Manager) HandleEventForTest(ev any, now time.Time) { m.handleEvent(ev, now) }

// MarkEnabledForTest marks the manager running without starting its
// goroutine, so tests can drive it with TickForTest.
func (m *Manager) MarkEnabledForTest() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.enabled = true
}

// HuntOrderForTest exposes the control-channel hunt order for sel.
func HuntOrderForTest(sel *Selection) []uint64 { return huntOrder(sel) }

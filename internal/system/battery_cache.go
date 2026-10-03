package system

import (
	"fmt"
	"sync"
	"time"
)

// BatteryFreshnessLimit bounds how long the dashboard may reuse a last-good
// reading after a transient monitor read failure. It is not a polling interval.
const BatteryFreshnessLimit = 5 * time.Second

// CachedBatteryProvider tolerates brief read errors without displaying old
// measurements as current indefinitely. Every request still reads the monitor,
// so hotplug is rediscovered by the underlying provider. It owns no goroutines.
// The current dashboard protocol has no stale flag: readings older than the
// limit fail closed as absent instead of retaining fresh-looking values.
type CachedBatteryProvider struct {
	provider BatteryProvider
	now      func() time.Time

	mu          sync.Mutex // protects the fields below
	last        BatteryStatus
	sampledAt   time.Time
	nextRead    uint64
	appliedRead uint64
	haveSample  bool
}

// NewCachedBatteryProvider wraps a read-only provider. now may be nil to use
// time.Now; an injected clock must be safe for concurrent use.
func NewCachedBatteryProvider(provider BatteryProvider, now func() time.Time) *CachedBatteryProvider {
	if now == nil {
		now = time.Now
	}

	return &CachedBatteryProvider{provider: provider, now: now}
}

// GetBatteryStatus attempts a fresh read without holding the cache mutex over
// I/O. Concurrent older reads cannot replace a newer completed result.
func (p *CachedBatteryProvider) GetBatteryStatus() (*BatteryStatus, error) {
	if p.provider == nil {
		return &BatteryStatus{ChargePercent: -1}, nil
	}

	p.mu.Lock()
	p.nextRead++
	readID := p.nextRead
	p.mu.Unlock()

	st, err := p.provider.GetBatteryStatus()
	now := p.now()

	if err == nil && st == nil {
		err = fmt.Errorf("battery provider returned a nil status")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if readID > p.appliedRead {
		p.appliedRead = readID
		if err == nil {
			p.last = *st
			p.sampledAt = now
			p.haveSample = st.Present
		}
	}

	if p.haveSample {
		age := now.Sub(p.sampledAt)
		if age >= 0 && age <= BatteryFreshnessLimit {
			copyStatus := p.last

			return &copyStatus, nil
		}
	}

	if err != nil {
		return nil, fmt.Errorf("read battery monitor without a fresh sample: %w", err)
	}

	return &BatteryStatus{ChargePercent: -1}, nil
}

package system_test

import (
	"sync"
	"time"

	"github.com/openmanet/openmanetd/internal/system"
)

type fakeBatteryProvider struct {
	mu     sync.Mutex // protects the fields below
	status system.BatteryStatus
	err    error
	block  <-chan struct{}
	read   chan<- struct{}
}

func (p *fakeBatteryProvider) GetBatteryStatus() (*system.BatteryStatus, error) {
	p.mu.Lock()
	st := p.status
	err, block, read := p.err, p.block, p.read
	p.mu.Unlock()

	if read != nil {
		read <- struct{}{}
	}

	if block != nil {
		<-block
	}

	return &st, err
}

func (p *fakeBatteryProvider) UnblockNext(st system.BatteryStatus) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.status = st
	p.block, p.read = nil, nil
}

func (p *fakeBatteryProvider) Set(st system.BatteryStatus, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.status, p.err = st, err
}

type fakeBatteryClock struct {
	mu  sync.Mutex // protects now
	now time.Time
}

func (c *fakeBatteryClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeBatteryClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

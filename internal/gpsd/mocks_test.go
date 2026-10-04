package gpsd

import (
	"net"
	"sync"
)

type fakeClosingConn struct {
	net.Conn
	closeErr error

	mu         sync.Mutex // protects the fields below
	closeCount int
}

func (c *fakeClosingConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closeCount++

	return c.closeErr
}

func (c *fakeClosingConn) CloseCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.closeCount
}

package tiles_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// jpegTile returns n bytes that sniff as image/jpeg.
func jpegTile(n int) []byte {
	b := bytes.Repeat([]byte{0x42}, max(n, 16))
	copy(b, []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F'})

	return b
}

// fakeClock is a settable clock.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// fakeTileSource is an upstream or peer tile server.
type fakeTileSource struct {
	mu       sync.Mutex
	calls    int
	paths    []string
	peerHdrs []string
	status   int
	body     []byte
	size     int
}

func (f *fakeTileSource) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls++
	f.paths = append(f.paths, r.URL.Path)
	f.peerHdrs = append(f.peerHdrs, r.Header.Get("X-Openmanet-Tile-Peer"))
	status, body, size := f.status, f.body, f.size
	f.mu.Unlock()

	if status == 0 {
		status = http.StatusOK
	}

	if body == nil {
		body = jpegTile(size)
	}

	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (f *fakeTileSource) set(status int, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.status, f.body = status, body
}

func (f *fakeTileSource) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls
}

func (f *fakeTileSource) Paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.paths...)
}

func (f *fakeTileSource) PeerHeaders() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.peerHdrs...)
}

func newFakeTileSource(t *testing.T) (*fakeTileSource, *httptest.Server) {
	t.Helper()

	f := &fakeTileSource{size: 1024}
	ts := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(ts.Close)

	return f, ts
}

// Package tiles serves ATAK map tiles from every node (GHO-95).
//
// A phone that went through onboarding has map sources pointing at
// http://<node>:8080/tiles/<layer>/{z}/{x}/{y}. The node answers from
// its disk cache. On a miss it asks its peers (a mast that pre-seeded
// the area) and then the internet, stores the tile, and serves it. With
// no peers and no internet it serves only what it already holds, so a
// pre-seeded area keeps working fully offline.
//
// The cache is bounded by size and evicts least recently used tiles
// first, and it always leaves a free-space floor on its filesystem.
package tiles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/rs/zerolog"
	"golang.org/x/sys/unix"
)

const (
	// PathPrefix is where tiles are served.
	PathPrefix = "/tiles/"
	// PeerHeader marks a request from another node. A peer answers it
	// from its cache or the internet but never asks its own peers, so
	// two nodes listing each other cannot loop.
	PeerHeader = "X-Openmanet-Tile-Peer"
	// maxTileBytes caps one tile; real tiles are 5 to 60 KB.
	maxTileBytes = 1 << 20
	// fetchSlots bounds concurrent on-demand fetches. ATAK asks for a
	// screenful of tiles at once; more than a few parallel fetches gain
	// nothing on a mesh link and cost memory.
	fetchSlots = 4
	// userAgent is sent upstream. It names the software, not the user.
	userAgent = "OpenMANET-tiles/1"
	// cacheMaxAge tells ATAK how long to keep a tile it was served.
	cacheMaxAge = "public, max-age=86400"
)

// errNotFound means a source answered that it has no such tile.
var errNotFound = errors.New("tile not found")

// Options configures a Cache. Zero values take the config defaults.
type Options struct {
	Log    zerolog.Logger
	Client *http.Client
	// Now and FreeFunc are test seams; nil uses the real clock and
	// statfs.
	Now      func() time.Time
	FreeFunc func(dir string) (int64, error)
	Config   config.TilesConfig
}

// Cache is the node's tile server and disk cache. It is safe for
// concurrent use.
type Cache struct {
	log      zerolog.Logger
	inflight map[flightKey]*flight
	skipTill map[string]time.Time // source -> skip until
	lastOK   map[string]time.Time // source -> last success
	seed     *seedJob
	client   *http.Client
	now      func() time.Time
	freeFunc func(dir string) (int64, error)
	evictCh  chan struct{}
	slots    chan struct{}
	layers   map[string]config.TileLayer
	root     *os.Root
	freeAt   time.Time
	dir      string
	peers    []string
	seeds    []config.TileSeedArea
	stats    Counters

	maxBytes     int64
	minFree      int64
	fetchTimeout time.Duration
	backoff      time.Duration
	seedWorkers  int
	tmpSeq       atomic.Uint64

	// mu protects the fields below and inflight, skipTill, lastOK, seed,
	// freeAt and stats above (grouped there for field alignment).
	mu      sync.Mutex
	free    int64
	used    int64
	evicted int64
	scanned bool
	closed  bool
}

// Counters are cumulative since start.
type Counters struct {
	Hits         int64 `json:"hits"`
	Misses       int64 `json:"misses"`
	FromPeer     int64 `json:"fromPeer"`
	FromInternet int64 `json:"fromInternet"`
	NotCached    int64 `json:"notCached"`
}

// New returns a Cache over cfg.Dir, creating the directory.
func New(opts Options) (*Cache, error) {
	cfg := opts.Config

	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("tile cache: mkdir %s: %w", cfg.Dir, err)
	}

	root, err := os.OpenRoot(cfg.Dir)
	if err != nil {
		return nil, fmt.Errorf("tile cache: open %s: %w", cfg.Dir, err)
	}

	c := &Cache{
		log:          opts.Log,
		client:       opts.Client,
		now:          opts.Now,
		freeFunc:     opts.FreeFunc,
		evictCh:      make(chan struct{}, 1), // one pending pass covers any number of requests
		slots:        make(chan struct{}, fetchSlots),
		layers:       make(map[string]config.TileLayer, len(cfg.Layers)),
		root:         root,
		dir:          cfg.Dir,
		peers:        cfg.Peers,
		seeds:        cfg.Seed,
		maxBytes:     cfg.MaxBytes,
		minFree:      cfg.MinFreeBytes,
		fetchTimeout: cfg.FetchTimeout,
		backoff:      cfg.OfflineBackoff,
		seedWorkers:  max(cfg.SeedConcurrency, 1),
		inflight:     make(map[flightKey]*flight),
		skipTill:     make(map[string]time.Time),
		lastOK:       make(map[string]time.Time),
	}

	if c.client == nil {
		c.client = &http.Client{}
	}

	if c.now == nil {
		c.now = time.Now
	}

	if c.freeFunc == nil {
		c.freeFunc = statfsFree
	}

	if c.fetchTimeout <= 0 {
		c.fetchTimeout = config.DefaultTilesFetchTimeout
	}

	if c.backoff <= 0 {
		c.backoff = config.DefaultTilesOfflineBackoff
	}

	for _, l := range cfg.Layers {
		c.layers[l.Layer] = l
	}

	return c, nil
}

func statfsFree(dir string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", dir, err)
	}

	return int64(st.Bavail) * int64(st.Bsize), nil //nolint:unconvert,gosec // field widths differ per arch; sizes fit int64
}

// Start measures the cache, runs the evictor, and seeds the configured
// areas. Everything stops when ctx is canceled.
func (c *Cache) Start(ctx context.Context) {
	go c.evictLoop(ctx)

	c.signalEvict() // first pass measures the cache and clears leftovers

	if len(c.seeds) > 0 {
		if err := c.startSeed(ctx, c.seeds, "config"); err != nil {
			c.log.Warn().Err(err).Msg("tile seed from config not started")
		}
	}

	go func() {
		<-ctx.Done()
		c.CancelSeed()

		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
	}()
}

func (c *Cache) evictLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.evictCh:
			if err := c.evictPass(); err != nil {
				c.log.Warn().Err(err).Msg("tile cache eviction failed")
			}
		}
	}
}

// Layer returns the served layer with this name.
func (c *Cache) Layer(name string) (config.TileLayer, bool) {
	l, ok := c.layers[name]

	return l, ok
}

// ServeHTTP answers GET /tiles/<layer>/<z>/<x>/<y>[.ext].
func (c *Cache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	k, layer, ok := c.parse(r.URL.Path)
	if !ok {
		http.NotFound(w, r)

		return
	}

	rel := k.rel(layer.TileType)

	if f, err := c.root.Open(rel); err == nil {
		c.serveFile(w, r, f, rel)

		return
	}

	c.count(func(s *Counters) { s.Misses++ })

	data, err := c.fetch(r.Context(), k, layer, r.Header.Get(PeerHeader) != "")
	if err != nil {
		if r.Context().Err() == nil {
			c.log.Debug().Err(err).Str("tile", r.URL.Path).Msg("tile unavailable")
		}

		http.NotFound(w, r)

		return
	}

	w.Header().Set("Cache-Control", cacheMaxAge)
	http.ServeContent(w, r, "", c.now(), bytes.NewReader(data))
}

func (c *Cache) serveFile(w http.ResponseWriter, r *http.Request, f *os.File, rel string) {
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)

		return
	}

	c.count(func(s *Counters) { s.Hits++ })
	c.touch(rel, fi.ModTime())

	w.Header().Set("Cache-Control", cacheMaxAge)
	// ServeContent sniffs the image type from the bytes.
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// parse validates /tiles/<layer>/<z>/<x>/<y>[.ext] against the layer's
// zoom range and the tile grid.
func (c *Cache) parse(p string) (tileKey, config.TileLayer, bool) {
	rest, ok := strings.CutPrefix(p, PathPrefix)
	if !ok {
		return tileKey{}, config.TileLayer{}, false
	}

	parts := strings.Split(rest, "/")
	if len(parts) != 4 {
		return tileKey{}, config.TileLayer{}, false
	}

	layer, ok := c.layers[parts[0]]
	if !ok {
		return tileKey{}, config.TileLayer{}, false
	}

	yStr := parts[3]
	if i := strings.IndexByte(yStr, '.'); i >= 0 {
		yStr = yStr[:i]
	}

	z, errZ := strconv.Atoi(parts[1])
	x, errX := strconv.Atoi(parts[2])
	y, errY := strconv.Atoi(yStr)

	if errZ != nil || errX != nil || errY != nil {
		return tileKey{}, config.TileLayer{}, false
	}

	if z < layer.MinZoom || z > layer.MaxZoom || x < 0 || y < 0 || x >= 1<<z || y >= 1<<z {
		return tileKey{}, config.TileLayer{}, false
	}

	return tileKey{layer: layer.Layer, z: z, x: x, y: y}, layer, true
}

func (c *Cache) count(f func(*Counters)) {
	c.mu.Lock()
	f(&c.stats)
	c.mu.Unlock()
}

// flightKey dedupes concurrent fetches of one tile. A peer request skips
// peers, so it is a different flight.
type flightKey struct {
	tileKey

	peerReq bool
}

type flight struct {
	err  error
	done chan struct{}
	data []byte
}

// fetch gets a missing tile from peers, then the internet, stores it,
// and returns its bytes. Concurrent requests for one tile share a fetch.
func (c *Cache) fetch(ctx context.Context, k tileKey, layer config.TileLayer, peerReq bool) ([]byte, error) {
	fk := flightKey{tileKey: k, peerReq: peerReq}

	c.mu.Lock()
	if fl, ok := c.inflight[fk]; ok {
		c.mu.Unlock()

		return waitFlight(ctx, fl)
	}

	fl := &flight{done: make(chan struct{})}
	c.inflight[fk] = fl
	c.mu.Unlock()

	// The fetch outlives the first requester so the others still get it;
	// fetchTimeout per source bounds it.
	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.inflight, fk)
			c.mu.Unlock()
			close(fl.done)
		}()

		select {
		case c.slots <- struct{}{}:
		case <-time.After(c.fetchTimeout):
			fl.err = errors.New("tile fetch queue full")

			return
		}

		defer func() { <-c.slots }()

		fl.data, fl.err = c.fetchAndStore(context.WithoutCancel(ctx), k, layer, !peerReq)
	}()

	return waitFlight(ctx, fl)
}

func waitFlight(ctx context.Context, fl *flight) ([]byte, error) {
	select {
	case <-fl.done:
		return fl.data, fl.err
	case <-ctx.Done():
		return nil, fmt.Errorf("tile fetch: %w", ctx.Err())
	}
}

// fetchAndStore tries each source in order and stores the first tile
// found. A tile that cannot be stored (disk full) is still returned.
func (c *Cache) fetchAndStore(ctx context.Context, k tileKey, layer config.TileLayer, usePeers bool) ([]byte, error) {
	data, from, err := c.fetchSources(ctx, k, layer, usePeers)
	if err != nil {
		return nil, err
	}

	if werr := c.writeTile(k, layer.TileType, data); werr != nil {
		c.count(func(s *Counters) { s.NotCached++ })
		c.log.Debug().Err(werr).Msg("tile served but not cached")
	}

	c.count(func(s *Counters) {
		if from == sourceInternet {
			s.FromInternet++
		} else {
			s.FromPeer++
		}
	})

	return data, nil
}

const sourceInternet = "internet"

// fetchSources asks peers (unless usePeers is false) and then the
// layer's upstream. It returns the tile and which source had it. The
// error is errNotFound only when every source answered that it has no
// such tile; any real failure makes it a failure.
func (c *Cache) fetchSources(ctx context.Context, k tileKey, layer config.TileLayer, usePeers bool) ([]byte, string, error) {
	var errs []error

	if usePeers {
		for _, p := range c.peers {
			u := p + PathPrefix + k.layer + "/" + strconv.Itoa(k.z) + "/" + strconv.Itoa(k.x) + "/" + strconv.Itoa(k.y)

			data, err := c.get(ctx, p, u, true)
			if err == nil {
				return data, p, nil
			}

			if !errors.Is(err, errNotFound) {
				errs = append(errs, err)
			}
		}
	}

	if layer.Upstream != "" {
		u := strings.NewReplacer(
			"{z}", strconv.Itoa(k.z),
			"{x}", strconv.Itoa(k.x),
			"{y}", strconv.Itoa(k.y),
		).Replace(layer.Upstream)

		data, err := c.get(ctx, sourceInternet, u, false)
		if err == nil {
			return data, sourceInternet, nil
		}

		if !errors.Is(err, errNotFound) {
			errs = append(errs, err)
		}
	}

	if len(errs) == 0 {
		return nil, "", errNotFound
	}

	return nil, "", errors.Join(errs...)
}

// get fetches one tile from one source, skipping a source that failed
// recently. Only real failures (network, 5xx, 429) start a backoff; a
// "no such tile" answer does not.
func (c *Cache) get(ctx context.Context, source, url string, peer bool) ([]byte, error) {
	now := c.now()

	c.mu.Lock()
	skip := now.Before(c.skipTill[source])
	c.mu.Unlock()

	if skip {
		return nil, fmt.Errorf("%s: offline, retry later", source)
	}

	data, err := c.download(ctx, url, peer)

	switch {
	case err == nil:
		c.mu.Lock()
		delete(c.skipTill, source)
		c.lastOK[source] = c.now()
		c.mu.Unlock()

		return data, nil
	case errors.Is(err, errNotFound):
		c.mu.Lock()
		c.lastOK[source] = c.now()
		c.mu.Unlock()
	case ctx.Err() == nil:
		c.mu.Lock()
		c.skipTill[source] = c.now().Add(c.backoff)
		c.mu.Unlock()
	}

	return nil, fmt.Errorf("%s: %w", source, err)
}

func (c *Cache) download(ctx context.Context, url string, peer bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("User-Agent", userAgent)

	if peer {
		req.Header.Set(PeerHeader, "1")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusNoContent, http.StatusGone:
		return nil, errNotFound
	default:
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	if len(data) > maxTileBytes {
		return nil, fmt.Errorf("tile larger than %d bytes", maxTileBytes)
	}

	// Never cache an HTML error page served with 200.
	if !strings.HasPrefix(http.DetectContentType(data), "image/") {
		return nil, errNotFound
	}

	return data, nil
}

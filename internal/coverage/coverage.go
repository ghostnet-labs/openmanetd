// Package coverage keeps a node's own coverage map: a grid of cells,
// each recording how well the node heard the mesh while it stood there.
// The ATAK feed draws the cells as a heatmap, and every node's cells
// together show where the mesh reaches and where it doesn't.
//
// A Grid is safe for concurrent use. It is bounded: past its cell cap the
// least recently visited cell is dropped. Persist it with a Recorder.
package coverage

import (
	"math"
	"slices"
	"sync"
	"time"
)

// metersPerDegLat is the length of one degree of latitude.
const metersPerDegLat = 111_320.0

// minCos keeps longitude cells finite near the poles.
const minCos = 0.01

// signalAlpha weights a new sample in a cell's running signal average.
const signalAlpha = 0.3

// Key names one grid cell. Row counts latitude bands from the equator;
// Col counts longitude steps within that band.
type Key struct {
	Row int32
	Col int32
}

// Sample is one reading of how well this node hears the mesh.
type Sample struct {
	// Links is the number of mesh stations heard directly.
	Links int
	// BestSignalDBm is the strongest of them; ignored when Links is 0.
	BestSignalDBm int
}

// Cell is one grid cell's record.
type Cell struct {
	FirstSeen time.Time
	LastSeen  time.Time
	// SignalDBm is a running average of the best signal over the samples
	// that heard at least one station. Meaningless when every sample had
	// no link.
	SignalDBm     float64
	Samples       int
	NoLinkSamples int
	MaxLinks      int
	Key           Key
}

// HasLink is true when at least one sample in the cell heard the mesh.
func (c *Cell) HasLink() bool { return c.Samples > c.NoLinkSamples }

// NoLinkPct is the share of samples that heard no mesh station.
func (c *Cell) NoLinkPct() float64 {
	if c.Samples == 0 {
		return 0
	}

	return float64(c.NoLinkSamples) * 100 / float64(c.Samples)
}

// Grid is a bounded coverage map.
type Grid struct {
	// cells holds every cell; order lists their keys for the publish
	// round robin. mu guards every field except cellM and max, which never
	// change; the fields sit above mu only to keep pointers together.
	cells map[Key]*Cell
	// queued dedupes changed.
	queued map[Key]struct{}
	// dirty and removed are changes since the last persist.
	dirty   map[Key]struct{}
	removed map[Key]struct{}
	order   []Key
	// changed are cells updated since the last publish, in update order.
	changed []Key
	mu      sync.Mutex
	cellM   float64
	max     int
	cursor  int
}

// New returns an empty grid of cellM-meter cells holding at most max.
func New(cellM float64, maxCells int) *Grid {
	return &Grid{
		cells:   make(map[Key]*Cell, maxCells),
		order:   make([]Key, 0, maxCells),
		queued:  make(map[Key]struct{}, 16),
		dirty:   make(map[Key]struct{}, 16),
		removed: make(map[Key]struct{}, 4),
		cellM:   cellM,
		max:     maxCells,
	}
}

// CellMeters is the grid's cell size.
func (g *Grid) CellMeters() float64 { return g.cellM }

// Len is the number of cells held.
func (g *Grid) Len() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	return len(g.cells)
}

// KeyFor returns the cell that holds a point.
func KeyFor(lat, lon, cellM float64) Key {
	latStep := cellM / metersPerDegLat
	row := math.Floor(lat / latStep)
	col := math.Floor(lon / lonStep(row, latStep))

	return Key{Row: int32(row), Col: int32(col)}
}

// Bounds returns a cell's south, west, north and east edges in degrees.
func Bounds(k Key, cellM float64) (south, west, north, east float64) {
	latStep := cellM / metersPerDegLat
	step := lonStep(float64(k.Row), latStep)

	south = float64(k.Row) * latStep
	north = south + latStep
	west = float64(k.Col) * step
	east = west + step

	return south, west, north, east
}

// lonStep is the longitude width of cells in a row, so cells stay about
// square. It depends only on the row, so every node uses the same grid.
func lonStep(row, latStep float64) float64 {
	centerLat := (row + 0.5) * latStep * math.Pi / 180

	return latStep / math.Max(math.Cos(centerLat), minCos)
}

// Record adds one sample taken at a point and returns its cell.
func (g *Grid) Record(now time.Time, lat, lon float64, s Sample) Key {
	k := KeyFor(lat, lon, g.cellM)

	g.mu.Lock()
	defer g.mu.Unlock()

	c, ok := g.cells[k]
	if !ok {
		if len(g.cells) >= g.max {
			g.evictOldest()
		}

		c = &Cell{Key: k, FirstSeen: now}
		g.cells[k] = c
		g.order = append(g.order, k)
		delete(g.removed, k)
	}

	c.Samples++
	c.LastSeen = now

	if s.Links <= 0 {
		c.NoLinkSamples++
	} else {
		sig := float64(s.BestSignalDBm)
		if c.Samples-c.NoLinkSamples == 1 {
			c.SignalDBm = sig
		} else {
			c.SignalDBm += signalAlpha * (sig - c.SignalDBm)
		}

		c.MaxLinks = max(c.MaxLinks, s.Links)
	}

	g.markChanged(k)

	return k
}

// markChanged queues a cell for publishing and persisting. Caller holds mu.
func (g *Grid) markChanged(k Key) {
	g.dirty[k] = struct{}{}

	if _, ok := g.queued[k]; !ok {
		g.queued[k] = struct{}{}
		g.changed = append(g.changed, k)
	}
}

// evictOldest drops the least recently visited cell. Caller holds mu.
func (g *Grid) evictOldest() {
	var (
		oldest Key
		at     time.Time
		found  bool
	)

	for k, c := range g.cells {
		if !found || c.LastSeen.Before(at) {
			oldest, at, found = k, c.LastSeen, true
		}
	}

	if !found {
		return
	}

	delete(g.cells, oldest)
	delete(g.dirty, oldest)
	g.removed[oldest] = struct{}{}

	if i := slices.Index(g.order, oldest); i >= 0 {
		g.order = slices.Delete(g.order, i, i+1)
		if g.cursor > i {
			g.cursor--
		}
	}

	if _, ok := g.queued[oldest]; ok {
		delete(g.queued, oldest)
		g.changed = slices.DeleteFunc(g.changed, func(k Key) bool { return k == oldest })
	}
}

// NextToPublish appends up to n cells to dst: changed cells first, then
// the rest in round robin, so a phone that joins late gets every cell
// within one full cycle.
func (g *Grid) NextToPublish(dst []Cell, n int) []Cell {
	g.mu.Lock()
	defer g.mu.Unlock()

	if n <= 0 || len(g.cells) == 0 {
		return dst
	}

	n = min(n, len(g.cells))
	start := len(dst)

	for len(g.changed) > 0 && len(dst)-start < n {
		k := g.changed[0]
		g.changed = g.changed[1:]
		delete(g.queued, k)

		if c, ok := g.cells[k]; ok {
			dst = append(dst, *c)
		}
	}

	if len(g.changed) == 0 {
		// Drop the drained prefix's backing array.
		g.changed = nil
	}

	for i := 0; len(dst)-start < n && i < len(g.order); i++ {
		if g.cursor >= len(g.order) {
			g.cursor = 0
		}

		k := g.order[g.cursor]
		g.cursor++

		if slices.ContainsFunc(dst[start:], func(c Cell) bool { return c.Key == k }) {
			continue
		}

		dst = append(dst, *g.cells[k])
	}

	return dst
}

// Cells appends every cell to dst, most recently visited first.
func (g *Grid) Cells(dst []Cell) []Cell {
	g.mu.Lock()

	for _, c := range g.cells {
		dst = append(dst, *c)
	}

	g.mu.Unlock()

	slices.SortFunc(dst, func(a, b Cell) int { return b.LastSeen.Compare(a.LastSeen) })

	return dst
}

// takeChanges returns the cells to write and the keys to delete since the
// last call, and clears them.
func (g *Grid) takeChanges() (upserts []Cell, deletes []Key) {
	g.mu.Lock()
	defer g.mu.Unlock()

	upserts = make([]Cell, 0, len(g.dirty))
	for k := range g.dirty {
		if c, ok := g.cells[k]; ok {
			upserts = append(upserts, *c)
		}
	}

	deletes = make([]Key, 0, len(g.removed))
	for k := range g.removed {
		deletes = append(deletes, k)
	}

	clear(g.dirty)
	clear(g.removed)

	return upserts, deletes
}

// restore puts back changes a failed persist could not write, unless the
// cell changed again or came back meanwhile.
func (g *Grid) restore(upserts []Cell, deletes []Key) {
	g.mu.Lock()
	defer g.mu.Unlock()

	for i := range upserts {
		if _, ok := g.cells[upserts[i].Key]; ok {
			g.dirty[upserts[i].Key] = struct{}{}
		}
	}

	for _, k := range deletes {
		if _, ok := g.cells[k]; !ok {
			g.removed[k] = struct{}{}
		}
	}
}

// load replaces the grid's contents with stored cells, newest first, up to
// the cap. Loaded cells are queued for publishing but not for persisting.
func (g *Grid) load(cells []Cell) {
	g.mu.Lock()
	defer g.mu.Unlock()

	clear(g.cells)
	clear(g.queued)
	clear(g.dirty)
	clear(g.removed)
	g.order = g.order[:0]
	g.changed = g.changed[:0]
	g.cursor = 0

	for i := range cells {
		if len(g.cells) >= g.max {
			break
		}

		c := cells[i]
		if _, dup := g.cells[c.Key]; dup {
			continue
		}

		g.cells[c.Key] = &c
		g.order = append(g.order, c.Key)
	}
}

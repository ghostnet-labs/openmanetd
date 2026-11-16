package tiles

import (
	"container/heap"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// tmpPrefix marks a tile still being written; a crash can leave one
	// behind, and the next scan removes it.
	tmpPrefix = ".tmp-"
	// touchAfter limits how often a cache hit rewrites a tile's mtime
	// (the LRU clock), so serving a map does not wear the flash.
	touchAfter = time.Hour
	// evictBatch is how many of the oldest tiles one directory walk
	// remembers. It bounds eviction memory to a few hundred KB however
	// big the cache is; a larger excess takes more walks.
	evictBatch = 2048
	// freeCheckEvery limits statfs calls to one per interval.
	freeCheckEvery = 5 * time.Second
)

// errDiskFull means the tile was not written because the cache's
// filesystem is below its free-space floor.
var errDiskFull = errors.New("tile cache: filesystem below its free-space floor")

// tileKey names one tile of one layer.
type tileKey struct {
	layer   string
	z, x, y int
}

// rel returns the tile's path inside the cache root:
// <layer>/<z>/<x>/<y>.<ext>. Every file operation goes through an
// os.Root, so no path can leave the cache directory.
func (k tileKey) rel(ext string) string {
	return filepath.Join(k.layer, strconv.Itoa(k.z), strconv.Itoa(k.x), strconv.Itoa(k.y)+"."+ext)
}

// freeBytes returns the free space on dir's filesystem, cached for
// freeCheckEvery.
func (c *Cache) freeBytes() (int64, error) {
	now := c.now()

	c.mu.Lock()
	if !c.freeAt.IsZero() && now.Sub(c.freeAt) < freeCheckEvery {
		free := c.free
		c.mu.Unlock()

		return free, nil
	}
	c.mu.Unlock()

	free, err := c.freeFunc(c.dir)
	if err != nil {
		return 0, err
	}

	c.mu.Lock()
	c.free = free
	c.freeAt = now
	c.mu.Unlock()

	return free, nil
}

// writeTile stores data for k atomically (temp file, then rename) and
// accounts for its size. It refuses when the filesystem is below the
// free-space floor.
func (c *Cache) writeTile(k tileKey, ext string, data []byte) error {
	free, err := c.freeBytes()
	if err != nil {
		return fmt.Errorf("tile cache: statfs: %w", err)
	}

	if free-int64(len(data)) < c.minFree {
		c.signalEvict()

		return errDiskFull
	}

	dst := k.rel(ext)
	if mkErr := c.root.MkdirAll(filepath.Dir(dst), 0o755); mkErr != nil {
		return fmt.Errorf("tile cache: mkdir: %w", mkErr)
	}

	var old int64
	if fi, statErr := c.root.Stat(dst); statErr == nil {
		old = fi.Size()
	}

	tmp := filepath.Join(filepath.Dir(dst), tmpPrefix+strconv.FormatUint(c.tmpSeq.Add(1), 36))

	f, err := c.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("tile cache: create: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = c.root.Remove(tmp) // best effort; the next scan removes leftovers

		return fmt.Errorf("tile cache: write: %w", err)
	}

	if err := f.Close(); err != nil {
		_ = c.root.Remove(tmp) // best effort; the next scan removes leftovers

		return fmt.Errorf("tile cache: close: %w", err)
	}

	if err := c.root.Rename(tmp, dst); err != nil {
		_ = c.root.Remove(tmp) // best effort; the next scan removes leftovers

		return fmt.Errorf("tile cache: rename: %w", err)
	}

	c.mu.Lock()
	c.used += int64(len(data)) - old
	c.free -= int64(len(data)) - old
	over := c.used > c.maxBytes
	c.mu.Unlock()

	if over {
		c.signalEvict()
	}

	return nil
}

// touch moves a tile to the front of the LRU order, at most once per
// touchAfter.
func (c *Cache) touch(rel string, mod time.Time) {
	now := c.now()
	if now.Sub(mod) < touchAfter {
		return
	}

	// A failed touch only makes the tile look older to eviction.
	_ = c.root.Chtimes(rel, now, now)
}

// signalEvict asks the evictor for a pass without blocking; a pass
// already requested covers this one.
func (c *Cache) signalEvict() {
	select {
	case c.evictCh <- struct{}{}:
	default:
	}
}

// fileAge is one tile in the eviction heap.
type fileAge struct {
	mod  time.Time
	path string // relative to the cache root
	size int64
}

// newestFirst is a max-heap on mtime: the root is the newest of the
// remembered tiles, so pushing past evictBatch drops the newest and the
// heap keeps the oldest.
type newestFirst []fileAge

func (h newestFirst) Len() int           { return len(h) }
func (h newestFirst) Less(i, j int) bool { return h[i].mod.After(h[j].mod) }
func (h newestFirst) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *newestFirst) Push(x any)        { *h = append(*h, x.(fileAge)) } //nolint:forcetypeassert // heap only holds fileAge
func (h *newestFirst) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]

	return x
}

// walkResult is what one directory walk learned.
type walkResult struct {
	oldest newestFirst
	total  int64
}

// walk sums every tile's size, removes leftover temp files, and keeps
// the evictBatch oldest tiles.
func (c *Cache) walk() (walkResult, error) {
	res := walkResult{oldest: make(newestFirst, 0, evictBatch+1)}

	err := fs.WalkDir(c.root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A tile or directory removed mid-walk is not an error.
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}

			return err
		}

		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // removed since listed
		}

		if strings.HasPrefix(d.Name(), tmpPrefix) {
			// An in-flight write is younger than a minute; anything
			// older is a crash leftover.
			if c.now().Sub(info.ModTime()) > time.Minute {
				_ = c.root.Remove(path) // best effort cleanup
			}

			return nil
		}

		res.total += info.Size()
		heap.Push(&res.oldest, fileAge{mod: info.ModTime(), path: path, size: info.Size()})

		if res.oldest.Len() > evictBatch {
			heap.Pop(&res.oldest)
		}

		return nil
	})
	if err != nil {
		return res, fmt.Errorf("tile cache: walk: %w", err)
	}

	return res, nil
}

// evictTarget reports whether the cache must shrink and to what size:
// it must when it is over MaxBytes or the filesystem is below its free
// floor, and then shrinks to 90% of MaxBytes, lower if free space needs.
func (c *Cache) evictTarget(total int64) (int64, bool) {
	target := c.maxBytes / 10 * 9
	need := total > c.maxBytes

	if free, err := c.freeBytes(); err == nil && free < c.minFree {
		target = min(target, total-(c.minFree-free))
		need = true
	}

	return max(target, 0), need && total > max(target, 0)
}

// evictPass measures the cache and deletes the oldest-used tiles until
// it is under its target. It walks again while a batch was not enough.
func (c *Cache) evictPass() error {
	for {
		res, err := c.walk()
		if err != nil {
			return err
		}

		total := res.total

		target, need := c.evictTarget(total)
		if !need {
			c.setUsed(total)

			return nil
		}

		batch := []fileAge(res.oldest)
		slices.SortFunc(batch, func(a, b fileAge) int { return a.mod.Compare(b.mod) })

		removed := 0

		for _, f := range batch {
			if total <= target {
				break
			}

			if err := c.root.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				continue
			}

			total -= f.size
			removed++
		}

		c.setUsed(total)

		c.mu.Lock()
		c.evicted += int64(removed)
		c.freeAt = time.Time{} // re-read free space after deleting
		c.mu.Unlock()

		if total <= target || removed == 0 {
			return nil
		}
	}
}

func (c *Cache) setUsed(total int64) {
	c.mu.Lock()
	c.used = total
	c.scanned = true
	c.mu.Unlock()
}

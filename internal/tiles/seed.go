package tiles

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/openmanet/openmanetd/internal/config"
)

const (
	// maxSeedTiles caps one seed job. A 25 km radius to zoom 16 is
	// about 15,600 tiles per layer.
	maxSeedTiles = 250_000
	// estTileBytes is the planning size of one tile (USGS tiles average
	// 15 to 25 KB).
	estTileBytes = 20 << 10
	// seedGiveUpAfter stops a job after this many failures in a row: the
	// sources are unreachable and every further tile would fail too.
	seedGiveUpAfter = 50
)

// Seed errors.
var (
	ErrSeedRunning  = errors.New("a map download is already running")
	ErrSeedTooLarge = errors.New("area is too large for the tile cache")
	ErrSeedBadArea  = errors.New("invalid map area")
	ErrClosed       = errors.New("tile cache stopped")
)

// SeedStatus is the progress of the current or last seed job.
type SeedStatus struct {
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Source     string     `json:"source"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	Areas      []string   `json:"areas"`
	Total      int64      `json:"total"`
	Done       int64      `json:"done"`
	Cached     int64      `json:"cached"`
	Downloaded int64      `json:"downloaded"`
	Failed     int64      `json:"failed"`
	Bytes      int64      `json:"bytes"`
	Running    bool       `json:"running"`
}

type seedJob struct {
	cancel context.CancelFunc
	status SeedStatus
}

// seedTile is one tile of one layer to seed.
type seedTile struct {
	layer config.TileLayer
	k     tileKey
}

// plan is the tile ranges of one area for one layer.
type plan struct {
	ranges []tileRange
	layer  config.TileLayer
}

// planAreas checks the areas and returns their tile ranges and count.
func (c *Cache) planAreas(areas []config.TileSeedArea) ([]plan, int64, error) {
	var (
		plans []plan
		total int64
	)

	for _, a := range areas {
		if err := config.ValidateTileSeedArea(a); err != nil {
			return nil, 0, fmt.Errorf("%w: %s: %w", ErrSeedBadArea, a.Name, err)
		}

		names := a.Layers
		if len(names) == 0 {
			names = make([]string, 0, len(c.layers))
			for n := range c.layers {
				names = append(names, n)
			}

			slices.Sort(names)
		}

		for _, n := range names {
			l, ok := c.layers[n]
			if !ok {
				return nil, 0, fmt.Errorf("%w: %s: unknown layer %q", ErrSeedBadArea, a.Name, n)
			}

			maxZ := l.MaxZoom
			if a.MaxZoom != 0 {
				maxZ = min(a.MaxZoom, l.MaxZoom)
			}

			minZ := max(a.MinZoom, l.MinZoom)
			if minZ > maxZ {
				continue
			}

			p := plan{layer: l, ranges: areaRanges(a.Lat, a.Lon, a.RadiusKm, minZ, maxZ)}
			for _, r := range p.ranges {
				total += r.count()
			}

			plans = append(plans, p)
		}
	}

	if total > maxSeedTiles {
		return nil, 0, fmt.Errorf("%w: %d tiles, the limit is %d; use a smaller radius or max zoom", ErrSeedTooLarge, total, maxSeedTiles)
	}

	if need := total * estTileBytes; need > c.maxBytes/10*9 {
		return nil, 0, fmt.Errorf("%w: about %d MB needed, the cache holds %d MB; use a smaller radius or max zoom, or raise tiles.maxBytes",
			ErrSeedTooLarge, need>>20, c.maxBytes>>20)
	}

	return plans, total, nil
}

// StartSeed downloads one area in the background. Tiles already cached
// are skipped.
func (c *Cache) StartSeed(ctx context.Context, area config.TileSeedArea) error {
	return c.startSeed(ctx, []config.TileSeedArea{area}, "manual")
}

func (c *Cache) startSeed(ctx context.Context, areas []config.TileSeedArea, source string) error {
	plans, total, err := c.planAreas(areas)
	if err != nil {
		return err
	}

	names := make([]string, 0, len(areas))
	for _, a := range areas {
		names = append(names, a.Name)
	}

	// The job belongs to the cache, not to the request that started it;
	// Start's shutdown and CancelSeed stop it.
	jobCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cancel()

		return ErrClosed
	}

	if c.seed != nil && c.seed.status.Running {
		c.mu.Unlock()
		cancel()

		return ErrSeedRunning
	}

	job := &seedJob{cancel: cancel, status: SeedStatus{
		Source:    source,
		Areas:     names,
		Total:     total,
		StartedAt: c.now(),
		Running:   true,
	}}
	c.seed = job
	c.mu.Unlock()

	c.log.Info().Strs("areas", names).Int64("tiles", total).Str("source", source).Msg("map download started")

	go c.runSeed(jobCtx, job, plans)

	return nil
}

// CancelSeed stops the running seed job, if any.
func (c *Cache) CancelSeed() {
	c.mu.Lock()
	job := c.seed
	c.mu.Unlock()

	if job != nil {
		job.cancel()
	}
}

func (c *Cache) runSeed(ctx context.Context, job *seedJob, plans []plan) {
	defer job.cancel()

	work := make(chan seedTile)

	var (
		wg       sync.WaitGroup
		failMu   sync.Mutex // protects failRun and stopErr
		failRun  int
		stopErr  error
		stopOnce sync.Once
	)

	stop := func(err error) {
		stopOnce.Do(func() {
			failMu.Lock()
			stopErr = err
			failMu.Unlock()
			job.cancel()
		})
	}

	for range c.seedWorkers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for t := range work {
				res, n, err := c.seedOne(ctx, t)

				failMu.Lock()

				if res == seedFailed {
					failRun++
				} else {
					failRun = 0
				}

				giveUp := failRun >= seedGiveUpAfter
				failMu.Unlock()

				c.recordSeed(job, res, n)

				switch {
				case errors.Is(err, errDiskFull):
					stop(errors.New("storage is full; free space or lower tiles.maxBytes"))
				case giveUp:
					stop(fmt.Errorf("%d tiles failed in a row; no peer or internet source answered (last: %w)", seedGiveUpAfter, err))
				}
			}
		}()
	}

	feedSeed(ctx, work, plans)
	close(work)
	wg.Wait()

	finished := c.now()

	failMu.Lock()
	err := stopErr
	failMu.Unlock()

	if err == nil && ctx.Err() != nil {
		err = errors.New("canceled")
	}

	c.mu.Lock()
	job.status.Running = false
	job.status.FinishedAt = &finished

	if err != nil {
		job.status.Error = err.Error()
	}

	st := job.status
	c.mu.Unlock()

	c.log.Info().Int64("downloaded", st.Downloaded).Int64("cached", st.Cached).Int64("failed", st.Failed).
		Str("error", st.Error).Msg("map download finished")
}

// feedSeed sends every planned tile until ctx is canceled.
func feedSeed(ctx context.Context, work chan<- seedTile, plans []plan) {
	for _, p := range plans {
		for _, r := range p.ranges {
			for x := r.minX; x <= r.maxX; x++ {
				for y := r.minY; y <= r.maxY; y++ {
					t := seedTile{k: tileKey{layer: p.layer.Layer, z: r.z, x: x, y: y}, layer: p.layer}

					select {
					case work <- t:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}

type seedResult int

const (
	seedCached seedResult = iota
	seedDownloaded
	seedFailed
	seedMissing
)

// seedOne makes sure one tile is cached. It returns the outcome, the
// bytes downloaded, and the error for a failure.
func (c *Cache) seedOne(ctx context.Context, t seedTile) (seedResult, int64, error) {
	if _, err := c.root.Stat(t.k.rel(t.layer.TileType)); err == nil {
		return seedCached, 0, nil
	}

	data, _, err := c.fetchSources(ctx, t.k, t.layer, true)

	switch {
	case errors.Is(err, errNotFound):
		// The source has no tile here (open sea, outside coverage).
		return seedMissing, 0, nil
	case err != nil:
		return seedFailed, 0, err
	}

	if err := c.writeTile(t.k, t.layer.TileType, data); err != nil {
		return seedFailed, 0, err
	}

	return seedDownloaded, int64(len(data)), nil
}

func (c *Cache) recordSeed(job *seedJob, res seedResult, n int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	job.status.Done++

	switch res {
	case seedCached:
		job.status.Cached++
	case seedDownloaded:
		job.status.Downloaded++
		job.status.Bytes += n
	case seedFailed:
		job.status.Failed++
	case seedMissing:
	}
}

package coverage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/openmanet/openmanetd/internal/database/models"
)

// DefaultFlushInterval is how often a Recorder writes changed cells. Cells
// change at most once per sample, so this bounds flash writes.
const DefaultFlushInterval = 5 * time.Minute

// finalFlushTimeout bounds the write on shutdown.
const finalFlushTimeout = 5 * time.Second

// Store is the database a Recorder persists to (*models.Queries).
type Store interface {
	ListCoverageCells(ctx context.Context, arg models.ListCoverageCellsParams) ([]models.CoverageCell, error)
	UpsertCoverageCell(ctx context.Context, arg models.UpsertCoverageCellParams) error
	DeleteCoverageCell(ctx context.Context, arg models.DeleteCoverageCellParams) error
	DeleteCoverageCellsOtherSizes(ctx context.Context, cellM int64) error
}

// Recorder loads a Grid from a Store and writes its changes back.
type Recorder struct {
	Grid  *Grid
	Store Store
	Log   zerolog.Logger
	// Interval defaults to DefaultFlushInterval.
	Interval time.Duration
}

// Load replaces the grid with the stored cells of its cell size and drops
// stored cells of any other size, left over from an earlier setting.
func (r *Recorder) Load(ctx context.Context) error {
	cellM := int64(r.Grid.CellMeters())

	if err := r.Store.DeleteCoverageCellsOtherSizes(ctx, cellM); err != nil {
		return fmt.Errorf("drop old coverage cells: %w", err)
	}

	rows, err := r.Store.ListCoverageCells(ctx, models.ListCoverageCellsParams{CellM: cellM, Limit: int64(r.Grid.max)})
	if err != nil {
		return fmt.Errorf("list coverage cells: %w", err)
	}

	cells := make([]Cell, 0, len(rows))
	for i := range rows {
		cells = append(cells, fromRow(&rows[i]))
	}

	r.Grid.load(cells)

	return nil
}

// Flush writes cells changed since the last flush. Changes that fail to
// write are kept for the next one.
func (r *Recorder) Flush(ctx context.Context) error {
	upserts, deletes := r.Grid.takeChanges()
	if len(upserts) == 0 && len(deletes) == 0 {
		return nil
	}

	cellM := int64(r.Grid.CellMeters())

	var errs []error

	failedUp := make([]Cell, 0, 1)

	for i := range upserts {
		if err := r.Store.UpsertCoverageCell(ctx, toRow(cellM, &upserts[i])); err != nil {
			errs = append(errs, err)
			failedUp = append(failedUp, upserts[i])
		}
	}

	failedDel := make([]Key, 0, 1)

	for _, k := range deletes {
		err := r.Store.DeleteCoverageCell(ctx, models.DeleteCoverageCellParams{
			CellM: cellM, CellRow: int64(k.Row), CellCol: int64(k.Col),
		})
		if err != nil {
			errs = append(errs, err)
			failedDel = append(failedDel, k)
		}
	}

	if len(errs) == 0 {
		return nil
	}

	r.Grid.restore(failedUp, failedDel)

	return fmt.Errorf("write coverage cells: %w", errors.Join(errs...))
}

// Run flushes every Interval until ctx is canceled, then flushes once
// more so a clean shutdown loses nothing.
func (r *Recorder) Run(ctx context.Context) {
	interval := r.Interval
	if interval <= 0 {
		interval = DefaultFlushInterval
	}

	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			final, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalFlushTimeout)
			if err := r.Flush(final); err != nil {
				r.Log.Warn().Err(err).Msg("Coverage map: final write failed")
			}

			cancel()

			return
		case <-t.C:
			if err := r.Flush(ctx); err != nil {
				r.Log.Warn().Err(err).Msg("Coverage map: write failed, will retry")
			}
		}
	}
}

func toRow(cellM int64, c *Cell) models.UpsertCoverageCellParams {
	return models.UpsertCoverageCellParams{
		CellM:         cellM,
		CellRow:       int64(c.Key.Row),
		CellCol:       int64(c.Key.Col),
		Samples:       int64(c.Samples),
		NoLinkSamples: int64(c.NoLinkSamples),
		SignalDbm:     c.SignalDBm,
		MaxLinks:      int64(c.MaxLinks),
		FirstSeen:     c.FirstSeen.UTC(),
		LastSeen:      c.LastSeen.UTC(),
	}
}

func fromRow(r *models.CoverageCell) Cell {
	return Cell{
		Key:           Key{Row: int32(r.CellRow), Col: int32(r.CellCol)},
		Samples:       int(r.Samples),
		NoLinkSamples: int(r.NoLinkSamples),
		SignalDBm:     r.SignalDbm,
		MaxLinks:      int(r.MaxLinks),
		FirstSeen:     r.FirstSeen,
		LastSeen:      r.LastSeen,
	}
}

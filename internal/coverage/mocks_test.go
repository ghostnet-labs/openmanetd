package coverage_test

import (
	"context"
	"sync"

	"github.com/openmanet/openmanetd/internal/database/models"
)

type cellKey struct {
	m, row, col int64
}

// fakeStore is an in-memory coverage_cells table.
type fakeStore struct {
	mu        sync.Mutex
	rows      map[cellKey]models.CoverageCell
	upserts   int
	deletes   int
	upsertErr error
	deleteErr error
	listErr   error
	dropErr   error
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: make(map[cellKey]models.CoverageCell)}
}

func (f *fakeStore) ListCoverageCells(_ context.Context, arg models.ListCoverageCellsParams) ([]models.CoverageCell, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.listErr != nil {
		return nil, f.listErr
	}

	out := make([]models.CoverageCell, 0, len(f.rows))
	for k, r := range f.rows {
		if k.m == arg.CellM {
			out = append(out, r)
		}
	}

	// Newest first, like the SQL.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].LastSeen.After(out[j-1].LastSeen); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}

	if int64(len(out)) > arg.Limit {
		out = out[:arg.Limit]
	}

	return out, nil
}

func (f *fakeStore) UpsertCoverageCell(_ context.Context, arg models.UpsertCoverageCellParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.upsertErr != nil {
		return f.upsertErr
	}

	f.upserts++
	f.rows[cellKey{arg.CellM, arg.CellRow, arg.CellCol}] = models.CoverageCell{
		CellM: arg.CellM, CellRow: arg.CellRow, CellCol: arg.CellCol,
		Samples: arg.Samples, NoLinkSamples: arg.NoLinkSamples, SignalDbm: arg.SignalDbm,
		MaxLinks: arg.MaxLinks, FirstSeen: arg.FirstSeen, LastSeen: arg.LastSeen,
	}

	return nil
}

func (f *fakeStore) DeleteCoverageCell(_ context.Context, arg models.DeleteCoverageCellParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.deleteErr != nil {
		return f.deleteErr
	}

	f.deletes++
	delete(f.rows, cellKey{arg.CellM, arg.CellRow, arg.CellCol})

	return nil
}

func (f *fakeStore) DeleteCoverageCellsOtherSizes(_ context.Context, cellM int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.dropErr != nil {
		return f.dropErr
	}

	for k := range f.rows {
		if k.m != cellM {
			delete(f.rows, k)
		}
	}

	return nil
}

func (f *fakeStore) setUpsertErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.upsertErr = err
}

func (f *fakeStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.rows)
}

func (f *fakeStore) getUpserts() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.upserts
}

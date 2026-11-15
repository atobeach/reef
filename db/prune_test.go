package db

import (
	"context"
	"slices"
	"testing"
)

// fakePrune is a prune query over a count of prunable rows: Count reports
// them and Delete removes up to the query's Limit.
type fakePrune struct {
	rows  *int64
	limit int
}

func (q fakePrune) Limit(n int) fakePrune {
	q.limit = n

	return q
}

func (q fakePrune) Count() (int64, error) { return *q.rows, nil }

func (q fakePrune) Delete() (int64, error) {
	deleted := *q.rows
	if q.limit > 0 {
		deleted = min(deleted, int64(q.limit))
	}

	*q.rows -= deleted

	return deleted, nil
}

// Count reports what a prune would delete and deletes nothing; PruneAll
// deletes it a chunk at a time, reporting the running total after each.
func TestAPrunableCountsWithoutDeletingAndPrunesInChunks(t *testing.T) {
	rows := int64(5)
	prunable := MassPrunable("Forecast", func(context.Context) fakePrune { return fakePrune{rows: &rows} })

	count, err := prunable.Count(t.Context())
	if err != nil || count != 5 {
		t.Fatalf("Count = %d, %v; want 5", count, err)
	}

	if rows != 5 {
		t.Fatalf("Count deleted rows: %d left of 5", rows)
	}

	var progress []int64
	total, err := prunable.PruneAll(t.Context(), 2, func(total int64) { progress = append(progress, total) })
	if err != nil || total != 5 {
		t.Fatalf("PruneAll = %d, %v; want 5", total, err)
	}

	if want := []int64{2, 4, 5}; !slices.Equal(progress, want) {
		t.Errorf("progress = %v, want %v", progress, want)
	}

	if rows != 0 {
		t.Errorf("%d rows left after PruneAll", rows)
	}
}

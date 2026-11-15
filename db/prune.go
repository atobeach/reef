package db

import (
	"context"
	"fmt"
)

// Prunable is a model whose stale rows `model:prune` deletes: its prune
// query, counted for --pretend and deleted in chunks otherwise.
type Prunable struct {
	// Model is the model's name (`WeatherForecast`) that --model / --except
	// match and the command prints.
	Model string
	count func(ctx context.Context) (int64, error)
	chunk func(ctx context.Context, size int) (int64, error)
}

type prunableQuery[Q any] interface {
	Limit(n int) Q
	Count() (int64, error)
	Delete() (int64, error)
}

// MassPrunable declares a model's prune query. query builds a fresh query on
// every call: each chunk needs its own builder.
func MassPrunable[Q prunableQuery[Q]](model string, query func(ctx context.Context) Q) Prunable {
	return Prunable{
		Model: model,
		count: func(ctx context.Context) (int64, error) { return query(ctx).Count() },
		chunk: func(ctx context.Context, size int) (int64, error) { return query(ctx).Limit(size).Delete() },
	}
}

// Count is how many rows a prune would delete.
func (p Prunable) Count(ctx context.Context) (int64, error) {
	return p.count(ctx)
}

// PruneAll deletes every prunable row, chunk rows at a time so one huge
// DELETE never holds locks on the whole table. progress, when set, receives
// the running total after each chunk.
func (p Prunable) PruneAll(ctx context.Context, chunk int, progress func(total int64)) (int64, error) {
	var total int64

	for {
		count, err := p.chunk(ctx, chunk)
		if err != nil {
			return total, fmt.Errorf("pruning %s: %w", p.Model, err)
		}

		if count == 0 {
			return total, nil
		}

		total += count
		if progress != nil {
			progress(total)
		}
	}
}

// Package collect maps and groups slices: Map reshapes every item, GroupBy
// gathers the items under each key in the order the keys first appear.
package collect

import "iter"

// Map returns fn applied to every item, in order. The result is never nil,
// so an empty list encodes as [], not null.
func Map[T, U any](items []T, fn func(T) U) []U {
	mapped := make([]U, len(items))
	for i, item := range items {
		mapped[i] = fn(item)
	}

	return mapped
}

// GroupBy yields each key with its items, keys in the order first seen and
// items in their original order.
func GroupBy[T any, K comparable](items []T, key func(T) K) iter.Seq2[K, []T] {
	return func(yield func(K, []T) bool) {
		var keys []K

		groups := map[K][]T{}
		for _, item := range items {
			k := key(item)
			if _, seen := groups[k]; !seen {
				keys = append(keys, k)
			}

			groups[k] = append(groups[k], item)
		}

		for _, k := range keys {
			if !yield(k, groups[k]) {
				return
			}
		}
	}
}

// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"errors"
	"iter"
)

var errPageTokenStalled = errors.New("reliant: the server returned the page token it was sent; stopping instead of repeating the page")

// Iter walks a list that the server returns a page at a time, following page
// tokens until the last page. It fetches lazily: nothing is requested until the
// first Next, and a page is requested only when the previous one is used up.
//
//	it := c.Runs.List(ctx, reliant.ListRuns{State: ...})
//	for it.Next() {
//		run := it.Value()
//	}
//	if err := it.Err(); err != nil { ... }
//
// An Iter is not safe for concurrent use.
type Iter[T any] struct {
	ctx   context.Context
	fetch func(ctx context.Context, token string) (items []T, next string, err error)
	limit int

	buf     []T
	token   string
	done    bool
	yielded int
	cur     T
	err     error
}

func newIter[T any](ctx context.Context, limit int, fetch func(ctx context.Context, token string) ([]T, string, error)) *Iter[T] {
	return &Iter[T]{ctx: ctx, fetch: fetch, limit: limit}
}

// Next advances to the next item and reports whether there is one. It returns
// false at the end of the list or on error; check Err.
func (it *Iter[T]) Next() bool {
	if it.err != nil || (it.limit > 0 && it.yielded >= it.limit) {
		return false
	}
	for len(it.buf) == 0 {
		if it.done {
			return false
		}
		if err := it.ctx.Err(); err != nil {
			it.err = err
			return false
		}
		items, next, err := it.fetch(it.ctx, it.token)
		if err != nil {
			it.err = err
			return false
		}
		// A token equal to the one just sent means the server is not advancing;
		// following it would repeat the page forever.
		if next != "" && next == it.token {
			it.err = errPageTokenStalled
			return false
		}
		it.buf, it.token = items, next
		if next == "" {
			it.done = true
		}
	}
	it.cur, it.buf = it.buf[0], it.buf[1:]
	it.yielded++
	return true
}

// Value is the item Next advanced to.
func (it *Iter[T]) Value() T { return it.cur }

// Err is the error that ended the walk, or nil if it reached the end.
func (it *Iter[T]) Err() error { return it.err }

// All collects every remaining item. Use it when the list is known to be small;
// otherwise range with Next.
func (it *Iter[T]) All() ([]T, error) {
	var out []T
	for it.Next() {
		out = append(out, it.Value())
	}
	return out, it.Err()
}

// Seq adapts the walk to a range-over-func loop. A failure is yielded as the
// final (zero, err) pair.
//
//	for run, err := range c.Runs.List(ctx, f).Seq() { ... }
func (it *Iter[T]) Seq() iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for it.Next() {
			if !yield(it.Value(), nil) {
				return
			}
		}
		if err := it.Err(); err != nil {
			var zero T
			yield(zero, err)
		}
	}
}

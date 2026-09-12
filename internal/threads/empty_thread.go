// Copyright (c) 2025 Reliant Labs
package threads

import (
	"context"
	"database/sql"
	"errors"
)

// isEmptyThread reports whether err from GetLatestContextWindow means "this
// thread has no messages yet" — as opposed to "the read failed".
//
// # Why this exists
//
// Every read path in this package used to treat ANY error from
// GetLatestContextWindow as an empty thread:
//
//	latestCW, err := s.repo.GetLatestContextWindow(ctx, threadID)
//	if err != nil {
//	    // No context window means empty thread - return empty slice
//	    return []*db.Message{}, nil
//	}
//
// That comment is only true for sql.ErrNoRows. The branch also swallowed
// context cancellation, and THAT is what produced the user-visible failure
// this function was written for:
//
//	cannot call LLM with empty message history
//	  (chat=087b4456-…, thread=69424fb0-…)
//
// The sequence, reconstructed from the worker log: the user interrupted a
// thread; the interrupt cancelled the activity context; the very next DB call
// — this one — failed with "context canceled"; the error was discarded and
// reported as an empty conversation; CallLLM's end-of-history guard fired.
// The thread was not empty. Two seconds later the same thread resolved 344
// messages.
//
// The tell in the log is that the failing CallLLM produced NO
// "[FORK-DEBUG] LoadCurrentMessages called" line at all — that statement sits
// immediately after the error branch, so its absence proves the function
// returned inside it.
//
// # Why "empty" is the dangerous default
//
// An empty history is not a neutral answer. Downstream it means either a hard
// failure (the guard above) or, worse, a SILENTLY TRUNCATED prompt: a
// cancelled mid-walk read that returned partial history would send the model a
// conversation missing its own recent turns, and nothing would flag it. Failing
// loudly on a real error is the only safe direction.
//
// A genuinely empty thread is a normal, expected state — a thread created but
// not yet written to — so it must stay cheap and non-erroring. Hence the
// narrow classification: no rows means empty, everything else propagates.
func isEmptyThread(err error) bool {
	if err == nil {
		return true
	}

	// Cancellation is never emptiness. Checked explicitly and first: this is
	// the case that caused the incident, and a wrapped cancellation must not
	// reach the sql.ErrNoRows test below by some future accident of wrapping.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// The store wraps with %w, so errors.Is sees through the message.
	return errors.Is(err, sql.ErrNoRows)
}

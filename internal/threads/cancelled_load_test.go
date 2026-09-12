// Copyright (c) 2025 Reliant Labs
package threads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// TestLoadCurrentMessages_CancelledReadIsNotEmptyThread is the regression test
// for the failure that started this investigation:
//
//	cannot call LLM with empty message history
//	  (chat=087b4456-…, thread=69424fb0-…)
//
// The thread was NOT empty — it held 344 messages, and resolved all of them
// two seconds later. What happened is that the user's interrupt cancelled the
// activity context, the next DB read failed with "context canceled", and
// LoadCurrentMessages' error branch reported that failure as an empty
// conversation:
//
//	latestCW, err := s.repo.GetLatestContextWindow(ctx, threadID)
//	if err != nil {
//	    // No context window means empty thread - return empty slice
//	    return []*db.Message{}, nil
//	}
//
// CallLLM then hit its end-of-history guard and failed the turn with a message
// that named neither the real cause nor the real state.
//
// The assertion is deliberately about the ERROR, not the message count: a
// cancelled read must propagate as a cancellation so callers can classify it
// as retryable, rather than silently becoming a well-formed "there is nothing
// here" that every downstream layer trusts.
func TestLoadCurrentMessages_CancelledReadIsNotEmptyThread(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()

	thread, _ := h.createThread("cancelled-load-thread", h.chatID)

	// Give the thread real history, so "empty" can only be a lie.
	for i := 0; i < 3; i++ {
		if _, err := h.svc.SaveMessage(context.Background(), SaveMessageOpts{
			ChatID:  h.chatID,
			Thread:  thread.ID,
			Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
			Content: fmt.Sprintf("message %d", i),
		}); err != nil {
			t.Fatalf("failed to seed message %d: %v", i, err)
		}
	}

	// Sanity: the thread really does resolve its history on a healthy context.
	healthy, err := h.svc.LoadCurrentMessages(context.Background(), thread.ID)
	if err != nil {
		t.Fatalf("baseline load failed: %v", err)
	}
	if len(healthy) != 3 {
		t.Fatalf("expected 3 messages on a healthy read, got %d", len(healthy))
	}

	// Now read with an already-cancelled context, exactly as an interrupted
	// activity does.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	msgs, err := h.svc.LoadCurrentMessages(cancelledCtx, thread.ID)

	if err == nil {
		t.Fatalf("a cancelled read returned %d messages and no error; "+
			"reporting cancellation as an empty thread is what produced "+
			"'cannot call LLM with empty message history' on a thread with real history",
			len(msgs))
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected the error to wrap context.Canceled so callers can classify it as retryable; got %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("a failed load must not also return partial history; got %d messages", len(msgs))
	}
}

// TestIsEmptyThread_ClassifiesOnlyNoRows pins the classifier directly. The
// whole defect was one branch treating every error as emptiness, so the
// boundary between "no rows" and "the read failed" is the thing worth pinning.
func TestIsEmptyThread_ClassifiesOnlyNoRows(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil is empty", nil, true},
		{"no rows is empty", sql.ErrNoRows, true},
		{"wrapped no rows is empty",
			fmt.Errorf("failed to get latest context window: %w", sql.ErrNoRows), true},

		// The incident cases.
		{"cancellation is NOT empty", context.Canceled, false},
		{"wrapped cancellation is NOT empty",
			fmt.Errorf("failed to get latest context window: %w", context.Canceled), false},
		{"deadline exceeded is NOT empty", context.DeadlineExceeded, false},

		// Any other infrastructure failure must propagate too.
		{"serialization failure is NOT empty",
			errors.New("could not serialize access due to concurrent update (SQLSTATE 40001)"), false},
		{"connection failure is NOT empty",
			errors.New("driver: bad connection"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isEmptyThread(tc.err); got != tc.want {
				t.Errorf("isEmptyThread(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

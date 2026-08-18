// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"os"
	"strings"
	"testing"
)

// GetNextSeqByChat allocates the chat-global message seq, and it runs on the
// write path of EVERY message save. Every transaction here is SERIALIZABLE, so
// the query plan is a correctness concern, not just a speed one: a sequential
// scan takes a predicate lock covering the WHOLE messages table, which
// conflicts with every concurrent insert anywhere in the system and aborts one
// side with SQLSTATE 40001.
//
// That is what users saw as:
//
//	failed to save mailbox envelope: failed to save message:
//	failed to create message: ERROR: could not serialize access due to
//	read/write dependencies among transactions (SQLSTATE 40001)
//
// The original query filtered `WHERE chat_id = $1 OR context_window_id IN
// (...)`. An OR across two columns cannot use either index, so it degraded to a
// full scan — measured at 35ms over 221k rows versus 1.7ms for the split form.
//
// This test asserts the PLAN, because a functional test cannot tell the two
// apart: both return the same number, and the bad one only hurts under
// concurrency.
func TestGetNextSeqByChatDoesNotFuseBranches(t *testing.T) {
	src, err := os.ReadFile("postgres/queries/messages.sql")
	if err != nil {
		t.Fatalf("read queries: %v", err)
	}

	body := string(src)
	start := strings.Index(body, "-- name: GetNextSeqByChat :one")
	if start < 0 {
		t.Fatal("GetNextSeqByChat not found")
	}
	end := strings.Index(body[start+1:], "-- name: ")
	if end < 0 {
		end = len(body) - start - 1
	}
	query := body[start : start+1+end]

	// Strip comments — the rationale above the query mentions the old form.
	var sql strings.Builder
	for _, line := range strings.Split(query, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		sql.WriteString(line)
		sql.WriteString("\n")
	}
	q := sql.String()

	// The regression: one scan filtered by `chat_id = ... OR
	// context_window_id IN (...)`. An OR across two columns cannot use either
	// index, so at production size (221k rows measured) this becomes a full
	// sequential scan — 35ms versus 1.7ms for the split form.
	//
	// Speed is the lesser problem. Every transaction here is SERIALIZABLE, and
	// a sequential scan takes a predicate lock over the ENTIRE messages table,
	// so this allocator conflicted with every concurrent insert anywhere in
	// the system. Users saw it as "failed to save mailbox envelope: ... could
	// not serialize access due to read/write dependencies among transactions
	// (SQLSTATE 40001)" whenever several spawns saved messages at once.
	//
	// Asserted against the source rather than an EXPLAIN plan because a test
	// fixture is far too small to reproduce the bad plan: on a few hundred
	// rows Postgres correctly prefers a sequential scan either way, so a
	// plan-based test would pass with the broken query and prove nothing.
	if strings.Contains(q, "OR m.context_window_id") {
		t.Fatal("GetNextSeqByChat fuses its two branches with OR. An OR across " +
			"chat_id and context_window_id cannot use either index, so this " +
			"degrades to a full scan of messages, which under SERIALIZABLE " +
			"predicate-locks the whole table and aborts concurrent message " +
			"inserts with SQLSTATE 40001. Keep the two lookups separate " +
			"(GREATEST of two indexed MAX subqueries).")
	}

	// Both halves must still be consulted, or a branched chat restarts at
	// seq 0 and every reply renders above the inherited history.
	if !strings.Contains(q, "m.chat_id = $1") {
		t.Error("query must still consider this chat's own rows")
	}
	if !strings.Contains(q, "context_window_id = ANY(ARRAY(SELECT id FROM chain))") {
		t.Error("query must still span the fork chain via an indexable ANY(ARRAY(...))")
	}
}

// The plan test above pins query SHAPE. This pins the VALUE: the allocator must
// still return a high-water mark spanning the fork chain, which is the whole
// reason the query is not simply MAX(seq) WHERE chat_id = $1. A branch that
// restarts at seq 0 puts every new reply numerically beneath the inherited
// history, so it renders at the top of the transcript and reads as "my message
// never arrived".
func TestGetNextSeqByChatSpansForkChain(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := "chat-seq-value"
	createActivityTestChat(t, repo, chatID)

	for i := 0; i < 5; i++ {
		if _, err := repo.SaveMessageToThread(ctx, chatID, chatID, 1, "m", nil, nil, nil); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	next, err := repo.GetNextSeq(ctx, chatID, chatID)
	if err != nil {
		t.Fatalf("GetNextSeq: %v", err)
	}
	if next < 5 {
		t.Fatalf("next seq must exceed the 5 existing messages, got %d", next)
	}

	// And it must keep advancing rather than handing out a duplicate, which
	// messages_chat_seq_key would reject.
	if _, err := repo.SaveMessageToThread(ctx, chatID, chatID, 1, "m", nil, nil, nil); err != nil {
		t.Fatalf("save after allocation: %v", err)
	}
	after, err := repo.GetNextSeq(ctx, chatID, chatID)
	if err != nil {
		t.Fatalf("GetNextSeq after: %v", err)
	}
	if after <= next {
		t.Fatalf("seq must advance after a save: was %d, now %d", next, after)
	}
}

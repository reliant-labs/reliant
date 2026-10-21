// Copyright (c) 2025 Reliant Labs
package store

import (
	"strings"
	"testing"
)

// NextSeqInChat allocates the chat-global message seq for every writer that
// still allocates for itself, and under SERIALIZABLE its plan is a
// correctness concern, not just a speed one: a sequential scan takes a
// predicate lock covering the WHOLE messages table, which conflicts with every
// concurrent insert anywhere in the system and aborts one side with SQLSTATE
// 40001. Users saw it as "failed to save mailbox envelope: … could not
// serialize access due to read/write dependencies among transactions".
//
// The original query filtered `WHERE chat_id = … OR context_window_id IN
// (…)`. An OR across two columns cannot use either index, so it degraded to a
// full scan — 35ms over 221k rows against 1.7ms for the split form.
//
// Asserted against the source rather than an EXPLAIN plan because a test
// fixture is far too small to reproduce the bad plan: on a few hundred rows
// Postgres correctly prefers a sequential scan either way, so a plan-based
// test would pass with the broken query and prove nothing.
func TestNextSeqInChatDoesNotFuseBranches(t *testing.T) {
	q := nextSeqInChatSQL
	if strings.Contains(q, "OR m.context_window_id") {
		t.Fatal("nextSeqInChatSQL fuses its two branches with OR. An OR across " +
			"chat_id and context_window_id cannot use either index, so this " +
			"degrades to a full scan of messages, which under SERIALIZABLE " +
			"predicate-locks the whole table and aborts concurrent message " +
			"inserts with SQLSTATE 40001. Keep the two lookups separate " +
			"(GREATEST of two indexed MAX subqueries).")
	}
	// Both halves must still be consulted, or a branched chat restarts at
	// seq 0 and every reply renders above the inherited history.
	if !strings.Contains(q, "m.chat_id = ?0") {
		t.Error("query must still consider this chat's own rows")
	}
	if !strings.Contains(q, "context_window_id = ANY(ARRAY(SELECT id FROM chain))") {
		t.Error("query must still span the fork chain via an indexable ANY(ARRAY(...))")
	}
}

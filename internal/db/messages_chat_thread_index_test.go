// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/store"
)

// ListMessages' total counts the chat's messages off its main thread on every
// chat open (store.CountMessagesOutsideThreadSQL). With only (chat_id)
// indexed, Postgres read each of the chat's rows from the heap to test
// thread_id: in prod that one COUNT was p50 3.9s on long chats, ~95% of every
// slow ListMessages. idx_messages_chat_thread carries thread_id, so the count
// never touches the heap.
//
// The property is an INDEX ONLY scan on idx_messages_chat_thread. A plain
// index scan on it (or on any chat_id index) still fetches every row's heap
// page, which is the cost this index exists to remove. Asserted under the
// generic plan, the one a prepared statement settles on after five runs.
func TestCountMessagesOutsideThreadIsIndexOnly(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	ctx := context.Background()
	conn, err := raw.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()

	// An empty table is cheaper to scan than to probe, so any plan would do
	// and the test would be vacuous; with seq and bitmap scans off the planner
	// must pick an index, and it picks index-only exactly when one covers both
	// columns.
	for _, stmt := range []string{
		`SET plan_cache_mode = force_generic_plan`,
		`SET enable_seqscan = off`,
		`SET enable_bitmapscan = off`,
	} {
		_, err := conn.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}

	// The repository's statement, with bun's ? placeholders as $n.
	query := strings.Replace(strings.Replace(store.CountMessagesOutsideThreadSQL, "?", "$1", 1), "?", "$2", 1)
	_, err = conn.ExecContext(ctx, `PREPARE count_outside_thread(text, text) AS `+query)
	require.NoError(t, err)

	plan := explainLines(t, ctx, conn, `EXPLAIN EXECUTE count_outside_thread('chat', 'main')`)
	require.Contains(t, plan, "Index Only Scan using idx_messages_chat_thread ",
		"counting a chat's messages off one thread must be an index-only scan of idx_messages_chat_thread; "+
			"anything else reads each of the chat's rows from the heap to test thread_id. Plan:\n%s", plan)
}

// idx_messages_chat_id is dropped because idx_messages_chat_thread serves every
// chat_id lookup it served. A chat-wide count (the snapshot's total) must
// still be an index-only scan, not a heap walk.
func TestCountMessagesInChatStillIndexOnly(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	ctx := context.Background()
	conn, err := raw.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	for _, stmt := range []string{
		`SET plan_cache_mode = force_generic_plan`,
		`SET enable_seqscan = off`,
		`SET enable_bitmapscan = off`,
	} {
		_, err := conn.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}
	_, err = conn.ExecContext(ctx, `PREPARE count_chat(text) AS SELECT count(*) FROM messages WHERE chat_id = $1`)
	require.NoError(t, err)

	plan := explainLines(t, ctx, conn, `EXPLAIN EXECUTE count_chat('chat')`)
	require.Contains(t, plan, "Index Only Scan using ", "a chat-wide message count must stay index-only. Plan:\n%s", plan)
	require.NotContains(t, plan, "idx_messages_chat_id ", "idx_messages_chat_id is dropped. Plan:\n%s", plan)
}

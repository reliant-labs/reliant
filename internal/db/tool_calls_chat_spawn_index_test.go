// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/store"
)

// Every chat open resolves its spawn links (store.SpawnToolCallIDsByChildThreadSQL).
// Without a chat-scoped index over the spawn rows, Postgres walked
// idx_tool_calls_child_workflow_id over every spawn call of every chat and
// filtered on chat_id: in prod the read was p95 807ms, max 2.7s.
//
// The property is that the tool_calls side is read through
// idx_tool_calls_chat_spawn, by chat_id, under the generic plan a prepared
// statement settles on after five runs.
func TestSpawnToolCallIDsByChildThreadUsesChatSpawnIndex(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	ctx := context.Background()
	conn, err := raw.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()

	// An empty table is cheaper to scan than to probe, so any plan would do
	// and the test would be vacuous; with seq and bitmap scans off the planner
	// must pick an index, and it picks the one that answers chat_id AND the
	// child_workflow_id predicate together.
	for _, stmt := range []string{
		`SET plan_cache_mode = force_generic_plan`,
		`SET enable_seqscan = off`,
		`SET enable_bitmapscan = off`,
	} {
		_, err := conn.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}

	// The repository's statement, with bun's ?0 placeholder as $1.
	query := strings.ReplaceAll(store.SpawnToolCallIDsByChildThreadSQL, "?0", "$1")
	_, err = conn.ExecContext(ctx, `PREPARE spawn_links(text) AS `+query)
	require.NoError(t, err)

	plan := explainLines(t, ctx, conn, `EXPLAIN EXECUTE spawn_links('chat')`)
	require.Contains(t, plan, "using idx_tool_calls_chat_spawn on tool_calls",
		"a chat's spawn links must be read through idx_tool_calls_chat_spawn; "+
			"anything else walks spawn rows of every chat, or all of this chat's tool calls. Plan:\n%s", plan)
	require.NotContains(t, plan, "idx_tool_calls_child_workflow_id",
		"the spawn-link read must not walk every chat's spawns by child_workflow_id. Plan:\n%s", plan)
}

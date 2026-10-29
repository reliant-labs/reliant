// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"io/fs"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	forgedb "github.com/reliant-labs/reliant/db"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/store"
)

// GetLatestNonMessageUpdatesPerEntity is fast only because it can walk
// idx_chat_updates_snapshot_heads, a PARTIAL index. Postgres uses a partial
// index only when it can prove the query's predicate implies the index's, and
// it proves that only from values visible at plan time.
//
// pgx prepares statements, and after five executions Postgres may switch a
// prepared statement to a GENERIC plan, which cannot see bound parameters. The
// first version of this query bound its update types as parameters: its custom
// plans used the index, its generic plan could not, and with the old full index
// dropped by the same migration it fell back to scanning the chat's whole
// history — measured >20s on a copy of the dev database, on the read every chat
// open performs. Nothing about the result shape changes, so no other test sees
// it.
//
// The skip scan may exclude MORE types than the index does (the live-only
// AGENT_MESSAGES_DRAINED is one): NOT IN of a superset still implies NOT IN of
// the subset. Excluding fewer would not, and this test would catch it.
//
// This test forces the generic plan, which is the one that bit.
func TestSnapshotHeadsQueryUsesPartialIndexUnderGenericPlan(t *testing.T) {
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	ctx := context.Background()
	conn, err := raw.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()

	// Planner settings are per session, so set them on the one connection the
	// plan is taken on. Seq scans are disabled because an empty test table is
	// cheaper to scan than to probe, which would make any plan acceptable and
	// this test vacuous; with them off the planner must use an index if one is
	// usable, so the assertion distinguishes "provable" from "not".
	for _, stmt := range []string{
		`SET plan_cache_mode = force_generic_plan`,
		`SET enable_seqscan = off`,
	} {
		_, err := conn.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}

	// The exact SQL the repository runs.
	_, err = conn.ExecContext(ctx, `PREPARE snapshot_heads(text) AS `+store.SnapshotHeadsSQL)
	require.NoError(t, err)

	plan := explainLines(t, ctx, conn, `EXPLAIN EXECUTE snapshot_heads('chat')`)

	// The index name appearing anywhere is NOT the property: the thread and
	// question read can use it too, so a plan whose skip scan had fallen back
	// to chat_updates_chat_sequence_key (read the chat's whole history, filter
	// every row) still mentions it. What must hold is that the recursive probe
	// — the step that runs once per entity, with `entity_id > h.entity_id` —
	// is answered by the partial index, i.e. the bound sits in an Index Cond on
	// it rather than in a Filter applied after reading everything.
	require.True(t, probeUsesIndex(plan, "idx_chat_updates_snapshot_entity_heads"),
		"the generic plan's skip-scan probe must be an index condition on idx_chat_updates_snapshot_entity_heads; "+
			"otherwise every probe reads the chat's whole history. This happens when the update types are bound "+
			"parameters instead of literals the planner can match against the index predicate. Plan:\n%s", plan)

	// The re-keyed read (THREAD, QUESTION) must find its rows through
	// idx_chat_updates_snapshot_rekeyed. Every other index that can answer
	// `chat_id = ?` covers the chat's whole non-message history — on the
	// measured chat, 165,843 rows rechecked on the heap to keep 758, ~350ms of
	// the ~475ms read.
	require.True(t, scanUsesIndex(plan, "idx_chat_updates_snapshot_rekeyed"),
		"the generic plan's re-keyed read must scan idx_chat_updates_snapshot_rekeyed; otherwise it rechecks "+
			"every non-message update of the chat to find a few hundred thread/question rows. This happens when "+
			"the query's `update_type IN (...)` no longer matches the index predicate. Plan:\n%s", plan)
}

// scanUsesIndex reports whether any scan node in the plan reads index. Plain
// and bitmap index scans spell it differently ("using" vs "on").
func scanUsesIndex(plan, index string) bool {
	return strings.Contains(plan, "Index Scan using "+index+" ") ||
		strings.Contains(plan, "Index Only Scan using "+index+" ") ||
		strings.Contains(plan, "Bitmap Index Scan on "+index+" ")
}

// probeUsesIndex reports whether the plan node that applies the skip scan's
// `entity_id > h.entity_id` bound is an index scan on index — with the bound
// as an Index Cond — rather than a Filter over rows found some other way.
func probeUsesIndex(plan, index string) bool {
	lines := strings.Split(plan, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "entity_id > h.entity_id") {
			continue
		}
		if !strings.Contains(line, "Index Cond") {
			return false // applied as a Filter: the probe is not using an index for it
		}
		// The Index Cond line belongs to the nearest scan node above it.
		for j := i; j >= 0; j-- {
			if strings.Contains(lines[j], "Scan") {
				return strings.Contains(lines[j], index)
			}
		}
		return false
	}
	return false // no probe in the plan at all: the query shape changed
}

// The literals in store.SnapshotHeadsSQL are enum values written out by hand, and
// each migration's index predicate is another copy. If the enum is renumbered,
// or an index predicate edited, the query would silently read the wrong set of
// rows or lose the index. Pin them all together.
func TestSnapshotHeadsQueryMatchesPartialIndex(t *testing.T) {
	t.Parallel()

	// Not snapshot state at all.
	notState := []reliantv1.ChatUpdateType{
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_STREAM_FINALIZED,
	}
	// Re-keyed types, read separately from the skip scan.
	rekeyed := []reliantv1.ChatUpdateType{
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION,
	}
	// Workflow state: keyed per workflow and per workflow node, so every run
	// adds entities. Not in a snapshot; a run-status view reads one run's
	// nodes (WorkflowNodeHeadsSQL).
	workflowState := []reliantv1.ChatUpdateType{
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_STATUS,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_NODE_EXECUTION,
	}
	// Live-only types: delivered by the live stream and replay, never by the
	// snapshot.
	liveOnly := []reliantv1.ChatUpdateType{
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_AGENT_MESSAGES_DRAINED,
	}

	excluded := append(append(append(append([]reliantv1.ChatUpdateType{}, notState...), rekeyed...), workflowState...), liveOnly...)
	slices.Sort(excluded)
	skipScan := "update_type NOT IN (" + joinEnum(excluded) + ")"
	require.Equal(t, 2, strings.Count(store.SnapshotHeadsSQL, skipScan),
		"both halves of the skip scan must exclude exactly %s", skipScan)
	require.Contains(t, store.SnapshotHeadsSQL, "update_type IN ("+joinEnum(rekeyed)+")")
	require.Contains(t, store.SnapshotHeadsSQL,
		"WHEN update_type = "+strconv.Itoa(int(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION))+" THEN")

	// EQUAL, not merely implied: with a narrower index predicate every probe
	// rechecks each entry on the heap and walks past the ones it excludes.
	require.Contains(t, latestIndexDefinition(t, "idx_chat_updates_snapshot_entity_heads"),
		"WHERE update_type <> ALL (ARRAY["+joinEnum(excluded)+"])",
		"idx_chat_updates_snapshot_entity_heads must exclude exactly the types the skip scan excludes")
	node := strconv.Itoa(int(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_NODE_EXECUTION))
	require.Equal(t, 2, strings.Count(store.WorkflowNodeHeadsSQL, "AND update_type = "+node+"\n"),
		"both halves of the node skip scan must read exactly NODE_EXECUTION")
	require.Contains(t, latestIndexDefinition(t, "idx_chat_updates_workflow_heads"),
		"WHERE update_type = "+node+";")
	require.Contains(t, latestIndexDefinition(t, "idx_chat_updates_snapshot_rekeyed"),
		"WHERE (update_type = ANY (ARRAY["+joinEnum(rekeyed)+"]))",
		"idx_chat_updates_snapshot_rekeyed must cover exactly the types the query's re-keyed read selects; "+
			"the planner uses a partial index only when the query's literal predicate implies the index's")
}

// latestIndexDefinition returns the last CREATE INDEX statement for index
// across the embedded migrations, in version order: the definition a database
// they build ends up with. The baseline spells it the way pg_dump does.
func latestIndexDefinition(t *testing.T, index string) string {
	t.Helper()
	files, err := fs.Glob(forgedb.Source(), forgedb.SourceDir+"/*.up.sql")
	require.NoError(t, err)
	sort.Strings(files)
	var def string
	for _, f := range files {
		body, err := fs.ReadFile(forgedb.Source(), f)
		require.NoError(t, err)
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, "CREATE INDEX") && strings.Contains(line, " "+index+" ON ") {
				def = line
			}
		}
	}
	require.NotEmpty(t, def, "no migration creates %s", index)
	return def
}

func joinEnum(values []reliantv1.ChatUpdateType) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = strconv.Itoa(int(v))
	}
	return strings.Join(parts, ", ")
}

func explainLines(t *testing.T, ctx context.Context, conn *sql.Conn, query string) string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, query)
	require.NoError(t, err)
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		lines = append(lines, line)
	}
	require.NoError(t, rows.Err())
	return strings.Join(lines, "\n")
}

// A run-status view's node read (store.WorkflowNodeHeadsSQL) walks one
// workflow's nodes through idx_chat_updates_workflow_heads, under the generic
// plan too: both the seed and the per-node probe must be index conditions on
// it, or a probe reads the chat's whole node history.
func TestWorkflowNodeHeadsQueryUsesPartialIndexUnderGenericPlan(t *testing.T) {
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
	_, err = conn.ExecContext(ctx, `PREPARE node_heads(text, text) AS `+store.WorkflowNodeHeadsSQL)
	require.NoError(t, err)

	plan := explainLines(t, ctx, conn, `EXPLAIN EXECUTE node_heads('chat', 'wf')`)
	scans := 0
	for _, line := range strings.Split(plan, "\n") {
		if strings.Contains(line, "Scan using idx_chat_updates_workflow_heads ") {
			scans++
		}
		require.NotContains(t, line, "Filter: ((entity_id",
			"a node probe's range must be an index condition, not a filter. Plan:\n%s", plan)
	}
	require.Equal(t, 2, scans,
		"the seed and the per-node probe must both scan idx_chat_updates_workflow_heads. Plan:\n%s", plan)
}

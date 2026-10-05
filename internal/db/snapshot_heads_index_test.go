// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
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

	// The exact SQL the repository runs, with ? rewritten to $n as it is there.
	query := (&Repo{driver: DriverPostgres}).bindQuery(snapshotHeadsQuery)
	_, err = conn.ExecContext(ctx, `PREPARE snapshot_heads(text, text, text, text) AS `+query)
	require.NoError(t, err)

	plan := explainLines(t, ctx, conn, `EXPLAIN EXECUTE snapshot_heads('chat', 'chat', 'chat', 'chat')`)

	// The index name appearing anywhere is NOT the property: the thread and
	// question read can use it too, so a plan whose skip scan had fallen back
	// to chat_updates_chat_sequence_key (read the chat's whole history, filter
	// every row) still mentions it. What must hold is that the recursive probe
	// — the step that runs once per entity, with `entity_id > h.entity_id` —
	// is answered by the partial index, i.e. the bound sits in an Index Cond on
	// it rather than in a Filter applied after reading everything.
	require.True(t, probeUsesIndex(plan, "idx_chat_updates_snapshot_heads"),
		"the generic plan's skip-scan probe must be an index condition on idx_chat_updates_snapshot_heads; "+
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

// The literals in snapshotHeadsQuery are enum values written out by hand, and
// each migration's index predicate is another copy. If the enum is renumbered,
// or an index predicate edited, the query would silently read the wrong set of
// rows or lose the index. Pin them all together.
func TestSnapshotHeadsQueryMatchesPartialIndex(t *testing.T) {
	// The types the index (and the skip scan) excludes.
	indexExcluded := []reliantv1.ChatUpdateType{
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_STREAM_FINALIZED,
	}
	// Re-keyed types, read separately from the skip scan.
	rekeyed := []reliantv1.ChatUpdateType{
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION,
	}
	// Live-only types: delivered by the live stream and replay, never by the
	// snapshot. Still in idx_chat_updates_snapshot_heads; the probe filters
	// them, which costs nothing in index usability because the skip scan's
	// list stays a superset of the index's.
	liveOnly := []reliantv1.ChatUpdateType{
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_AGENT_MESSAGES_DRAINED,
	}

	skipScanTypes := append(append(append([]reliantv1.ChatUpdateType{}, indexExcluded...), rekeyed...), liveOnly...)
	skipScan := "update_type NOT IN (" + joinEnum(skipScanTypes) + ")"
	require.Equal(t, 2, strings.Count(snapshotHeadsQuery, skipScan),
		"both halves of the skip scan must exclude exactly %s", skipScan)
	// The partial index is usable only if the skip scan's predicate implies
	// its predicate, i.e. every type the index excludes is excluded here too.
	require.Subset(t, skipScanTypes, indexExcluded,
		"the skip scan must exclude at least everything idx_chat_updates_snapshot_heads excludes")
	require.Contains(t, snapshotHeadsQuery, "update_type IN ("+joinEnum(rekeyed)+")")
	require.Contains(t, snapshotHeadsQuery,
		"WHEN update_type = "+strconv.Itoa(int(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION))+" THEN")

	migration, err := FS.ReadFile("migrations/postgres/20261004010819_bound_chat_open_reads.sql")
	require.NoError(t, err)
	require.Contains(t, string(migration), "WHERE update_type NOT IN ("+joinEnum(indexExcluded)+");",
		"idx_chat_updates_snapshot_heads must exclude exactly the types the query's skip scan relies on it excluding")

	rekeyedMigration, err := FS.ReadFile("migrations/postgres/20261004172515_snapshot_rekeyed_updates_index.sql")
	require.NoError(t, err)
	require.Contains(t, string(rekeyedMigration), "WHERE update_type IN ("+joinEnum(rekeyed)+");",
		"idx_chat_updates_snapshot_rekeyed must cover exactly the types the query's re-keyed read selects; "+
			"the planner uses a partial index only when the query's literal predicate implies the index's")
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

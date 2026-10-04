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
// the migration's index predicate is a third copy. If the enum is renumbered, or
// the index predicate edited, the query would silently read the wrong set of
// rows or lose the index. Pin all three together.
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

	skipScan := "update_type NOT IN (" + joinEnum(append(append([]reliantv1.ChatUpdateType{}, indexExcluded...), rekeyed...)) + ")"
	require.Equal(t, 2, strings.Count(snapshotHeadsQuery, skipScan),
		"both halves of the skip scan must exclude exactly %s", skipScan)
	require.Contains(t, snapshotHeadsQuery, "update_type IN ("+joinEnum(rekeyed)+")")
	require.Contains(t, snapshotHeadsQuery,
		"WHEN update_type = "+strconv.Itoa(int(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION))+" THEN")

	migration, err := FS.ReadFile("migrations/postgres/20261004010819_bound_chat_open_reads.sql")
	require.NoError(t, err)
	require.Contains(t, string(migration), "WHERE update_type NOT IN ("+joinEnum(indexExcluded)+");",
		"idx_chat_updates_snapshot_heads must exclude exactly the types the query's skip scan relies on it excluding")
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

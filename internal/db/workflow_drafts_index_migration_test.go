// Copyright (c) 2025 Reliant Labs
package db

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

func TestMigrate_WorkflowDraftsSharedByOrgIndex(t *testing.T) {
	testkit.Slow(t, "builds a fresh database through every migration")
	t.Parallel()

	_, raw := newMigratedTestDB(t)
	var valid, ready bool
	var table, method, firstKey, secondKey, predicate string
	var keyCount, attributeCount, firstOptions, secondOptions int
	err := raw.QueryRow(`
		SELECT i.indisvalid, i.indisready, i.indrelid::regclass::text,
		       am.amname, i.indnkeyatts, i.indnatts,
		       pg_get_indexdef(i.indexrelid, 1, true),
		       pg_get_indexdef(i.indexrelid, 2, true),
		       pg_get_expr(i.indpred, i.indrelid), i.indoption[0], i.indoption[1]
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		JOIN pg_am am ON am.oid = c.relam
		WHERE i.indexrelid = to_regclass('public.workflow_drafts_shared_by_org')
	`).Scan(&valid, &ready, &table, &method, &keyCount, &attributeCount,
		&firstKey, &secondKey, &predicate, &firstOptions, &secondOptions)
	require.NoError(t, err, "the shared workflow drafts index must exist after migration")
	require.True(t, valid, "a failed concurrent build must not leave an invalid index")
	require.True(t, ready)
	require.Equal(t, "workflow_drafts", table)
	require.Equal(t, "btree", method)
	require.Equal(t, 2, keyCount)
	require.Equal(t, 2, attributeCount)
	require.Equal(t, "org_id", firstKey)
	require.Equal(t, "shared_at", secondKey)
	// B-tree indoption bits: DESC = 1, NULLS FIRST = 2.
	require.Equal(t, 0, firstOptions, "org_id ASC NULLS LAST")
	require.Equal(t, 3, secondOptions, "shared_at DESC NULLS FIRST")
	require.Equal(t, "(visibility = 'org'::text)", predicate)
}

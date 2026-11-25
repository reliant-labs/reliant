package db

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

func TestMigrate_LLMCallUsageChatCreatedAtIndex(t *testing.T) {
	testkit.Slow(t, "builds a fresh database through every migration")
	t.Parallel()

	_, raw := newMigratedTestDB(t)
	var valid, ready, correctTable, unfiltered bool
	var method, firstKey, secondKey string
	var keyCount, attributeCount, firstOptions, secondOptions int
	err := raw.QueryRow(`
		SELECT i.indisvalid, i.indisready,
		       i.indrelid = 'public.llm_call_usage'::regclass,
		       am.amname, i.indnkeyatts, i.indnatts,
		       pg_get_indexdef(i.indexrelid, 1, true),
		       pg_get_indexdef(i.indexrelid, 2, true),
		       i.indpred IS NULL, i.indoption[0], i.indoption[1]
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		JOIN pg_am am ON am.oid = c.relam
		WHERE i.indexrelid = to_regclass('public.llm_call_usage_chat_created_at_idx')
	`).Scan(&valid, &ready, &correctTable, &method, &keyCount, &attributeCount,
		&firstKey, &secondKey, &unfiltered, &firstOptions, &secondOptions)
	require.NoError(t, err, "the per-chat ledger index must exist after migration")
	require.True(t, valid, "a failed concurrent build must not leave an invalid index")
	require.True(t, ready)
	require.True(t, correctTable)
	require.Equal(t, "btree", method)
	require.Equal(t, 2, keyCount)
	require.Equal(t, 2, attributeCount)
	require.Equal(t, "chat_id", firstKey)
	require.Equal(t, "created_at", secondKey)
	require.True(t, unfiltered)
	require.Equal(t, 0, firstOptions, "chat_id ASC NULLS LAST")
	require.Equal(t, 0, secondOptions, "created_at ASC NULLS LAST")
}

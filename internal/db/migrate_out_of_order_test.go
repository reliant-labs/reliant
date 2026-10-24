// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/reliant-labs/forge/pkg/migratekit"
	"github.com/stretchr/testify/require"

	forgedb "github.com/reliant-labs/reliant/db"
)

// 20261024024221_chats_worktree_binding_index was versioned below
// 20261024035148, which prod applied first from a release branch. The migrator
// refuses a binary embedding a version the schema has passed, so the index was
// re-versioned to 20261024170635 (IF NOT EXISTS). These tests boot the current
// binary's Migrate over both databases that history left behind.

const (
	prodSchemaVersion    = 20261024035148 // prod: 035148 applied, 024221 never ran
	devSchemaVersion     = 20261024052601 // dev/e2e: main's head before the re-version
	oldWorktreeIndexFile = "20261024024221_chats_worktree_binding_index.up.sql"
	oldWorktreeIndexSQL  = "CREATE INDEX CONCURRENTLY idx_chats_worktree_id ON chats (worktree_id) WHERE worktree_id IS NOT NULL;\n"
	worktreeIndexName    = "idx_chats_worktree_id"
)

// embeddedUpTo is the embedded migration set cut at version (inclusive), plus
// extra files, as the filesystem an older binary would have embedded.
func embeddedUpTo(t *testing.T, version uint, extra map[string]string) fs.FS {
	t.Helper()
	entries, err := fs.ReadDir(forgedb.Source(), forgedb.SourceDir)
	require.NoError(t, err)
	out := fstest.MapFS{}
	for _, e := range entries {
		v, err := strconv.ParseUint(strings.SplitN(e.Name(), "_", 2)[0], 10, 64)
		if err != nil || uint(v) > version {
			continue
		}
		body, err := fs.ReadFile(forgedb.Source(), path.Join(forgedb.SourceDir, e.Name()))
		require.NoError(t, err)
		out[path.Join(forgedb.SourceDir, e.Name())] = &fstest.MapFile{Data: body}
	}
	for name, body := range extra {
		out[path.Join(forgedb.SourceDir, name)] = &fstest.MapFile{Data: []byte(body)}
	}
	return out
}

func requireWorktreeIndex(t *testing.T, dsn string) {
	t.Helper()
	var n int
	require.NoError(t, openTestDSN(t, dsn).QueryRow(
		`SELECT count(*) FROM pg_indexes WHERE tablename = 'chats' AND indexname = $1`, worktreeIndexName).Scan(&n))
	require.Equal(t, 1, n, "chats has %s", worktreeIndexName)
}

func TestMigrateFromProdStateAppliesTheReversionedWorktreeIndex(t *testing.T) {
	t.Parallel()
	dsn := newEmptyTestDB(t)
	raw := openTestDSN(t, dsn)
	_, err := migratekit.Apply(context.Background(), raw, embeddedUpTo(t, prodSchemaVersion, nil), forgedb.SourceDir)
	require.NoError(t, err)
	require.Equal(t, uint(prodSchemaVersion), migrationState(t, raw).Version)

	_, err = migrate(dsn)
	require.NoError(t, err, "the current binary migrates prod's schema")
	require.Equal(t, latestEmbedded(t), migrationState(t, raw).Version)
	requireWorktreeIndex(t, dsn)
}

func TestMigrateFromDevStateKeepsTheAlreadyBuiltWorktreeIndex(t *testing.T) {
	t.Parallel()
	dsn := newEmptyTestDB(t)
	raw := openTestDSN(t, dsn)
	old := embeddedUpTo(t, devSchemaVersion, map[string]string{oldWorktreeIndexFile: oldWorktreeIndexSQL})
	_, err := migratekit.Apply(context.Background(), raw, old, forgedb.SourceDir)
	require.NoError(t, err)
	require.Equal(t, uint(devSchemaVersion), migrationState(t, raw).Version)
	requireWorktreeIndex(t, dsn)

	_, err = migrate(dsn)
	require.NoError(t, err, "the current binary migrates a database that ran the old 024221 file")
	require.Equal(t, latestEmbedded(t), migrationState(t, raw).Version)
	requireWorktreeIndex(t, dsn)

	_, err = migrate(dsn)
	require.NoError(t, err, "a second boot is a no-op")
}

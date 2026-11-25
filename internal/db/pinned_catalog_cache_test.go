// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pinnedCatalogOfSize(n int) *PinnedCatalog {
	s := strings.Repeat("x", n)
	return &PinnedCatalog{Workflows: &s}
}

// The cache holds at most its byte budget, evicting the least recently used
// pin first, and never holds a catalog larger than the whole budget.
func TestPinnedCatalogCacheEvictsLeastRecentlyUsedByBytes(t *testing.T) {
	now := time.Now()
	c := newPinnedCatalogCache(3*(1000+256), time.Hour)
	c.add("a", pinnedCatalogOfSize(1000), now)
	c.add("b", pinnedCatalogOfSize(1000), now)
	c.add("c", pinnedCatalogOfSize(1000), now)
	_, _, ok := c.get("a", now) // a is now the most recently used
	require.True(t, ok)
	c.add("d", pinnedCatalogOfSize(1000), now)

	for pin, want := range map[string]bool{"a": true, "b": false, "c": true, "d": true} {
		_, _, ok := c.get(pin, now)
		assert.Equal(t, want, ok, "pin %s", pin)
	}
	assert.LessOrEqual(t, c.bytes, c.maxBytes)

	c.add("huge", pinnedCatalogOfSize(10_000), now)
	_, _, ok = c.get("huge", now)
	assert.False(t, ok, "a catalog larger than the cache is not kept")
	_, _, ok = c.get("d", now)
	assert.True(t, ok, "and evicts nothing")

	c.remove("d")
	_, _, ok = c.get("d", now)
	assert.False(t, ok)
	assert.Equal(t, int64(2*(1000+256)), c.bytes)
}

// A hit is due a last_used_at bump once touchEvery has passed since the last.
func TestPinnedCatalogCacheTouchIsDueAfterTheInterval(t *testing.T) {
	now := time.Now()
	c := newPinnedCatalogCache(1<<20, time.Hour)
	c.add("a", pinnedCatalogOfSize(10), now)
	_, due, _ := c.get("a", now.Add(59*time.Minute))
	assert.False(t, due)
	_, due, _ = c.get("a", now.Add(time.Hour))
	assert.True(t, due)
	c.touched("a", now.Add(time.Hour))
	_, due, _ = c.get("a", now.Add(90*time.Minute))
	assert.False(t, due)
}

func newPinnedCatalogTestProject(t *testing.T, repo *Repo) string {
	t.Helper()
	now := time.Now().UTC()
	userID, projectID := "user-"+uuid.NewString(), "project-"+uuid.NewString()
	require.NoError(t, repo.CreateProject(context.Background(), &Project{
		ID: projectID, UserID: userID, Name: "p", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return projectID
}

func snapshotLastUsed(t *testing.T, repo *Repo, pin string) time.Time {
	t.Helper()
	var at time.Time
	require.NoError(t, repo.ORM().Bun().NewRaw(`SELECT last_used_at FROM workflow_catalog_snapshots WHERE id = ?`, pin).
		Scan(context.Background(), &at))
	return at
}

// A pinned catalog is read from the database once per process: later reads
// are served from the Repo's cache without a statement (the row can be gone
// and the read still answers), and its last_used_at is not rewritten on every
// read. Once a bump is due, the bump finds the row gone and the pin reads as
// not stored.
func TestGetPinnedProjectCatalogServesRepeatReadsFromTheCache(t *testing.T) {
	repo := NewTestRepo(t)
	defer repo.Close()
	ctx := context.Background()
	projectID := newPinnedCatalogTestProject(t, repo)

	pin, err := repo.PinProjectCatalog(ctx, CatalogPinScope{ProjectID: projectID})
	require.NoError(t, err)
	stale := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)
	_, err = repo.ORM().Exec(ctx, `UPDATE workflow_catalog_snapshots SET last_used_at = $1 WHERE id = $2`, stale, pin)
	require.NoError(t, err)

	// Pinning the same catalog again stores nothing new...
	again, err := repo.PinProjectCatalog(ctx, CatalogPinScope{ProjectID: projectID})
	require.NoError(t, err)
	require.Equal(t, pin, again)
	// ...and reading it rewrites nothing.
	for range 3 {
		_, found, err := repo.GetPinnedProjectCatalog(ctx, pin)
		require.NoError(t, err)
		require.True(t, found)
	}
	assert.True(t, snapshotLastUsed(t, repo, pin).Equal(stale), "a cached pin's last_used_at is bumped at most hourly")

	_, err = repo.ORM().Exec(ctx, `DELETE FROM workflow_catalog_snapshots WHERE id = $1`, pin)
	require.NoError(t, err)
	_, found, err := repo.GetPinnedProjectCatalog(ctx, pin)
	require.NoError(t, err)
	assert.True(t, found, "served from the cache: no statement read the row")

	repo.pinnedCatalogs().touchEvery = 0
	_, found, err = repo.GetPinnedProjectCatalog(ctx, pin)
	require.NoError(t, err)
	assert.False(t, found, "the due bump finds the row gone")
	_, _, cached := repo.pinnedCatalogs().get(pin, time.Now())
	assert.False(t, cached, "and forgets the pin")
}

// A due bump on a cache hit keeps a pin in use from the retention prune.
func TestGetPinnedProjectCatalogBumpsLastUsedWhenDue(t *testing.T) {
	repo := NewTestRepo(t)
	defer repo.Close()
	ctx := context.Background()
	projectID := newPinnedCatalogTestProject(t, repo)
	pin, err := repo.PinProjectCatalog(ctx, CatalogPinScope{ProjectID: projectID})
	require.NoError(t, err)
	stale := time.Now().UTC().Add(-48 * time.Hour)
	_, err = repo.ORM().Exec(ctx, `UPDATE workflow_catalog_snapshots SET last_used_at = $1 WHERE id = $2`, stale, pin)
	require.NoError(t, err)

	repo.pinnedCatalogs().touchEvery = 0
	_, found, err := repo.GetPinnedProjectCatalog(ctx, pin)
	require.NoError(t, err)
	require.True(t, found)
	assert.WithinDuration(t, time.Now(), snapshotLastUsed(t, repo, pin), time.Minute)
}

// A read inside a transaction never fills the cache: the transaction may be
// reading a pin it stored itself, and may roll back.
func TestGetPinnedProjectCatalogInATransactionDoesNotFillTheCache(t *testing.T) {
	repo := NewTestRepo(t)
	defer repo.Close()
	ctx := context.Background()
	projectID := newPinnedCatalogTestProject(t, repo)

	var pin string
	require.NoError(t, repo.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		if pin, err = repo.PinProjectCatalog(ctx, CatalogPinScope{ProjectID: projectID}); err != nil {
			return err
		}
		_, found, err := repo.GetPinnedProjectCatalog(ctx, pin)
		require.True(t, found)
		return err
	}))
	_, _, cached := repo.pinnedCatalogs().get(pin, time.Now())
	assert.False(t, cached)

	_, found, err := repo.GetPinnedProjectCatalog(ctx, pin)
	require.NoError(t, err)
	require.True(t, found)
	_, _, cached = repo.pinnedCatalogs().get(pin, time.Now())
	assert.True(t, cached, "a read outside one does")
}

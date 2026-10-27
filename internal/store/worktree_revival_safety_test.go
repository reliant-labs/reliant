package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/stretchr/testify/require"
)

func TestRevivalAtomicallyRefusesSavedCatalogAndNonLandedArchives(t *testing.T) {
	for _, kind := range []string{"not-landed", "legacy-snapshot", "catalog-capturing", "catalog-removed"} {
		t.Run(kind, func(t *testing.T) {
			env := storetest.New(t)
			ctx := context.Background()
			c := landedFixture(t, env, "safe-revival")
			id := *c.WorktreeID
			ok, err := store.ArchiveLandedWorktree(ctx, env.ORM, id, time.Now())
			require.NoError(t, err)
			require.True(t, ok)
			switch kind {
			case "not-landed":
				require.NoError(t, env.Repo.MergeWorktreeCleanupMetadata(ctx, id, func(m *core.CleanupMetadata, _ bool) bool { m.ArchivedBecause = ""; return true }))
			case "legacy-snapshot":
				require.NoError(t, env.Repo.MergeWorktreeCleanupMetadata(ctx, id, func(m *core.CleanupMetadata, _ bool) bool { m.SnapshotRefs = []string{"saved"}; return true }))
			default:
				phase := "capturing"
				if kind == "catalog-removed" {
					phase = "removed"
				}
				mustExecConv(t, env, `INSERT INTO worktree_recoveries (id,owner_user_id,source_worktree_id,project_id,daemon_id,capture_id,fence,activity_generation,observation_id,manifest_digest,idempotency_key,phase) VALUES ('capture','test-user',$1,'test-project','daemon','capture-id','fence',0,'obs','digest','once',$2)`, id, phase)
				// Dirty/untracked state lives in the canonical checkout catalog, not
				// CleanupMetadata.SnapshotRefs. Multiple repos and a completed finalizer
				// must not become branch-only revival authorization.
				for _, rel := range []string{"api", "web"} {
					mustExecConv(t, env, `INSERT INTO worktree_recovery_checkouts (recovery_id,repo_id,rel,remote_url,recovery_ref,commit_oid,manifest_digest,descriptor_json) VALUES ('capture',$1,$1,$2,$3,'oid','digest','{"dirty":true,"untracked":true}')`, rel, "https://example.invalid/"+rel, "refs/recovery/"+rel)
				}
			}
			before, err := env.Repo.GetWorktree(ctx, id)
			require.NoError(t, err)
			// Call the store directly: no stale caller-side eligibility check can
			// authorize revival after the locked row/catalog says otherwise.
			fence, err := store.ReviveArchivedWorktree(ctx, env.ORM, id)
			require.Error(t, err)
			require.Empty(t, fence)
			after, err := env.Repo.GetWorktree(ctx, id)
			require.NoError(t, err)
			require.Equal(t, before.DeletedAt, after.DeletedAt)
			require.Equal(t, before.CleanupMetadata, after.CleanupMetadata)
		})
	}
}

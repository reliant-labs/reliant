package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/stretchr/testify/require"
)

func TestArchiveRecoverableIdleAndFenceSafeRevival(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	chat := landedFixture(t, env, "idle-retire")
	id := *chat.WorktreeID
	ok, err := store.ArchiveRecoverableIdleWorktree(ctx, env.ORM, id, time.Now().Add(-2*time.Hour))
	require.NoError(t, err)
	require.False(t, ok, "fresh db activity must preserve workspace")
	ok, err = store.ArchiveRecoverableIdleWorktree(ctx, env.ORM, id, time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	wt, err := env.Repo.GetWorktree(ctx, id)
	require.NoError(t, err)
	require.Equal(t, core.ArchivedBecauseRecoverableIdle, wt.CleanupMetadata.ArchivedBecause)
	fence, err := store.ReviveArchivedWorktree(ctx, env.ORM, id)
	require.NoError(t, err)
	require.NotEmpty(t, fence)
	again, err := store.ReviveArchivedWorktree(ctx, env.ORM, id)
	require.NoError(t, err)
	require.Equal(t, fence, again, "all batches deliver pending retirement until acknowledged")
}
func TestArchiveRecoverableIdleNeverOverwritesSavedRecoveryProvenance(t *testing.T) {
	for _, kind := range []string{"snapshot", "pending-fence", "catalog"} {
		t.Run(kind, func(t *testing.T) {
			env := storetest.New(t)
			ctx := context.Background()
			chat := landedFixture(t, env, "saved-idle")
			id := *chat.WorktreeID
			switch kind {
			case "snapshot":
				require.NoError(t, env.Repo.MergeWorktreeCleanupMetadata(ctx, id, func(m *core.CleanupMetadata, _ bool) bool { m.SnapshotRefs = []string{"saved"}; return true }))
			case "pending-fence":
				require.NoError(t, env.Repo.MergeWorktreeCleanupMetadata(ctx, id, func(m *core.CleanupMetadata, _ bool) bool { m.RetireFence = "123"; return true }))
			case "catalog":
				mustExecConv(t, env, `INSERT INTO worktree_recoveries (id,owner_user_id,source_worktree_id,project_id,daemon_id,capture_id,fence,activity_generation,observation_id,manifest_digest,idempotency_key) VALUES ('saved','test-user',$1,'test-project','daemon','capture','123',0,'obs','digest','key')`, id)
			}
			ok, err := store.ArchiveRecoverableIdleWorktree(ctx, env.ORM, id, time.Now().Add(time.Hour))
			require.NoError(t, err)
			require.False(t, ok)
			wt, err := env.Repo.GetWorktree(ctx, id)
			require.NoError(t, err)
			require.Nil(t, wt.DeletedAt)
		})
	}
}

func TestArchiveRecoverableIdleExcludesSpawnedThreadAndPausedRuns(t *testing.T) {
	for _, status := range []core.WorkflowStatus{core.Pending(), core.Active(), core.Paused()} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			env := storetest.New(t)
			ctx := context.Background()
			chat := landedFixture(t, env, "parent-a")
			child := &core.Worktree{ID: "child-b", ProjectID: storetest.SeedProjectID, Name: "child-b", Path: "/tmp/child-b", Branch: "child"}
			require.NoError(t, env.Repo.CreateWorktree(ctx, child))
			require.NoError(t, env.Repo.CreateWorkflow(ctx, newWorkflow("wf", chat.ID, nil, status, time.Now())))
			mustExecConv(t, env, `INSERT INTO threads (id,chat_id,created_at,origin,status,worktree_id) VALUES ('child',$1,NOW(),'spawn',2,$2)`, chat.ID, child.ID)
			ok, err := store.ArchiveRecoverableIdleWorktree(ctx, env.ORM, child.ID, time.Now().Add(time.Hour))
			require.NoError(t, err)
			require.False(t, ok)
			wt, err := env.Repo.GetWorktree(ctx, child.ID)
			require.NoError(t, err)
			require.Nil(t, wt.DeletedAt)
		})
	}
}

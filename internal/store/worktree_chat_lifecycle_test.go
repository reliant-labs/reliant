package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/stretchr/testify/require"
)

func boundFixture(t *testing.T, env *storetest.Env, id string, main bool) *core.Chat {
	t.Helper()
	ctx := context.Background()
	wt := &core.Worktree{ID: id, ProjectID: storetest.SeedProjectID, Name: id, Path: "/tmp/" + id, Branch: id, IsMain: main}
	require.NoError(t, env.Repo.CreateWorktree(ctx, wt))
	c := convChat("chat-"+id, time.Now().UTC())
	c.WorktreeID = &wt.ID
	require.NoError(t, env.Repo.CreateChat(ctx, c))
	return c
}

func TestGetLiveChatIDForWorktree(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	c := boundFixture(t, env, "bound", false)
	// Older bound chats must remain visible regardless of unrelated project size.
	for i := 0; i < 1001; i++ {
		require.NoError(t, env.Repo.CreateChat(ctx, convChat(fmt.Sprintf("unrelated-%d", i), time.Now().UTC())))
	}
	id, err := env.Repo.GetLiveChatIDForWorktree(ctx, *c.WorktreeID)
	require.NoError(t, err)
	require.Equal(t, c.ID, id)
	id, err = env.Repo.GetLiveChatIDForWorktree(ctx, "unused")
	require.NoError(t, err)
	require.Empty(t, id)
	require.NoError(t, env.Repo.UpdateChatState(ctx, c.ID, core.ChatStateArchived, "test"))
	id, err = env.Repo.GetLiveChatIDForWorktree(ctx, *c.WorktreeID)
	require.NoError(t, err)
	require.Empty(t, id)
}

// Pending, active and paused/resumable executions exclude every chat/thread
// workspace binding. Completed runs and unbound workspaces are idle.
func TestListWorktreeIDsWithRunningChat(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for id, status := range map[string]*core.WorkflowStatus{
		"active": ptr(core.Active()), "pending": ptr(core.Pending()),
		"done": ptr(core.Completed()), "paused": ptr(core.Paused()), "nochat": nil,
	} {
		if status == nil {
			wt := &core.Worktree{ID: id, ProjectID: storetest.SeedProjectID, Name: id, Path: "/tmp/" + id, Branch: id}
			require.NoError(t, env.Repo.CreateWorktree(ctx, wt))
			continue
		}
		c := boundFixture(t, env, id, false)
		require.NoError(t, env.Repo.CreateWorkflow(ctx, newWorkflow("wf-"+id, c.ID, nil, *status, now)))
	}
	got, err := env.Repo.ListWorktreeIDsWithRunningChat(ctx, []string{"active", "pending", "done", "paused", "nochat", "missing"})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"active": true, "pending": true, "paused": true}, got)
	got, err = env.Repo.ListWorktreeIDsWithRunningChat(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestRunningChatExcludesSpawnedThreadWorkspaceOverride(t *testing.T) {
	for _, status := range []core.WorkflowStatus{core.Pending(), core.Active(), core.Paused()} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			env := storetest.New(t)
			ctx := context.Background()
			chat := boundFixture(t, env, "chat-workspace-a", false)
			child := &core.Worktree{ID: "thread-workspace-b", ProjectID: storetest.SeedProjectID, Name: "child", Path: "/tmp/child", Branch: "child"}
			require.NoError(t, env.Repo.CreateWorktree(ctx, child))
			require.NoError(t, env.Repo.CreateWorkflow(ctx, newWorkflow("wf-parent", chat.ID, nil, status, time.Now().UTC())))
			mustExecConv(t, env, `INSERT INTO threads (id, chat_id, created_at, origin, status, worktree_id) VALUES ('spawn-child', $1, NOW(), 'spawn', 2, $2)`, chat.ID, child.ID)
			got, err := env.Repo.ListWorktreeIDsWithRunningChat(ctx, []string{*chat.WorktreeID, child.ID})
			require.NoError(t, err)
			require.Equal(t, map[string]bool{*chat.WorktreeID: true, child.ID: true}, got)
		})
	}
}

func TestChatWorktreeArchiveSymmetry(t *testing.T) {
	for _, main := range []bool{false, true} {
		t.Run(fmt.Sprintf("main=%v", main), func(t *testing.T) {
			env := storetest.New(t)
			ctx := context.Background()
			c := boundFixture(t, env, "symmetry", main)
			require.NoError(t, env.Repo.UpdateChatState(ctx, c.ID, core.ChatStateArchived, "test"))
			wt, err := env.Repo.GetWorktree(ctx, *c.WorktreeID)
			require.NoError(t, err)
			if main {
				require.Nil(t, wt.DeletedAt)
			} else {
				require.NotNil(t, wt.DeletedAt)
			}
			require.NoError(t, env.Repo.UpdateChatState(ctx, c.ID, core.ChatStateIdle, "test"))
			wt, err = env.Repo.GetWorktree(ctx, *c.WorktreeID)
			require.NoError(t, err)
			require.Nil(t, wt.DeletedAt)
		})
	}
}

func TestChatArchiveSharedWorktree(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	a := boundFixture(t, env, "shared", false)
	b := convChat("second", time.Now().UTC())
	b.WorktreeID = a.WorktreeID
	require.NoError(t, env.Repo.CreateChat(ctx, b))
	require.NoError(t, env.Repo.UpdateChatState(ctx, a.ID, core.ChatStateArchived, "test"))
	wt, err := env.Repo.GetWorktree(ctx, *a.WorktreeID)
	require.NoError(t, err)
	require.Nil(t, wt.DeletedAt)
	require.NoError(t, env.Repo.UpdateChatState(ctx, b.ID, core.ChatStateArchived, "test"))
	wt, err = env.Repo.GetWorktree(ctx, *a.WorktreeID)
	require.NoError(t, err)
	require.NotNil(t, wt.DeletedAt)
}

func TestChatWorktreeTransitionFailureRollsBack(t *testing.T) {
	for _, archive := range []bool{true, false} {
		t.Run(fmt.Sprintf("archive=%v", archive), func(t *testing.T) {
			env := storetest.New(t)
			ctx := context.Background()
			c := boundFixture(t, env, "failure", false)
			before, after := core.ChatStateIdle, core.ChatStateArchived
			if !archive {
				require.NoError(t, env.Repo.UpdateChatState(ctx, c.ID, core.ChatStateArchived, "test"))
				before, after = after, before
			}
			_, err := env.SQL.Exec(`CREATE FUNCTION reject_worktree_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END $$;
CREATE TRIGGER reject_worktree_update BEFORE UPDATE ON worktrees FOR EACH ROW EXECUTE FUNCTION reject_worktree_update()`)
			require.NoError(t, err)
			require.Error(t, env.Repo.UpdateChatState(ctx, c.ID, after, "test"))
			got, err := env.Repo.GetChat(ctx, c.ID)
			require.NoError(t, err)
			require.Equal(t, before, got.State)
			wt, err := env.Repo.GetWorktree(ctx, *c.WorktreeID)
			require.NoError(t, err)
			require.Equal(t, !archive, wt.DeletedAt != nil)
		})
	}
}

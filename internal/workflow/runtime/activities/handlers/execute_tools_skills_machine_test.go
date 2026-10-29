// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	cfgpkg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/configadapter"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// The skill tool reads the skills of the machine the call runs on, routed as
// tool dispatch routes it: the chat's pin, overridden by an explicit node or
// workflow selector, and by a thread's own worktree on its owning machine.
// Two machines synced different skills of one name.
func TestSkillToolReadsTheSkillsOfTheMachineTheCallRunsOn(t *testing.T) {
	t.Parallel()

	h := NewIdempotencyTestHelper(t)
	t.Cleanup(h.Cleanup)
	ctx := context.Background()
	repo := h.Repo()

	userID := "user-" + uuid.NewString()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), userID)
	a, b := uuid.NewString(), uuid.NewString()
	for _, id := range []string{a, b} {
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: userID}))
	}
	for daemonID, body := range map[string]string{a: "Deploy from machine A.", b: "Deploy from machine B."} {
		sj, err := cfgpkg.EncodeCatalogJSON([]cfgpkg.StoredSkill{{SkillPath: "house-deploy", Name: "house-deploy", Description: "How we deploy", Scope: "project", Body: body}})
		require.NoError(t, err)
		_, err = repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{
			ProjectID: project.ID, DaemonID: daemonID, ProjectSkillsJSON: sj, PushedAt: time.Now().UTC(),
		}, true)
		require.NoError(t, err)
	}
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, userID)
	_, err := repo.PinChatDaemonIfUnset(ctx, chat.ID, a)
	require.NoError(t, err)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	provider := cfgpkg.NewCachedStoredConfigProvider(configadapter.NewRepoConfigStore(repo), repo, cfgpkg.DefaultParsedConfigCacheBytes)
	activity := NewExecuteToolsActivity(repo, serverToolExecutor(repo)).WithConfigProvider(provider)
	load := func(thread string, selector *activitytypes.DaemonSelector) string {
		t.Helper()
		callID := "call-" + uuid.NewString()
		var output ExecuteToolsOutput
		require.NoError(t, h.ExecuteActivity(activity.Execute, ExecuteToolsInput{
			ChatID: chat.ID, Thread: thread, DaemonSelector: selector,
			ToolCalls: []message.ToolCall{{ID: callID, Name: tools.ToolSkill, Input: `{"action":"load","path":"house-deploy"}`}},
		}, &output))
		results := resultsByID(output.GetToolResults())
		require.Contains(t, results, callID)
		require.False(t, results[callID].GetIsError(), results[callID].GetContent())
		return results[callID].GetContent()
	}

	require.Contains(t, load(chat.ID, nil), "Deploy from machine A.", "the chat's pin")
	require.Contains(t, load(chat.ID, &activitytypes.DaemonSelector{ID: b}), "Deploy from machine B.", "an explicit selector beats the pin")

	// A spawned thread bound to a worktree checked out on B runs there, so
	// it reads B's skills even under the chat's pin to A.
	worktreeID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{
		ID: worktreeID, Name: "feature", Path: t.TempDir(), Branch: "feature", BaseBranch: "main", ProjectID: project.ID,
		Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), DaemonID: &b, CreatedAt: now, UpdatedAt: now,
	}))
	thread := "thread-" + uuid.NewString()
	_, err = repo.CreateThread(ctx, &db.Thread{ID: thread, ChatID: chat.ID, WorktreeID: &worktreeID})
	require.NoError(t, err)
	h.CreateTestUserMessage(ctx, chat.ID, thread)
	require.Contains(t, load(thread, nil), "Deploy from machine B.", "the thread's worktree owner beats the inherited pin")
}

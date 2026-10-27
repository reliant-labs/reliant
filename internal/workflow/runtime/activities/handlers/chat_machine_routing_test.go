package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// An in-flight run from before launch-time pinning has no session selector.
// The migration (or another send) can pin its chat, but the run's snapshotted
// inputs never change. Registering a CI-only cloud machine must not move its
// workspace check OR its tools to that new, empty disk.
func TestExecuteTools_LiveChatStaysOnMachineWhenSecondCloudRegisters(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	user, project, chat := uuid.NewString(), uuid.NewString(), uuid.NewString()
	dir := t.TempDir()
	h.CreateTestProjectWithPath(ctx, project, user, dir)
	h.CreateTestChat(ctx, chat, project, user)
	original := "ws-ws-2aab1465"
	require.NoError(t, h.Repo().UpdateChatActiveDaemon(ctx, chat, &original))

	register := func(id string, updated time.Time) {
		t.Helper()
		managed := "managed"
		require.NoError(t, h.Repo().UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: user, DaemonType: &managed}))
		// Fix the ordering without a sleep; real registration refreshes updated_at.
		_, err := h.sqlDB.ExecContext(ctx, `UPDATE daemons SET updated_at = $1 WHERE id = $2`, updated, id)
		require.NoError(t, err)
		now := time.Now()
		require.NoError(t, h.Repo().UpsertDaemonAttachment(ctx, &db.DaemonAttachment{
			DaemonID: id, UserID: user, Source: db.DaemonAttachmentSourceInbound,
			AttachedAt: now, LastStreamActivity: now,
		}))
	}
	register(original, time.Now().Add(-time.Minute))
	router := toolexec.NewNATSDaemonRouter(nil, toolexec.WithDatabase(h.Repo()))
	executor := &daemonLikeExecutor{}
	run := func() {
		t.Helper()
		executor.requests, executor.selectors = nil, nil
		var output ExecuteToolsOutput
		err := h.ExecuteActivity(NewExecuteToolsActivity(h.Repo(), executor).Execute, ExecuteToolsInput{
			ChatID: chat, Thread: chat, ProjectPath: dir,
			ToolCalls: []message.ToolCall{bashCall("pwd")},
			// Deliberately no DaemonSelector: this is an already-running workflow.
		}, &output)
		require.NoError(t, err)
		require.Len(t, output.ToolResults, 1)
		require.False(t, output.ToolResults[0].GetIsError(), output.ToolResults[0].GetContent())
		require.Len(t, executor.requests, 1)
		require.Len(t, executor.selectors, 1)
		for name, selector := range map[string]*toolexec.DaemonSelector{
			"workspace check": executor.selectors[0],
			"tool dispatch":   executor.requests[0].DaemonSelector,
		} {
			id, err := router.ResolveDaemonIDForSelector(ctx, user, selector)
			require.NoError(t, err)
			assert.Equal(t, original, id, "%s must use the durable chat pin", name)
		}
	}
	run()
	register("ws-ws-e164d47d", time.Now())
	defaultID, err := router.ResolveDaemonIDForSelector(ctx, user, nil)
	require.NoError(t, err)
	require.Equal(t, "ws-ws-e164d47d", defaultID, "prove default resolution now chooses the new cloud machine")
	run()
	stored, err := h.Repo().GetChat(ctx, chat)
	require.NoError(t, err)
	assert.Equal(t, original, *stored.ActiveDaemonID)
}

package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/db/dbtest"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cancelling or backgrounding a tool call is a user action taken from the UI.
// Before this slice it produced only a transient chat_updates event, so a
// reload showed the call as if nothing had happened. These tests read the row
// back after the RPC returns — the reload's view.

func TestCancelToolCall_PersistsCancelledStatus(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	f := dbtest.SeedToolCall(t, ctx, repo, `{"command":"sleep 600"}`)

	svc := NewToolCallService(repo, nil, &toolexectest.Recorder{})
	resp, err := svc.CancelToolCall(ctx, connect.NewRequest(&reliantv1.CancelToolCallRequest{
		ToolCallId: f.ToolCallID,
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)

	call, err := repo.GetToolCall(ctx, f.ToolCallID)
	require.NoError(t, err, "cancelling must leave a durable row, not just an event")
	assert.Equal(t, core.ToolCallStatusCancelled, call.Status)
	assert.Equal(t, f.ChatID, call.ChatID)
	assert.Equal(t, "bash", call.ToolName)
	require.NotNil(t, call.CompletedAt, "a cancelled call is terminal and will never produce a result")

	// This handler already looked up the block and message to get here, so the
	// attribution columns are free — and worth having.
	require.NotNil(t, call.MessageID)
	assert.Equal(t, f.MessageID, *call.MessageID)
	require.NotNil(t, call.ThreadID)
	assert.Equal(t, f.ThreadID, *call.ThreadID)
	assert.JSONEq(t, `{"command":"sleep 600"}`, string(call.Input))
}

func TestConvertToBackground_PersistsBackgroundedStatus(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	f := dbtest.SeedToolCall(t, ctx, repo, `{"command":"npm run dev"}`)

	svc := NewToolCallService(repo, nil, &toolexectest.Recorder{})
	resp, err := svc.ConvertToBackground(ctx, connect.NewRequest(&reliantv1.ConvertToBackgroundRequest{
		ToolCallId: f.ToolCallID,
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)

	call, err := repo.GetToolCall(ctx, f.ToolCallID)
	require.NoError(t, err, "backgrounding must leave a durable row")
	assert.Equal(t, core.ToolCallStatusBackgrounded, call.Status)
	assert.Nil(t, call.CompletedAt, "a backgrounded call is still running elsewhere")
	assert.JSONEq(t, `{"command":"npm run dev"}`, string(call.Input))
}

// The handler runs after the execution path has already written the call, so
// it must update that row in place rather than blanking the timing the
// activity recorded.
func TestCancelToolCall_PreservesExistingExecutionFields(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	f := dbtest.SeedToolCall(t, ctx, repo, `{"command":"sleep 600"}`)

	// The execute_tools activity got there first.
	startedAt := time.Now().UTC()
	require.NoError(t, repo.UpsertToolCall(ctx, &core.ToolCall{
		ID:          f.ToolCallID,
		ChatID:      f.ChatID,
		ToolName:    "bash",
		Status:      core.ToolCallStatusExecuting,
		StartedAt:   &startedAt,
		RequestedAt: startedAt,
		CreatedAt:   startedAt,
		UpdatedAt:   startedAt,
	}))

	svc := NewToolCallService(repo, nil, &toolexectest.Recorder{})
	_, err := svc.CancelToolCall(ctx, connect.NewRequest(&reliantv1.CancelToolCallRequest{
		ToolCallId: f.ToolCallID,
	}))
	require.NoError(t, err)

	call, err := repo.GetToolCall(ctx, f.ToolCallID)
	require.NoError(t, err)
	assert.Equal(t, core.ToolCallStatusCancelled, call.Status)
	require.NotNil(t, call.StartedAt, "the activity's started_at must survive a later cancel")
	assert.Equal(t, startedAt.Unix(), call.RequestedAt.Unix(),
		"requested_at belongs to the first write and must not move")

	calls, err := repo.ListToolCallsByChat(ctx, f.ChatID)
	require.NoError(t, err)
	require.Len(t, calls, 1, "the handler must update the existing row, not add one")
}

// A repeated cancel (double-click, retried request) must converge.
func TestCancelToolCall_IsIdempotent(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	f := dbtest.SeedToolCall(t, ctx, repo, `{"command":"sleep 600"}`)

	svc := NewToolCallService(repo, nil, &toolexectest.Recorder{})
	for i := 0; i < 3; i++ {
		_, err := svc.CancelToolCall(ctx, connect.NewRequest(&reliantv1.CancelToolCallRequest{
			ToolCallId: f.ToolCallID,
		}))
		require.NoError(t, err)
	}

	calls, err := repo.ListToolCallsByChat(ctx, f.ChatID)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, core.ToolCallStatusCancelled, calls[0].Status)
}

// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/stretchr/testify/require"
)

// spawnCancelTemporalClient records signals. Embeds client.Client so any other
// SDK call panics rather than silently succeeding — the previous version of
// this fix called TerminateWorkflow, which can never work for a spawn, and
// tests that passed a nil client could not catch it.
type spawnCancelTemporalClient struct {
	client.Client

	signals   []recordedSignal
	signalErr error
}

type recordedSignal struct {
	workflowID string
	name       string
	arg        interface{}
}

func (c *spawnCancelTemporalClient) SignalWorkflow(
	_ context.Context, workflowID, _ string, signalName string, arg interface{},
) error {
	c.signals = append(c.signals, recordedSignal{workflowID: workflowID, name: signalName, arg: arg})
	return c.signalErr
}

// seedCancellableSpawn creates a spawn tool call whose child workflow is
// running — the state a user is in when they click cancel on a spawn card.
func seedCancellableSpawn(
	t *testing.T,
	repo *db.Repo,
	ctx context.Context,
	childStatus core.WorkflowStatus,
) (toolCallID, childWorkflowID string) {
	t.Helper()
	now := time.Now().UTC()

	chatID := uuid.New().String()
	threadID := chatID
	cwID := uuid.New().String()
	messageID := uuid.New().String()
	toolCallID = "toolu_" + uuid.New().String()[:8]
	childWorkflowID = uuid.New().String()

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "cancel spawn", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: threadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: cwID, ThreadID: threadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID: messageID, ChatID: chatID, Ordinal: 1, Seq: 1, ThreadID: threadID,
		ContextWindowID: cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID: uuid.New().String(), MessageID: messageID, Position: 0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: &toolCallID,
		ToolName:   ptr.Of("spawn"),
		Version:    ptr.Of(1),
		CreatedAt:  now, UpdatedAt: now,
	}))

	// The child workflow doing the actual work.
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: childWorkflowID, ChatID: chatID, WorkflowName: "builtin://agent",
		Thread: childWorkflowID, Status: childStatus, CreatedAt: now,
	}))

	// The durable row linking the call to that workflow.
	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID: toolCallID, ChatID: chatID, MessageID: &messageID, ThreadID: &threadID,
		ToolName: "spawn", Status: core.ToolCallStatusExecuting,
		ChildWorkflowID: &childWorkflowID,
		RequestedAt:     now, CreatedAt: now, UpdatedAt: now,
	}))

	return toolCallID, childWorkflowID
}

// Cancelling a spawn must SIGNAL THE PARENT workflow.
//
// A spawn is not a Temporal execution of its own — executeSpawnInline runs it
// as a goroutine inside the parent — so there is nothing to terminate. The
// previous version of this fix called TerminateWorkflow(child_workflow_id) and
// failed every time with "workflow not found for ID", because that id names a
// thread and a DB row. The failure was best-effort, so the tool call was still
// marked cancelled: the UI said "cancelled" while the spawn wrote another
// twelve messages over the following seventeen minutes.
func TestCancelToolCall_SignalsParentToCancelSpawn(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	toolCallID, childThreadID := seedCancellableSpawn(t, repo, ctx, core.WorkflowStatusRunning)

	tc := &spawnCancelTemporalClient{}
	svc := NewToolCallService(repo, tc, &fakeDaemonRouter{})

	resp, err := svc.CancelToolCall(ctx, connect.NewRequest(&reliantv1.CancelToolCallRequest{
		ToolCallId: toolCallID,
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)

	require.Len(t, tc.signals, 1, "cancelling a spawn must reach the parent that is running it")
	require.Equal(t, v2.CancelThreadSignalName, tc.signals[0].name)

	sig, ok := tc.signals[0].arg.(v2.CancelThreadSignal)
	require.True(t, ok, "signal payload must be a CancelThreadSignal")
	require.Equal(t, childThreadID, sig.Thread)
	require.Equal(t, toolCallID, sig.ToolCallID,
		"the tool call id is the only handle the UI has; the spawn is addressed by both")
}

// If the signal cannot be delivered the spawn is still running, so the RPC must
// fail rather than report a cancellation that did not happen.
func TestCancelToolCall_SignalFails_DoesNotClaimCancelled(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	toolCallID, childThreadID := seedCancellableSpawn(t, repo, ctx, core.WorkflowStatusRunning)

	tc := &spawnCancelTemporalClient{signalErr: errors.New("workflow not found")}
	svc := NewToolCallService(repo, tc, &fakeDaemonRouter{})

	_, err := svc.CancelToolCall(ctx, connect.NewRequest(&reliantv1.CancelToolCallRequest{
		ToolCallId: toolCallID,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))

	wf, wfErr := repo.GetWorkflow(ctx, childThreadID)
	require.NoError(t, wfErr)
	require.Equal(t, core.WorkflowStatusRunning, wf.Status,
		"a spawn that is still running must not be recorded as cancelled")
}

// Once the signal is delivered, the spawn's DB row is reconciled to CANCELLED
// so a reload agrees with what the user asked for.
func TestCancelToolCall_ReconcilesSpawnWorkflowRow(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	toolCallID, childThreadID := seedCancellableSpawn(t, repo, ctx, core.WorkflowStatusRunning)

	svc := NewToolCallService(repo, &spawnCancelTemporalClient{}, &fakeDaemonRouter{})
	_, err := svc.CancelToolCall(ctx, connect.NewRequest(&reliantv1.CancelToolCallRequest{
		ToolCallId: toolCallID,
	}))
	require.NoError(t, err)

	wf, err := repo.GetWorkflow(ctx, childThreadID)
	require.NoError(t, err)
	require.Equal(t, core.WorkflowStatusCancelled, wf.Status)
}

// A non-spawn tool call has no child workflow, and cancelling it must not
// reach for one. This pins the blast radius: cancel stays scoped to the one
// tool, which is why it was narrowed away from cancelling the chat workflow in
// the first place.
func TestCancelToolCall_RegularToolTouchesNoWorkflow(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	chatID := uuid.New().String()
	threadID := chatID
	cwID := uuid.New().String()
	messageID := uuid.New().String()
	toolCallID := "toolu_" + uuid.New().String()[:8]
	chatWorkflowID := uuid.New().String()

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "regular tool", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: threadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: cwID, ThreadID: threadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID: messageID, ChatID: chatID, Ordinal: 1, Seq: 1, ThreadID: threadID,
		ContextWindowID: cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID: uuid.New().String(), MessageID: messageID, Position: 0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: &toolCallID,
		ToolName:   ptr.Of("bash"),
		Version:    ptr.Of(1),
		CreatedAt:  now, UpdatedAt: now,
	}))
	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID: toolCallID, ChatID: chatID, MessageID: &messageID, ThreadID: &threadID,
		ToolName: "bash", Status: core.ToolCallStatusExecuting,
		RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	}))
	// The chat's own workflow, which a tool cancel must never touch.
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: chatWorkflowID, ChatID: chatID, WorkflowName: "builtin://agent",
		Thread: threadID, Status: core.WorkflowStatusRunning, CreatedAt: now,
	}))

	svc := NewToolCallService(repo, nil, &fakeDaemonRouter{})
	_, err = svc.CancelToolCall(ctx, connect.NewRequest(&reliantv1.CancelToolCallRequest{
		ToolCallId: toolCallID,
	}))
	require.NoError(t, err)

	wf, err := repo.GetWorkflow(ctx, chatWorkflowID)
	require.NoError(t, err)
	require.Equal(t, core.WorkflowStatusRunning, wf.Status,
		"cancelling one tool must not stop the chat, nor its siblings")
}

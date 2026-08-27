// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// absorbTestTemporalClient reports whatever execution status the test needs and
// accepts workflow starts. Embeds client.Client so any other SDK call SendMessage
// makes is a panic rather than a silent no-op.
type absorbTestTemporalClient struct {
	client.Client
	status enums.WorkflowExecutionStatus
	exists bool
}

func (c *absorbTestTemporalClient) DescribeWorkflowExecution(
	_ context.Context, workflowID, _ string,
) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	if !c.exists {
		return nil, connect.NewError(connect.CodeNotFound, assertNotFound{})
	}
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Status:    c.status,
			Execution: &commonpb.WorkflowExecution{WorkflowId: workflowID, RunId: "run-1"},
		},
	}, nil
}

func (c *absorbTestTemporalClient) ExecuteWorkflow(
	_ context.Context, options client.StartWorkflowOptions, _ interface{}, _ ...interface{},
) (client.WorkflowRun, error) {
	return &fakeWorkflowRun{id: options.ID, runID: "run-" + options.ID}, nil
}

// Sending to a live thread rings the thread-wake doorbell, so this fake must
// accept a signal. These tests are about what SendMessage persists, not about
// the wake — see chat_send_thread_wake_test.go for that — so it is dropped.
func (c *absorbTestTemporalClient) SignalWorkflow(
	_ context.Context, _, _ string, _ string, _ interface{},
) error {
	return nil
}

type assertNotFound struct{}

func (assertNotFound) Error() string { return "workflow not found" }

// absorbFixture is a chat whose root thread is the one the user talks to, with
// a workflow row the test moves between statuses.
type absorbFixture struct {
	chatID       string
	rootThreadID string
}

func setupAbsorbFixture(t *testing.T, repo *db.Repo, userID string, status db.WorkflowStatus) (context.Context, absorbFixture) {
	t.Helper()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	now := time.Now().UTC()

	projectID := "test-project-absorb-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "Absorb Mailbox Test",
		Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	chatID := uuid.NewString()
	rootThreadID := chatID
	workflowName := "builtin://agent"
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: userID, Title: "Absorb Test", ProjectID: projectID,
		State: db.ChatStateIdle, WorkflowID: &rootThreadID, WorkflowName: &workflowName,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{
		ID: rootThreadID, ChatID: chatID, Origin: db.ThreadOriginMain,
		Status: db.ThreadStatusRunning, CreatedAt: now,
	})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{
		ID: uuid.NewString(), ThreadID: rootThreadID, Sequence: 0, CreatedAt: now,
	})
	require.NoError(t, err)
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: rootThreadID, ChatID: chatID, WorkflowName: workflowName,
		Thread: rootThreadID, Status: status, CreatedAt: now,
	}))

	return ctx, absorbFixture{chatID: chatID, rootThreadID: rootThreadID}
}

// sendMessageRequest builds a SendMessage for the chat's own root thread with a
// mock model, so validateWorkflowInputs does not need real provider keys.
func sendMessageRequest(t *testing.T, chatID, content string) *connect.Request[reliantv1.SendMessageRequest] {
	t.Helper()
	return connect.NewRequest(&reliantv1.SendMessageRequest{
		ChatId: chatID,
		Messages: []*reliantv1.InputMessage{{
			Role:    reliantv1.MessageRole_MESSAGE_ROLE_USER,
			Content: content,
		}},
		WorkflowParams: map[string]*structpb.Value{
			"model": mustStructValue(t, map[string]interface{}{"id": "mock"}),
		},
	})
}

// transcriptBodies returns every message body in the chat, in the seq order the
// transcript renders (queries in messages.sql all ORDER BY seq).
func transcriptBodies(t *testing.T, ctx context.Context, repo *db.Repo, chatID string) []string {
	t.Helper()
	msgs, err := repo.ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)

	bodies := make([]string, 0, len(msgs))
	for _, m := range msgs {
		blocks, err := repo.ListContentBlocks(ctx, m.ID)
		require.NoError(t, err)
		for _, b := range blocks {
			if b.Content != nil {
				bodies = append(bodies, *b.Content)
			}
		}
	}
	return bodies
}

// TestSendMessage_NonRunningWorkflowLeavesMailboxForCallLLM pins the single
// delivery boundary: starting a new run must not move queued messages into the
// transcript. The run's first call_llm drains and frames them before reading
// history, exactly like every subsequent turn.
func TestSendMessage_NonRunningWorkflowLeavesMailboxForCallLLM(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Completed())
	temporal := &absorbTestTemporalClient{exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_COMPLETED}
	service := &ChatService{
		database:   repo,
		tempClient: temporal,
		runs:       runs.NewService(repo, temporal, nil),
	}

	base := time.Now().UTC().Add(-time.Hour)
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.rootThreadID, "queued first", base)
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.rootThreadID, "queued second", base.Add(time.Minute))

	resp, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "typed last"))
	require.NoError(t, err)
	require.NotEmpty(t, resp.Msg.MessageId)

	assert.Equal(t, []string{"typed last"}, transcriptBodies(t, ctx, repo, fx.chatID),
		"SendMessage must not duplicate call_llm's mailbox delivery")

	remaining, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	require.Len(t, remaining, 2, "queued messages must remain for the run's first call_llm drain")
	assert.Equal(t, "queued first", remaining[0].Body)
	assert.Equal(t, "queued second", remaining[1].Body)
}

// TestSendMessage_RunningWorkflowLeavesMailboxAlone is the boundary of the fix.
// A genuinely running agent drains its own mailbox at its next loop step, with
// the envelope framing that tells the model those messages arrived mid-turn.
// Claiming them here would reorder them AND defeat the mailbox's purpose.
func TestSendMessage_RunningWorkflowLeavesMailboxAlone(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	temporal := &absorbTestTemporalClient{exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING}
	service := &ChatService{
		database:   repo,
		tempClient: temporal,
		runs:       runs.NewService(repo, temporal, nil),
	}

	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.rootThreadID,
		"queued for the live agent", time.Now().UTC().Add(-time.Minute))

	// No workflow params: a running send with params would signal the live
	// workflow, which is a different code path than the one under test.
	_, err := service.SendMessage(ctx, connect.NewRequest(&reliantv1.SendMessageRequest{
		ChatId: fx.chatID,
		Messages: []*reliantv1.InputMessage{{
			Role:    reliantv1.MessageRole_MESSAGE_ROLE_USER,
			Content: "sent while running",
		}},
	}))
	require.NoError(t, err)

	remaining, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	require.Len(t, remaining, 1, "a running agent's mailbox must be left for its own drain boundary")
	assert.Equal(t, "queued for the live agent", remaining[0].Body)

	assert.Equal(t, []string{"sent while running"}, transcriptBodies(t, ctx, repo, fx.chatID),
		"only the new message is persisted; the queued one arrives through the drain with its envelope")
}

// TestSendMessage_LeavesEveryMailboxKindQueued verifies SendMessage does not
// become a second deliverer for either human or peer-agent mailbox rows.
func TestSendMessage_LeavesEveryMailboxKindQueued(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Completed())
	temporal := &absorbTestTemporalClient{exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_COMPLETED}
	service := &ChatService{
		database:   repo,
		tempClient: temporal,
		runs:       runs.NewService(repo, temporal, nil),
	}

	require.NoError(t, repo.EnqueueAgentMessage(ctx, &db.AgentMessage{
		ID:           uuid.NewString(),
		ChatID:       fx.chatID,
		FromThreadID: fx.rootThreadID,
		ToThreadID:   fx.rootThreadID,
		Kind:         core.AgentMessageKindMessage, // agent-to-agent
		Body:         "peer agent instruction",
		Status:       core.AgentMessageStatusQueued,
		CreatedAt:    time.Now().UTC().Add(-time.Minute),
	}))
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.rootThreadID,
		"human instruction", time.Now().UTC().Add(-time.Minute))

	_, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "typed last"))
	require.NoError(t, err)

	assert.Equal(t, []string{"typed last"}, transcriptBodies(t, ctx, repo, fx.chatID))

	remaining, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	require.Len(t, remaining, 2, "both mailbox kinds must stay queued for call_llm")
	assert.Equal(t, core.AgentMessageKindMessage, remaining[0].Kind)
	assert.Equal(t, core.AgentMessageKindHumanMessage, remaining[1].Kind)
}

// TestSendMessage_PausedResumeLeavesMailboxForCallLLM covers the other
// non-running entry point. Resume mechanics remain separate from queue delivery;
// the resumed run drains on its next call_llm.
func TestSendMessage_PausedResumeLeavesMailboxForCallLLM(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Paused())
	temporal := &absorbTestTemporalClient{exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING}
	service := &ChatService{
		database:   repo,
		tempClient: temporal,
		// No pause controller: the resume panics after the messages are
		// saved, which is exactly the seam this test wants.
		runs: runs.NewService(repo, temporal, nil),
	}

	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.rootThreadID,
		"queued while paused", time.Now().UTC().Add(-time.Minute))

	// The run-lifecycle service is nil, so the resume itself panics after the
	// messages are saved. The transaction under test has already committed by
	// then; recover and assert on what it persisted.
	func() {
		defer func() { _ = recover() }()
		_, _ = service.SendMessage(ctx, connect.NewRequest(&reliantv1.SendMessageRequest{
			ChatId: fx.chatID,
			Messages: []*reliantv1.InputMessage{{
				Role:    reliantv1.MessageRole_MESSAGE_ROLE_USER,
				Content: "resume now",
			}},
		}))
	}()

	assert.Equal(t, []string{"resume now"}, transcriptBodies(t, ctx, repo, fx.chatID),
		"resume persists the new message but does not deliver the mailbox")

	remaining, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	assert.Equal(t, "queued while paused", remaining[0].Body)
}

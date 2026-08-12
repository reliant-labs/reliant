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

// TestSendMessage_AbsorbsQueuedMailboxAheadOfNewMessage is the ordering bug.
//
// The user queued two messages into a thread whose agent never reached another
// drain boundary, then typed a new one. seq is allocated at SAVE time, so the
// old behaviour (save the new message, start a run, let the run drain the
// mailbox later) gave the OLDER text HIGHER seq values and rendered it beneath
// the message sent after it — "my most recent message shows first". Absorbing
// the mailbox before saving the new message is what restores send order.
func TestSendMessage_AbsorbsQueuedMailboxAheadOfNewMessage(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.WorkflowStatusCompleted)
	service := &ChatService{
		database:   repo,
		tempClient: &absorbTestTemporalClient{exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_COMPLETED},
	}

	base := time.Now().UTC().Add(-time.Hour)
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.rootThreadID, "queued first", base)
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.rootThreadID, "queued second", base.Add(time.Minute))

	resp, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "typed last"))
	require.NoError(t, err)
	require.NotEmpty(t, resp.Msg.MessageId)

	assert.Equal(t, []string{"queued first", "queued second", "typed last"},
		transcriptBodies(t, ctx, repo, fx.chatID),
		"queued messages must land ahead of the message the user typed after them")

	remaining, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	assert.Empty(t, remaining,
		"absorbed rows are claimed by a DELETE ... RETURNING, so they must not remain queued for a later drain")
}

// TestSendMessage_RunningWorkflowLeavesMailboxAlone is the boundary of the fix.
// A genuinely running agent drains its own mailbox at its next loop step, with
// the envelope framing that tells the model those messages arrived mid-turn.
// Claiming them here would reorder them AND defeat the mailbox's purpose.
func TestSendMessage_RunningWorkflowLeavesMailboxAlone(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.WorkflowStatusRunning)
	service := &ChatService{
		database:   repo,
		tempClient: &absorbTestTemporalClient{exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING},
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

// TestSendMessage_LeavesPeerAgentMessagesQueued scopes the absorb to the human's
// own messages, exactly as ClaimQueuedAgentMessages does. A sub-agent's
// spawn_send is machine output that belongs in the drain's envelope framing, not
// in the transcript as if the user had typed it.
func TestSendMessage_LeavesPeerAgentMessagesQueued(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.WorkflowStatusCompleted)
	service := &ChatService{
		database:   repo,
		tempClient: &absorbTestTemporalClient{exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_COMPLETED},
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

	assert.Equal(t, []string{"human instruction", "typed last"},
		transcriptBodies(t, ctx, repo, fx.chatID),
		"only the human's own queued messages are absorbed into the transcript")

	remaining, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	require.Len(t, remaining, 1, "a peer agent's message must stay queued for the drain")
	assert.Equal(t, core.AgentMessageKindMessage, remaining[0].Kind)
}

// TestSendMessage_AbsorbsMailboxOnPausedResume covers the other non-running
// entry point: a paused run takes no loop steps either, so its mailbox is just
// as stale, and the resume saves its message inside a transaction the claim must
// join (a claim that committed without its saves would DELETE the user's words).
func TestSendMessage_AbsorbsMailboxOnPausedResume(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.WorkflowStatusPaused)
	service := &ChatService{
		database:     repo,
		tempClient:   &absorbTestTemporalClient{exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING},
		pauseService: nil,
	}

	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.rootThreadID,
		"queued while paused", time.Now().UTC().Add(-time.Minute))

	// pauseService is nil, so the resume itself panics after the messages are
	// saved. The transaction under test has already committed by then; recover
	// and assert on what it persisted.
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

	assert.Equal(t, []string{"queued while paused", "resume now"},
		transcriptBodies(t, ctx, repo, fx.chatID),
		"a paused resume must absorb the stale mailbox ahead of the resuming message")

	remaining, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	assert.Empty(t, remaining)
}

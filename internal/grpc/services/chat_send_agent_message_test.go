// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sendAgentMessageFixture is a chat with a root thread and a running
// (spawned) child thread -- the shape SendAgentMessage exists to target.
type sendAgentMessageFixture struct {
	chatID        string
	rootThreadID  string
	childThreadID string
}

func setupSendAgentMessageFixture(t *testing.T, repo *db.Repo, userID string) (context.Context, sendAgentMessageFixture) {
	t.Helper()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	now := time.Now().UTC()

	projectID := "test-project-sam-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "SendAgentMessage Test",
		Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	chatID := uuid.NewString()
	rootThreadID := chatID
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: userID, Title: "Test Chat", ProjectID: projectID,
		State: db.ChatStateIdle, WorkflowID: &rootThreadID,
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

	childThreadID := uuid.NewString()
	_, err = repo.CreateThread(ctx, &db.Thread{
		ID: childThreadID, ChatID: chatID, ParentThreadID: &rootThreadID,
		Origin: db.ThreadOriginSpawn, Status: db.ThreadStatusRunning, CreatedAt: now,
		WorkflowID: &childThreadID,
	})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{
		ID: uuid.NewString(), ThreadID: childThreadID, Sequence: 0, CreatedAt: now,
	})
	require.NoError(t, err)

	// Each thread's own workflow row. SendAgentMessage reads this to decide
	// whether there is a next agent turn to deliver into, because a thread's
	// own status never records "went idle" -- see the liveness comment in
	// SendAgentMessage. Both start RUNNING, matching a live agent.
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: rootThreadID, ChatID: chatID, WorkflowName: "builtin://agent",
		Thread: rootThreadID, Status: db.WorkflowStatusRunning, CreatedAt: now,
	}))
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: childThreadID, ChatID: chatID, WorkflowName: "builtin://agent",
		Thread: childThreadID, ParentID: &rootThreadID,
		Status: db.WorkflowStatusRunning, CreatedAt: now,
	}))

	return ctx, sendAgentMessageFixture{chatID: chatID, rootThreadID: rootThreadID, childThreadID: childThreadID}
}

// countAgentMessageRows counts every agent_messages row for a thread
// regardless of status, so a test can assert that a refusal inserted NOTHING
// (ListQueuedAgentMessagesForThread only sees status=queued, and would not
// notice a row written with some other status).
func countAgentMessageRows(t *testing.T, rawDB *sql.DB, ctx context.Context, threadID string) int {
	t.Helper()
	var count int
	require.NoError(t, rawDB.QueryRowContext(ctx,
		"SELECT count(*) FROM agent_messages WHERE to_thread_id = $1", threadID).Scan(&count))
	return count
}

func TestSendAgentMessage_QueuesForRunningThread(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	service := &ChatService{database: repo}

	resp, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId:   fx.chatID,
		ThreadId: fx.childThreadID,
		Message:  "check the logs before continuing",
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)
	assert.Contains(t, resp.Msg.Message, "Queued")

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	assert.Equal(t, fx.chatID, queued[0].ChatID)
	assert.Equal(t, fx.childThreadID, queued[0].ToThreadID)
	assert.Equal(t, core.AgentMessageKindHumanMessage, queued[0].Kind)
	assert.Equal(t, core.AgentMessageStatusQueued, queued[0].Status)
	assert.Equal(t, "check the logs before continuing", queued[0].Body)
}

// TestSendAgentMessage_RefusesIdleThread pins the core bug: a thread that is
// non-terminal but whose run has ended takes no loop steps, so it never
// reaches the drain boundary and the message would sit queued forever behind
// a receipt promising it would be read "at that agent's next turn".
//
// The thread status stays RUNNING here on purpose -- that is exactly the
// live-DB shape (every main thread is status=running with no completed_at,
// even for chats idle for weeks), and it is why the pre-existing terminal
// check does not catch this.
func TestSendAgentMessage_RefusesIdleThread(t *testing.T) {
	repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	// The run finished; the thread row is deliberately left at RUNNING.
	require.NoError(t, repo.UpdateWorkflowStatus(ctx, fx.childThreadID, db.WorkflowStatusCompleted))
	thread, err := repo.GetThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	require.Equal(t, db.ThreadStatusRunning, thread.Status,
		"fixture must reproduce the live shape: an idle agent still claiming thread status=running")

	before := countAgentMessageRows(t, rawDB, ctx, fx.childThreadID)
	require.Equal(t, 0, before)

	service := &ChatService{database: repo}
	resp, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId:   fx.chatID,
		ThreadId: fx.childThreadID,
		Message:  "nobody is listening for this",
	}))
	require.NoError(t, err, "an idle agent is an honest success:false, not a transport error")
	assert.False(t, resp.Msg.Success)
	assert.Contains(t, resp.Msg.Message, "isn't currently running")
	assert.Contains(t, resp.Msg.Message, "Send a normal message",
		"the refusal must tell the user what to do instead")

	// The core assertion: nothing was written. A queued row here is a message
	// that can never be delivered.
	assert.Equal(t, before, countAgentMessageRows(t, rawDB, ctx, fx.childThreadID),
		"an idle agent must never receive a row that can never be drained")
}

// TestSendAgentMessage_AcceptsAboutToRunThread covers the states that look
// idle but are not: a run that has not started yet (PENDING) and one that is
// paused and will resume (PAUSED). Both reach a drain boundary, so refusing
// them would destroy a message that would in fact have been delivered --
// strictly worse than the late delivery this fix exists to prevent.
func TestSendAgentMessage_AcceptsAboutToRunThread(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status db.WorkflowStatus
	}{
		{"pending run has its first loop step ahead of it", db.WorkflowStatusPending},
		{"paused run resumes and drains", db.WorkflowStatusPaused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, cleanup := db.SetupTestDB(t)
			t.Cleanup(cleanup)

			ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
			require.NoError(t, repo.UpdateWorkflowStatus(ctx, fx.childThreadID, tc.status))

			service := &ChatService{database: repo}
			resp, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
				ChatId:   fx.chatID,
				ThreadId: fx.childThreadID,
				Message:  "this will be drained when the loop runs",
			}))
			require.NoError(t, err)
			require.True(t, resp.Msg.Success, "a run that has not finished still has a next turn")

			queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.childThreadID)
			require.NoError(t, err)
			require.Len(t, queued, 1)
		})
	}
}

// TestSendAgentMessage_AcceptsThreadRevivedByNewTurn is the end-to-end shape
// of the "already finished" bug, exercised through the same repo call the
// workflow runtime makes when a new run starts.
//
// A chat's main thread is REUSED for every turn: SendMessage starts a fresh
// Temporal run under the same workflow ID and the same thread ID. The end of
// turn N stamps threads.status terminal, and before the fix the start of turn
// N+1 moved only the WORKFLOW row back to running -- nothing moved the
// thread. Every turn from the second onward therefore ran behind a thread row
// still reading completed, and SendAgentMessage refused to queue into a
// visibly-working agent with "This agent has already finished".
//
// ReviveThread (called by WorkflowStatusActivity's "started" arm) is what
// closes that gap, so queueing must succeed after it runs.
func TestSendAgentMessage_AcceptsThreadRevivedByNewTurn(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")

	// Turn N ended: both halves of the lifecycle went terminal.
	completedAt := time.Now().UTC()
	_, err := repo.UpdateThreadStatus(ctx, fx.rootThreadID, db.ThreadStatusCompleted, &completedAt)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateWorkflowStatus(ctx, fx.rootThreadID, db.WorkflowStatusCompleted))

	// Pre-fix, queueing here is refused -- correctly, since nothing is running.
	service := &ChatService{database: repo}
	idleResp, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: fx.rootThreadID, Message: "between turns",
	}))
	require.NoError(t, err)
	require.False(t, idleResp.Msg.Success)

	// Turn N+1 starts. This is what WorkflowStatusActivity's "started" arm
	// does: revive the thread, then move the workflow back to running.
	revived, err := repo.ReviveThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), revived, "the terminal main thread must be the row revived")
	require.NoError(t, repo.UpdateWorkflowStatus(ctx, fx.rootThreadID, db.WorkflowStatusRunning))

	thread, err := repo.GetThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	require.Equal(t, db.ThreadStatusRunning, thread.Status,
		"a thread executing a new turn must not still read as finished")
	require.Nil(t, thread.CompletedAt,
		"a revived thread must not keep the completed_at of the turn that ended")

	resp, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId:   fx.chatID,
		ThreadId: fx.rootThreadID,
		Message:  "steer the turn that is running right now",
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success,
		"queueing into the running turn must be accepted, got: %s", resp.Msg.Message)

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	assert.Equal(t, "steer the turn that is running right now", queued[0].Body)
}

func TestSendAgentMessage_RejectsCrossChatThread(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	// A second, unrelated chat with its own thread.
	_, otherFx := setupSendAgentMessageFixture(t, repo, "test-user")
	service := &ChatService{database: repo}

	_, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId:   fx.chatID,
		ThreadId: otherFx.childThreadID, // belongs to a different chat
		Message:  "should not be deliverable",
	}))
	require.Error(t, err)
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeNotFound, connectErr.Code())

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, otherFx.childThreadID)
	require.NoError(t, err)
	assert.Empty(t, queued, "a cross-chat thread must never receive a queued message")
}

func TestSendAgentMessage_RejectsTerminalThread(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	_, err := repo.UpdateThreadStatus(ctx, fx.childThreadID, db.ThreadStatusCompleted, nil)
	require.NoError(t, err)

	service := &ChatService{database: repo}
	resp, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId:   fx.chatID,
		ThreadId: fx.childThreadID,
		Message:  "too late",
	}))
	require.NoError(t, err, "a finished agent is a normal (non-error) response, matching spawn_send's honesty contract")
	assert.False(t, resp.Msg.Success)
	assert.Contains(t, resp.Msg.Message, "already finished")

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	assert.Empty(t, queued, "a finished thread must never silently receive a queued message")
}

func TestSendAgentMessage_WrongOwner(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	_, fx := setupSendAgentMessageFixture(t, repo, "user-a")
	ctxB := context.WithValue(context.Background(), auth.UserIDContextKey, "user-b")
	service := &ChatService{database: repo}

	_, err := service.SendAgentMessage(ctxB, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId:   fx.chatID,
		ThreadId: fx.childThreadID,
		Message:  "not yours to send",
	}))
	require.Error(t, err)
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeNotFound, connectErr.Code())
}

func TestListQueuedAgentMessages_ReturnsQueuedInSendOrderExcludingDelivered(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	service := &ChatService{database: repo}

	_, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: fx.childThreadID, Message: "first",
	}))
	require.NoError(t, err)
	_, err = service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: fx.childThreadID, Message: "second",
	}))
	require.NoError(t, err)
	_, err = service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: fx.childThreadID, Message: "delivered before list",
	}))
	require.NoError(t, err)

	// Deliver the third message before listing -- it must be excluded, and
	// must never resurface once drained.
	all, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	require.Len(t, all, 3)
	savedMsg, err := repo.SaveMessageToThread(ctx, fx.chatID, fx.childThreadID, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "delivered before list", nil, nil, nil)
	require.NoError(t, err)
	require.NoError(t, repo.MarkAgentMessagesDelivered(ctx, []string{all[2].ID}, time.Now(), savedMsg.ID))

	resp, err := service.ListQueuedAgentMessages(ctx, connect.NewRequest(&reliantv1.ListQueuedAgentMessagesRequest{
		ChatId: fx.chatID, ThreadId: fx.childThreadID,
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Messages, 2)
	assert.Equal(t, "first", resp.Msg.Messages[0].Body)
	assert.Equal(t, "second", resp.Msg.Messages[1].Body)
	assert.Equal(t, int32(core.AgentMessageKindHumanMessage), resp.Msg.Messages[0].SenderKind)
	assert.NotEmpty(t, resp.Msg.Messages[0].CreatedAt)
}

func TestListQueuedAgentMessages_RejectsCrossChatThread(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	_, otherFx := setupSendAgentMessageFixture(t, repo, "test-user")
	service := &ChatService{database: repo}

	_, err := service.ListQueuedAgentMessages(ctx, connect.NewRequest(&reliantv1.ListQueuedAgentMessagesRequest{
		ChatId: fx.chatID, ThreadId: otherFx.childThreadID,
	}))
	require.Error(t, err)
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeNotFound, connectErr.Code())
}

func TestListQueuedAgentMessages_WrongOwner(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	_, fx := setupSendAgentMessageFixture(t, repo, "user-a")
	ctxB := context.WithValue(context.Background(), auth.UserIDContextKey, "user-b")
	service := &ChatService{database: repo}

	_, err := service.ListQueuedAgentMessages(ctxB, connect.NewRequest(&reliantv1.ListQueuedAgentMessagesRequest{
		ChatId: fx.chatID, ThreadId: fx.childThreadID,
	}))
	require.Error(t, err)
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeNotFound, connectErr.Code())
}

func TestCancelQueuedAgentMessage_RemovesQueuedMessage(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	service := &ChatService{database: repo}

	_, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: fx.childThreadID, Message: "cancel me",
	}))
	require.NoError(t, err)

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1)

	resp, err := service.CancelQueuedAgentMessage(ctx, connect.NewRequest(&reliantv1.CancelQueuedAgentMessageRequest{
		ChatId: fx.chatID, MessageId: queued[0].ID,
	}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.Success)

	after, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	assert.Empty(t, after, "a subsequent list must no longer return the cancelled message")
}

func TestCancelQueuedAgentMessage_AlreadyDeliveredReturnsFailureAndKeepsRow(t *testing.T) {
	repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	service := &ChatService{database: repo}

	_, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: fx.childThreadID, Message: "already delivered",
	}))
	require.NoError(t, err)

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	msgID := queued[0].ID

	// Simulate the agent's drain winning the race before cancel arrives.
	savedMsg, err := repo.SaveMessageToThread(ctx, fx.chatID, fx.childThreadID, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "already delivered", nil, nil, nil)
	require.NoError(t, err)
	require.NoError(t, repo.MarkAgentMessagesDelivered(ctx, []string{msgID}, time.Now(), savedMsg.ID))

	resp, err := service.CancelQueuedAgentMessage(ctx, connect.NewRequest(&reliantv1.CancelQueuedAgentMessageRequest{
		ChatId: fx.chatID, MessageId: msgID,
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.Success, "an already-delivered message must never be reported as cancelled")
	assert.Contains(t, resp.Msg.Message, "Already delivered")

	// The row must still exist and stay delivered -- cancel must not delete
	// a delivered row, since delivered_message_id links it into the transcript.
	var status int32
	err = rawDB.QueryRowContext(ctx, "SELECT status FROM agent_messages WHERE id = $1", msgID).Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, int32(core.AgentMessageStatusDelivered), status)
}

func TestCancelQueuedAgentMessage_RejectsWrongOwnerAndCrossChat(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "user-a")
	service := &ChatService{database: repo}

	_, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: fx.childThreadID, Message: "protect me",
	}))
	require.NoError(t, err)
	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1)

	// Wrong owner: user-b doesn't own fx.chatID, so the chat-ownership check
	// itself must reject before even looking at the message.
	ctxB := context.WithValue(context.Background(), auth.UserIDContextKey, "user-b")
	_, err = service.CancelQueuedAgentMessage(ctxB, connect.NewRequest(&reliantv1.CancelQueuedAgentMessageRequest{
		ChatId: fx.chatID, MessageId: queued[0].ID,
	}))
	require.Error(t, err)
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeNotFound, connectErr.Code())

	// Cross-chat: caller owns a different chat, but tries to cancel a
	// message that belongs to fx.chatID. The DELETE's chat_id scoping means
	// this comes back as an honest success:false, not an error -- same
	// "already delivered" style receipt, since the row is simply untouched.
	_, otherFx := setupSendAgentMessageFixture(t, repo, "user-a")
	crossResp, err := service.CancelQueuedAgentMessage(ctx, connect.NewRequest(&reliantv1.CancelQueuedAgentMessageRequest{
		ChatId: otherFx.chatID, MessageId: queued[0].ID,
	}))
	require.NoError(t, err)
	assert.False(t, crossResp.Msg.Success, "a message_id from a different chat must never be cancellable")

	// The message must still be queued after both rejected attempts.
	after, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	require.Len(t, after, 1)
}

func TestSendAgentMessage_MissingFields(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	service := &ChatService{database: repo}

	cases := []*reliantv1.SendAgentMessageRequest{
		{ChatId: "", ThreadId: fx.childThreadID, Message: "x"},
		{ChatId: fx.chatID, ThreadId: "", Message: "x"},
		{ChatId: fx.chatID, ThreadId: fx.childThreadID, Message: ""},
		{ChatId: fx.chatID, ThreadId: fx.childThreadID, Message: "   "},
	}
	for _, req := range cases {
		_, err := service.SendAgentMessage(ctx, connect.NewRequest(req))
		require.Error(t, err)
		connectErr := new(connect.Error)
		require.ErrorAs(t, err, &connectErr)
		assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
	}
}

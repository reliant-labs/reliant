// Copyright (c) 2025 Reliant Labs
package services

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

// queueHumanMessage puts one human-sent entry in a thread's mailbox.
func queueHumanMessage(t *testing.T, repo *db.Repo, chatID, fromThreadID, toThreadID, body string, createdAt time.Time) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, repo.EnqueueAgentMessage(t.Context(), &db.AgentMessage{
		ID:           id,
		ChatID:       chatID,
		FromThreadID: fromThreadID,
		ToThreadID:   toThreadID,
		Kind:         core.AgentMessageKindHumanMessage,
		Body:         body,
		Status:       core.AgentMessageStatusQueued,
		CreatedAt:    createdAt,
	}))
	return id
}

// TestClaimQueuedAgentMessages_ClaimsWholeQueueInSendOrder is the "send all"
// path: one call takes every queued human message and returns them oldest
// first, leaving the mailbox empty.
func TestClaimQueuedAgentMessages_ClaimsWholeQueueInSendOrder(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	service := &ChatService{database: repo}

	base := time.Now().UTC().Add(-time.Hour)
	// Enqueued newest-first to prove the response order comes from
	// created_at, not from insertion or from DELETE's return order.
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.childThreadID, "third", base.Add(2*time.Minute))
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.childThreadID, "first", base)
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.childThreadID, "second", base.Add(time.Minute))

	resp, err := service.ClaimQueuedAgentMessages(ctx, connect.NewRequest(&reliantv1.ClaimQueuedAgentMessagesRequest{
		ChatId:   fx.chatID,
		ThreadId: fx.childThreadID,
	}))
	require.NoError(t, err)

	bodies := make([]string, len(resp.Msg.Messages))
	for i, m := range resp.Msg.Messages {
		bodies[i] = m.Body
	}
	assert.Equal(t, []string{"first", "second", "third"}, bodies,
		"claimed messages must come back in send order, oldest first")

	remaining, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	assert.Empty(t, remaining, "a whole-queue claim must empty the mailbox")
}

// TestClaimQueuedAgentMessages_ClaimsSingleMessage is the "send now" path:
// naming one message takes exactly that one and leaves the rest queued.
func TestClaimQueuedAgentMessages_ClaimsSingleMessage(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	service := &ChatService{database: repo}

	base := time.Now().UTC().Add(-time.Hour)
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.childThreadID, "keep me", base)
	targetID := queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.childThreadID, "take me", base.Add(time.Minute))

	resp, err := service.ClaimQueuedAgentMessages(ctx, connect.NewRequest(&reliantv1.ClaimQueuedAgentMessagesRequest{
		ChatId:    fx.chatID,
		ThreadId:  fx.childThreadID,
		MessageId: &targetID,
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Messages, 1)
	assert.Equal(t, "take me", resp.Msg.Messages[0].Body)

	remaining, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	require.Len(t, remaining, 1, "claiming one message must leave the others queued")
	assert.Equal(t, "keep me", remaining[0].Body)
}

// TestClaimQueuedAgentMessages_LosesToTheDrain is the race this endpoint
// exists to make unloseable, run in the order that used to be dangerous: the
// agent drains first, THEN the user clicks send-now.
//
// The claim returns nothing, and nothing is what the caller may resend. Under
// the old cancel-then-send flow the client could observe a stale queue and
// push the text again, putting the same message into the conversation twice.
// Here the empty result IS the verdict — there is no second call to disagree
// with the first.
func TestClaimQueuedAgentMessages_LosesToTheDrain(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	service := &ChatService{database: repo}

	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.childThreadID,
		"already gone", time.Now().UTC())

	// The agent reaches its step boundary first and drains the mailbox.
	drainActivity := handlers.NewDrainAgentMessagesActivity(repo, threads.NewService(repo))
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(drainActivity.Execute)
	val, err := env.ExecuteActivity(drainActivity.Execute, handlers.DrainAgentMessagesInput{
		ChatID: fx.chatID,
		Thread: fx.childThreadID,
	})
	require.NoError(t, err)
	var drainOut handlers.DrainAgentMessagesOutput
	require.NoError(t, val.Get(&drainOut))
	require.Equal(t, 1, drainOut.Count)

	// Only now does the user try to pull it back.
	resp, err := service.ClaimQueuedAgentMessages(ctx, connect.NewRequest(&reliantv1.ClaimQueuedAgentMessagesRequest{
		ChatId:   fx.chatID,
		ThreadId: fx.childThreadID,
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.Messages,
		"a message the agent already drained must not come back as claimable")
}

// TestClaimQueuedAgentMessages_LeavesPeerAgentMessages scopes the claim to the
// user's own messages. A spawn_send from a peer agent is not the human's to
// reclaim, and the UI never offered it as one.
func TestClaimQueuedAgentMessages_LeavesPeerAgentMessages(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	service := &ChatService{database: repo}

	require.NoError(t, repo.EnqueueAgentMessage(ctx, &db.AgentMessage{
		ID:           uuid.NewString(),
		ChatID:       fx.chatID,
		FromThreadID: fx.rootThreadID,
		ToThreadID:   fx.childThreadID,
		Kind:         core.AgentMessageKindMessage, // agent-to-agent
		Body:         "peer agent instruction",
		Status:       core.AgentMessageStatusQueued,
		CreatedAt:    time.Now().UTC(),
	}))
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.childThreadID,
		"human instruction", time.Now().UTC())

	resp, err := service.ClaimQueuedAgentMessages(ctx, connect.NewRequest(&reliantv1.ClaimQueuedAgentMessagesRequest{
		ChatId:   fx.chatID,
		ThreadId: fx.childThreadID,
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Messages, 1)
	assert.Equal(t, "human instruction", resp.Msg.Messages[0].Body)

	remaining, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	require.Len(t, remaining, 1, "a peer agent's message must stay queued")
	assert.Equal(t, core.AgentMessageKindMessage, remaining[0].Kind)
}

// TestClaimQueuedAgentMessages_RejectsCrossChatThread mirrors the ownership
// checks on SendAgentMessage: a thread in someone else's chat is NotFound,
// indistinguishable from one that does not exist.
func TestClaimQueuedAgentMessages_RejectsCrossChatThread(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	_, other := setupSendAgentMessageFixture(t, repo, "test-user")
	service := &ChatService{database: repo}

	_, err := service.ClaimQueuedAgentMessages(ctx, connect.NewRequest(&reliantv1.ClaimQueuedAgentMessagesRequest{
		ChatId:   fx.chatID,
		ThreadId: other.childThreadID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestClaimQueuedAgentMessages_WrongOwner(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	_, fx := setupSendAgentMessageFixture(t, repo, "owner-user")
	intruderCtx, _ := setupSendAgentMessageFixture(t, repo, "intruder-user")
	service := &ChatService{database: repo}

	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.childThreadID,
		"not yours", time.Now().UTC())

	_, err := service.ClaimQueuedAgentMessages(intruderCtx, connect.NewRequest(&reliantv1.ClaimQueuedAgentMessagesRequest{
		ChatId:   fx.chatID,
		ThreadId: fx.childThreadID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	remaining, err := repo.ListQueuedAgentMessagesForThread(t.Context(), fx.childThreadID)
	require.NoError(t, err)
	assert.Len(t, remaining, 1, "a rejected claim must not remove anything")
}

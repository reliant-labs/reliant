// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"testing"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

// agentMessageStatusOf reads a single mailbox row's status and delivery time
// directly, so a test can assert the row's FINAL state explicitly rather than
// inferring it from a queue listing (which only ever shows status=queued and
// would report an undelivered row and a delivered one identically: absent).
func agentMessageStatusOf(t *testing.T, rawDB *sql.DB, ctx context.Context, threadID string) (core.AgentMessageStatus, sql.NullTime) {
	t.Helper()
	var status int32
	var deliveredAt sql.NullTime
	require.NoError(t, rawDB.QueryRowContext(ctx,
		"SELECT status, delivered_at FROM agent_messages WHERE to_thread_id = $1", threadID,
	).Scan(&status, &deliveredAt))
	return core.AgentMessageStatus(status), deliveredAt
}

// TestSendAgentMessage_ThreadExitsBeforeNextBoundary is the regression test
// for the last hole in human message-queuing.
//
// The agent is genuinely running when the user queues a message, so every
// enqueue-time liveness check correctly accepts it -- and then the agent's
// loop exits before reaching another step boundary. Delivery only ever
// happens at a boundary (drainAgentMessagesAtBoundary), so the row is
// undeliverable from that moment on. Before the fix nothing marked it,
// nothing surfaced it and nothing retried it: it sat at queued with
// delivered_at NULL forever, while the user held a receipt saying it would
// be read at the agent's next turn.
//
// Reproduces the live-DB shape exactly: thread 5753b25c had two human
// messages queued at 00:06:31 and 00:06:51 and completed at 00:06:56.
func TestSendAgentMessage_ThreadExitsBeforeNextBoundary(t *testing.T) {
	repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	service := &ChatService{database: repo}

	// The user queues into a live agent. This must succeed -- the agent
	// really is running, which is precisely why no enqueue-time check can
	// close this race.
	resp, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId:   fx.chatID,
		ThreadId: fx.childThreadID,
		Message:  "queued while you were still running",
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1, "the message must be accepted into the mailbox of a live agent")

	// The loop now exits WITHOUT another boundary: no drain runs. The thread
	// reaches a terminal status through the ThreadStatus activity, which is
	// the single writer of terminal threads.status for every exit path
	// (completion, failure, cancellation, expiry).
	threadStatus := handlers.NewThreadStatusActivity(repo)
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(threadStatus.Execute)
	_, err = env.ExecuteActivity(threadStatus.Execute, handlers.ThreadStatusInput{
		ChatID:   fx.chatID,
		ThreadID: fx.childThreadID,
		Status:   "completed",
	})
	require.NoError(t, err)

	thread, err := repo.GetThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	require.True(t, core.ThreadStatusIsTerminal(thread.Status),
		"fixture must reproduce the bug: the loop has exited")

	// The core assertion. The row must be RESOLVED, not left pending.
	status, deliveredAt := agentMessageStatusOf(t, rawDB, ctx, fx.childThreadID)
	assert.Equal(t, core.AgentMessageStatusUndelivered, status,
		"a message the agent can never read must not be left claiming to be queued")
	assert.NotEqual(t, core.AgentMessageStatusQueued, status,
		"this is the bug: the row is stranded at queued forever")
	assert.False(t, deliveredAt.Valid,
		"an undelivered row must have no delivery time -- the NULL is the evidence no delivery happened")

	// And it must be gone from the pending queue the UI polls, so the user
	// stops being told it is about to be read.
	stillQueued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	assert.Empty(t, stillQueued, "a resolved row must no longer read as pending delivery")
}

// TestThreadStatus_ResolvesMailboxOnEveryTerminalPath pins that the fix is
// not tied to the happy path. A thread that fails, is cancelled, or expires
// strands its mailbox exactly as completion does, so all four terminal verbs
// must resolve it -- handled coherently in one place rather than patched per
// exit path.
func TestThreadStatus_ResolvesMailboxOnEveryTerminalPath(t *testing.T) {
	for _, verb := range []string{"completed", "failed", "cancelled", "expired"} {
		t.Run(verb, func(t *testing.T) {
			repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
			t.Cleanup(cleanup)

			ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
			service := &ChatService{database: repo}
			resp, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
				ChatId:   fx.chatID,
				ThreadId: fx.childThreadID,
				Message:  "queued just before the loop ended",
			}))
			require.NoError(t, err)
			require.True(t, resp.Msg.Success)

			threadStatus := handlers.NewThreadStatusActivity(repo)
			suite := &testsuite.WorkflowTestSuite{}
			env := suite.NewTestActivityEnvironment()
			env.RegisterActivity(threadStatus.Execute)
			_, err = env.ExecuteActivity(threadStatus.Execute, handlers.ThreadStatusInput{
				ChatID:   fx.chatID,
				ThreadID: fx.childThreadID,
				Status:   verb,
			})
			require.NoError(t, err)

			status, _ := agentMessageStatusOf(t, rawDB, ctx, fx.childThreadID)
			assert.Equal(t, core.AgentMessageStatusUndelivered, status,
				"a %s thread strands its mailbox exactly as a completed one does", verb)
		})
	}
}

// TestThreadStatus_LeavesQueueAloneWhileThreadRuns is the counterweight: the
// resolution must fire only on a terminal transition. A "started" update
// happens while the agent is very much alive and its queued messages are
// waiting for the next boundary -- resolving them there would destroy
// messages that were about to be delivered.
func TestThreadStatus_LeavesQueueAloneWhileThreadRuns(t *testing.T) {
	repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	service := &ChatService{database: repo}
	resp, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId:   fx.chatID,
		ThreadId: fx.childThreadID,
		Message:  "this one is still on its way",
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)

	threadStatus := handlers.NewThreadStatusActivity(repo)
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(threadStatus.Execute)
	_, err = env.ExecuteActivity(threadStatus.Execute, handlers.ThreadStatusInput{
		ChatID:   fx.chatID,
		ThreadID: fx.childThreadID,
		Status:   "started",
	})
	require.NoError(t, err)

	status, _ := agentMessageStatusOf(t, rawDB, ctx, fx.childThreadID)
	assert.Equal(t, core.AgentMessageStatusQueued, status,
		"a live agent's queued message must stay queued and be delivered at its next boundary")
}

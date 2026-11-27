// Copyright (c) 2025 Reliant Labs
package chat

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/api/enums/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/agentmailbox"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/questions"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/store"
)

// A message sent to a failed run follows the same rule as one sent to a paused
// run (chat_send_paused_mailbox_order_test.go): it lands after the messages
// already queued for its thread. Both recoveries — reset-and-replay, and the
// coarse restart at position when there is nothing to replay — saved it
// straight into the transcript, so the recovered run read it before the older
// queued messages, which its first call_llm delivers only then.

func newFailedSendService(repo *db.Repo, temporalExists bool) (*Service, *wakeTestTemporalClient, *questionSignalPause) {
	temporal := &wakeTestTemporalClient{absorbTestTemporalClient: absorbTestTemporalClient{
		exists: temporalExists, status: enums.WORKFLOW_EXECUTION_STATUS_FAILED,
	}}
	pause := &questionSignalPause{}
	return &Service{
		deps: Deps{Repo: repo, Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()), Temporal: temporal, Mailbox: agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}), AgentMessages: store.NewAgentMessageExtStore(repo.ORM())},
		runs: runs.New(runs.Deps{Repo: repo, Resolver: questions.NewFromRepo(repo), Chats: store.NewChatsWithActivityExtStore(repo.ORM()), Workflows: store.NewWorkflowExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Temporal: temporal, Pause: pause}),
	}, temporal, pause
}

func TestSendMessage_FailedRunSendIsDeliveredAfterTheQueuedMailbox(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Failed())
	service, _, pause := newFailedSendService(repo, true)

	base := time.Now().UTC().Add(-time.Hour)
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.rootThreadID, "queued first", base)
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.rootThreadID, "queued second", base.Add(time.Minute))

	req := sendMessageRequest(t, fx.chatID, "typed after the failure")
	clientID := uuid.NewString()
	req.Msg.ClientMessageId = &clientID
	resp, err := service.SendMessage(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, 1, pause.interruptResumes, "the run is recovered by reset-and-replay")
	assert.True(t, resp.Msg.Queued)
	assert.Equal(t, clientID, resp.Msg.MessageId)

	assert.Empty(t, only(transcriptBodies(t, ctx, repo, fx.chatID), "queued first", "queued second", "typed after the failure"),
		"nothing is written into history ahead of the queue")

	// The recovered run's next call_llm.
	drainThreadMailbox(t, repo, fx.chatID, fx.rootThreadID)
	assert.Equal(t,
		[]string{"queued first", "queued second", "typed after the failure"},
		only(transcriptBodies(t, ctx, repo, fx.chatID), "queued first", "queued second", "typed after the failure"),
		"the inbox is swept first, then the new message")
}

// The interrupted run left its thread terminal. A message queued for the
// reset run must not sit behind a terminal thread, which the reconciler's
// orphaned-mailbox sweep marks undelivered: the reset path returns without
// the new-run path's revive, so it revives the thread itself, before the reset.
func TestSendMessage_FailedRunResetRevivesTheThreadItQueuedTo(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Failed())
	service, _, pause := newFailedSendService(repo, true)

	failedAt := time.Now().UTC()
	_, err := repo.UpdateThreadStatus(ctx, fx.rootThreadID, db.ThreadStatusFailed, &failedAt)
	require.NoError(t, err)
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.rootThreadID, "queued first", failedAt.Add(-time.Minute))

	resp, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "typed after the failure"))
	require.NoError(t, err)
	require.Equal(t, 1, pause.interruptResumes, "the run is recovered by reset-and-replay")
	require.True(t, resp.Msg.Queued)

	thread, err := repo.GetThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	assert.Equal(t, db.ThreadStatusRunning, thread.Status,
		"the thread the message was queued to is revived, so the orphaned-mailbox sweep cannot take it")
}

// No replayable history (Temporal has no such execution): the new run started
// at the checkpoint drains the mailbox at its first call_llm, the new message
// last.
func TestSendMessage_FailedRunCoarseRestartQueuesBehindTheMailbox(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Failed())
	service, _, pause := newFailedSendService(repo, false)
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.rootThreadID, "queued first", time.Now().UTC().Add(-time.Minute))

	resp, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "typed after the failure"))
	require.NoError(t, err)
	assert.Zero(t, pause.interruptResumes, "nothing to replay")
	assert.Equal(t, "run-"+fx.rootThreadID, resp.Msg.RunId, "a new run was started at the checkpoint")
	assert.True(t, resp.Msg.Queued)

	assert.Empty(t, only(transcriptBodies(t, ctx, repo, fx.chatID), "queued first", "typed after the failure"))
	remaining, err := store.ListQueuedAgentMessagesForThread(ctx, repo.ORM(), fx.rootThreadID)
	require.NoError(t, err)
	require.Len(t, remaining, 2)
	assert.Equal(t, "queued first", remaining[0].Body)
	assert.Equal(t, "typed after the failure", remaining[1].Body)
	assert.Equal(t, resp.Msg.MessageId, remaining[1].ID)
}

// With nothing queued, the message is saved to the transcript as it always was.
func TestSendMessage_FailedRunSendWithEmptyMailboxIsSaved(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Failed())
	service, _, pause := newFailedSendService(repo, true)

	resp, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "go on"))
	require.NoError(t, err)
	assert.Equal(t, 1, pause.interruptResumes)
	assert.False(t, resp.Msg.Queued)
	assert.Equal(t, []string{"go on"}, only(transcriptBodies(t, ctx, repo, fx.chatID), "go on"))
}

// An answer to the question the failed run died parked on is NOT queued: it is
// delivered as that question's answer (signal.question.<id>), which the
// replayed run receives as the ask_question tool's result. A tool result must
// follow its tool call, so nothing can be read between them; the queued
// messages are delivered at the next call_llm, after the answer — exactly as
// for an answer given while the run was live.
func TestSendMessage_FailedRunQuestionAnswerStillResumesTheQuestion(t *testing.T) {
	t.Parallel()

	repo, ctx, fx, service, pause, questionID := setupFailedRunWithQuestion(t, func(fx absorbFixture) string { return fx.rootThreadID })
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.rootThreadID, "queued first", time.Now().UTC().Add(-time.Minute))

	resp, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "go with the minimal one"))
	require.NoError(t, err)
	assert.Equal(t, []string{"signal.question." + questionID}, pause.signals,
		"the message still answers the question its thread is parked on")
	assert.False(t, resp.Msg.Queued)
	q, err := repo.GetQuestionByID(ctx, questionID)
	require.NoError(t, err)
	assert.Equal(t, db.QuestionStatusResolved, q.Status)

	assert.Equal(t, []string{markResumeAnswer("go with the minimal one")},
		only(transcriptBodies(t, ctx, repo, fx.chatID), markResumeAnswer("go with the minimal one"), "queued first"))
	remaining, err := store.ListQueuedAgentMessagesForThread(ctx, repo.ORM(), fx.rootThreadID)
	require.NoError(t, err)
	require.Len(t, remaining, 1, "the queue is left for the next call_llm")
	assert.Equal(t, "queued first", remaining[0].Body)
}

// A repeat of a send whose first attempt queued the message and recovered the
// run (its response was lost) is answered with the first receipt. The row
// still says failed until the recovered run's first activity writes
// "started", and Temporal says running, so without recognising the queued
// earlier attempt the repeat was refused as a stuck run.
func TestSendMessage_FailedRunRepeatAfterQueuedRecoveryIsReplayed(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Failed())
	service, temporal, pause := newFailedSendService(repo, true)
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.rootThreadID, "queued first", time.Now().UTC().Add(-time.Minute))

	clientID := uuid.NewString()
	send := func() (string, bool, error) {
		req := sendMessageRequest(t, fx.chatID, "typed after the failure")
		req.Msg.ClientMessageId = &clientID
		resp, err := service.SendMessage(ctx, req)
		if err != nil {
			return "", false, err
		}
		return resp.Msg.MessageId, resp.Msg.Queued, nil
	}
	first, queued, err := send()
	require.NoError(t, err)
	require.True(t, queued)

	// The recovered run is open; its row has not caught up.
	require.NoError(t, repo.UpdateWorkflowStatus(ctx, fx.rootThreadID, db.Failed()))
	temporal.status = enums.WORKFLOW_EXECUTION_STATUS_RUNNING

	second, queued, err := send()
	require.NoError(t, err, "a repeat of an accepted send is not a stuck run")
	assert.Equal(t, first, second)
	assert.True(t, queued)
	assert.Equal(t, 1, pause.interruptResumes, "the repeat does not recover the run a second time")

	remaining, err := store.ListQueuedAgentMessagesForThread(ctx, repo.ORM(), fx.rootThreadID)
	require.NoError(t, err)
	assert.Len(t, remaining, 2, "queued once")
}

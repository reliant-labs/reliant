// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"fmt"
	"slices"
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
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/reliant-labs/reliant/internal/workflow/threadwake"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// Pausing a run and sending a message must not jump the queue. Messages the
// user queued while the run was working wait in the thread's mailbox for its
// next call_llm; a message sent to the paused run was saved straight into the
// transcript, so the resumed run read it FIRST and the older queued messages
// only after it. The owner's rule: pausing and sending sweeps the inbox first,
// then adds the new message.

// drainThreadMailbox runs the drain call_llm runs immediately before it reads
// history (DrainAgentMessagesActivity), in a Temporal test activity
// environment because Execute reads activity.GetLogger.
func drainThreadMailbox(t *testing.T, repo *db.Repo, chatID, threadID string) {
	t.Helper()
	drain := handlers.NewDrainAgentMessagesActivity(handlers.DrainAgentMessagesDeps{Store: repo, Mailbox: store.NewAgentMessageExtStore(repo.ORM()), Threads: threads.New(threads.Deps{Repo: repo, Messages: store.NewMessageExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM())})})
	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(drain.Execute)
	_, err := env.ExecuteActivity(drain.Execute, handlers.DrainAgentMessagesInput{ChatID: chatID, Thread: threadID})
	require.NoError(t, err)
}

// only keeps the bodies in want, in the order they appear: the drain also
// writes a hidden envelope around what it delivers, and resuming may add a
// hidden "params changed" note.
func only(bodies []string, want ...string) []string {
	var out []string
	for _, b := range bodies {
		if slices.Contains(want, b) {
			out = append(out, b)
		}
	}
	return out
}

func newPausedSendService(repo *db.Repo, pause runs.PauseController) (*Service, *wakeTestTemporalClient) {
	temporal := &wakeTestTemporalClient{absorbTestTemporalClient: absorbTestTemporalClient{
		exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
	}}
	return &Service{
		deps: Deps{Repo: repo, Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()), Temporal: temporal, Mailbox: agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}), AgentMessages: store.NewAgentMessageExtStore(repo.ORM())},
		runs: runs.New(runs.Deps{Repo: repo, Resolver: questions.NewFromRepo(repo), Chats: store.NewChatsWithActivityExtStore(repo.ORM()), Workflows: store.NewWorkflowExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Temporal: temporal, Pause: pause}),
	}, temporal
}

func TestSendMessage_PausedSendIsDeliveredAfterTheQueuedMailbox(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Paused())
	service, temporal := newPausedSendService(repo, succeedingPauseController{})

	base := time.Now().UTC().Add(-time.Hour)
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.rootThreadID, "queued first", base)
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.rootThreadID, "queued second", base.Add(time.Minute))

	req := sendMessageRequest(t, fx.chatID, "typed after pausing")
	clientID := uuid.NewString()
	req.Msg.ClientMessageId = &clientID
	resp, err := service.SendMessage(ctx, req)
	require.NoError(t, err)
	assert.True(t, resp.Msg.Queued, "the client shows it in the queue strip until the drain moves it")
	assert.Equal(t, clientID, resp.Msg.MessageId,
		"the queued row carries the client's id, so its optimistic copy and the row are one item")

	wakes := threadWakeSignals(temporal.signals)
	require.Len(t, wakes, 1, "the resumed thread is still woken")
	sig, ok := wakes[0].Arg.(v2.ThreadWakeSignal)
	require.True(t, ok)
	assert.Equal(t, fx.rootThreadID, sig.Thread)
	assert.Equal(t, threadwake.ReasonMailbox, sig.Reason, "the message is in the mailbox, not in history")

	// The resumed run's next call_llm.
	drainThreadMailbox(t, repo, fx.chatID, fx.rootThreadID)

	assert.Equal(t,
		[]string{"queued first", "queued second", "typed after pausing"},
		only(transcriptBodies(t, ctx, repo, fx.chatID), "queued first", "queued second", "typed after pausing"),
		"the inbox is swept first, then the new message, in the order they were typed")
	remaining, err := store.ListQueuedAgentMessagesForThread(ctx, repo.ORM(), fx.rootThreadID)
	require.NoError(t, err)
	assert.Empty(t, remaining)
}

// With nothing queued there is nothing to order against: the message is saved
// to the transcript, as it always was, and the receipt is not a queued one.
func TestSendMessage_PausedSendWithEmptyMailboxIsSaved(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Paused())
	service, _ := newPausedSendService(repo, succeedingPauseController{})

	resp, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "go on"))
	require.NoError(t, err)
	assert.False(t, resp.Msg.Queued)
	assert.Equal(t, []string{"go on"}, only(transcriptBodies(t, ctx, repo, fx.chatID), "go on"))
	remaining, err := store.ListQueuedAgentMessagesForThread(ctx, repo.ORM(), fx.rootThreadID)
	require.NoError(t, err)
	assert.Empty(t, remaining)
}

// Only the target thread's mailbox decides: a message queued for another
// thread of the chat is that thread's input, not something to order behind.
func TestSendMessage_PausedSendIgnoresOtherThreadsMailbox(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Paused())
	service, _ := newPausedSendService(repo, succeedingPauseController{})

	otherThread := uuid.NewString()
	_, err := repo.CreateThread(ctx, &db.Thread{
		ID: otherThread, ChatID: fx.chatID, Origin: db.ThreadOriginMain,
		Status: db.ThreadStatusRunning, CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, otherThread, "for the other agent", time.Now().UTC().Add(-time.Minute))

	resp, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "for the root"))
	require.NoError(t, err)
	assert.False(t, resp.Msg.Queued)
	assert.Equal(t, []string{"for the root"}, only(transcriptBodies(t, ctx, repo, fx.chatID), "for the root", "for the other agent"))
}

// A repeat of a paused send whose first attempt queued the message (its
// response was lost) finishes around that row: it is not queued twice, and
// the receipt names the same message.
func TestSendMessage_PausedSendRepeatQueuesOnce(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Paused())
	service, _ := newPausedSendService(repo, succeedingPauseController{})
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.rootThreadID, "queued first", time.Now().UTC().Add(-time.Minute))

	clientID := uuid.NewString()
	send := func() string {
		req := sendMessageRequest(t, fx.chatID, "typed after pausing")
		req.Msg.ClientMessageId = &clientID
		resp, err := service.SendMessage(ctx, req)
		require.NoError(t, err)
		assert.True(t, resp.Msg.Queued)
		return resp.Msg.MessageId
	}
	first := send()
	// The first resume marked the run active; a repeat finds it paused again
	// when its response was lost before the run moved on.
	require.NoError(t, repo.UpdateWorkflowStatus(ctx, fx.rootThreadID, db.Paused()))
	second := send()
	assert.Equal(t, first, second)

	remaining, err := store.ListQueuedAgentMessagesForThread(ctx, repo.ORM(), fx.rootThreadID)
	require.NoError(t, err)
	require.Len(t, remaining, 2, "the repeat must not queue the message a second time")
	assert.Equal(t, "queued first", remaining[0].Body)
	assert.Equal(t, "typed after pausing", remaining[1].Body)
	assert.Empty(t, only(transcriptBodies(t, ctx, repo, fx.chatID), "queued first", "typed after pausing"))
}

// lostRunPause is a paused run Temporal no longer has: SendMessage recovers by
// starting a new run that resumes at position.
type lostRunPause struct{ succeedingPauseController }

func (lostRunPause) ResumeWorkflow(_ context.Context, _, _ string, _ map[string]interface{}) error {
	return fmt.Errorf("resume: %w", workflow.ErrWorkflowNotFound)
}

// When the paused run is lost, the new run started for the message drains the
// mailbox at its first call_llm. The queued message must not also be written
// to the transcript, which would deliver it twice and ahead of the queue.
func TestSendMessage_PausedSendQueuedForALostRunIsLeftForTheNewRun(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Paused())
	service, _ := newPausedSendService(repo, lostRunPause{})
	queueHumanMessage(t, repo, fx.chatID, fx.rootThreadID, fx.rootThreadID, "queued first", time.Now().UTC().Add(-time.Minute))

	resp, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "typed after pausing"))
	require.NoError(t, err)
	assert.True(t, resp.Msg.Queued)
	assert.Equal(t, "run-"+fx.rootThreadID, resp.Msg.RunId, "a new run was started for the message")

	assert.Empty(t, only(transcriptBodies(t, ctx, repo, fx.chatID), "queued first", "typed after pausing"))
	remaining, err := store.ListQueuedAgentMessagesForThread(ctx, repo.ORM(), fx.rootThreadID)
	require.NoError(t, err)
	require.Len(t, remaining, 2)
	assert.Equal(t, "queued first", remaining[0].Body)
	assert.Equal(t, "typed after pausing", remaining[1].Body)
	assert.Equal(t, resp.Msg.MessageId, remaining[1].ID)
}

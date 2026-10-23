// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/temporal/signaltest"
)

// A send the client named is idempotent. The first attempt may have committed
// and lost its response (a dropped connection, a client timeout that cancelled
// the handler after the commit), and the client's Retry sends the same message
// again under the same id. These tests pin what that repeat does.

// startCountingTemporal counts run starts, so a test can say "no second run".
type startCountingTemporal struct {
	wakeTestTemporalClient
	mu     sync.Mutex
	starts int
}

func (c *startCountingTemporal) ExecuteWorkflow(
	ctx context.Context, options client.StartWorkflowOptions, workflow interface{}, args ...interface{},
) (client.WorkflowRun, error) {
	c.mu.Lock()
	c.starts++
	c.mu.Unlock()
	return c.wakeTestTemporalClient.ExecuteWorkflow(ctx, options, workflow, args...)
}

// DescribeWorkflowExecution reports the run open once one has been started,
// as Temporal does; the embedded fake otherwise answers one fixed status.
func (c *startCountingTemporal) DescribeWorkflowExecution(
	ctx context.Context, workflowID, runID string,
) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	c.mu.Lock()
	started := c.starts > 0
	c.mu.Unlock()
	if started {
		return &workflowservice.DescribeWorkflowExecutionResponse{
			WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
				Status:    enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
				Execution: &commonpb.WorkflowExecution{WorkflowId: workflowID, RunId: "run-new"},
			},
		}, nil
	}
	return c.wakeTestTemporalClient.DescribeWorkflowExecution(ctx, workflowID, runID)
}

func (c *startCountingTemporal) startCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.starts
}

func idempotentService(t *testing.T, repo *db.Repo, status db.WorkflowStatus, temporalStatus enums.WorkflowExecutionStatus) (*Service, *startCountingTemporal) {
	t.Helper()
	temporal := &startCountingTemporal{wakeTestTemporalClient: wakeTestTemporalClient{
		absorbTestTemporalClient: absorbTestTemporalClient{exists: true, status: temporalStatus},
	}}
	return &Service{
		deps: Deps{
			Repo:     repo,
			Temporal: temporal,
		},
		runs: runs.NewService(repo, temporal, succeedingPauseController{}),
	}, temporal
}

func timeNow() time.Time { return time.Now().UTC() }

// seedAssistantTurn is the agent's reply landing in the root thread, standing
// in for the history message a drain folds queued rows into.
func seedAssistantTurn(t *testing.T, ctx context.Context, repo *db.Repo, fx absorbFixture) string {
	t.Helper()
	msg, err := repo.SaveMessageToThread(ctx, fx.chatID, fx.rootThreadID,
		int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "[queued messages]", nil, nil, nil)
	require.NoError(t, err)
	return msg.ID
}

func newSendAgentMessageService(repo *db.Repo) *Service {
	tc := &signaltest.Recorder{}
	return &Service{deps: Deps{Repo: repo, Temporal: tc}, runs: runs.NewService(repo, tc, nil)}
}

func sendNamed(t *testing.T, chatID, content, id string) *connect.Request[reliantv1.SendMessageRequest] {
	t.Helper()
	req := sendMessageRequest(t, chatID, content)
	req.Msg.ClientMessageId = &id
	return req
}

// countUserMessages is how many transcript rows hold this text.
func countUserMessages(t *testing.T, ctx context.Context, repo *db.Repo, chatID, text string) int {
	t.Helper()
	n := 0
	for _, body := range transcriptBodies(t, ctx, repo, chatID) {
		if body == text {
			n++
		}
	}
	return n
}

// The reported bug. SendMessage saved the message and the response was lost;
// the client's Retry sent it again and the server saved a second copy and took
// a second turn.
func TestSendMessage_RepeatedWithTheSameIDSavesOnceAndStartsOneRun(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	// A finished run: the send starts a new one.
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Completed())
	service, temporal := idempotentService(t, repo, db.Completed(), enums.WORKFLOW_EXECUTION_STATUS_COMPLETED)
	id := uuid.NewString()

	first, err := service.SendMessage(ctx, sendNamed(t, fx.chatID, "ship it", id))
	require.NoError(t, err)
	require.Equal(t, 1, temporal.startCount(), "the first send starts the run")

	// The run the first send started is open, and has not written anything yet.
	temporal.status = enums.WORKFLOW_EXECUTION_STATUS_RUNNING
	require.NoError(t, repo.UpdateWorkflowStatus(ctx, fx.rootThreadID, db.Active()))

	second, err := service.SendMessage(ctx, sendNamed(t, fx.chatID, "ship it", id))
	require.NoError(t, err, "a repeat is answered, not refused")

	assert.Equal(t, 1, countUserMessages(t, ctx, repo, fx.chatID, "ship it"),
		"the repeat must not save the message a second time")
	assert.Equal(t, 1, temporal.startCount(), "the repeat must not start a second run")
	assert.Equal(t, first.Msg.MessageId, second.Msg.MessageId, "both receipts name the one message")
	assert.Equal(t, id, second.Msg.MessageId, "the message's id is the client's id")
}

// The failed-send fixture's window: the first attempt saved the message and
// started the run, and the run is still open and has not answered. The repeat
// has the message and the run; it must start neither again.
func TestSendMessage_RepeatAfterTheRunStartedStartsNothing(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Completed())
	service, temporal := idempotentService(t, repo, db.Completed(), enums.WORKFLOW_EXECUTION_STATUS_COMPLETED)
	id := uuid.NewString()

	_, err := service.SendMessage(ctx, sendNamed(t, fx.chatID, "go", id))
	require.NoError(t, err)

	// The database still says completed: the new run's own first activity has
	// not written "started" yet. Temporal already has the run open.
	temporal.status = enums.WORKFLOW_EXECUTION_STATUS_RUNNING
	_, err = service.SendMessage(ctx, sendNamed(t, fx.chatID, "go", id))
	require.NoError(t, err)

	assert.Equal(t, 1, temporal.startCount(),
		"a repeat landing before the run's first activity must not start a second run over the first")
	assert.Equal(t, 1, countUserMessages(t, ctx, repo, fx.chatID, "go"))
}

// A message the agent already answered: the repeat is a pure replay.
func TestSendMessage_RepeatOfAnAnsweredMessageIsAReplay(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Completed())
	service, temporal := idempotentService(t, repo, db.Completed(), enums.WORKFLOW_EXECUTION_STATUS_COMPLETED)
	id := uuid.NewString()

	first, err := service.SendMessage(ctx, sendNamed(t, fx.chatID, "question", id))
	require.NoError(t, err)

	// The run answered and finished.
	_, err = repo.SaveMessageToThread(ctx, fx.chatID, fx.rootThreadID,
		int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT), "answer", nil, nil, nil)
	require.NoError(t, err)

	second, err := service.SendMessage(ctx, sendNamed(t, fx.chatID, "question", id))
	require.NoError(t, err)

	assert.Equal(t, first.Msg.MessageId, second.Msg.MessageId)
	assert.Equal(t, 1, countUserMessages(t, ctx, repo, fx.chatID, "question"))
	assert.Equal(t, 1, temporal.startCount(), "answering a message again would be a second turn")
}

// A message queued to a running thread has a mailbox row, not a transcript row.
// The repeat must find it there.
func TestSendMessage_RepeatOfAQueuedMessageQueuesOnce(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	service, temporal := idempotentService(t, repo, db.Active(), enums.WORKFLOW_EXECUTION_STATUS_RUNNING)
	id := uuid.NewString()

	first, err := service.SendMessage(ctx, sendNamed(t, fx.chatID, "steer", id))
	require.NoError(t, err)
	require.True(t, first.Msg.Queued)

	second, err := service.SendMessage(ctx, sendNamed(t, fx.chatID, "steer", id))
	require.NoError(t, err)

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1, "the repeat must not queue the message a second time")
	assert.Equal(t, id, queued[0].ID)
	assert.True(t, second.Msg.Queued, "the repeat's receipt says what the first said: queued")
	assert.Equal(t, id, second.Msg.MessageId)
	assert.Zero(t, temporal.startCount())
}

// The mailbox row was drained into history before the repeat arrived.
func TestSendMessage_RepeatOfADrainedQueuedMessageDoesNotRequeueIt(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	service, _ := idempotentService(t, repo, db.Active(), enums.WORKFLOW_EXECUTION_STATUS_RUNNING)
	id := uuid.NewString()

	_, err := service.SendMessage(ctx, sendNamed(t, fx.chatID, "steer", id))
	require.NoError(t, err)

	// The agent's next turn drains it into history.
	envelope := seedAssistantTurn(t, ctx, repo, fx)
	moved, err := repo.MarkAgentMessagesDelivered(ctx, []string{id}, timeNow(), envelope)
	require.NoError(t, err)
	require.Equal(t, []string{id}, moved)

	second, err := service.SendMessage(ctx, sendNamed(t, fx.chatID, "steer", id))
	require.NoError(t, err)

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	assert.Empty(t, queued, "a message the agent already read must not be queued again")
	assert.Equal(t, id, second.Msg.MessageId, "the receipt is the one the first attempt got: the queued row's id")
	assert.True(t, second.Msg.Queued, "and says queued, as the first did")
	assert.Equal(t, 1, countUserMessages(t, ctx, repo, fx.chatID, "steer")+countUserMessages(t, ctx, repo, fx.chatID, "[queued messages]"),
		"the drained row is in history once")
}

// Same id, different text: the id names a message, and this is another one.
func TestSendMessage_SameIDDifferentContentIsRefused(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status db.WorkflowStatus
		temp   enums.WorkflowExecutionStatus
	}{
		{"saved to the transcript", db.Completed(), enums.WORKFLOW_EXECUTION_STATUS_COMPLETED},
		{"queued to a running thread", db.Active(), enums.WORKFLOW_EXECUTION_STATUS_RUNNING},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, cleanup := db.SetupTestDB(t)
			t.Cleanup(cleanup)

			ctx, fx := setupAbsorbFixture(t, repo, "test-user", tc.status)
			service, temporal := idempotentService(t, repo, tc.status, tc.temp)
			id := uuid.NewString()

			_, err := service.SendMessage(ctx, sendNamed(t, fx.chatID, "original text", id))
			require.NoError(t, err)
			startsBefore := temporal.startCount()

			_, err = service.SendMessage(ctx, sendNamed(t, fx.chatID, "different text", id))
			require.Error(t, err, "an id that names one message must not be accepted for another")
			assert.Equal(t, connect.CodeAlreadyExists, svcerr.Code(err))

			assert.Zero(t, countUserMessages(t, ctx, repo, fx.chatID, "different text"))
			queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
			require.NoError(t, err)
			for _, row := range queued {
				assert.NotEqual(t, "different text", row.Body)
			}
			assert.Equal(t, startsBefore, temporal.startCount(), "a refused send starts nothing")
		})
	}
}

// Different attachments are a different message too.
func TestSendMessage_SameIDDifferentAttachmentsIsRefused(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	service, _ := idempotentService(t, repo, db.Active(), enums.WORKFLOW_EXECUTION_STATUS_RUNNING)
	id := uuid.NewString()

	_, err := service.SendMessage(ctx, sendNamed(t, fx.chatID, "look", id))
	require.NoError(t, err)

	req := sendNamed(t, fx.chatID, "look", id)
	req.Msg.Attachments = []string{"some-attachment"}
	_, err = service.SendMessage(ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, svcerr.Code(err))
}

// An id is scoped to its owner. Another user's message under the same id must
// not be readable (the refusal discloses nothing about it) or collide.
func TestSendMessage_AnotherUsersIDIsRefusedWithoutDisclosure(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctxA, fxA := setupAbsorbFixture(t, repo, "user-a", db.Completed())
	ctxB, fxB := setupAbsorbFixture(t, repo, "user-b", db.Completed())
	service, _ := idempotentService(t, repo, db.Completed(), enums.WORKFLOW_EXECUTION_STATUS_COMPLETED)
	id := uuid.NewString()

	_, err := service.SendMessage(ctxA, sendNamed(t, fxA.chatID, "A's secret", id))
	require.NoError(t, err)

	// B uses A's id for B's own message in B's own chat, with the same text and
	// with different text: neither learns anything, and neither collides.
	for _, content := range []string{"A's secret", "B's own words"} {
		_, err = service.SendMessage(ctxB, sendNamed(t, fxB.chatID, content, id))
		require.Error(t, err)
		assert.Equal(t, connect.CodeAlreadyExists, svcerr.Code(err))
		assert.NotContains(t, err.Error(), "secret", "the refusal must not echo the other user's message")
	}
	assert.Zero(t, countUserMessages(t, ctxB, repo, fxB.chatID, "B's own words"))
	assert.Equal(t, 1, countUserMessages(t, ctxA, repo, fxA.chatID, "A's secret"))

	// And B cannot reach A's message by naming A's chat.
	_, err = service.SendMessage(ctxB, sendNamed(t, fxA.chatID, "A's secret", id))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
}

// A message that is not a UUID is refused rather than quietly replaced.
func TestSendMessage_RefusesAnIDThatIsNotAUUID(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Completed())
	service, temporal := idempotentService(t, repo, db.Completed(), enums.WORKFLOW_EXECUTION_STATUS_COMPLETED)

	_, err := service.SendMessage(ctx, sendNamed(t, fx.chatID, "hi", "not-a-uuid"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	assert.Zero(t, countUserMessages(t, ctx, repo, fx.chatID, "hi"))
	assert.Zero(t, temporal.startCount())
}

// Two attempts of one send in flight at once: exactly one message, exactly one
// run, and neither fails.
func TestSendMessage_ConcurrentDuplicatesSaveOnce(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Completed())
	service, temporal := idempotentService(t, repo, db.Completed(), enums.WORKFLOW_EXECUTION_STATUS_COMPLETED)
	id := uuid.NewString()

	const attempts = 6
	var wg sync.WaitGroup
	errs := make([]error, attempts)
	ids := make([]string, attempts)
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			resp, err := service.SendMessage(ctx, sendNamed(t, fx.chatID, "race", id))
			errs[i] = err
			if err == nil {
				ids[i] = resp.Msg.MessageId
			}
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "attempt %d", i)
		assert.Equal(t, id, ids[i])
	}
	assert.Equal(t, 1, countUserMessages(t, ctx, repo, fx.chatID, "race"),
		"concurrent duplicates must leave exactly one message")
	assert.Equal(t, 1, temporal.startCount(), "and exactly one run")
}

// The same race for a message queued to a running thread.
func TestSendMessage_ConcurrentQueuedDuplicatesQueueOnce(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	service, _ := idempotentService(t, repo, db.Active(), enums.WORKFLOW_EXECUTION_STATUS_RUNNING)
	id := uuid.NewString()

	const attempts = 6
	var wg sync.WaitGroup
	errs := make([]error, attempts)
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = service.SendMessage(ctx, sendNamed(t, fx.chatID, "race", id))
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "attempt %d", i)
	}
	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	assert.Len(t, queued, 1)
}

// The notes a send writes alongside its message (a model-switch note, say) are
// part of the send. A repeat that finds the message already saved must not
// leave a second copy of them.
func TestSendMessage_RepeatDoesNotDuplicateTheSendsSystemNotes(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Completed())
	service, temporal := idempotentService(t, repo, db.Completed(), enums.WORKFLOW_EXECUTION_STATUS_COMPLETED)
	id := uuid.NewString()

	withNote := func() *connect.Request[reliantv1.SendMessageRequest] {
		req := sendNamed(t, fx.chatID, "with a note", id)
		req.Msg.Messages = append(req.Msg.Messages, &reliantv1.InputMessage{
			Role: reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM, Content: "a note",
		})
		return req
	}
	_, err := service.SendMessage(ctx, withNote())
	require.NoError(t, err)
	temporal.status = enums.WORKFLOW_EXECUTION_STATUS_RUNNING

	_, err = service.SendMessage(ctx, withNote())
	require.NoError(t, err)

	notes := 0
	for _, body := range transcriptBodies(t, ctx, repo, fx.chatID) {
		if body == "a note" {
			notes++
		}
	}
	assert.Equal(t, 1, notes, "the repeat must not write the send's notes again")
}

// A send that names no id keeps today's behaviour: every call is a new message.
func TestSendMessage_WithoutAnIDEveryCallIsANewMessage(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	service, _ := idempotentService(t, repo, db.Active(), enums.WORKFLOW_EXECUTION_STATUS_RUNNING)

	for i := 0; i < 2; i++ {
		_, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "again"))
		require.NoError(t, err)
	}
	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	assert.Len(t, queued, 2)
}

// A queued message whose thread ended before reading it is not a message the
// agent has. Retrying it is a fresh send, and it must be able to take the id.
func TestSendMessage_RepeatOfAnUndeliveredMessageSendsItAgain(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	service, _ := idempotentService(t, repo, db.Active(), enums.WORKFLOW_EXECUTION_STATUS_RUNNING)
	id := uuid.NewString()

	_, err := service.SendMessage(ctx, sendNamed(t, fx.chatID, "lost", id))
	require.NoError(t, err)
	moved, err := repo.MarkQueuedAgentMessagesUndeliveredForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	require.EqualValues(t, 1, moved)

	resp, err := service.SendMessage(ctx, sendNamed(t, fx.chatID, "lost", id))
	require.NoError(t, err)
	assert.Equal(t, id, resp.Msg.MessageId)

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1, "the message that was never read goes to the agent now")
	assert.Equal(t, id, queued[0].ID)
}

// ---- StartChat ----

func startNamed(e *startDaemonEnv, t *testing.T, content, id string) (*connect.Response[reliantv1.StartChatResponse], error) {
	t.Helper()
	return e.start(t, func(r *reliantv1.StartChatRequest) {
		r.Messages[0].Content = content
		r.ClientMessageId = &id
	})
}

func countChats(t *testing.T, e *startDaemonEnv) int {
	t.Helper()
	chats, err := e.repo.ListChats(e.ctx, db.ChatFilters{UserID: "test-user", Limit: 100})
	require.NoError(t, err)
	return len(chats)
}

// The first message of a new chat is a send too.
func TestStartChat_RepeatedWithTheSameIDMakesOneChatAndOneRun(t *testing.T) {
	e := newStartDaemonEnv(t)
	id := uuid.NewString()

	first, err := startNamed(e, t, "build me a thing", id)
	require.NoError(t, err)
	second, err := startNamed(e, t, "build me a thing", id)
	require.NoError(t, err, "a repeat is answered with the chat the first call made")

	assert.Equal(t, first.Msg.Chat.Id, second.Msg.Chat.Id)
	assert.Equal(t, 1, countChats(t, e), "a repeat must not create a second chat")
	assert.Len(t, e.temporal.inputs, 1, "and must not start a second run")
	assert.Equal(t, 1, countUserMessages(t, e.ctx, e.repo, first.Msg.Chat.Id, "build me a thing"))

	// The first message is the client's id, which is what lets a SendMessage
	// that races it find it.
	msg, err := e.repo.GetMessage(e.ctx, id)
	require.NoError(t, err)
	assert.Equal(t, first.Msg.Chat.Id, msg.ChatID)
}

func TestStartChat_SameIDDifferentContentIsRefused(t *testing.T) {
	e := newStartDaemonEnv(t)
	id := uuid.NewString()

	_, err := startNamed(e, t, "the first message", id)
	require.NoError(t, err)
	_, err = startNamed(e, t, "a different message", id)
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, svcerr.Code(err))
	assert.Equal(t, 1, countChats(t, e))
	assert.Len(t, e.temporal.inputs, 1)
}

func TestStartChat_ConcurrentDuplicatesMakeOneChat(t *testing.T) {
	e := newStartDaemonEnv(t)
	id := uuid.NewString()

	const attempts = 5
	var wg sync.WaitGroup
	chatIDs := make([]string, attempts)
	errs := make([]error, attempts)
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			resp, err := startNamed(e, t, "race", id)
			errs[i] = err
			if err == nil {
				chatIDs[i] = resp.Msg.Chat.Id
			}
		}(i)
	}
	close(start)
	wg.Wait()

	var made string
	for i, err := range errs {
		require.NoError(t, err, "attempt %d", i)
		if made == "" {
			made = chatIDs[i]
		}
		assert.Equal(t, made, chatIDs[i])
	}
	assert.Equal(t, 1, countChats(t, e))
	assert.Equal(t, 1, countUserMessages(t, e.ctx, e.repo, made, "race"))
	assert.Len(t, e.temporal.inputs, 1, "one run, however many attempts")
}

// Another user's start under the same id is their own chat, and does not
// collide with, or learn about, the first user's.
func TestStartChat_IDIsScopedToTheUser(t *testing.T) {
	e := newStartDaemonEnv(t)
	id := uuid.NewString()

	a, err := startNamed(e, t, "first user's message", id)
	require.NoError(t, err)

	// A second user with a project of their own.
	otherCtx := context.WithValue(context.Background(), auth.UserIDContextKey, "other-user")
	otherProject, _ := newInvariantTestProject(t, otherCtx, e.repo, true)
	resp, err := e.svc.StartChat(otherCtx, connect.NewRequest(&reliantv1.StartChatRequest{
		ProjectId: otherProject,
		Workflow:  "builtin://agent",
		Messages: []*reliantv1.InputMessage{{
			Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: "second user's message",
		}},
		ClientMessageId: &id,
	}))
	// Message ids share one id space, so the second user cannot take it; what
	// matters is that the answer is a refusal that names nothing of the first.
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.NotContains(t, err.Error(), "first user's message")
	assert.NotContains(t, err.Error(), a.Msg.Chat.Id)
}

func TestStartChat_RefusesAnIDThatIsNotAUUID(t *testing.T) {
	e := newStartDaemonEnv(t)
	_, err := startNamed(e, t, "hi", "nope")
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	assert.Equal(t, 0, countChats(t, e))
}

// ---- A branch's first send, then ordinary sends ----

// The first send of a branch goes through StartChat with a chat_id. The chat
// already exists, so the dedupe is the launch event plus the message row's id.
func TestStartChat_BranchFirstSendRepeatedStartsOnce(t *testing.T) {
	e := newStartDaemonEnv(t)
	_, branch := setupAbsorbFixture(t, e.repo, "test-user", db.Pending())
	id := uuid.NewString()

	send := func(content string) (*connect.Response[reliantv1.StartChatResponse], error) {
		return e.start(t, func(r *reliantv1.StartChatRequest) {
			r.ProjectId = ""
			r.ChatId = &branch.chatID
			r.Messages[0].Content = content
			r.ClientMessageId = &id
		})
	}
	first, err := send("go on")
	require.NoError(t, err)
	second, err := send("go on")
	require.NoError(t, err, "the repeat is answered with the chat the first call started")

	assert.Equal(t, first.Msg.Chat.Id, second.Msg.Chat.Id)
	assert.Equal(t, 1, countUserMessages(t, e.ctx, e.repo, branch.chatID, "go on"))
	assert.Len(t, e.temporal.inputs, 1, "the run starts once")

	_, err = send("something else")
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, svcerr.Code(err))
}

// A StartChat that committed and lost its response, followed by the client's
// Retry arriving as a SendMessage (the chat now exists and is started), is the
// same message.
func TestSendMessage_RepeatOfAFirstMessageStartChatSavedIsAReplay(t *testing.T) {
	e := newStartDaemonEnv(t)
	id := uuid.NewString()

	started, err := startNamed(e, t, "first words", id)
	require.NoError(t, err)

	temporal := &startCountingTemporal{wakeTestTemporalClient: wakeTestTemporalClient{
		absorbTestTemporalClient: absorbTestTemporalClient{exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING},
	}}
	e.svc.deps.Temporal = temporal
	e.svc.runs = runs.NewService(e.repo, temporal, succeedingPauseController{})

	resp, err := e.svc.SendMessage(e.ctx, sendNamed(t, started.Msg.Chat.Id, "first words", id))
	require.NoError(t, err)
	assert.Equal(t, id, resp.Msg.MessageId)
	assert.Equal(t, 1, countUserMessages(t, e.ctx, e.repo, started.Msg.Chat.Id, "first words"))
	queued, err := e.repo.ListQueuedAgentMessagesForThread(e.ctx, started.Msg.Chat.Id)
	require.NoError(t, err)
	assert.Empty(t, queued, "the run was started with the message in history; it is not queued again")
}

// ---- SendAgentMessage ----

func TestSendAgentMessage_RepeatedWithTheSameIDQueuesOnce(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	service := newSendAgentMessageService(repo)
	id := uuid.NewString()

	send := func(body string) (*connect.Response[reliantv1.SendAgentMessageResponse], error) {
		return service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
			ChatId: fx.chatID, ThreadId: fx.childThreadID, Message: body, ClientMessageId: &id,
		}))
	}
	first, err := send("steer left")
	require.NoError(t, err)
	require.True(t, first.Msg.Success)
	second, err := send("steer left")
	require.NoError(t, err)
	assert.True(t, second.Msg.Success)

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	assert.Len(t, queued, 1, "a repeat must not queue the message a second time")

	_, err = send("steer right")
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, svcerr.Code(err))
	queued, err = repo.ListQueuedAgentMessagesForThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	assert.Len(t, queued, 1)

	bad := "not-a-uuid"
	_, err = service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: fx.childThreadID, Message: "x", ClientMessageId: &bad,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
}

var _ = core.AgentMessageStatusQueued

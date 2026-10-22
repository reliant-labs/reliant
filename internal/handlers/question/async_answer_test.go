// Copyright (c) 2025 Reliant Labs
package question

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/threadwake"
)

// ResolveQuestion on an async question (ask_user async: true) delivers the
// answer to the asking thread the way a message the user types to it is
// delivered — SendAgentMessage for a sub-agent, SendMessage for the main
// thread — and only then resolves the question. Nothing waits on it, so no
// workflow is signalled. research/ASYNC_ASK_USER.md.

// asyncAnswerFixture is a chat with a main thread and a sub-agent, on a real
// database, with the chat service's send paths recorded.
type asyncAnswerFixture struct {
	repo     *db.Repo
	chatID   string
	mainID   string
	spawnID  string
	svc      *Service
	messages *recordingMessenger
}

// recordingMessenger records each send and answers it as programmed.
type recordingMessenger struct {
	mu         sync.Mutex
	agentSends []*reliantv1.SendAgentMessageRequest
	mainSends  []*reliantv1.SendMessageRequest
	// yielded records, per send, whether its doorbell would make a blocking
	// question's wait yield (threadwake.YieldsQuestions on its ctx).
	yielded []bool

	// agentReply answers SendAgentMessage; nil accepts it.
	agentReply func(*reliantv1.SendAgentMessageRequest) (*reliantv1.SendAgentMessageResponse, error)
	// mainErr fails SendMessage.
	mainErr error
}

func (m *recordingMessenger) SendAgentMessage(ctx context.Context, req *connect.Request[reliantv1.SendAgentMessageRequest]) (*connect.Response[reliantv1.SendAgentMessageResponse], error) {
	m.mu.Lock()
	m.agentSends = append(m.agentSends, req.Msg)
	m.yielded = append(m.yielded, threadwake.YieldsQuestions(ctx))
	reply := m.agentReply
	m.mu.Unlock()
	if reply == nil {
		return connect.NewResponse(&reliantv1.SendAgentMessageResponse{Success: true, Message: "Queued for delivery."}), nil
	}
	resp, err := reply(req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (m *recordingMessenger) SendMessage(ctx context.Context, req *connect.Request[reliantv1.SendMessageRequest]) (*connect.Response[reliantv1.SendMessageResponse], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mainSends = append(m.mainSends, req.Msg)
	m.yielded = append(m.yielded, threadwake.YieldsQuestions(ctx))
	if m.mainErr != nil {
		return nil, m.mainErr
	}
	return connect.NewResponse(&reliantv1.SendMessageResponse{ChatId: req.Msg.ChatId, Status: "processing"}), nil
}

// noSignals fails the test if anything signals a workflow.
type noSignals struct{ t *testing.T }

func (s noSignals) SignalWithRecovery(_ context.Context, workflowID, signalName string, _ interface{}, _ map[string]interface{}) error {
	s.t.Errorf("an async answer must not signal a workflow (got %s on %s)", signalName, workflowID)
	return nil
}

func newAsyncAnswerFixture(t *testing.T) *asyncAnswerFixture {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := AuthedContext(t)
	now := time.Now().UTC()

	projectID := "test-project-async-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Async Answers",
		Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	chatID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: "test-user", Title: "Async", ProjectID: projectID,
		State: db.ChatStateIdle, WorkflowID: &chatID, CreatedAt: now, UpdatedAt: now,
	}))

	messages := &recordingMessenger{}
	svc := NewTestQuestion(t, WithQuestionDeps(Deps{Repo: repo, Messages: messages, Signals: noSignals{t}}))
	return &asyncAnswerFixture{
		repo: repo, chatID: chatID, mainID: chatID, spawnID: "spawn-" + uuid.NewString(),
		svc: svc, messages: messages,
	}
}

func (f *asyncAnswerFixture) askAsync(t *testing.T, id, thread string) {
	t.Helper()
	metadata := `{"type":"ask_user","async":true,"tool_call_id":"call-` + id + `","questions":[{"question":"Which database?","options":[{"label":"Postgres","description":"d"},{"label":"SQLite","description":"d"}]}]}`
	toolCall := "call-" + id
	require.NoError(t, f.repo.CreateQuestion(context.Background(), &db.Question{
		ID: id, ChatID: f.chatID, WorkflowID: thread, TemporalWorkflowID: f.chatID,
		ThreadID: thread, StepID: "agent_loop", Status: db.QuestionStatusPending,
		Metadata: &metadata, ToolCallID: &toolCall, Async: true, CreatedAt: time.Now().UTC(),
	}))
}

func (f *asyncAnswerFixture) answer(t *testing.T, id, responseData string) error {
	t.Helper()
	_, err := f.svc.ResolveQuestion(AuthedContext(t), connect.NewRequest(&reliantv1.ResolveQuestionRequest{
		QuestionId: id, Action: "reply", ResponseData: &responseData,
	}))
	return err
}

func (f *asyncAnswerFixture) status(t *testing.T, id string) *db.Question {
	t.Helper()
	q, err := f.repo.GetQuestionByID(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, q)
	return q
}

const sqliteAnswer = `{"answers":[{"question":"Which database?","selected":["SQLite"],"freetext":"small footprint"}]}`

const wantSQLiteAnswer = "Q: Which database?\nA: SQLite — small footprint"

// A live sub-agent gets the answer in its mailbox (SendAgentMessage), and the
// question is resolved with the answer kept.
func TestResolveAsyncQuestion_DeliversToTheAskingSubAgent(t *testing.T) {
	f := newAsyncAnswerFixture(t)
	f.askAsync(t, "q-live", f.spawnID)

	require.NoError(t, f.answer(t, "q-live", sqliteAnswer))

	require.Len(t, f.messages.agentSends, 1)
	sent := f.messages.agentSends[0]
	assert.Equal(t, f.chatID, sent.ChatId)
	assert.Equal(t, f.spawnID, sent.ThreadId)
	assert.Equal(t, asyncAnswerBody("q-live", wantSQLiteAnswer), sent.Message)
	assert.Equal(t, asyncAnswerMessageID("q-live", f.spawnID, sent.Message), sent.GetClientMessageId(),
		"named by its content, so a retry of the same answer never queues it twice")
	assert.Empty(t, f.messages.mainSends)

	assert.Equal(t, []bool{false}, f.messages.yielded,
		"the answer's doorbell is not the user's: a blocking question its thread is parked on stays open")

	q := f.status(t, "q-live")
	assert.Equal(t, db.QuestionStatusResolved, q.Status)
	require.NotNil(t, q.ResponseData)
	assert.Equal(t, sqliteAnswer, *q.ResponseData)
}

// The question was asked by a sub-agent that has since finished: the chat
// service refuses a message to it, so the answer goes to the main thread
// through SendMessage — which queues it for a working run, resumes a paused
// one, and starts a run when the chat's has ended — saying who asked.
func TestResolveAsyncQuestion_AfterTheAskingThreadFinishedDeliversToTheMainThread(t *testing.T) {
	f := newAsyncAnswerFixture(t)
	f.askAsync(t, "q-finished", f.spawnID)
	f.messages.agentReply = func(*reliantv1.SendAgentMessageRequest) (*reliantv1.SendAgentMessageResponse, error) {
		return &reliantv1.SendAgentMessageResponse{Success: false, Message: "This agent has already finished (status: completed)"}, nil
	}

	require.NoError(t, f.answer(t, "q-finished", sqliteAnswer))

	require.Len(t, f.messages.agentSends, 1, "the asking thread is tried first")
	require.Len(t, f.messages.mainSends, 1, "then the main thread takes the answer")
	sent := f.messages.mainSends[0]
	assert.Equal(t, f.chatID, sent.ChatId)
	assert.Empty(t, sent.GetTargetThread(), "the main thread")
	require.Len(t, sent.Messages, 1)
	assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, sent.Messages[0].Role)
	assert.Equal(t, asyncAnswerForwardedBody("q-finished", f.spawnID, wantSQLiteAnswer), sent.Messages[0].Content)
	assert.Contains(t, sent.Messages[0].Content, `spawn(agent_id="`+f.spawnID+`")`)
	assert.Equal(t, asyncAnswerMessageID("q-finished", f.mainID, sent.Messages[0].Content), sent.GetClientMessageId())

	assert.Equal(t, db.QuestionStatusResolved, f.status(t, "q-finished").Status)
}

// A question the main thread asked goes straight to SendMessage, the
// composer's send, whatever state the chat's run is in.
func TestResolveAsyncQuestion_MainThreadQuestionUsesSendMessage(t *testing.T) {
	f := newAsyncAnswerFixture(t)
	f.askAsync(t, "q-main", f.mainID)

	require.NoError(t, f.answer(t, "q-main", sqliteAnswer))

	assert.Empty(t, f.messages.agentSends)
	require.Len(t, f.messages.mainSends, 1)
	assert.Equal(t, asyncAnswerBody("q-main", wantSQLiteAnswer), f.messages.mainSends[0].Messages[0].Content)
	assert.Equal(t, db.QuestionStatusResolved, f.status(t, "q-main").Status)
}

// A delivery that fails leaves the question pending — the answer is never
// lost, and the user can send it again.
func TestResolveAsyncQuestion_FailedDeliveryKeepsTheQuestionPending(t *testing.T) {
	f := newAsyncAnswerFixture(t)
	f.askAsync(t, "q-refused", f.spawnID)
	f.messages.agentReply = func(*reliantv1.SendAgentMessageRequest) (*reliantv1.SendAgentMessageResponse, error) {
		return &reliantv1.SendAgentMessageResponse{Success: false, Message: "This chat is paused and its run could not be resumed"}, nil
	}
	f.messages.mainErr = connect.NewError(connect.CodeFailedPrecondition, errors.New("the run cannot be resumed"))

	err := f.answer(t, "q-refused", sqliteAnswer)

	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Equal(t, db.QuestionStatusPending, f.status(t, "q-refused").Status, "still answerable")

	// Answered again once the chat can take it: delivered and resolved.
	f.messages.mainErr = nil
	require.NoError(t, f.answer(t, "q-refused", sqliteAnswer))
	assert.Equal(t, db.QuestionStatusResolved, f.status(t, "q-refused").Status)
}

// A bare "Continue" tells the agent nothing: resolved, and no message wakes
// or restarts anything.
func TestResolveAsyncQuestion_EmptyAnswerSendsNothing(t *testing.T) {
	f := newAsyncAnswerFixture(t)
	f.askAsync(t, "q-continue", f.spawnID)

	require.NoError(t, f.answer(t, "q-continue", `{"answers":[{"question":"Which database?","selected":["Continue"]}]}`))

	assert.Empty(t, f.messages.agentSends)
	assert.Empty(t, f.messages.mainSends)
	assert.Equal(t, db.QuestionStatusResolved, f.status(t, "q-continue").Status)
}

func TestListAsyncQuestions(t *testing.T) {
	f := newAsyncAnswerFixture(t)
	f.askAsync(t, "q-async", f.spawnID)
	require.NoError(t, f.repo.CreateQuestion(context.Background(), &db.Question{
		ID: "q-blocking", ChatID: f.chatID, WorkflowID: f.spawnID, ThreadID: f.spawnID,
		StepID: "agent_loop", Status: db.QuestionStatusPending, CreatedAt: time.Now().UTC(),
	}))

	resp, err := f.svc.ListAsyncQuestions(AuthedContext(t), connect.NewRequest(&reliantv1.ListAsyncQuestionsRequest{ChatId: f.chatID}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Questions, 1)
	got := resp.Msg.Questions[0]
	assert.Equal(t, "q-async", got.QuestionId)
	assert.True(t, got.Async)
	assert.Equal(t, "call-q-async", got.ToolCallId)
	assert.Equal(t, f.spawnID, got.ThreadId)

	pending, err := f.svc.ListPendingQuestions(AuthedContext(t), connect.NewRequest(&reliantv1.ListPendingQuestionsRequest{ChatId: &f.chatID}))
	require.NoError(t, err)
	require.Len(t, pending.Msg.Questions, 1, "the composer's list holds only the blocking question")
	assert.Equal(t, "q-blocking", pending.Msg.Questions[0].QuestionId)

	_, err = f.svc.ListAsyncQuestions(AuthedContext(t), connect.NewRequest(&reliantv1.ListAsyncQuestionsRequest{ChatId: uuid.NewString()}))
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "not the caller's chat")
}

func TestFormatAsyncAnswer(t *testing.T) {
	metadata := `{"type":"ask_user","questions":[{"question":"Color?","options":[]},{"question":"Tone?","options":[]}]}`
	for _, tc := range []struct {
		name, data, want string
	}{
		{"selection and freetext", sqliteAnswer, wantSQLiteAnswer},
		{"several answers", `{"answers":[{"question":"Color?","selected":["Blue"]},{"question":"Tone?","freetext":"playful"}]}`, "Q: Color?\nA: Blue\n\nQ: Tone?\nA: playful"},
		{"answer without its question takes it from the metadata", `{"answers":[{"selected":["Blue"]}]}`, "Q: Color?\nA: Blue"},
		{"continue only", `{"answers":[{"question":"Color?","selected":["Continue"]}]}`, ""},
		{"empty", ``, ""},
		{"plain text", `just do whatever`, "A: just do whatever"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, formatAsyncAnswer(&metadata, tc.data))
		})
	}
}

// The asking thread is parked on a blocking question B when the user answers
// async question A. The answer is A's, not B's: it goes into the thread's
// mailbox and nothing else happens — no doorbell, no resume, no run started,
// no answer-as-resume of B — so B stays pending and the thread reads A's
// answer at the call_llm after B is answered.
func TestResolveAsyncQuestion_ThreadParkedOnABlockingQuestionGetsItInItsMailboxOnly(t *testing.T) {
	for name, thread := range map[string]func(*asyncAnswerFixture) string{
		"sub-agent":   func(f *asyncAnswerFixture) string { return f.spawnID },
		"main thread": func(f *asyncAnswerFixture) string { return f.mainID },
	} {
		t.Run(name, func(t *testing.T) {
			f := newAsyncAnswerFixture(t)
			asker := thread(f)
			ctx := context.Background()
			// The mailbox row names threads: the main thread (its sender)
			// and the asker.
			_, err := f.repo.CreateThread(ctx, &db.Thread{
				ID: f.mainID, ChatID: f.chatID, Status: db.ThreadStatusRunning, CreatedAt: time.Now(),
			})
			require.NoError(t, err)
			if asker != f.mainID {
				parent := f.mainID
				_, err := f.repo.CreateThread(ctx, &db.Thread{
					ID: asker, ChatID: f.chatID, ParentThreadID: &parent, Origin: db.ThreadOriginSpawn,
					Status: db.ThreadStatusRunning, CreatedAt: time.Now(),
				})
				require.NoError(t, err)
			}
			f.askAsync(t, "q-a", asker)
			require.NoError(t, f.repo.CreateQuestion(ctx, &db.Question{
				ID: "q-b", ChatID: f.chatID, WorkflowID: asker, ThreadID: asker,
				StepID: "agent_loop", Status: db.QuestionStatusPending, CreatedAt: time.Now().UTC(),
			}))

			require.NoError(t, f.answer(t, "q-a", sqliteAnswer))

			assert.Empty(t, f.messages.agentSends, "no send path: they ring doorbells and resume runs")
			assert.Empty(t, f.messages.mainSends)
			assert.Equal(t, db.QuestionStatusResolved, f.status(t, "q-a").Status, "A is answered")
			assert.Equal(t, db.QuestionStatusPending, f.status(t, "q-b").Status, "B stays the user's to answer")

			queued, err := f.repo.ListQueuedAgentMessagesForThread(ctx, asker)
			require.NoError(t, err)
			require.Len(t, queued, 1, "A's answer waits in the asking thread's mailbox")
			assert.Equal(t, asyncAnswerBody("q-a", wantSQLiteAnswer), queued[0].Body)
			assert.Equal(t, asyncAnswerMessageID("q-a", asker, queued[0].Body), queued[0].ID)

			// The thread failing while parked does not lose it: its pending
			// question is what resumes it.
			moved, err := f.repo.MarkQueuedAgentMessagesUndeliveredForThread(ctx, asker)
			require.NoError(t, err)
			assert.Zero(t, moved, "a parked thread's mailbox is not swept")
		})
	}
}

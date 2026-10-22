// Copyright (c) 2025 Reliant Labs
package question

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// TestListPendingQuestionsRejectsUnknownChat is the regression behind
// "`workflow questions <short-id>` reports no gate while a gate is open".
//
// The pending-question lookup used to skip the chat-existence check every other
// supervision RPC performs, so `WHERE chat_id = '<short-id>'` found no row and
// the handler returned an empty-but-successful response. The CLI cannot tell
// that apart from a genuinely quiet run, so it prints "No open questions." and
// exits 0 — a confident, clean, wrong answer. `workflow status` on the same id
// errors honestly, which is what made the pair so misleading: one command says
// the id is bad, the other says the run is fine.
//
// An id that does not name a chat must be an error, not an empty answer.
func TestListPendingQuestionsRejectsUnknownChat(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	// AuthedContext's principal is UserID "test-user", installed the way the
	// API auth interceptor installs a real caller.
	ctx := AuthedContext(t)
	now := time.Now().UTC()

	projectID := "test-project-questions-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Questions Test Project",
		Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	chatID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: "test-user", Title: "Gated run", ProjectID: projectID,
		State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now,
	}))

	svc := NewTestQuestion(t, WithQuestionDeps(Deps{Repo: repo}))

	get := func(ctx context.Context, id string) (*connect.Response[reliantv1.ListPendingQuestionsResponse], error) {
		return svc.ListPendingQuestions(ctx, connect.NewRequest(&reliantv1.ListPendingQuestionsRequest{ChatId: &id}))
	}

	t.Run("short id prefix of a real chat is not found", func(t *testing.T) {
		// The exact shape a supervisor hits: `workflow ps` printed an 8-char
		// prefix, and it was pasted into `workflow questions`.
		_, err := get(ctx, chatID[:8])
		require.Error(t, err, "a short id must not be answered as if it were a chat with no gate")
		assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	})

	t.Run("unknown id is not found", func(t *testing.T) {
		_, err := get(ctx, uuid.NewString())
		require.Error(t, err)
		assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	})

	t.Run("another user's chat is not found", func(t *testing.T) {
		otherCtx := AuthedContext(t, testkit.WithUserID("other-user"))
		_, err := get(otherCtx, chatID)
		require.Error(t, err)
		assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	})

	t.Run("real chat with no gate answers empty", func(t *testing.T) {
		// The honest empty answer must still work, or the fix has just traded
		// a false negative for a false positive.
		resp, err := get(ctx, chatID)
		require.NoError(t, err)
		assert.Empty(t, resp.Msg.GetQuestions())
	})
}

// TestListPendingQuestionsReturnsEveryThreadsQuestion is the regression behind
// a spawned agent that stayed blocked for hours (chat adcee226): spawn A asked
// a question, spawn B later asked newer ones, and the old single-question
// lookup (newest from ANY thread, LIMIT 1) only ever surfaced B's. A's
// question never reached the composer, so the user's reply went to A's
// mailbox, which a thread parked on a question never drains.
//
// Every pending question must come back, oldest first, each naming the thread
// that asked it, so the client can show the one for the thread in view.
func TestListPendingQuestionsReturnsEveryThreadsQuestion(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := AuthedContext(t)
	now := time.Now().UTC().Truncate(time.Second)

	projectID := "test-project-questions-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Questions Test Project",
		Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	chatID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: "test-user", Title: "Two spawns asking", ProjectID: projectID,
		State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now,
	}))

	ask := func(id, threadID string, status int, at time.Time) {
		t.Helper()
		metadata := `{"questions":[{"question":"` + id + `?"}]}`
		require.NoError(t, repo.CreateQuestion(ctx, &db.Question{
			ID: id, ChatID: chatID, WorkflowID: "wf-" + threadID, TemporalWorkflowID: chatID,
			ThreadID: threadID, StepID: "ask_user", Status: status, Metadata: &metadata, CreatedAt: at,
		}))
	}
	spawnA, spawnB := "thread-a-"+uuid.NewString(), "thread-b-"+uuid.NewString()
	qA, qB, qBDone := uuid.NewString(), uuid.NewString(), uuid.NewString()
	ask(qA, spawnA, db.QuestionStatusPending, now.Add(-2*time.Hour))
	ask(qBDone, spawnB, db.QuestionStatusResolved, now.Add(-time.Hour))
	ask(qB, spawnB, db.QuestionStatusPending, now.Add(-time.Minute))

	svc := NewTestQuestion(t, WithQuestionDeps(Deps{Repo: repo}))
	resp, err := svc.ListPendingQuestions(ctx, connect.NewRequest(&reliantv1.ListPendingQuestionsRequest{ChatId: &chatID}))
	require.NoError(t, err)

	got := resp.Msg.GetQuestions()
	require.Len(t, got, 2, "both threads' pending questions, and not the resolved one")
	assert.Equal(t, qA, got[0].GetQuestionId(), "oldest first: spawn A's question must not be hidden behind B's newer one")
	assert.Equal(t, spawnA, got[0].GetThreadId())
	assert.Equal(t, "wf-"+spawnA, got[0].GetWorkflowId())
	assert.Equal(t, "pending", got[0].GetStatus())
	assert.Equal(t, qB, got[1].GetQuestionId())
	assert.Equal(t, spawnB, got[1].GetThreadId())
}

// chat_id is a filter: unset lists the caller's pending questions in every chat
// they own (and no one else's), set narrows to that chat.
func TestListPendingQuestionsChatIDIsAFilter(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := AuthedContext(t)
	now := time.Now().UTC().Truncate(time.Second)

	projectID := "test-project-questions-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Questions Filter Project",
		Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	newChat := func(userID string) string {
		id := uuid.NewString()
		require.NoError(t, repo.CreateChat(ctx, &db.Chat{
			ID: id, UserID: userID, Title: "chat", ProjectID: projectID,
			State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now,
		}))
		return id
	}
	ask := func(chatID string, status int, at time.Time) string {
		id := uuid.NewString()
		metadata := `{"questions":[{"question":"?"}]}`
		require.NoError(t, repo.CreateQuestion(ctx, &db.Question{
			ID: id, ChatID: chatID, WorkflowID: "wf-" + id, TemporalWorkflowID: chatID,
			ThreadID: "thread-" + id, StepID: "ask_user", Status: status, Metadata: &metadata, CreatedAt: at,
		}))
		return id
	}
	chatA, chatB, othersChat := newChat("test-user"), newChat("test-user"), newChat("other-user")
	qA := ask(chatA, db.QuestionStatusPending, now.Add(-2*time.Minute))
	qB := ask(chatB, db.QuestionStatusPending, now.Add(-time.Minute))
	ask(chatB, db.QuestionStatusResolved, now.Add(-3*time.Minute))
	ask(othersChat, db.QuestionStatusPending, now.Add(-4*time.Minute))

	svc := NewTestQuestion(t, WithQuestionDeps(Deps{Repo: repo}))
	ids := func(req *reliantv1.ListPendingQuestionsRequest) []string {
		t.Helper()
		resp, err := svc.ListPendingQuestions(ctx, connect.NewRequest(req))
		require.NoError(t, err)
		var out []string
		for _, q := range resp.Msg.GetQuestions() {
			out = append(out, q.GetQuestionId())
		}
		return out
	}

	assert.Equal(t, []string{qA, qB}, ids(&reliantv1.ListPendingQuestionsRequest{}),
		"unset: every chat the caller owns, oldest first, never another user's")
	assert.Equal(t, []string{qB}, ids(&reliantv1.ListPendingQuestionsRequest{ChatId: &chatB}),
		"set: only that chat's")

	empty := ""
	_, err := svc.ListPendingQuestions(ctx, connect.NewRequest(&reliantv1.ListPendingQuestionsRequest{ChatId: &empty}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), "an empty string is not 'unset'")
}

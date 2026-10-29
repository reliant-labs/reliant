package question_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	question "github.com/reliant-labs/reliant/internal/handlers/question"
)

// The question reads pass a classified repository error through and redact an
// opaque one to the handler's summary: an outage is no longer "question not
// found", and the driver's text no longer reaches the client.
func TestQuestionRPCs_RepositoryFailureCodes(t *testing.T) {
	t.Parallel()

	ownRow := func(_ context.Context, id string) (*db.ChatRow, error) {
		return &db.ChatRow{ID: id, UserID: "test-user"}, nil
	}
	for _, tc := range []struct {
		name string
		err  error
		want connect.Code
	}{
		{"a store miss is NotFound", svcerr.NotFound("chat"), connect.CodeNotFound},
		{"a deadline is DeadlineExceeded", context.DeadlineExceeded, connect.CodeDeadlineExceeded},
		{"a cancellation is Canceled", context.Canceled, connect.CodeCanceled},
		{"an opaque error is Internal", errors.New("dsn=postgres://u:secret@h"), connect.CodeInternal},
	} {
		t.Run("ResolveQuestion, reading the question: "+tc.name, func(t *testing.T) {
			t.Parallel()
			svc := question.NewTestQuestion(t, question.WithQuestionDeps(question.Deps{Repo: &question.MockRepository{
				GetQuestionByIDFunc: func(context.Context, string) (*db.Question, error) { return nil, tc.err },
			}}))
			_, err := svc.ResolveQuestion(question.AuthedContext(t), connect.NewRequest(&pb.ResolveQuestionRequest{QuestionId: "q-1", Action: "reply"}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("ListAsyncQuestions, reading the chat: "+tc.name, func(t *testing.T) {
			t.Parallel()
			svc := question.NewTestQuestion(t, question.WithQuestionDeps(question.Deps{Repo: &question.MockRepository{
				GetChatRowFunc: func(context.Context, string) (*db.ChatRow, error) { return nil, tc.err },
			}}))
			_, err := svc.ListAsyncQuestions(question.AuthedContext(t), connect.NewRequest(&pb.ListAsyncQuestionsRequest{ChatId: "chat-1"}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("ListPendingQuestions, listing: "+tc.name, func(t *testing.T) {
			t.Parallel()
			svc := question.NewTestQuestion(t, question.WithQuestionDeps(question.Deps{Repo: &question.MockRepository{
				GetChatRowFunc: ownRow,
				ListAnswerablePendingQuestionsByChatFunc: func(context.Context, string) ([]*db.Question, error) {
					return nil, tc.err
				},
			}}))
			chatID := "chat-1"
			_, err := svc.ListPendingQuestions(question.AuthedContext(t), connect.NewRequest(&pb.ListPendingQuestionsRequest{ChatId: &chatID}))
			requireClientReads(t, err, tc.want)
		})
	}
}

func requireClientReads(t *testing.T, err error, want connect.Code) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, want, svcerr.Code(err), "err = %v", err)
	require.NotContains(t, svcerr.ToConnect(err).Message(), "secret")
}

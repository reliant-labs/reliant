package question

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// A shared chat's questions, asked as owner, viewer and outsider: no read
// site serves viewers yet, so the viewer is refused like an outsider
// (org-sharing S3 flips it deliberately).
func TestQuestionReadsRefuseAViewerUntilTheyServeViewers(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestQuestion(t, WithQuestionDeps(Deps{Repo: repo}))
	chat := f.ChatID
	viewertest.Run(t, f, []viewertest.Case{
		{Name: "ListPendingQuestions", Call: func(ctx context.Context) error {
			_, err := svc.ListPendingQuestions(ctx, connect.NewRequest(&reliantv1.ListPendingQuestionsRequest{ChatId: &chat}))
			return err
		}},
		{Name: "ListAsyncQuestions", Call: func(ctx context.Context) error {
			_, err := svc.ListAsyncQuestions(ctx, connect.NewRequest(&reliantv1.ListAsyncQuestionsRequest{ChatId: chat}))
			return err
		}},
	})
}

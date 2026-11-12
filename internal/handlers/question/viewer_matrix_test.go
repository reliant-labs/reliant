package question

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// The viewer matrix for QuestionService (control-plane docs/design/org-sharing.md
// §4): every rpc that takes a chat or a project answers a viewer of a shared
// one per viewertest.Matrix.
func TestQuestionServiceViewerMatrix(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestQuestion(t, WithQuestionDeps(Deps{Repo: repo}))
	viewertest.RunService(t, f, svc, "reliant.v1.QuestionService", nil)
}

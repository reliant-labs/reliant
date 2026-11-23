// Package workflowdraftstest builds the workflow-drafts service over a test
// database.
package workflowdraftstest

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/workflowdrafts"
)

// New is the workflow-drafts service over repo's database, transactions and
// user updates.
func New(t testing.TB, repo *db.Repo) workflowdrafts.Service {
	t.Helper()
	svc, err := workflowdrafts.NewInstrumented(workflowdrafts.Deps{
		Tx:      repo,
		Locks:   repo,
		Updates: repo,
		Drafts:  store.NewWorkflowDraftExtStore(repo.ORM()),
	})
	if err != nil {
		t.Fatalf("workflowdraftstest.New: %v", err)
	}
	return svc
}

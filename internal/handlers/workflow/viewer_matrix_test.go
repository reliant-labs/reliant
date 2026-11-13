package workflow

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// The viewer matrix for WorkflowService (control-plane docs/design/org-sharing.md
// §4): every rpc that takes a chat or a project answers a viewer of a shared
// one per viewertest.Matrix.
func TestWorkflowServiceViewerMatrix(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestWorkflow(t, WithWorkflowDeps(Deps{Repo: repo, Orgs: f.Membership}))
	viewertest.RunService(t, f, svc, "reliant.v1.WorkflowService", nil)
}

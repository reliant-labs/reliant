package project

import (
	"testing"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// The viewer matrix for ProjectService (control-plane docs/design/org-sharing.md
// §4): every rpc that takes a chat or a project answers a viewer of a shared
// one per viewertest.Matrix.
func TestProjectServiceViewerMatrix(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestProject(t, WithProjectDeps(Deps{Repo: repo, Orgs: f.Membership}))
	viewertest.RunService(t, f, svc, "reliant.v1.ProjectService", nil)
}

// A viewer's GetProject is the card without the owner's path.
func TestGetProjectForAViewerHasNoPath(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestProject(t, WithProjectDeps(Deps{Repo: repo, Orgs: f.Membership}))
	resp, err := svc.GetProject(f.Viewer, connect.NewRequest(&reliantv1.GetProjectRequest{ProjectId: f.ProjectID}))
	if err != nil {
		t.Fatal(err)
	}
	p := resp.Msg.Project
	if !p.GetViewerAccess() || p.GetPath() != "" || p.GetVisibility() != reliantv1.ResourceVisibility_RESOURCE_VISIBILITY_ORG {
		t.Fatalf("viewer's project: viewer_access %v path %q visibility %v", p.GetViewerAccess(), p.GetPath(), p.GetVisibility())
	}
}

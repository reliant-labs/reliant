package project

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// The project read, asked as owner, viewer and outsider of a project shared
// with its org in the database: no read site serves viewers yet, so the
// viewer is refused like an outsider (org-sharing S3 flips it deliberately).
func TestProjectReadsRefuseAViewerUntilTheyServeViewers(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestProject(t, WithProjectDeps(Deps{Repo: repo, Orgs: f.Membership}))
	viewertest.Run(t, f, []viewertest.Case{
		{Name: "GetProject", Call: func(ctx context.Context) error {
			_, err := svc.GetProject(ctx, connect.NewRequest(&reliantv1.GetProjectRequest{ProjectId: f.ProjectID}))
			return err
		}},
	})
}

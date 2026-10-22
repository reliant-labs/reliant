package forge_test

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	forge "github.com/reliant-labs/reliant/internal/handlers/forge"
)

// The projects the store ownedProjectRepo builds.
const (
	ownProjectID   = "p-1"
	otherProjectID = "p-2" // another user's project
)

// newOwnedProjectService is the Service over a store where the harness caller
// (test-user) owns project p-1 at /work, and every other project is not
// found, as GetProjectWithUserCheck answers. Its router is the harness's
// unprogrammed stub, so every row built on it is answered before any command
// reaches a daemon.
func newOwnedProjectService(t *testing.T) *forge.Service {
	t.Helper()
	store := &forge.StubRepo{
		GetProjectWithUserCheckFunc: func(_ context.Context, id, userID string) (*db.Project, error) {
			if id == ownProjectID && userID == "test-user" {
				return &db.Project{ID: id, UserID: userID, Name: "proj", Path: "/work"}, nil
			}
			return nil, core.ErrProjectNotFound // the store's shape
		},
	}
	return forge.NewTestForge(t, forge.WithForgeDeps(forge.Deps{Repo: store}))
}

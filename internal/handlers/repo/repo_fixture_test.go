package repo_test

import (
	"context"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	repo "github.com/reliant-labs/reliant/internal/handlers/repo"
)

// daemonFunc answers a daemon command the way the caller's daemon would.
type daemonFunc func(commandType string, payload []byte) ([]byte, error)

func (f daemonFunc) SendDaemonCommand(_ context.Context, _, commandType string, payload []byte, _ int32) ([]byte, error) {
	return f(commandType, payload)
}

// ownedProjectRepo is a store where the harness caller (test-user) owns
// project p-1 at /work, which holds repo r-1 at svc/. Every other project or
// repo is not found, as GetProjectWithUserCheck and GetRepo answer.
func ownedProjectRepo(t *testing.T) *repo.MockRepository {
	t.Helper()
	return &repo.MockRepository{
		GetProjectWithUserCheckFunc: func(_ context.Context, id, userID string) (*db.Project, error) {
			if id == "p-1" && userID == "test-user" {
				return &db.Project{ID: id, UserID: userID, Name: "proj", Path: "/work", IsGitRepo: true}, nil
			}
			return nil, core.ErrProjectNotFound
		},
		GetRepoFunc: func(_ context.Context, id string) (*core.Repo, error) {
			if id == "r-1" {
				return &core.Repo{ID: id, ProjectID: "p-1", Name: "svc", RelativePath: "svc"}, nil
			}
			return nil, svcerr.NotFound("repo")
		},
	}
}

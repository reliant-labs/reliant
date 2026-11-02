package repo_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	repo "github.com/reliant-labs/reliant/internal/handlers/repo"
)

// daemonFunc answers a daemon command the way the machine holding the
// checkout would. It answers only commands pinned to that machine
// (checkoutDaemon): a command for any other is the bug.
type daemonFunc func(commandType string, payload []byte) ([]byte, error)

func (f daemonFunc) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, payload []byte, _ int32) ([]byte, error) {
	if daemonID != checkoutDaemon {
		return nil, fmt.Errorf("%s sent to %q, not to %q, the machine holding the checkout", commandType, daemonID, checkoutDaemon)
	}
	return f(commandType, payload)
}

func (daemonFunc) PlacementDaemonID(_ context.Context, _, daemonID string) (string, error) {
	return daemonID, nil
}

// checkoutDaemon holds p-1's checkout, at /work. The project row's own path
// is a different machine's (creatorPath), so every test asserting /work also
// proves the per-machine path is the one sent.
const (
	checkoutDaemon = "daemon-1"
	creatorPath    = "/Users/creator/src/proj"
)

// ownedProjectRepo is a store where the harness caller (test-user) owns
// project p-1, checked out at /work on checkoutDaemon, which holds repo r-1 at
// svc/. Every other project or
// repo is not found, as GetProjectWithUserCheck and GetRepo answer.
func ownedProjectRepo(t *testing.T) *repo.MockRepository {
	t.Helper()
	return &repo.MockRepository{
		GetProjectWithUserCheckFunc: func(_ context.Context, id, userID string) (*db.Project, error) {
			if id == "p-1" && userID == "test-user" {
				return &db.Project{ID: id, UserID: userID, Name: "proj", Path: creatorPath, IsGitRepo: true}, nil
			}
			return nil, core.ErrProjectNotFound
		},
		GetRepoFunc: func(_ context.Context, id string) (*core.Repo, error) {
			if id == "r-1" {
				return &core.Repo{ID: id, ProjectID: "p-1", Name: "svc", RelativePath: "svc"}, nil
			}
			return nil, svcerr.NotFound("repo")
		},
		ListProjectDaemonsForProjectFunc: func(_ context.Context, projectID string) ([]*core.ProjectDaemon, error) {
			return []*core.ProjectDaemon{{ProjectID: projectID, DaemonID: checkoutDaemon, Path: "/work", InstallState: core.ProjectInstallInstalled}}, nil
		},
		GetDaemonFunc: func(_ context.Context, id string) (*db.Daemon, error) {
			return &db.Daemon{ID: id, UserID: "test-user"}, nil
		},
	}
}

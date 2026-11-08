package project_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

// fakeDaemon answers a daemon command the way the caller's daemon would.
// Queued and config-load commands are accepted and dropped.
type fakeDaemon func(commandType string, payload []byte) ([]byte, error)

func (f fakeDaemon) SendDaemonCommand(_ context.Context, _, commandType string, payload []byte, _ int32) ([]byte, error) {
	return f(commandType, payload)
}

func (f fakeDaemon) SendDaemonCommandToDaemon(_ context.Context, _, _, commandType string, payload []byte, _ int32) ([]byte, error) {
	return f(commandType, payload)
}

func (fakeDaemon) ResolveDaemonID(context.Context, string) (string, error) { return "daemon-1", nil }

func (fakeDaemon) PlacementDaemonID(_ context.Context, _, daemonID string) (string, error) {
	if daemonID == "" {
		return "daemon-1", nil
	}
	return daemonID, nil
}

func (fakeDaemon) EnqueueDaemonCommand(context.Context, string, string, []byte, int32) (int, error) {
	return 0, nil
}

func (fakeDaemon) SendLoadProjectConfigs(context.Context, string, string, string) error { return nil }

func (fakeDaemon) SendWatchProjectConfigs(context.Context, string, string, bool) error { return nil }

// answering is a daemon that runs exactly one command, at exactly one
// payload, and refuses anything else.
func answering(commandType, payload, response string) fakeDaemon {
	return func(gotType string, gotPayload []byte) ([]byte, error) {
		if gotType != commandType || string(gotPayload) != payload {
			return nil, fmt.Errorf("unexpected daemon command %s %s", gotType, gotPayload)
		}
		return []byte(response), nil
	}
}

// creatorPath is projects.path in ownedProject: where the project was
// created, on another machine. No daemon command may ever carry it — the
// checkout this store records is /work on daemon-1 (ownedProjectRepo), so
// every test asserting "/work" also proves the per-machine path is sent.
const creatorPath = "/Users/creator/src/proj"

// ownedProject is project p-1, a git repository on main with a remote, owned
// by the harness caller (test-user), checked out at /work on daemon-1.
func ownedProject() *db.Project {
	branch := "main"
	remote := "git@example.com:acme/proj.git"
	return &db.Project{
		ID:            "p-1",
		UserID:        "test-user",
		Name:          "proj",
		Path:          creatorPath,
		IsGitRepo:     true,
		DefaultBranch: &branch,
		RemoteURL:     &remote,
	}
}

// ownedProjectRepo is a store holding only ownedProject, with no nested
// repos. Every other project is not found, as GetProject and
// GetProjectWithUserCheck answer.
func ownedProjectRepo(t *testing.T) *project.MockRepository {
	t.Helper()
	return &project.MockRepository{
		GetProjectWithUserCheckFunc: func(_ context.Context, id, userID string) (*db.Project, error) {
			if id == "p-1" && userID == "test-user" {
				return ownedProject(), nil
			}
			return nil, core.ErrProjectNotFound
		},
		GetProjectFunc: func(_ context.Context, id string) (*db.Project, error) {
			if id == "p-1" {
				return ownedProject(), nil
			}
			return nil, svcerr.NotFound("project")
		},
		ListReposByProjectFunc: func(context.Context, string) ([]*core.Repo, error) { return nil, nil },
		ListProjectDaemonsForProjectFunc: func(_ context.Context, projectID string) ([]*core.ProjectDaemon, error) {
			return []*core.ProjectDaemon{{ProjectID: projectID, DaemonID: "daemon-1", Path: "/work", InstallState: core.ProjectInstallInstalled}}, nil
		},
		GetDaemonFunc: func(_ context.Context, id string) (*db.Daemon, error) {
			return &db.Daemon{ID: id, UserID: "test-user"}, nil
		},
	}
}

// newService builds the service over store. router is the caller's daemon;
// nil builds it without a daemon router.
func newService(t *testing.T, store project.Repository, router project.Router) *project.Service {
	t.Helper()
	return project.NewTestProject(t, project.WithProjectDeps(project.Deps{Repo: store, Router: router}))
}

// onDaemon is a store where ownedProject is cloned at /clone on daemon d-1,
// on its default branch.
func onDaemon(t *testing.T) *project.MockRepository {
	t.Helper()
	store := ownedProjectRepo(t)
	store.ListProjectDaemonsForProjectFunc = func(_ context.Context, projectID string) ([]*core.ProjectDaemon, error) {
		branch := "main"
		return []*core.ProjectDaemon{{ProjectID: projectID, DaemonID: "d-1", Path: "/clone", DefaultBranch: &branch}}, nil
	}
	// No branch workspaces anywhere: a remove or reclone of the checkout is
	// not refused for them.
	store.ListWorktreesFunc = func(context.Context, db.WorktreeFilters) ([]*db.Worktree, error) { return nil, nil }
	store.TearDownProjectCheckoutFunc = func(ctx context.Context, _, _ string, guard func(context.Context) error, destroy func(context.Context, bool) bool) error {
		if err := guard(ctx); err != nil {
			return err
		}
		destroy(ctx, false)
		return nil
	}
	return store
}

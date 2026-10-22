// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package project

import (
	"context"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Repository is the slice of *db.Repo the project handlers use.
type Repository interface {
	CreateProject(ctx context.Context, project *db.Project) error
	GetProject(ctx context.Context, id string) (*db.Project, error)
	GetProjectByPathAndUser(ctx context.Context, path, userID string) (*db.Project, error)
	GetProjectByRemoteURLAndUser(ctx context.Context, remoteURL, userID string) (*db.Project, error)
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	ListProjects(ctx context.Context, filters db.ProjectFilters) ([]*db.Project, error)
	UpdateProject(ctx context.Context, project *db.Project, userID string) error
	SetProjectForgeName(ctx context.Context, id, userID, forgeProjectName string) (bool, error)
	TouchProject(ctx context.Context, id string, userID string) error
	DeleteProject(ctx context.Context, id string, userID string) error

	UpsertProjectDaemon(ctx context.Context, projectID, daemonID, path string, defaultBranch *string) error
	UpsertQueuedProjectDaemon(ctx context.Context, projectID, daemonID, path string, defaultBranch *string, requestID string) error
	ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*core.ProjectDaemon, error)
	ListProjectDaemonsForDaemon(ctx context.Context, daemonID string) ([]*core.ProjectDaemon, error)
	DeleteProjectDaemon(ctx context.Context, projectID, daemonID string) error

	CreateRepo(ctx context.Context, repo *core.Repo) error
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)

	CreateWorktree(ctx context.Context, worktree *db.Worktree) error
	ListWorktrees(ctx context.Context, filters db.WorktreeFilters) ([]*db.Worktree, error)
	UpdateWorktree(ctx context.Context, worktree *db.Worktree) error

	UpsertProjectConfigRecord(ctx context.Context, record *db.ProjectConfigRecord) error

	CreateSetting(ctx context.Context, setting *db.Setting) error
	GetSetting(ctx context.Context, userID string, projectID *string, key string) (*db.Setting, error)
	UpdateSetting(ctx context.Context, setting *db.Setting) error
}

// Router sends commands to a user's daemon. toolexec.DaemonRouter
// implements it.
type Router interface {
	// SendDaemonCommand runs one request/response command on the daemon the
	// user's commands route to.
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	// SendDaemonCommandToDaemon runs one on a specific daemon (reading a
	// fresh clone's forge name).
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	// ResolveDaemonID names the daemon SendDaemonCommand would route to.
	ResolveDaemonID(ctx context.Context, userID string) (string, error)
	// EnqueueDaemonCommand queues a command for the user's daemons'
	// next connect.
	EnqueueDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) (int, error)
	SendLoadProjectConfigs(ctx context.Context, userID, projectPath, requestID string) error
	SendWatchProjectConfigs(ctx context.Context, userID, projectPath string, includeInitial bool) error
}

// CloneDispatcher asks the control plane to clone a repository onto one of
// the caller's daemons. controlplane.Service implements it.
type CloneDispatcher interface {
	CloneRepoOntoDaemon(ctx context.Context, jwt string, req controlplane.CloneRepoRequest) (controlplane.CloneRepoResult, error)
}

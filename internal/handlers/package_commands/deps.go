// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package package_commands

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/pkgmgr"
)

// Router is the slice of toolexec.DaemonRouter ListCommands uses
// (daemoncmd.Sender).
type Router interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// Repository is the slice of *db.Repo the package-commands handlers use.
type Repository interface {
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	ListCommandFavorites(ctx context.Context, userID, projectID string) ([]string, error)
	AddCommandFavorite(ctx context.Context, userID, projectID, commandKey string) error
	RemoveCommandFavorite(ctx context.Context, userID, projectID, commandKey string) error
}

// Processes is the slice of *pkgmgr.Service the process rpcs use.
type Processes interface {
	RunCommand(ctx context.Context, req pkgmgr.RunRequest) (*pkgmgr.RunResponse, error)
	GetProcessesByWorktree(worktreeID string) []*shell.BackgroundProcess
	GetAllProcesses() []*shell.BackgroundProcess
	GetProcess(processID string) (*shell.BackgroundProcess, error)
	GetProcessOutput(processID string) (stdout, stderr string, err error)
	GetCombinedOutput(processID string) ([]shell.OutputLine, error)
	KillProcess(processID string) error
}

// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from the database; mock_gen.go mocks them. *db.Repo satisfies them.
package bgprocess

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
)

// OwnerReader resolves who owns a background process from its chat or its
// worktree's project.
type OwnerReader interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	GetProject(ctx context.Context, id string) (*db.Project, error)
}

// PersistenceStore is what SetupPersistence writes a process's rows with,
// plus what it hands the BackgroundManager.
type PersistenceStore interface {
	OwnerReader
	// GetBackgroundProcess and UpdateBackgroundProcessStatus are
	// shell.ProcessStore: the store is handed to the BackgroundManager.
	GetBackgroundProcess(ctx context.Context, id string) (*db.BackgroundProcess, error)
	UpdateBackgroundProcessStatus(ctx context.Context, id string, status db.BackgroundProcessStatus, exitCode *int, endedAt *time.Time) error
	CreateBackgroundProcess(ctx context.Context, process *db.BackgroundProcess) error
	CreateBackgroundProcessOutputBatch(ctx context.Context, lines []db.BackgroundProcessOutputLine) error
}

// EventStore is what SetupEvents publishes a process event to the owner's
// user_updates with.
type EventStore interface {
	OwnerReader
	CreateUserUpdate(ctx context.Context, update *db.UserUpdate) error
}

// RecoveryStore is what RecoverProcesses validates and reloads the running
// processes from. It is pkgmgr.ProcessStore: the store is handed to pkgmgr's
// RecoveryService.
type RecoveryStore interface {
	GetRunningBackgroundProcesses(ctx context.Context) ([]*db.BackgroundProcess, error)
	MarkStaleProcesses(ctx context.Context, processIDs []string) error
}

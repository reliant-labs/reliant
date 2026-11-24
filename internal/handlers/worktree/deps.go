// forge:deps — consumer-declared interfaces for this package's Deps.
// Repository is this package's own declaration of what the worktree handlers
// read and write; mock_gen.go mocks it. *db.Repo satisfies it, and the
// composition root hands it to New.
//
// It lists the methods the handlers call directly, then embeds the role of
// every callee they hand the repository to (workspacecreate, repo adoption,
// machine wake, daemon placement), so a callee that needs more points the
// compiler here.
package worktree

import (
	"context"

	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/machinewake"
	repopkg "github.com/reliant-labs/reliant/internal/repo"
	"github.com/reliant-labs/reliant/internal/workspacecreate"
)

// Recoveries is the recovery journal the recovery rpcs read and advance.
// store.WorktreeRecoveryExtStore satisfies it. Every query takes the owner and
// fails closed on a row that is not theirs.
type Recoveries interface {
	GetWorktreeRecovery(ctx context.Context, owner, id string) (*core.WorktreeRecovery, error)
	ListWorktreeRecoveries(ctx context.Context, owner, project string) ([]*core.WorktreeRecovery, error)
	BeginWorktreeRecoveryOperation(ctx context.Context, owner, id, operation, key, targetDaemonID string) error
	CompleteWorktreeRecoveryCheckout(ctx context.Context, owner, id, repo, phase, detail string) error
}

// Rows is the worktree row's two writes-or-lookups the handlers own: a create's
// prior row by its client-generated key, and the permanent delete.
// store.WorktreeExtStore satisfies it.
type Rows interface {
	// GetWorktreeByIdempotencyKey is a prior create's worktree for this key in
	// the project, nil when there is none (and for an empty key: the caller
	// opted out of idempotency).
	GetWorktreeByIdempotencyKey(ctx context.Context, projectID, key string) (*core.Worktree, error)
	// GetWorktree is the worktree row by id, archived or not; a missing one
	// is core.ErrWorktreeNotFound. It checks no owner: the caller decides.
	GetWorktree(ctx context.Context, id string) (*core.Worktree, error)
	// RemoveWorktree hard-deletes the row, archived or not; a missing one is
	// not an error.
	RemoveWorktree(ctx context.Context, id string) error
}

// ProjectReader is the project row by id, with no owner check: the caller
// decides access on the row (or reads through Deps.Projects).
// store.ProjectExtStore satisfies it.
type ProjectReader interface {
	GetProject(ctx context.Context, id string) (*core.Project, error)
}

// RepoLister is a project's nested repos. store.RepoExtStore satisfies it.
type RepoLister interface {
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
}

// PRLinker records the chats a pull request was opened from.
// chatprlinks.Service satisfies it.
type PRLinker interface {
	LinkWorktreePR(ctx context.Context, userID, worktreeID, prURL string) (int, error)
}

// DaemonReader is the daemons row by id, with no owner check: the caller
// decides with authz. store.DaemonExtStore satisfies it.
type DaemonReader interface {
	GetDaemon(ctx context.Context, id string) (*core.Daemon, error)
}

// Repository is everything the worktree handlers read and write.
type Repository interface {
	// Worktree rows: reads, idempotent creates, archive/unarchive, the
	// cleanup metadata the sweep reads, and the permanent delete.
	ListWorktrees(ctx context.Context, filters db.WorktreeFilters) ([]*db.Worktree, error)
	UnarchiveWorktree(ctx context.Context, id string) error
	UpdateWorktreeCleanupMetadata(ctx context.Context, id string, metadata *db.CleanupMetadata) error
	MergeWorktreeCleanupMetadata(ctx context.Context, id string, fn func(m *db.CleanupMetadata, archived bool) bool) error

	// The machines a project is installed on.
	ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*db.ProjectDaemon, error)

	// The chats archived with a worktree.
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	ListChats(ctx context.Context, filters db.ChatFilters) ([]*db.Chat, error)
	UpdateChatState(ctx context.Context, chatID string, state db.ChatState, reason string) error
	// LockChatRunControl takes the chat's run-control lock (runs.Service.LockRunControl):
	// archiving a chat holds it so no delivery starts a run around the archive.
	LockChatRunControl(ctx context.Context, chatID string) (release func(), err error)

	EmitUserRefetch(ctx context.Context, userID string, refetchType db.RefetchType, opts db.RefetchOpts) error

	// CreateWorktree, UpdateWorktree, ArchiveWorktree and
	// GetLiveWorktreeByName are called directly too; the roles below already
	// declare them. Worktree, project, repo and daemon reads go through
	// Deps.Rows, Deps.Projects, Deps.ProjectRows, Deps.Repos and Deps.Daemons.
	workspacecreate.Store
	repopkg.AdoptStore
	machinewake.Owners
	daemonplacement.WorktreeReader
	daemonplacement.Installs
}

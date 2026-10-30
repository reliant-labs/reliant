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
	"github.com/reliant-labs/reliant/internal/machinewake"
	repopkg "github.com/reliant-labs/reliant/internal/repo"
	"github.com/reliant-labs/reliant/internal/workspacecreate"
)

// Repository is everything the worktree handlers read and write.
type Repository interface {
	GetWorktreeRecovery(context.Context, string, string) (*db.WorktreeRecovery, error)
	ListWorktreeRecoveries(context.Context, string, string) ([]*db.WorktreeRecovery, error)
	BeginWorktreeRecoveryOperation(context.Context, string, string, string, string, string) error
	CompleteWorktreeRecoveryCheckout(context.Context, string, string, string, string, string) error
	CancelWorktreeRecovery(context.Context, string, string, string) error
	// Worktree rows: reads, idempotent creates, archive/unarchive, the
	// cleanup metadata the sweep reads, and the permanent delete.
	ListWorktrees(ctx context.Context, filters db.WorktreeFilters) ([]*db.Worktree, error)
	GetWorktreeByIdempotencyKey(ctx context.Context, projectID, key string) (*db.Worktree, error)
	UnarchiveWorktree(ctx context.Context, id string) error
	UpdateWorktreeCleanupMetadata(ctx context.Context, id string, metadata *db.CleanupMetadata) error
	MergeWorktreeCleanupMetadata(ctx context.Context, id string, fn func(m *db.CleanupMetadata, archived bool) bool) error
	DeleteWorktree(ctx context.Context, id string) error

	// The project a worktree belongs to, checked against the caller.
	GetProject(ctx context.Context, id string) (*db.Project, error)
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*db.ProjectDaemon, error)

	// The chats archived with a worktree.
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	ListChats(ctx context.Context, filters db.ChatFilters) ([]*db.Chat, error)
	UpdateChatState(ctx context.Context, chatID string, state db.ChatState, reason string) error
	// LockChatRunControl takes the chat's run-control lock (runs.Service.LockRunControl):
	// archiving a chat holds it so no delivery starts a run around the archive.
	LockChatRunControl(ctx context.Context, chatID string) (release func(), err error)

	EmitUserRefetch(ctx context.Context, userID string, refetchType db.RefetchType, opts db.RefetchOpts) error

	// ListDaemonsByUserID and AdoptWorktreeDaemon settle the machine of a
	// workspace whose row records none (daemonplacement.Resolver).
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
	AdoptWorktreeDaemon(ctx context.Context, id, daemonID string) (string, error)

	// GetWorktree, CreateWorktree, UpdateWorktree, ArchiveWorktree,
	// GetLiveWorktreeByName and ListReposByProject are called directly too;
	// the roles below already declare them.
	workspacecreate.Store
	repopkg.AdoptStore
	machinewake.Owners
	daemonplacement.WorktreeReader
	daemonplacement.Installs
}

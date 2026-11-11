// Copyright (c) 2025 Reliant Labs

package workspacecreate

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// The package's surface is two functions, Insert and Finish, that take their
// collaborators per call: the caller's Store, and a Machine bound to the
// workspace's owner daemon. There is no long-lived component to construct, so
// this contract declares those collaborators, and forge generates their mocks.

// Machine sends one command to the ONE daemon a workspace is created on. All
// of a workspace's checkouts, cleanups and the copy must reach the same
// machine, so the implementation is bound to the owner daemon up front.
// resp may be nil when the caller ignores the response.
type Machine interface {
	Send(ctx context.Context, commandType string, payload, resp any, timeoutMs int32) error
}

// Store is the slice of *db.Repo creation writes through.
type Store interface {
	CreateWorktree(ctx context.Context, worktree *db.Worktree) error
	UpdateWorktree(ctx context.Context, worktree *db.Worktree) error
	ArchiveWorktree(ctx context.Context, id string) error
	GetLiveWorktreeByName(ctx context.Context, projectID, name string) (*db.Worktree, error)
	ListWorktreesByBranch(ctx context.Context, projectID, branch string) ([]*db.Worktree, error)
	// SettleCheckoutTeardown records how a remove or reclone of the checkout
	// ended, once the machine says (checkoutteardown.Query).
	SettleCheckoutTeardown(ctx context.Context, id, state, detail string) (*db.CheckoutTeardown, error)
}

// FailedCreateStore is the slice of *db.Repo that RemoveFailedCreatesOn reads
// and records through when a machine connects.
type FailedCreateStore interface {
	ListFailedWorktreesForDaemon(ctx context.Context, daemonID string, since time.Time, limit int) ([]*db.Worktree, error)
	ListProjectDaemonsForDaemon(ctx context.Context, daemonID string) ([]*core.ProjectDaemon, error)
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
	MergeWorktreeCleanupMetadata(ctx context.Context, id string, fn func(m *db.CleanupMetadata, archived bool) bool) error
}

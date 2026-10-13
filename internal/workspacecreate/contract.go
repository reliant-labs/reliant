// Copyright (c) 2025 Reliant Labs

package workspacecreate

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
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

// Store is the slice of db.Repository creation writes through.
type Store interface {
	CreateWorktree(ctx context.Context, worktree *db.Worktree) error
	UpdateWorktree(ctx context.Context, worktree *db.Worktree) error
	ArchiveWorktree(ctx context.Context, id string) error
	GetLiveWorktreeByName(ctx context.Context, projectID, name string) (*db.Worktree, error)
	ListWorktreesByBranch(ctx context.Context, projectID, branch string) ([]*db.Worktree, error)
}

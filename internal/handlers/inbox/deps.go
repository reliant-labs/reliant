// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package inbox

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
)

// Repository is the slice of *db.Repo the inbox handlers use. It
// includes worktreesweep.StorageStore because the storage view and the
// clean-up read through the same store.
type Repository interface {
	ListWorktreeRecoveries(context.Context, string, string) ([]*core.WorktreeRecovery, error)
	worktreesweep.StorageStore
	// WorkflowResolver reads an activation's workflow, to tell which
	// automations still need setting up.
	launch.WorkflowResolver

	ListInboxPending(ctx context.Context, userID string) ([]*core.InboxPending, error)
	ListDismissedInboxItemIDs(ctx context.Context, userID string, itemIDs []string) (map[string]bool, error)
	DismissInboxItems(ctx context.Context, userID string, itemIDs []string, at time.Time) error
	RestoreInboxItems(ctx context.Context, userID string, itemIDs []string) error

	ListTriggers(ctx context.Context, f core.TriggerFilters) ([]*core.Trigger, error)
	// RecentTriggerOutcomes, not RecentTriggerFirings: the Inbox judges
	// health and never shows an event, so it does not read their bodies.
	RecentTriggerOutcomes(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error)
	FiringsSinceLastSuccess(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error)
	ListTriggerRegistrations(ctx context.Context, userID string, triggerIDs []string) (map[string]*core.TriggerRegistration, error)

	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
	// GetChat names the agents whose processes fill a machine near its
	// process limit.
	GetChat(ctx context.Context, id string) (*db.Chat, error)
}

// Sweeper starts a storage clean-up on one of the caller's machines.
// *worktreesweep.Sweeper implements it.
type Sweeper interface {
	StartCleanup(ctx context.Context, store worktreesweep.StorageStore, userID, daemonID string, wantIDs []string) (accepted []string, skipped []worktreesweep.CleanupResult, err error)
}

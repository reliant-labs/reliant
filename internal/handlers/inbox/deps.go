// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package inbox

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
)

// OrgResolver answers ListInbox's org question. *orgs.Resolver is one.
type OrgResolver interface {
	ListScope(ctx context.Context, userID, requested string, stamp orgs.Backfill) (*core.OrgScope, error)
}

// OrgLister lists the caller's live org memberships with their role in each, from the
// control plane (controlplane.Service.ListMyOrganizations), as the caller: jwt is their session.
type OrgLister interface {
	ListMyOrganizations(ctx context.Context, jwt string) ([]controlplane.MyOrganization, error)
}

// Repository is the slice of *db.Repo the inbox handlers use. It
// includes worktreesweep.StorageStore because the storage view and the
// clean-up read through the same store.
type Repository interface {
	ListWorktreeRecoveries(context.Context, string, string) ([]*core.WorktreeRecovery, error)
	worktreesweep.StorageStore
	// MarkDecisionStore lists marked worktrees and records keep / reclaim now.
	worktreesweep.MarkDecisionStore
	// WorkflowResolver reads an activation's workflow, to tell which
	// automations still need setting up.
	launch.WorkflowResolver

	// AssignUnassignedOrg stamps the caller's rows that have no org with
	// their default org (the lazy backfill, orgs.ListScope).
	AssignUnassignedOrg(ctx context.Context, userID, orgID string) error

	ListInboxPending(ctx context.Context, userID string) ([]*core.InboxPending, error)
	// HasRejectedOrgKeys and ListRejectedOrgKeysIn are the rejected org-owned API keys the
	// inbox lists to their orgs' owners and admins.
	HasRejectedOrgKeys(ctx context.Context) (bool, error)
	ListRejectedOrgKeysIn(ctx context.Context, orgIDs []string) ([]core.ProviderAccount, error)
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
	// GetProject decides an item about a project (a recovery, an automation)
	// through authz: one of an org the caller has left is not listed.
	GetProject(ctx context.Context, id string) (*db.Project, error)
}

// Sweeper starts a storage clean-up on one of the caller's machines.
// *worktreesweep.Sweeper implements it.
type Sweeper interface {
	StartCleanup(ctx context.Context, store worktreesweep.StorageStore, userID, daemonID string, wantIDs []string) (accepted []string, skipped []worktreesweep.CleanupResult, err error)
	// KeepMarked and ReclaimMarkedNow answer a marked worktree's item.
	KeepMarked(ctx context.Context, store worktreesweep.MarkDecisionStore, userID, id, markID string) error
	ReclaimMarkedNow(ctx context.Context, store worktreesweep.MarkDecisionStore, userID, id, markID string) error
	// GraceFor is when a marked worktree will be reclaimed.
	GraceFor(wt *core.Worktree) time.Time
}

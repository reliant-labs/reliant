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

// OrgMemberCounter is how many live members an org has, asked of the control
// plane now as userID (orgs.Resolver.LiveMembersFresh); known is false when it
// gives no count.
type OrgMemberCounter interface {
	LiveMembersFresh(ctx context.Context, userID, orgID string) (n int, known bool, err error)
}

// Recoveries lists a user's worktree recoveries. store.WorktreeRecoveryExtStore
// satisfies it.
type Recoveries interface {
	ListWorktreeRecoveries(ctx context.Context, owner, project string) ([]*core.WorktreeRecovery, error)
}

// Items is what the Inbox lists and what a person dismisses of it: the pending
// rows, the dismissals, and the attention incidents and managed chats the
// automation items are judged against. Every query takes the owner and fails
// closed on an empty one. store.InboxDismissalExtStore satisfies it.
type Items interface {
	ListInboxPending(ctx context.Context, userID string) ([]*core.InboxPending, error)
	ListDismissedInboxItemIDs(ctx context.Context, userID string, itemIDs []string) (map[string]bool, error)
	DismissInboxItems(ctx context.Context, userID string, itemIDs []string, at time.Time) error
	RestoreInboxItems(ctx context.Context, userID string, itemIDs []string) error
	// Incidents and safe-to-suppress managed-chat membership are scoped by
	// repository ownership, never YAML scope or a request project filter.
	// An unfinished observer without an open incident is not suppressible.
	ListAttentionIncidents(ctx context.Context, userID string) ([]*core.AttentionIncident, error)
	AttentionManagedChats(ctx context.Context, userID string, chatIDs []string) (map[string]bool, error)
}

// Firings is what an automation's health is judged from. store.TriggerEventExtStore
// satisfies it.
type Firings interface {
	// RecentTriggerOutcomes, not RecentTriggerFirings: the Inbox judges
	// health and never shows an event, so it does not read their bodies.
	RecentTriggerOutcomes(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error)
	FiringsSinceLastSuccess(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error)
	FiringsSinceLastUnmanagedSuccess(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error)
	// GetLatestTriggerEvent is a trigger's newest event with outcome, nil
	// when it has none.
	GetLatestTriggerEvent(ctx context.Context, triggerID string, outcome *core.TriggerEventOutcome) (*core.TriggerEvent, error)
}

// Notices are the automations of the org-provider-accounts §11 notices: paused
// for want of an org account, and still on their owner's own accounts.
// store.TriggerExtStore satisfies it.
type Notices interface {
	HasNoOrgAccountPauses(ctx context.Context) (bool, error)
	ListNoOrgAccountPauses(ctx context.Context, userID string, adminOrgIDs []string) ([]*core.Trigger, error)
	ListPersonallyFundedTriggers(ctx context.Context, userID string) ([]*core.Trigger, error)
}

// Repository is the slice of *db.Repo the inbox handlers use. It
// includes worktreesweep.StorageStore because the storage view and the
// clean-up read through the same store.
type Repository interface {
	worktreesweep.StorageStore
	// WorkflowResolver reads an activation's workflow, to tell which
	// automations still need setting up.
	launch.WorkflowResolver

	// AssignUnassignedOrg stamps the caller's rows that have no org with
	// their default org (the lazy backfill, orgs.ListScope).
	AssignUnassignedOrg(ctx context.Context, userID, orgID string) error

	// HasRejectedOrgKeys and ListRejectedOrgKeysIn are the rejected org-owned API keys the
	// inbox lists to their orgs' owners and admins.
	HasRejectedOrgKeys(ctx context.Context) (bool, error)
	ListRejectedOrgKeysIn(ctx context.Context, orgIDs []string) ([]core.ProviderAccount, error)

	ListTriggers(ctx context.Context, f core.TriggerFilters) ([]*core.Trigger, error)
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

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

// OrgKeys lists the org-owned API keys a provider rejected: the inbox shows them to the org's
// owners and admins. credentials/orgaccounts.Service satisfies it.
type OrgKeys interface {
	HasRejectedOrgKeys(ctx context.Context) (bool, error)
	ListRejectedOrgKeysIn(ctx context.Context, orgIDs []string) ([]core.ProviderAccount, error)
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
	// InboxEpisodes and InboxEpisodeFailures, not FiringsSinceLastUnmanagedSuccess
	// and FiringsSinceLastSuccess: the same episodes, without the skipped
	// firings that cannot change what the Inbox derives from them.
	InboxEpisodes(ctx context.Context, userID string, triggerIDs []string, perTrigger, window int) (map[string][]*core.TriggerEventWithRun, error)
	InboxEpisodeFailures(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error)
	// GetLatestTriggerEvent is a trigger's newest event with outcome, nil
	// when it has none.
	GetLatestTriggerEvent(ctx context.Context, triggerID string, outcome *core.TriggerEventOutcome) (*core.TriggerEvent, error)
	// PriorTriggerFailures is, for each triggerIDs[i], its newest failed
	// firing in the window before before[i] (nil for none): what makes a
	// failing automation's item habitual (triggers.Habitual).
	PriorTriggerFailures(ctx context.Context, userID string, triggerIDs []string, before []time.Time, window time.Duration) ([]*time.Time, error)
}

// Preferences reads the person's settings whose key is LIKE keyPattern, every
// scope: the Inbox's filters (inbox.show_*). store.SettingExtStore satisfies it.
type Preferences interface {
	ListSettingsByKey(ctx context.Context, userID, keyPattern string) ([]*core.Setting, error)
}

// Registrations reads the integration registrations of the caller's triggers,
// for the health of a polled automation. store.TriggerRegistrationExtStore
// satisfies it.
type Registrations interface {
	ListTriggerRegistrationsForUser(ctx context.Context, userID string, triggerIDs []string) (map[string]*core.TriggerRegistration, error)
}

// Notices are the automations of the org-provider-accounts §11 notices: paused
// for want of an org account, and still on their owner's own accounts.
// store.TriggerExtStore satisfies it.
type Notices interface {
	HasNoOrgAccountPauses(ctx context.Context) (bool, error)
	ListNoOrgAccountPauses(ctx context.Context, userID string, adminOrgIDs []string) ([]*core.Trigger, error)
	ListPersonallyFundedTriggers(ctx context.Context, userID string) ([]*core.Trigger, error)
	// ListTriggersForUser is the caller's own triggers (UserID is required).
	ListTriggersForUser(ctx context.Context, f core.TriggerFilters) ([]*core.Trigger, error)
}

// Storage is what the storage view, the forget and the clean-up read: the
// sweep's shared reads and the worktree rows (worktreesweep.Stores).
type Storage interface {
	worktreesweep.StorageStore
}

// ProjectReader reads a project by id. It checks no owner: every caller
// decides access on the row it returns. store.ProjectExtStore satisfies it.
type ProjectReader interface {
	GetProject(ctx context.Context, id string) (*db.Project, error)
}

// DaemonReader reads a machine by id. It checks no owner: the caller decides
// whose machine it is. store.DaemonExtStore satisfies it.
type DaemonReader interface {
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
}

// Repository is the slice of *db.Repo the inbox handlers use.
type Repository interface {
	// WorkflowResolver reads an activation's workflow, to tell which
	// automations still need setting up.
	launch.WorkflowResolver

	// AssignUnassignedOrg stamps the caller's rows that have no org with
	// their default org (the lazy backfill, orgs.ListScope).
	AssignUnassignedOrg(ctx context.Context, userID, orgID string) error

	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
}

// Chats reads the chat an item or a machine process names, to decide whether
// the caller may see its title or its state. store.ChatsWithActivityExtStore
// satisfies it.
type Chats interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
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

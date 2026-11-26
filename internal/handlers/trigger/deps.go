// forge:deps — consumer-declared interfaces for this package's Deps.
// Repository is this package's own declaration of what the trigger handlers
// read and write; mock_gen.go mocks it. *db.Repo satisfies it, and the
// composition root hands it to New.
//
// It lists the methods the handlers call directly, then embeds the role of
// every callee they hand the repository to (launch's workflow resolution,
// daemonchoice, automationGrants), so a callee that needs more points the
// compiler here. The connection store is still reached through
// db.Repo.Connections by assertion (Service.connections) until P6 lane Y.
package trigger

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonchoice"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/machinepool"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// OrgMoveResumer turns a trigger a project move paused back on
// (ResumeAfterOrgMove). store.ProjectOrgMoveExtStore satisfies it.
type OrgMoveResumer interface {
	// ResumePausedTrigger turns a trigger on only while the system's pause
	// for reason still stands and the row is the version checked (seen, its
	// updated_at), recording it in the trigger's change history.
	ResumePausedTrigger(ctx context.Context, id, reason string, seen time.Time) (bool, error)
}

// Repository is everything the trigger handlers read and write.
// OrgResolver answers the org questions of these rpcs. *orgs.Resolver is
// one; nil (or a nil *orgs.Resolver) is a deployment with no orgs.
type OrgResolver interface {
	Named(ctx context.Context, userID, requested string) (string, error)
	DefaultOrg(ctx context.Context, userID string) (string, error)
	ActingOrg(ctx context.Context, userID, requested string) (string, error)
	ListScope(ctx context.Context, userID, requested string, stamp orgs.Backfill) (*core.OrgScope, error)
	// LiveMembersFresh is how many live members orgID has, asked now; known
	// is false when the control plane gives no count.
	LiveMembersFresh(ctx context.Context, userID, orgID string) (n int, known bool, err error)
}

// Owners re-owns an orphaned trigger in place (AdoptAs), only while
// previousOwner still owns it; with offerID it accepts that offer.
// internal/triggerowner's Service satisfies it.
type Owners interface {
	AdoptTrigger(ctx context.Context, t *core.Trigger, previousOwner, offerID string, reads ...db.OrgRead) error
}

type Repository interface {
	triggers.OwnerSource
	// DefaultConnection is the owner's default login for an integration: an
	// integration trigger's write path reads it, so a repository without it
	// does not compile (it used to be asserted at run time, and a repository
	// without it made every integration trigger answer Unavailable).
	DefaultConnection(ctx context.Context, userID, integrationID string) (*core.Connection, error)
	// AssignUnassignedOrg stamps the caller's rows that have no org with
	// their default org (the lazy backfill, orgs.ListScope).
	AssignUnassignedOrg(ctx context.Context, userID, orgID string) error
	RunTx(context.Context, orm.TxPolicy, func(context.Context) error) error
	ListSettingsByKey(context.Context, string, string) ([]*db.Setting, error)
	ListRunNodeExecutions(ctx context.Context, workflowID string, limit int) ([]*db.StepExecution, error)
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)

	launch.WorkflowResolver
	daemonchoice.Reader
}

// Pools reads the machine pool a pool-bound trigger names, and its members
// (each gets the trigger's automation grant). store.MachinePoolExtStore
// satisfies it; machinepool.MemberReader is the part AgreedCatalog reads.
// Firings reads each trigger's recent firings for the list and detail views.
// store.TriggerEventExtStore satisfies it.
type Firings interface {
	RecentTriggerFirings(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error)
}

// WebhookSecrets stores a webhook trigger's sealed HMAC secret; nil clears it.
// store.TriggerExtStore satisfies it.
type WebhookSecrets interface {
	SetTriggerWebhookSecret(ctx context.Context, id string, sealed []byte) error
}

// Triggers reads and removes stored triggers and their webhook credentials.
// store.TriggerExtStore satisfies it.
type Triggers interface {
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
	ListTriggersForUser(ctx context.Context, f core.TriggerFilters) ([]*core.Trigger, error)
	RemoveTrigger(ctx context.Context, id string) error
	SetTriggerWebhookTokenHash(ctx context.Context, id string, hash []byte) error
	GetTriggerWebhookCredentials(ctx context.Context, id string) (*core.TriggerWebhookCredentials, error)
}

// FiringReads reads the firings and the feed of a trigger.
// store.TriggerEventExtStore satisfies it.
type FiringReads interface {
	ListTriggerEventsPage(ctx context.Context, f core.TriggerEventFilters) ([]*core.TriggerEventWithRun, bool, error)
	GetTriggerEventByChatID(ctx context.Context, chatID string) (*core.TriggerEvent, error)
}

// RegistrationReads reads the integration registrations a trigger holds.
// store.TriggerRegistrationExtStore satisfies it.
type RegistrationReads interface {
	ListTriggerRegistrationsForUser(ctx context.Context, userID string, triggerIDs []string) (map[string]*core.TriggerRegistration, error)
}

// TriggerWrites writes a trigger's definition under the owner's org lock, only
// while every org the handler's decision read still holds. Called with the ctx
// of the transaction the handler opens (Deps.Repo.RunTx), each joins it.
// store.TriggerWritesExtStore satisfies it.
type TriggerWrites interface {
	CreateTriggerInOrg(ctx context.Context, t *core.Trigger, reads ...db.OrgRead) error
	UpdateTriggerInOrg(ctx context.Context, t *core.Trigger, reads ...db.OrgRead) error
	// EnableTrigger turns a trigger on or off in one transaction and records
	// the change.
	EnableTrigger(ctx context.Context, id string, enabled bool) error
	// TriggerChanges reads a trigger's change history, owner scoped.
	TriggerChanges(ctx context.Context, userID, triggerID string) ([]core.TriggerChange, error)
	// LockActivationDependencies serializes an activation with edits of what
	// it depends on.
	LockActivationDependencies(ctx context.Context, user, project string) error
}

type Pools interface {
	machinepool.MemberReader
	// GetMachinePoolByName resolves the pool an agent names by name
	// (create/update_trigger_activation's machine_pool).
	GetMachinePoolByName(ctx context.Context, userID, name string) (*core.MachinePool, error)
}

// AutomationKeyStore is where automationGrants keeps the delegated
// daemon:resume token of each (user, daemon) with an enabled trigger.
// credentials/automationtokens.Service satisfies it.
type AutomationKeyStore interface {
	SetAutomationToken(ctx context.Context, userID, daemonID, token string) error
	DeleteAutomationToken(ctx context.Context, userID, daemonID string) error
}

// TriggerLister lists a user's triggers: the handlers' List, and
// automationGrants' check that no enabled trigger still names a daemon
// before it revokes that daemon's token.
type TriggerLister interface {
	ListTriggersForUser(ctx context.Context, f core.TriggerFilters) ([]*core.Trigger, error)
}

// AutomationDrafter writes an automation draft from a description
// (DraftAutomation). The model call runs on the worker — the api-server has no
// LLM drivers — so internal/app implements it over Temporal.
//
// A draft only the caller's own account could make, in an org whose accounts
// cannot, with no consent to it on record, fails with ErrDraftNeedsConsent.
type AutomationDrafter interface {
	DraftAutomation(ctx context.Context, in DraftRequest) (*reliantv1.AutomationDraft, error)
}

// DraftRequest is a validated DraftAutomationRequest and its caller.
type DraftRequest struct {
	UserID string
	// OrgID is the org of the project the draft is for, from the project row
	// ("" for a project in no org): whose accounts may draft it (§10).
	OrgID       string
	Description string
	Workflow    string
	Timezone    string
}

// Chats reads the chat a launch event belongs to, for the access check. store.ChatExtStore satisfies it.
type Chats interface {
	GetChatRow(ctx context.Context, id string) (*db.ChatRow, error)
}

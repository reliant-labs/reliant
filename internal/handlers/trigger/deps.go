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

	"github.com/reliant-labs/forge/pkg/orm"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonchoice"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
)

// Repository is everything the trigger handlers read and write.
type Repository interface {
	RunTx(context.Context, orm.TxPolicy, func(context.Context) error) error
	LockActivationDependencies(context.Context, string, string) error
	ListSettingsByKey(context.Context, string, string) ([]*db.Setting, error)
	ListRunNodeExecutions(ctx context.Context, workflowID string, limit int) ([]*db.StepExecution, error)
	// Trigger rows and their webhook credentials.
	CreateTrigger(ctx context.Context, t *core.Trigger) error
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
	ListTriggerChanges(ctx context.Context, userID, triggerID string) ([]core.TriggerChange, error)
	UpdateTrigger(ctx context.Context, t *core.Trigger) error
	DeleteTrigger(ctx context.Context, id string) error
	SetTriggerEnabled(ctx context.Context, id string, enabled bool) error
	SetTriggerWebhookSecret(ctx context.Context, id string, sealed []byte) error
	SetTriggerWebhookTokenHash(ctx context.Context, id string, hash []byte) error
	GetTriggerWebhookCredentials(ctx context.Context, id string) (*core.TriggerWebhookCredentials, error)

	// Firings, the feed and the integration registrations a trigger holds.
	RecentTriggerFirings(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error)
	ListTriggerEvents(ctx context.Context, f core.TriggerEventFilters) ([]*core.TriggerEventWithRun, bool, error)
	GetTriggerEventByChatID(ctx context.Context, chatID string) (*core.TriggerEvent, error)
	ListTriggerRegistrations(ctx context.Context, userID string, triggerIDs []string) (map[string]*core.TriggerRegistration, error)

	// The project, worktree and chat a trigger names, checked against the
	// caller.
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	GetChatRow(ctx context.Context, id string) (*db.ChatRow, error)

	// The machine pool a pool-bound trigger names, and its members (each gets
	// the trigger's automation grant).
	GetMachinePoolForUser(ctx context.Context, userID, id string) (*core.MachinePool, error)
	ListMachinePoolMembers(ctx context.Context, pool *core.MachinePool, projectID string) ([]core.MachinePoolMemberRow, error)

	// ListTriggers is called directly too; TriggerLister declares it.
	launch.WorkflowResolver
	daemonchoice.Reader
	AutomationKeyStore
	TriggerLister
}

// AutomationKeyStore is where automationGrants keeps the delegated
// daemon:resume token of each (user, daemon) with an enabled trigger.
type AutomationKeyStore interface {
	SetAutomationToken(ctx context.Context, userID, daemonID, token string) error
	DeleteAutomationToken(ctx context.Context, userID, daemonID string) error
}

// TriggerLister lists a user's triggers: the handlers' List, and
// automationGrants' check that no enabled trigger still names a daemon
// before it revokes that daemon's token.
type TriggerLister interface {
	ListTriggers(ctx context.Context, f core.TriggerFilters) ([]*core.Trigger, error)
}

// AutomationDrafter writes an automation draft from a description
// (DraftAutomation). The model call runs on the worker — the api-server has no
// LLM drivers — so internal/app implements it over Temporal.
type AutomationDrafter interface {
	DraftAutomation(ctx context.Context, in DraftRequest) (*reliantv1.AutomationDraft, error)
}

// DraftRequest is a validated DraftAutomationRequest and its caller.
type DraftRequest struct {
	UserID      string
	Description string
	Workflow    string
	Timezone    string
}

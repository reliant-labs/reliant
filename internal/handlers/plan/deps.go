// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. The composition root fills
// them from the generated stores (internal/store): store.PlanExtStore,
// store.TaskExtStore, store.ThreadExtStore and store.ChatExtStore.
package plan

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// PlanStore reads and writes plans. store.PlanExtStore satisfies it.
type PlanStore interface {
	GetPlan(ctx context.Context, id string) (*core.Plan, error)
	InsertPlan(ctx context.Context, p *core.Plan) error
	ReplacePlan(ctx context.Context, p *core.Plan) error
	RemovePlan(ctx context.Context, id string) error
	ListPlansByChatID(ctx context.Context, chatID string) ([]*core.Plan, error)
}

// TaskStore lists a plan's tasks. store.TaskExtStore satisfies it.
type TaskStore interface {
	ListTasksByPlan(ctx context.Context, planID string) ([]*core.Task, error)
}

// ThreadStore resolves a plan's thread. store.ThreadExtStore satisfies it.
type ThreadStore interface {
	GetThread(ctx context.Context, id string) (*core.Thread, error)
}

// ChatStore reads the chat that authorizes a plan. store.ChatExtStore
// satisfies it.
type ChatStore interface {
	GetChatRow(ctx context.Context, id string) (*core.ChatRow, error)
}

// ChatRefetcher tells a chat's clients to re-read its plan and tasks.
// *db.Repo satisfies it.
type ChatRefetcher interface {
	EmitChatRefetch(ctx context.Context, chatID string, refetchType core.RefetchType) error
}

// TxRunner opens the transactions of the read-check-write rpcs. *orm.Client
// satisfies it.
type TxRunner interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, fn func(ctx context.Context) error) error
}

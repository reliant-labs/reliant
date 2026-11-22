// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. The composition root fills
// them from the generated stores (internal/store): store.TaskExtStore,
// store.PlanExtStore, store.ThreadExtStore and store.ChatExtStore.
package task

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// TaskStore reads and writes tasks. store.TaskExtStore satisfies it.
type TaskStore interface {
	GetTask(ctx context.Context, id string) (*core.Task, error)
	InsertTask(ctx context.Context, t *core.Task) error
	ReplaceTask(ctx context.Context, t *core.Task) error
	RemoveTask(ctx context.Context, id string) error
	ListTasksByPlan(ctx context.Context, planID string) ([]*core.Task, error)
}

// PlanStore reads the plan a task belongs to. store.PlanExtStore satisfies
// it.
type PlanStore interface {
	GetPlan(ctx context.Context, id string) (*core.Plan, error)
}

// ThreadStore resolves a plan's thread. store.ThreadExtStore satisfies it.
type ThreadStore interface {
	GetThread(ctx context.Context, id string) (*core.Thread, error)
}

// ChatStore reads the chat that authorizes a task. store.ChatExtStore
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

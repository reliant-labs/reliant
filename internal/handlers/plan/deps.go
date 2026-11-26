// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. The composition root fills
// them from the generated stores (internal/store): store.PlanExtStore,
// store.TaskExtStore, store.ThreadExtStore and store.ChatExtStore.
package plan

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// PlanStore reads plans outside the CRUD rpcs. store.PlanExtStore satisfies
// it. The plan CRUD rpcs run on Deps.DB through the generated ops.
type PlanStore interface {
	GetPlan(ctx context.Context, id string) (*core.Plan, error)
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

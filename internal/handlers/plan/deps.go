// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package plan

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// Repository is the slice of *db.Repo the plan handlers use.
type Repository interface {
	GetChatRow(ctx context.Context, id string) (*db.ChatRow, error)
	GetThread(ctx context.Context, id string) (*db.Thread, error)
	CreatePlan(ctx context.Context, plan *db.Plan) error
	GetPlan(ctx context.Context, id string) (*db.Plan, error)
	ListPlansByChatID(ctx context.Context, chatID string) ([]*db.Plan, error)
	UpdatePlan(ctx context.Context, plan *db.Plan) error
	DeletePlan(ctx context.Context, id string) error
	ListTasksByPlan(ctx context.Context, planID string) ([]*db.Task, error)
	EmitChatRefetch(ctx context.Context, chatID string, refetchType db.RefetchType) error
}

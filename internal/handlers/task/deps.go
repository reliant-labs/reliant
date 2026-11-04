// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package task

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// Repository is the slice of *db.Repo the task handlers use.
type Repository interface {
	GetChatRow(ctx context.Context, id string) (*db.ChatRow, error)
	GetThread(ctx context.Context, id string) (*db.Thread, error)
	GetPlan(ctx context.Context, id string) (*db.Plan, error)
	CreateTask(ctx context.Context, task *db.Task) error
	GetTask(ctx context.Context, id string) (*db.Task, error)
	ListTasksByPlan(ctx context.Context, planID string) ([]*db.Task, error)
	UpdateTask(ctx context.Context, task *db.Task) error
	DeleteTask(ctx context.Context, id string) error
	EmitChatRefetch(ctx context.Context, chatID string, refetchType db.RefetchType) error
}

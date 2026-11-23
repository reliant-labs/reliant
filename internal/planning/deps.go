// forge:deps — consumer-declared interfaces for this package's Deps.
// yours: scaffolded once, never touched again — forge will not overwrite this file
// Dependency interfaces; canonical pattern in `forge skill load service-layer`.
package planning

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// ─── Dependency interfaces ──────────────────────────────────────────
//
// Declare here the *behaviour* this package needs from each thing it
// calls — one interface per dep, named for the role it plays, NOT for
// the concrete type that will satisfy it. Each one gets a matching
// field in Deps (service.go), and that field must be the interface,
// not the concrete type: `Store Store`, never `Store *db.Postgres`.
// That is what makes New(Deps{...}) constructible from mocks alone,
// and it is lint-enforced for every package
// (forgeconv-deps-are-interfaces).
//
// Keep each one minimal — only the methods this package calls. When
// the far end is another internal package, its own Service interface
// is usually the right type to use directly, and its mock_gen.go
// already has a mock of it.
//
// `forge generate` writes a mock for EVERY interface in this file,
// alongside the Service mock, into mock_gen.go — the first line above
// is what tells it to. Never hand-roll a fake for something declared
// here. `forge fix narrow-deps internal/planning` appends the
// interfaces it narrows a wide dep to here too.
//
//	// Store is this package's persistence boundary.
//	type Store interface {
//		Get(ctx context.Context, id string) (Thing, error)
//	}

// Plans reads and rewrites plan rows. store.PlanExtStore satisfies it.
type Plans interface {
	GetPlanByThreadID(ctx context.Context, threadID string) (*core.Plan, error)
	ReplacePlan(ctx context.Context, p *core.Plan) error
}

// Tasks reads and rewrites task rows. store.TaskExtStore satisfies it.
type Tasks interface {
	ListTasksByPlan(ctx context.Context, planID string) ([]*core.Task, error)
	ReplaceTask(ctx context.Context, t *core.Task) error
}

// Dependencies holds the typed links between tasks.
// store.TaskDependencyExtStore satisfies it.
type Dependencies interface {
	InsertTaskDependency(ctx context.Context, d *core.TaskDependency) error
	ListDependenciesByPlan(ctx context.Context, planID string) ([]*core.TaskDependency, error)
	DeleteTaskDependencyByPair(ctx context.Context, fromTaskID, toTaskID string, depType int32) error
}

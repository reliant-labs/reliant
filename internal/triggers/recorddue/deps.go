// forge:deps — consumer-declared interfaces for this package's Deps.
package recorddue

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// Store is what the scan reads. *db.Repo satisfies it.
type Store interface {
	// WithDuePRPipelinesAfter calls fn with the due pr_pipeline rows (at
	// most limit) after the cursor, and their records, the rows locked
	// while fn runs; it does not call fn when none is due. It returns the
	// last row of a full page, the next call's cursor, or nil to start over.
	WithDuePRPipelinesAfter(ctx context.Context, now time.Time, limit int, after *core.PRPipeline, fn func([]core.DuePRPipeline) error) (*core.PRPipeline, error)
	ListEnabledTriggersOfKind(ctx context.Context, kind core.TriggerKind) ([]*core.Trigger, error)
	GetTriggerEventByDedupe(ctx context.Context, kind core.TriggerEventKind, dedupeKey string) (*core.TriggerEvent, error)
}

// Intake records an arrived event and starts its fire. *triggers.Intake
// satisfies it.
type Intake interface {
	Accept(ctx context.Context, trigger *core.Trigger, ev triggers.InboundEvent, opts triggers.AcceptOptions) (*triggers.AcceptResult, error)
}

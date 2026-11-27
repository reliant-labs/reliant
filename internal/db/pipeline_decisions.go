package db

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Owner decisions (pipeline_decisions), read side. Each method is the
// internal/store query of the same name.

// ActivePipelineDecisionsFor lists the decisions in force at now on the
// subject at head, newest first.
func (r *Repo) ActivePipelineDecisionsFor(ctx context.Context, kind core.PipelineDecisionSubjectKind, subjectID, head string, now time.Time) ([]*core.PipelineDecision, error) {
	return store.ActivePipelineDecisionsFor(ctx, r.ORM(), kind, subjectID, head, now)
}

// ActivePipelineHold returns the hold in force at now on the PR record, or nil.
func (r *Repo) ActivePipelineHold(ctx context.Context, recordID string, now time.Time) (*core.PipelineDecision, error) {
	return store.ActivePipelineHold(ctx, r.ORM(), recordID, now)
}

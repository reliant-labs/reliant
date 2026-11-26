package db

import (
	"context"
	"encoding/json"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// The PR pipeline's per-PR state (pr_pipeline, pr_head_verdicts,
// pipeline_attempts). Each method is the internal/store query of the same
// name; its doc there says what it locks and which errors it returns.

// ListOpenPRPipelines lists a repository's rows that are not landed or
// closed: the daily PR sweep's input.
func (r *Repo) ListOpenPRPipelines(ctx context.Context, repoID string) ([]*core.PRPipeline, error) {
	return store.ListOpenPRPipelines(ctx, r.ORM(), repoID)
}

// GetPRPipeline returns the PR record's pipeline row, or nil.
func (r *Repo) GetPRPipeline(ctx context.Context, recordID string) (*core.PRPipeline, error) {
	return store.GetPRPipeline(ctx, r.ORM(), recordID)
}

// EnsurePRPipeline creates a new PR's row, or returns its existing row
// unchanged.
func (r *Repo) EnsurePRPipeline(ctx context.Context, p core.PRPipelineNew) (*core.PRPipeline, bool, error) {
	return store.EnsurePRPipeline(ctx, r.ORM(), p)
}

// ResetPRPipelineForHead starts the PR over at awaiting_review on head, if
// the row is still at version.
func (r *Repo) ResetPRPipelineForHead(ctx context.Context, recordID string, version int64, head string, next core.PRPipelineNext) (*core.PRPipeline, error) {
	return store.ResetPRPipelineForHead(ctx, r.ORM(), recordID, version, head, next)
}

// TransitionPRPipeline writes to's mutable fields, if the row is still at
// version: a lost race is core.ErrPipelineVersionConflict.
func (r *Repo) TransitionPRPipeline(ctx context.Context, recordID string, version int64, to core.PRPipeline) (*core.PRPipeline, error) {
	return store.TransitionPRPipeline(ctx, r.ORM(), recordID, version, to)
}

// ListDuePRPipelines is the due scan, FOR UPDATE SKIP LOCKED: run it in the
// transaction that acts on the rows.
func (r *Repo) ListDuePRPipelines(ctx context.Context, now time.Time, limit int) ([]*core.PRPipeline, error) {
	return store.ListDuePRPipelines(ctx, r.ORM(), now, limit)
}

// InsertPRHeadVerdict appends a verdict; a repeated (record, head, round) is
// core.ErrPipelineVerdictExists.
func (r *Repo) InsertPRHeadVerdict(ctx context.Context, v core.PRHeadVerdictRecord) (*core.PRHeadVerdictRecord, error) {
	return store.InsertPRHeadVerdict(ctx, r.ORM(), v)
}

// LatestPRHeadVerdict returns the highest round's verdict on head, or nil.
func (r *Repo) LatestPRHeadVerdict(ctx context.Context, recordID, head string) (*core.PRHeadVerdictRecord, error) {
	return store.LatestPRHeadVerdict(ctx, r.ORM(), recordID, head)
}

// NextPipelineAttempt starts the next numbered attempt at (subject, stage,
// signature).
func (r *Repo) NextPipelineAttempt(ctx context.Context, a core.PipelineAttempt) (*core.PipelineAttempt, error) {
	return store.NextPipelineAttempt(ctx, r.ORM(), a)
}

// FinishPipelineAttempt records how a running attempt ended.
func (r *Repo) FinishPipelineAttempt(ctx context.Context, id string, outcome core.PipelineAttemptOutcome, detail json.RawMessage) (*core.PipelineAttempt, error) {
	return store.FinishPipelineAttempt(ctx, r.ORM(), id, outcome, detail)
}

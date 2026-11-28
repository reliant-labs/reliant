package db

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// The reads the pr.* server actions make beyond the pipeline store's own.
// Each method is the internal/store query of the same name.

// GetPRRecordByID returns the PR record with id, or nil.
func (r *Repo) GetPRRecordByID(ctx context.Context, id string) (*core.PRRecord, error) {
	return store.GetPRRecordByID(ctx, r.ORM(), id)
}

// GetPRRecord returns the record of the repository's PR number among the
// given repository identities, or nil.
func (r *Repo) GetPRRecord(ctx context.Context, provider, repository string, number int, repositoryIDs []string) (*core.PRRecord, error) {
	return store.GetPRRecord(ctx, r.ORM(), provider, repository, number, repositoryIDs)
}

// ListPRRecordsAtHead lists the repository's other records at head.
func (r *Repo) ListPRRecordsAtHead(ctx context.Context, provider, repositoryID, head, except string) ([]*core.PRRecord, error) {
	return store.ListPRRecordsAtHead(ctx, r.ORM(), provider, repositoryID, head, except)
}

// ListPRHeadVerdicts lists every verdict on the record, oldest first.
func (r *Repo) ListPRHeadVerdicts(ctx context.Context, recordID string) ([]*core.PRHeadVerdictRecord, error) {
	return store.ListPRHeadVerdicts(ctx, r.ORM(), recordID)
}

// ListPipelineAttempts lists a subject's attempts at stage, oldest first.
func (r *Repo) ListPipelineAttempts(ctx context.Context, kind core.PipelineAttemptSubjectKind, subjectID string, stage core.PipelineAttemptStage) ([]*core.PipelineAttempt, error) {
	return store.ListPipelineAttempts(ctx, r.ORM(), kind, subjectID, stage)
}

// LatestQueueEntryForRecord returns the record's most recent queue entry, or nil.
func (r *Repo) LatestQueueEntryForRecord(ctx context.Context, recordID string) (*core.QueueEntry, error) {
	return store.LatestQueueEntryForRecord(ctx, r.ORM(), recordID)
}

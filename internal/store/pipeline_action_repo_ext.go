package store

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The reads the pr.* server actions make beyond the pipeline store's own
// (internal/pipeline/practions; pipeline build plan M5): a record by id, a
// PR's verdicts and attempts in full, its latest queue entry, and the other
// records at its head. Each reads one PR's handful of rows.

// GetPRRecordByID returns the PR record with id, or nil when there is none.
func GetPRRecordByID(ctx context.Context, db orm.Context, id string) (*core.PRRecord, error) {
	var rows []prRecordRow
	if err := db.Bun().NewRaw(`SELECT `+prRecordColumns+` FROM pr_records WHERE id = ?`, id).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("get pr record %s: %w", id, err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0].core()
}

// ListPRRecordsAtHead lists the other records of a repository whose head is
// head, oldest number first: the PRs that carry the same commit (supersession
// by the same head). It never lists except.
func ListPRRecordsAtHead(ctx context.Context, db orm.Context, provider, repositoryID, head, except string) ([]*core.PRRecord, error) {
	var rows []prRecordRow
	if err := db.Bun().NewRaw(`SELECT `+prRecordColumns+` FROM pr_records
		WHERE provider = ? AND repository_id = ? AND head_sha = ? AND id <> ?
		ORDER BY number LIMIT 50`, provider, repositoryID, head, except).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list pr records at head: %w", err)
	}
	out := make([]*core.PRRecord, 0, len(rows))
	for _, r := range rows {
		rec, err := r.core()
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// ListPRHeadVerdicts lists every verdict on the PR record, on every head, in
// the order they were written.
func ListPRHeadVerdicts(ctx context.Context, db orm.Context, recordID string) ([]*core.PRHeadVerdictRecord, error) {
	var rows []prHeadVerdictRow
	if err := db.Bun().NewRaw(`SELECT `+prHeadVerdictColumns+` FROM pr_head_verdicts
		WHERE record_id = ? ORDER BY created_at, round, id LIMIT 1000`, recordID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list pr head verdicts: %w", err)
	}
	out := make([]*core.PRHeadVerdictRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.core())
	}
	return out, nil
}

// ListPipelineAttempts lists a subject's attempts at stage, in the order
// they started.
func ListPipelineAttempts(ctx context.Context, db orm.Context, kind core.PipelineAttemptSubjectKind, subjectID string, stage core.PipelineAttemptStage) ([]*core.PipelineAttempt, error) {
	var rows []*core.PipelineAttempt
	if err := db.Bun().NewRaw(`SELECT `+pipelineAttemptColumns+` FROM pipeline_attempts
		WHERE subject_kind = ? AND subject_id = ? AND stage = ? ORDER BY started_at, attempt, id LIMIT 1000`,
		kind, subjectID, stage).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list pipeline attempts: %w", err)
	}
	return rows, nil
}

// LatestQueueEntryForRecord returns the PR record's most recent queue entry,
// in any state, or nil when it was never queued.
func LatestQueueEntryForRecord(ctx context.Context, db orm.Context, recordID string) (*core.QueueEntry, error) {
	var rows []*core.QueueEntry
	if err := db.Bun().NewRaw(`SELECT `+queueEntryColumns+` FROM queue_entries
		WHERE record_id = ? ORDER BY enqueued_at DESC, id DESC LIMIT 1`, recordID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("latest queue entry: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

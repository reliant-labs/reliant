// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// The workflow_check_runs queries: the engine's record of the GitHub check
// run a workflow's `check:` block declares (internal/workflowcheck). One row
// per firing and check name. The fire path opens it, the run stamps its
// start and end on it, and the worker's syncer claims due rows with SKIP
// LOCKED, reads the firing and its run (ObserveWorkflowCheckRun) and writes
// back what it wrote to GitHub. Functions take (ctx, orm.Context) and speak
// core.WorkflowCheckRun: the jsonb output is mapped at this boundary.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/crud"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// launchEventOfChatSQL is the firing that launched a chat: its earliest
// event, the same rule as GetTriggerEventByChatID and SetLaunchEventRunStatus.
const launchEventOfChatSQL = `(SELECT l.id FROM trigger_events l WHERE l.chat_id = ?0::text ORDER BY l.created_at ASC, l.id ASC LIMIT 1)`

// InsertWorkflowCheckRun writes row, doing nothing when its firing already has
// a check of that name: the first row is the record, and a retried or
// redriven fire must not add a second. created reports whether this call
// wrote it.
func InsertWorkflowCheckRun(ctx context.Context, db orm.Context, row *core.WorkflowCheckRun) (created bool, err error) {
	now := time.Now().UTC()
	if row.CreatedAt.IsZero() {
		row.CreatedAt = now
	}
	if row.UpdatedAt.IsZero() {
		row.UpdatedAt = row.CreatedAt
	}
	if row.NextSyncAt.IsZero() {
		row.NextSyncAt = row.CreatedAt
	}
	res, err := db.Bun().NewInsert().
		Model(&WorkflowCheckRun{
			ID: row.ID, TriggerEventID: row.TriggerEventID, Name: row.Name,
			UserID: row.UserID, OrgID: row.OrgID, ActingIdentity: row.ActingIdentity, Automation: row.Automation,
			Owner: row.Owner, Repo: row.Repo, HeadSha: row.HeadSHA,
			TimeoutSeconds: int64(row.Timeout / time.Second),
			NextSyncAt:     row.NextSyncAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}).
		ExcludeColumn(WorkflowCheckRunFieldStartedAt, WorkflowCheckRunFieldOutput, WorkflowCheckRunFieldGithubCheckRunID,
			WorkflowCheckRunFieldPausedSince, WorkflowCheckRunFieldClaimedUntil, WorkflowCheckRunFieldFinishedAt).
		On("CONFLICT (trigger_event_id, name) DO NOTHING").
		Returning("NULL").
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to create workflow check run: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to create workflow check run: %w", err)
	}
	return n > 0, nil
}

// MarkWorkflowCheckRunsStarted stamps the checks of chatID's launch firing as
// started — the first stamp stands — and makes them due now. A chat with no
// check is a no-op.
func MarkWorkflowCheckRunsStarted(ctx context.Context, db orm.Context, chatID string, at time.Time) error {
	_, err := db.Bun().NewRaw(`UPDATE workflow_check_runs
SET started_at = COALESCE(started_at, ?1::timestamptz),
    next_sync_at = LEAST(next_sync_at, ?1::timestamptz),
    updated_at = ?1::timestamptz
WHERE trigger_event_id = `+launchEventOfChatSQL+`
  AND finished_at IS NULL`, chatID, at).Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to mark workflow check runs started: %w", err)
	}
	return nil
}

// RecordWorkflowCheckRunsEnded records how chatID's launch run ended on its
// firing's checks — the first record stands, so a retried report changes
// nothing — and makes them due now.
func RecordWorkflowCheckRunsEnded(ctx context.Context, db orm.Context, chatID string, ending core.WorkflowCheckEnding, at time.Time) error {
	var output any
	if ending.Output != nil {
		raw, err := json.Marshal(ending.Output)
		if err != nil {
			ending.OutputError = fmt.Sprintf("check.output is not JSON: %v", err)
		} else {
			output = string(raw)
		}
	}
	_, err := db.Bun().NewRaw(`UPDATE workflow_check_runs
SET ended_status = ?1::text, verdict = ?2::text, output = ?3::jsonb, output_error = ?4::text,
    next_sync_at = LEAST(next_sync_at, ?5::timestamptz),
    updated_at = ?5::timestamptz
WHERE trigger_event_id = `+launchEventOfChatSQL+`
  AND ended_status = ''
  AND finished_at IS NULL`, chatID, ending.Status, ending.Verdict, output, ending.OutputError, at).Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to record workflow check runs ended: %w", err)
	}
	return nil
}

// ClaimWorkflowCheckRuns leases up to max due, unfinished checks whose lease
// is free or expired, the most overdue first. FOR UPDATE SKIP LOCKED lets
// every worker's syncer drain the queue at once without two of them writing
// one check.
func ClaimWorkflowCheckRuns(ctx context.Context, db orm.Context, now, leaseUntil time.Time, max int) ([]*core.WorkflowCheckRun, error) {
	if max <= 0 {
		return nil, nil
	}
	claimable := db.Bun().NewSelect().
		TableExpr("workflow_check_runs AS c").
		Column("c.id").
		Where("c.finished_at IS NULL").
		Where("c.next_sync_at <= ?", now).
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.Where("c.claimed_until IS NULL").WhereOr("c.claimed_until < ?", now)
		}).
		OrderExpr("c.next_sync_at, c.id").
		Limit(int64(max)).
		For("UPDATE SKIP LOCKED")

	var rows []*WorkflowCheckRun
	err := db.Bun().NewUpdate().
		Model((*WorkflowCheckRun)(nil)).
		Set("claimed_until = ?", leaseUntil).
		Where("id IN (?)", claimable).
		Returning("?TableColumns").
		Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("failed to claim workflow check runs: %w", err)
	}
	out := make([]*core.WorkflowCheckRun, 0, len(rows))
	for _, row := range rows {
		c, err := workflowCheckRunToCore(row)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// observeWorkflowCheckRunSQL reads a check's firing and the root workflow row
// of the chat it launched (the root workflow's id is the chat's).
const observeWorkflowCheckRunSQL = `
SELECT e.outcome, e.outcome_detail, COALESCE(e.chat_id, '') AS chat_id, COALESCE(e.run_status, '') AS run_status,
       w.state, w.stop_reason, COALESCE(w.outcome, '') AS verdict, COALESCE(w.failure_detail, '') AS failure_detail
FROM trigger_events e
LEFT JOIN workflows w ON w.id = e.chat_id
WHERE e.id = ?0::text`

// ObserveWorkflowCheckRun reads a check's firing and its run; a firing that
// is gone is core.ErrWorkflowCheckRunNotFound.
func ObserveWorkflowCheckRun(ctx context.Context, db orm.Context, triggerEventID string) (*core.WorkflowCheckObservation, error) {
	var row struct {
		Outcome       string                   `bun:"outcome"`
		OutcomeDetail string                   `bun:"outcome_detail"`
		ChatID        string                   `bun:"chat_id"`
		RunStatus     string                   `bun:"run_status"`
		State         *core.WorkflowState      `bun:"state"`
		StopReason    *core.WorkflowStopReason `bun:"stop_reason"`
		Verdict       string                   `bun:"verdict"`
		FailureDetail string                   `bun:"failure_detail"`
	}
	err := db.Bun().NewRaw(observeWorkflowCheckRunSQL, triggerEventID).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrWorkflowCheckRunNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to observe workflow check run: %w", err)
	}
	obs := &core.WorkflowCheckObservation{
		EventOutcome: core.TriggerEventOutcome(row.Outcome), EventDetail: row.OutcomeDetail,
		ChatID: row.ChatID, RunStatus: row.RunStatus,
		RootVerdict: row.Verdict, RootFailureDetail: row.FailureDetail,
	}
	if row.State != nil {
		status := core.WorkflowStatus{State: *row.State}
		if row.StopReason != nil {
			status.StopReason = *row.StopReason
		}
		obs.Root = &status
	}
	return obs, nil
}

// SaveWorkflowCheckRunSync writes a sync's result — what GitHub holds, the
// queue fields, paused_since and finished_at — and releases the lease. The
// run's own reports (started_at, ended_status and the output) are never
// written here, so a report that lands during a sync is not lost.
func SaveWorkflowCheckRunSync(ctx context.Context, db orm.Context, row *core.WorkflowCheckRun) error {
	return ignoreNotFound(UpdateWorkflowCheckRunMasked(ctx, db, &WorkflowCheckRun{
		ID:               row.ID,
		GithubCheckRunID: row.GitHubCheckRunID,
		GithubStatus:     row.GitHubStatus,
		GithubConclusion: row.GitHubConclusion,
		GithubDigest:     row.GitHubDigest,
		AnnotationsSent:  int32(row.AnnotationsSent),
		PausedSince:      row.PausedSince,
		NextSyncAt:       row.NextSyncAt,
		ClaimedUntil:     nil,
		Attempts:         int32(row.Attempts),
		LastError:        row.LastError,
		FinishedAt:       row.FinishedAt,
	}, []string{
		WorkflowCheckRunFieldGithubCheckRunID, WorkflowCheckRunFieldGithubStatus, WorkflowCheckRunFieldGithubConclusion,
		WorkflowCheckRunFieldGithubDigest, WorkflowCheckRunFieldAnnotationsSent, WorkflowCheckRunFieldPausedSince,
		WorkflowCheckRunFieldNextSyncAt, WorkflowCheckRunFieldClaimedUntil, WorkflowCheckRunFieldAttempts,
		WorkflowCheckRunFieldLastError, WorkflowCheckRunFieldFinishedAt,
	}, crud.NoReadBack()))
}

func workflowCheckRunToCore(row *WorkflowCheckRun) (*core.WorkflowCheckRun, error) {
	var output map[string]any
	if len(row.Output) > 0 && string(row.Output) != "null" {
		if err := json.Unmarshal(row.Output, &output); err != nil {
			return nil, fmt.Errorf("unmarshal workflow check run output (check %s): %w", row.ID, err)
		}
	}
	return &core.WorkflowCheckRun{
		ID: row.ID, TriggerEventID: row.TriggerEventID, Name: row.Name,
		UserID: row.UserID, OrgID: row.OrgID, ActingIdentity: row.ActingIdentity, Automation: row.Automation,
		Owner: row.Owner, Repo: row.Repo, HeadSHA: row.HeadSha,
		Timeout:   time.Duration(row.TimeoutSeconds) * time.Second,
		StartedAt: row.StartedAt, EndedStatus: row.EndedStatus, Verdict: row.Verdict,
		Output: output, OutputError: row.OutputError,
		GitHubCheckRunID: row.GithubCheckRunID, GitHubStatus: row.GithubStatus, GitHubConclusion: row.GithubConclusion,
		AnnotationsSent: int(row.AnnotationsSent), GitHubDigest: row.GithubDigest,
		PausedSince: row.PausedSince, NextSyncAt: row.NextSyncAt, ClaimedUntil: row.ClaimedUntil,
		Attempts: int(row.Attempts), LastError: row.LastError, FinishedAt: row.FinishedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

// WorkflowCheckRunExtra carries fields on WorkflowCheckRun that are NOT columns — computed
// values, transient state, anything derived rather than stored. WorkflowCheckRun is
// embedded on WorkflowCheckRunExtra (see workflow_check_run_orm.go), so a field added here is a field on
// every WorkflowCheckRun value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type WorkflowCheckRunExtra struct {
}

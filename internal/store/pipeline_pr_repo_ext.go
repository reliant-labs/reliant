package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The PR pipeline's per-PR state (pr_pipeline, pr_head_verdicts,
// pipeline_attempts; pipeline redesign §4.2). The tables are
// forge:no-entity, so their columns are named here once.
//
// pr_pipeline is written only by compare-and-set: every write is one
// UPDATE … WHERE version = ? that bumps the version, so a superseded run
// changes nothing and gets core.ErrPipelineVersionConflict. Which state may
// follow which is the pr.* server actions' closed table, not the store's.

const prPipelineColumns = `record_id, org_id, project_id, repo_id, created_at, updated_at, version,
	state, state_head_sha, priority, priority_source, review_round, fix_attempts, fix_signature,
	owner_run_id, next_action, next_action_at, needs_person_reason, needs_person_since`

// GetPRPipeline returns the PR record's pipeline row, or nil when it has
// none.
func GetPRPipeline(ctx context.Context, db orm.Context, recordID string) (*core.PRPipeline, error) {
	return scanPRPipeline(ctx, db.Bun().NewRaw(`SELECT `+prPipelineColumns+` FROM pr_pipeline WHERE record_id = ?`, recordID))
}

// EnsurePRPipeline creates a new PR's row, or returns the row it already
// has, unchanged: created says which. It never overwrites a row (every later
// write is a compare-and-set), so a redelivered "opened" event is harmless.
// One statement: the ON CONFLICT arm locks the existing row and returns it as
// committed, so two concurrent callers both get the one row.
func EnsurePRPipeline(ctx context.Context, db orm.Context, p core.PRPipelineNew) (row *core.PRPipeline, created bool, err error) {
	if p.State == "" {
		p.State = core.PRPipelineAwaitingReview
	}
	if p.PrioritySource == "" {
		p.PrioritySource = core.PRPriorityDefault
	}
	if p.Priority == 0 && p.PrioritySource == core.PRPriorityDefault {
		p.Priority = 50
	}
	var out struct {
		core.PRPipeline
		Created bool `bun:"created"`
	}
	err = db.Bun().NewRaw(`INSERT INTO pr_pipeline AS p
		(record_id, org_id, project_id, repo_id, state, state_head_sha, priority, priority_source, next_action, next_action_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?)
		ON CONFLICT (record_id) DO UPDATE SET record_id = p.record_id
		RETURNING `+prPipelineColumns+`, (xmax = 0) AS created`,
		p.RecordID, p.OrgID, p.ProjectID, p.RepoID, p.State, p.StateHeadSHA, p.Priority, p.PrioritySource,
		p.NextAction, p.NextActionAt).Scan(ctx, &out)
	if err != nil {
		return nil, false, fmt.Errorf("ensure pr pipeline %s: %w", p.RecordID, err)
	}
	return &out.PRPipeline, out.Created, nil
}

// ResetPRPipelineForHead starts the PR over on a new head, if the row is
// still at version: state awaiting_review about head, review round, fix
// attempts and signature cleared, no owning run, no needs_person, and next
// as what it waits for. Priority is kept.
func ResetPRPipelineForHead(ctx context.Context, db orm.Context, recordID string, version int64, head string, next core.PRPipelineNext) (*core.PRPipeline, error) {
	if head == "" {
		return nil, errors.New("reset pr pipeline: a head is required")
	}
	return casPRPipeline(ctx, db, recordID, version, `state = ?, state_head_sha = ?, review_round = 0, fix_attempts = 0,
		fix_signature = NULL, owner_run_id = NULL, next_action = NULLIF(?, ''), next_action_at = ?,
		needs_person_reason = NULL, needs_person_since = NULL`,
		core.PRPipelineAwaitingReview, head, next.Action, next.At)
}

// TransitionPRPipeline writes to's mutable fields (State through
// NeedsPersonSince) to the PR's row, if it is still at version: the caller
// reads the row, changes what the transition changes, and writes it back.
// An empty string is stored as NULL for the nullable columns.
func TransitionPRPipeline(ctx context.Context, db orm.Context, recordID string, version int64, to core.PRPipeline) (*core.PRPipeline, error) {
	if !slices.Contains(core.AllPRPipelineStates, to.State) {
		return nil, fmt.Errorf("transition pr pipeline: unknown state %q", to.State)
	}
	return casPRPipeline(ctx, db, recordID, version, `state = ?, state_head_sha = ?, priority = ?, priority_source = ?,
		review_round = ?, fix_attempts = ?, fix_signature = NULLIF(?, ''), owner_run_id = NULLIF(?, ''),
		next_action = NULLIF(?, ''), next_action_at = ?, needs_person_reason = NULLIF(?, ''), needs_person_since = ?`,
		to.State, to.StateHeadSHA, to.Priority, to.PrioritySource, to.ReviewRound, to.FixAttempts, to.FixSignature,
		to.OwnerRunID, to.NextAction, to.NextActionAt, to.NeedsPersonReason, to.NeedsPersonSince)
}

func casPRPipeline(ctx context.Context, db orm.Context, recordID string, version int64, set string, args ...any) (*core.PRPipeline, error) {
	args = append(args, recordID, version)
	p, err := scanPRPipeline(ctx, db.Bun().NewRaw(`UPDATE pr_pipeline SET `+set+`,
		version = version + 1, updated_at = now()
		WHERE record_id = ? AND version = ? RETURNING `+prPipelineColumns, args...))
	if err != nil {
		return nil, fmt.Errorf("update pr pipeline %s: %w", recordID, err)
	}
	if p == nil {
		return nil, fmt.Errorf("pr pipeline %s at version %d: %w", recordID, version, core.ErrPipelineVersionConflict)
	}
	return p, nil
}

// ListDuePRPipelines is the due scan: up to limit PRs whose next action is
// due at now, oldest due first, in repositories whose mode is not off. The
// rows are locked FOR UPDATE SKIP LOCKED, so run it inside the transaction
// that acts on them (orm.ReadCommitted: the row locks are what keep two
// workers apart): a concurrent scan skips the rows this one holds instead of
// waiting for them, and the two claim disjoint sets. Outside a transaction
// the locks end with the statement. It reads pr_pipeline_due_idx.
func ListDuePRPipelines(ctx context.Context, db orm.Context, now time.Time, limit int) ([]*core.PRPipeline, error) {
	return ListDuePRPipelinesAfter(ctx, db, now, limit, nil)
}

// ListDuePRPipelinesAfter continues the due scan strictly after the cursor's
// (next_action_at, record_id), retaining the same ordering and row locks.
func ListDuePRPipelinesAfter(ctx context.Context, db orm.Context, now time.Time, limit int, after *core.PRPipeline) ([]*core.PRPipeline, error) {
	if limit <= 0 {
		return nil, nil
	}
	var out []*core.PRPipeline
	cursor := ""
	args := []any{now.UTC(), core.PipelineRepoModeOff}
	if after != nil {
		cursor = " AND (p.next_action_at, p.record_id) > (?, ?)"
		args = append(args, after.NextActionAt, after.RecordID)
	}
	args = append(args, limit)
	if err := db.Bun().NewRaw(`SELECT `+prPipelineColumns+` FROM pr_pipeline AS p
		WHERE p.next_action_at IS NOT NULL AND p.next_action_at <= ?
		  AND EXISTS (SELECT 1 FROM pipeline_repos r WHERE r.id = p.repo_id AND r.mode <> ?)`+cursor+`
		ORDER BY p.next_action_at, p.record_id LIMIT ?
		FOR UPDATE OF p SKIP LOCKED`, args...).Scan(ctx, &out); err != nil {
		return nil, fmt.Errorf("list due pr pipelines: %w", err)
	}
	return out, nil
}

func scanPRPipeline(ctx context.Context, q rawScanner) (*core.PRPipeline, error) {
	var p core.PRPipeline
	if err := q.Scan(ctx, &p); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

const prHeadVerdictColumns = `id, org_id, project_id, record_id, head_sha, round, verdict, rule, risk_tier,
	findings_json, required_decisions, run_id, node_id, reviewer_principal, trigger_id, daemon_id, created_at`

type prHeadVerdictRow struct {
	core.PRHeadVerdictRecord
	RequiredDecisions []string `bun:"required_decisions,array"`
}

func (r prHeadVerdictRow) core() *core.PRHeadVerdictRecord {
	v := r.PRHeadVerdictRecord
	v.RequiredDecisions = make([]core.PipelineDecisionKind, 0, len(r.RequiredDecisions))
	for _, k := range r.RequiredDecisions {
		v.RequiredDecisions = append(v.RequiredDecisions, core.PipelineDecisionKind(k))
	}
	return &v
}

// InsertPRHeadVerdict appends a verdict. A verdict for the same (record,
// head, round) already there is core.ErrPipelineVerdictExists: rows are
// never updated (a trigger refuses it), so a new verdict needs a new round.
// ON CONFLICT DO NOTHING decides that without aborting the caller's
// transaction.
func InsertPRHeadVerdict(ctx context.Context, db orm.Context, v core.PRHeadVerdictRecord) (*core.PRHeadVerdictRecord, error) {
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	findings := string(v.FindingsJSON)
	if findings == "" {
		findings = "[]"
	}
	required := make([]string, 0, len(v.RequiredDecisions))
	for _, k := range v.RequiredDecisions {
		if !slices.Contains(core.AllPipelineDecisionKinds, k) {
			return nil, fmt.Errorf("insert pr head verdict: unknown required decision %q", k)
		}
		required = append(required, string(k))
	}
	var rows []prHeadVerdictRow
	err := db.Bun().NewRaw(`INSERT INTO pr_head_verdicts
		(id, org_id, project_id, record_id, head_sha, round, verdict, rule, risk_tier, findings_json,
		 required_decisions, run_id, node_id, reviewer_principal, trigger_id, daemon_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?::text[], NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''))
		ON CONFLICT ON CONSTRAINT pr_head_verdicts_head_round_key DO NOTHING
		RETURNING `+prHeadVerdictColumns,
		v.ID, v.OrgID, v.ProjectID, v.RecordID, v.HeadSHA, v.Round, v.Verdict, v.Rule, v.RiskTier, findings,
		orm.Arg(required), v.RunID, v.NodeID, v.ReviewerPrincipal, v.TriggerID, v.DaemonID).Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("insert pr head verdict: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("verdict %s@%s round %d: %w", v.RecordID, v.HeadSHA, v.Round, core.ErrPipelineVerdictExists)
	}
	return rows[0].core(), nil
}

// LatestPRHeadVerdict returns the verdict of the highest round on the PR's
// head, or nil when the head has none.
func LatestPRHeadVerdict(ctx context.Context, db orm.Context, recordID, head string) (*core.PRHeadVerdictRecord, error) {
	var rows []prHeadVerdictRow
	if err := db.Bun().NewRaw(`SELECT `+prHeadVerdictColumns+` FROM pr_head_verdicts
		WHERE record_id = ? AND head_sha = ? ORDER BY round DESC LIMIT 1`, recordID, head).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("latest pr head verdict: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0].core(), nil
}

const pipelineAttemptColumns = `id, org_id, project_id, subject_kind, subject_id, head_sha, stage, signature,
	attempt, run_id, outcome, detail_json, started_at, finished_at`

// nextAttemptTries bounds NextPipelineAttempt's retries of a number another
// caller took first.
const nextAttemptTries = 4

// NextPipelineAttempt starts the next attempt at (subject, stage,
// signature): attempt 1 for a new signature, else one past the highest. The
// number is computed and inserted in one statement; when a concurrent caller
// took it first (ON CONFLICT DO NOTHING on the number key, which waits for
// that caller's commit), it computes again, up to nextAttemptTries times, so
// concurrent callers get distinct numbers. Inside a REPEATABLE READ or
// SERIALIZABLE transaction the retry sees the same snapshot and cannot
// advance; it then returns core.ErrPipelineAttemptConflict, as it does when
// the tries run out. Neither aborts the caller's transaction.
func NextPipelineAttempt(ctx context.Context, db orm.Context, a core.PipelineAttempt) (*core.PipelineAttempt, error) {
	if a.SubjectID == "" {
		return nil, errors.New("next pipeline attempt: a subject is required")
	}
	detail := any(nil)
	if len(a.DetailJSON) > 0 {
		detail = string(a.DetailJSON)
	}
	for range nextAttemptTries {
		id := a.ID
		if id == "" {
			id = uuid.NewString()
		}
		var rows []core.PipelineAttempt
		err := db.Bun().NewRaw(`INSERT INTO pipeline_attempts
			(id, org_id, project_id, subject_kind, subject_id, head_sha, stage, signature, attempt, run_id, detail_json)
			SELECT ?0, ?1, ?2, ?3, ?4, NULLIF(?5, ''), ?6, ?7, COALESCE(max(attempt), 0) + 1, NULLIF(?8, ''), ?9::jsonb
			FROM pipeline_attempts WHERE subject_kind = ?3 AND subject_id = ?4 AND stage = ?6 AND signature = ?7
			ON CONFLICT ON CONSTRAINT pipeline_attempts_number_key DO NOTHING
			RETURNING `+pipelineAttemptColumns,
			id, a.OrgID, a.ProjectID, a.SubjectKind, a.SubjectID, a.HeadSHA, a.Stage, a.Signature, a.RunID, detail).Scan(ctx, &rows)
		if err != nil {
			return nil, fmt.Errorf("next pipeline attempt: %w", err)
		}
		if len(rows) > 0 {
			return &rows[0], nil
		}
	}
	return nil, fmt.Errorf("%s %s %s attempt: %w", a.SubjectKind, a.SubjectID, a.Stage, core.ErrPipelineAttemptConflict)
}

// FinishPipelineAttempt records how a running attempt ended. An attempt
// that already finished is left as it is: core.ErrPipelineVersionConflict.
func FinishPipelineAttempt(ctx context.Context, db orm.Context, id string, outcome core.PipelineAttemptOutcome, detail json.RawMessage) (*core.PipelineAttempt, error) {
	if !slices.Contains(core.AllPipelineAttemptOutcomes, outcome) {
		return nil, fmt.Errorf("finish pipeline attempt: unknown outcome %q", outcome)
	}
	d := any(nil)
	if len(detail) > 0 {
		d = string(detail)
	}
	var rows []core.PipelineAttempt
	if err := db.Bun().NewRaw(`UPDATE pipeline_attempts
		SET outcome = ?, finished_at = now(), detail_json = COALESCE(?::jsonb, detail_json)
		WHERE id = ? AND outcome IS NULL RETURNING `+pipelineAttemptColumns, outcome, d, id).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("finish pipeline attempt: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("attempt %s is not running: %w", id, core.ErrPipelineVersionConflict)
	}
	return &rows[0], nil
}

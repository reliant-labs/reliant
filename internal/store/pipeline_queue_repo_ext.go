package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The merge queue (queue_entries, queue_batches, main_health; pipeline
// redesign §3.2, §4.4). Only the queue service writes these tables. They are
// forge:no-entity, so their columns are named here once.
//
// Every state change is one guarded statement (UPDATE … WHERE state = the
// state the caller read), so it needs no transaction and a writer that lost
// a race changes nothing and gets core.ErrPipelineVersionConflict. The one
// transaction, AttachQueueEntries, is READ COMMITTED: the batch's row lock
// is what makes it correct.

const queueEntryColumns = `id, org_id, project_id, repo_id, record_id, head_sha, priority, enqueued_at,
	batch_id, state, eject_reason, landed_sha, landed_at, updated_at`

// queueEntryOrder is the pick order, and the order a batch's entries are
// squashed and landed in.
const queueEntryOrder = `priority DESC, enqueued_at, id`

// enqueueTries bounds EnqueueQueueEntry's retries when the entry it
// conflicted with left the queue before it could be read.
const enqueueTries = 3

// EnqueueQueueEntry puts a PR head in the queue, waiting. It is idempotent
// per PR: a PR already waiting or batched at the same head returns that
// entry with created false; at another head it is
// core.ErrQueueEntryOtherHead (eject the old entry first). The partial
// unique index on the PR's active entry decides it, through ON CONFLICT DO
// NOTHING, so a concurrent enqueue of the same PR never makes a second
// entry and never aborts the caller's transaction.
func EnqueueQueueEntry(ctx context.Context, db orm.Context, e core.QueueEntryNew) (entry *core.QueueEntry, created bool, err error) {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	for range enqueueTries {
		var rows []*core.QueueEntry
		if err := db.Bun().NewRaw(`INSERT INTO queue_entries (id, org_id, project_id, repo_id, record_id, head_sha, priority)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (record_id) WHERE state IN ('waiting', 'batched') DO NOTHING
			RETURNING `+queueEntryColumns,
			e.ID, e.OrgID, e.ProjectID, e.RepoID, e.RecordID, e.HeadSHA, e.Priority).Scan(ctx, &rows); err != nil {
			return nil, false, fmt.Errorf("enqueue %s: %w", e.RecordID, err)
		}
		if len(rows) > 0 {
			return rows[0], true, nil
		}
		if err := db.Bun().NewRaw(`SELECT `+queueEntryColumns+` FROM queue_entries
			WHERE record_id = ? AND state IN ('waiting', 'batched')`, e.RecordID).Scan(ctx, &rows); err != nil {
			return nil, false, fmt.Errorf("enqueue %s: read the active entry: %w", e.RecordID, err)
		}
		if len(rows) == 0 {
			continue // it left the queue in between: insert again
		}
		if rows[0].HeadSHA != e.HeadSHA {
			return nil, false, fmt.Errorf("enqueue %s at %s (queued at %s): %w", e.RecordID, e.HeadSHA, rows[0].HeadSHA, core.ErrQueueEntryOtherHead)
		}
		return rows[0], false, nil
	}
	return nil, false, fmt.Errorf("enqueue %s: the active entry kept changing", e.RecordID)
}

// ListAdmissibleQueueEntries lists the repository's waiting entries that
// pass every admission condition that is DB state alone (§3.1), in pick
// order (priority DESC, enqueued_at): the PR's recorded head is still the
// entry's head, the latest verdict on that head is a pass, and no hold
// decision is in force at now. The conditions that depend on the repo's
// config (required decisions, the check tier) are the scheduler's to apply
// to this list. limit <= 0 lists every one.
func ListAdmissibleQueueEntries(ctx context.Context, db orm.Context, repoID string, now time.Time, limit int) ([]*core.QueueEntry, error) {
	q := `SELECT ` + prefixColumns("e", queueEntryColumns) + ` FROM queue_entries AS e
		JOIN pr_records AS r ON r.id = e.record_id AND r.head_sha = e.head_sha
		WHERE e.repo_id = ?0 AND e.state = ?1
		  AND (SELECT v.verdict FROM pr_head_verdicts v WHERE v.record_id = e.record_id AND v.head_sha = e.head_sha
		       ORDER BY v.round DESC LIMIT 1) = ?2
		  AND NOT EXISTS (SELECT 1 FROM pipeline_decisions d
		       WHERE d.subject_kind = ?3 AND d.subject_id = e.record_id AND d.kind = ?4 AND d.state = ?5
		         AND (d.expires_at IS NULL OR d.expires_at > ?6))
		ORDER BY e.priority DESC, e.enqueued_at, e.id`
	args := []any{repoID, core.QueueEntryWaiting, core.PRHeadVerdictPass, core.PipelineDecisionSubjectPR,
		core.PipelineDecisionHold, core.PipelineDecisionApproved, now.UTC()}
	if limit > 0 {
		q += ` LIMIT ?7`
		args = append(args, limit)
	}
	var out []*core.QueueEntry
	if err := db.Bun().NewRaw(q, args...).Scan(ctx, &out); err != nil {
		return nil, fmt.Errorf("list admissible queue entries: %w", err)
	}
	return out, nil
}

// EjectQueueEntry takes a waiting or batched entry out of the queue for
// reason. An entry no longer in the queue is core.ErrPipelineVersionConflict.
func EjectQueueEntry(ctx context.Context, db orm.Context, id string, reason core.QueueEjectReason) (*core.QueueEntry, error) {
	if !slices.Contains(core.AllQueueEjectReasons, reason) {
		return nil, fmt.Errorf("eject queue entry: unknown reason %q", reason)
	}
	return updateQueueEntry(ctx, db, id, `state = 'ejected', eject_reason = ?`, `state IN ('waiting', 'batched')`, reason)
}

// MarkQueueEntryLanded records a batched entry's squash merge: main's sha
// after it. An entry that is not batched is core.ErrPipelineVersionConflict.
func MarkQueueEntryLanded(ctx context.Context, db orm.Context, id, landedSHA string) (*core.QueueEntry, error) {
	if landedSHA == "" {
		return nil, errors.New("mark queue entry landed: a sha is required")
	}
	return updateQueueEntry(ctx, db, id, `state = 'landed', landed_sha = ?, landed_at = now()`, `state = 'batched'`, landedSHA)
}

func updateQueueEntry(ctx context.Context, db orm.Context, id, set, guard string, args ...any) (*core.QueueEntry, error) {
	args = append(args, id)
	var rows []*core.QueueEntry
	if err := db.Bun().NewRaw(`UPDATE queue_entries SET `+set+`, updated_at = now()
		WHERE id = ? AND `+guard+` RETURNING `+queueEntryColumns, args...).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("update queue entry %s: %w", id, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("queue entry %s: %w", id, core.ErrPipelineVersionConflict)
	}
	return rows[0], nil
}

// RequeueQueueEntries puts the batch's still-batched entries back to
// waiting (a batch abandoned or left unproven), keeping their batch_id as
// the batch they were last in. It returns how many moved.
func RequeueQueueEntries(ctx context.Context, db orm.Context, batchID string) (int64, error) {
	res, err := db.Bun().NewRaw(`UPDATE queue_entries SET state = 'waiting', updated_at = now()
		WHERE batch_id = ? AND state = 'batched'`, batchID).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("requeue entries of batch %s: %w", batchID, err)
	}
	return res.RowsAffected()
}

// ListQueueBatchEntries lists the entries in (or last in) the batch, in the
// order they are squashed onto its base and landed.
func ListQueueBatchEntries(ctx context.Context, db orm.Context, batchID string) ([]*core.QueueEntry, error) {
	var out []*core.QueueEntry
	if err := db.Bun().NewRaw(`SELECT `+queueEntryColumns+` FROM queue_entries
		WHERE batch_id = ? ORDER BY `+queueEntryOrder, batchID).Scan(ctx, &out); err != nil {
		return nil, fmt.Errorf("list batch entries: %w", err)
	}
	return out, nil
}

const queueBatchColumns = `id, org_id, project_id, repo_id, base_sha, candidate_sha, candidate_ref,
	parent_batch_id, kind, expected_trees_json, landed_sha, isolation_backend, gate_machine_id, state,
	gate_runner, gate_run_id, check_names, attempt, verdict_json, stages_json, started_at, finished_at, updated_at`

type queueBatchRow struct {
	core.QueueBatch
	CheckNames []string `bun:"check_names,array"`
}

func scanQueueBatches(ctx context.Context, q rawScanner) ([]*core.QueueBatch, error) {
	var rows []queueBatchRow
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, err
	}
	out := make([]*core.QueueBatch, 0, len(rows))
	for _, r := range rows {
		b := r.QueueBatch
		b.CheckNames = r.CheckNames
		out = append(out, &b)
	}
	return out, nil
}

func scanQueueBatch(ctx context.Context, q rawScanner) (*core.QueueBatch, error) {
	bs, err := scanQueueBatches(ctx, q)
	if err != nil || len(bs) == 0 {
		return nil, err
	}
	return bs[0], nil
}

// CreateQueueBatch starts a batch, building, on base_sha.
func CreateQueueBatch(ctx context.Context, db orm.Context, b core.QueueBatchNew) (*core.QueueBatch, error) {
	if b.ID == "" {
		b.ID = uuid.NewString()
	}
	if b.Kind == "" {
		b.Kind = core.QueueBatchNormal
	}
	out, err := scanQueueBatch(ctx, db.Bun().NewRaw(`INSERT INTO queue_batches
		(id, org_id, project_id, repo_id, base_sha, parent_batch_id, kind)
		VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?) RETURNING `+queueBatchColumns,
		b.ID, b.OrgID, b.ProjectID, b.RepoID, b.BaseSHA, b.ParentBatchID, b.Kind))
	if err != nil {
		return nil, fmt.Errorf("create queue batch: %w", err)
	}
	return out, nil
}

// GetQueueBatch returns the batch with id, or nil when there is none.
func GetQueueBatch(ctx context.Context, db orm.Context, id string) (*core.QueueBatch, error) {
	out, err := scanQueueBatch(ctx, db.Bun().NewRaw(`SELECT `+queueBatchColumns+` FROM queue_batches WHERE id = ?`, id))
	if err != nil {
		return nil, fmt.Errorf("get queue batch: %w", err)
	}
	return out, nil
}

// LatestQueueBatches lists the repository's newest batches, newest first.
func LatestQueueBatches(ctx context.Context, db orm.Context, repoID string, limit int) ([]*core.QueueBatch, error) {
	if limit <= 0 {
		return nil, nil
	}
	out, err := scanQueueBatches(ctx, db.Bun().NewRaw(`SELECT `+queueBatchColumns+` FROM queue_batches
		WHERE repo_id = ? ORDER BY started_at DESC, id DESC LIMIT ?`, repoID, limit))
	if err != nil {
		return nil, fmt.Errorf("latest queue batches: %w", err)
	}
	return out, nil
}

// AttachQueueEntries puts entries in a building batch, batched. An entry may
// join when it is the batch's repository's and is waiting, or is batched in
// the batch's parent (a bisect half or a rerun takes its entries from the
// batch it splits). Every entry joins or none does:
// core.ErrQueueEntryNotAttachable, as for a batch no longer building.
//
// READ COMMITTED, one attempt: the batch's row lock (FOR UPDATE) holds its
// state while the entries move, and the UPDATE re-checks each entry's state
// against the row it locks, so a concurrent eject either lands first (and
// the attach fails) or waits.
func AttachQueueEntries(ctx context.Context, db orm.Context, batchID string, entryIDs []string) error {
	ids := slices.Compact(slices.Sorted(slices.Values(entryIDs)))
	if len(ids) == 0 {
		return nil
	}
	return db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var b struct {
			State         core.QueueBatchState
			RepoID        string
			ParentBatchID string
		}
		err := db.Bun().NewRaw(`SELECT state, repo_id, parent_batch_id FROM queue_batches WHERE id = ? FOR UPDATE`, batchID).Scan(ctx, &b)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("attach to batch %s: no such batch: %w", batchID, core.ErrQueueEntryNotAttachable)
		}
		if err != nil {
			return fmt.Errorf("attach to batch %s: %w", batchID, err)
		}
		if b.State != core.QueueBatchBuilding {
			return fmt.Errorf("attach to batch %s (%s): %w", batchID, b.State, core.ErrQueueEntryNotAttachable)
		}
		res, err := db.Bun().NewRaw(`UPDATE queue_entries SET state = 'batched', batch_id = ?0, updated_at = now()
			WHERE id = ANY(?1::text[]) AND repo_id = ?2
			  AND (state = 'waiting' OR (state = 'batched' AND batch_id = NULLIF(?3, '')))`,
			batchID, orm.Arg(ids), b.RepoID, b.ParentBatchID).Exec(ctx)
		if err != nil {
			return fmt.Errorf("attach to batch %s: %w", batchID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != int64(len(ids)) {
			return fmt.Errorf("attach %d entries to batch %s: %d could: %w", len(ids), batchID, n, core.ErrQueueEntryNotAttachable)
		}
		return nil
	})
}

// SetQueueBatchState moves the batch from state from to state to, recording
// u's non-zero fields beside it, if §3.2 allows the move
// (core.QueueBatchState.CanTransitionTo; otherwise
// core.ErrQueueBatchTransition) and the batch is still in from (otherwise
// core.ErrPipelineVersionConflict). Gating again after an unproven gate
// counts one more attempt. A batch lands through MarkQueueBatchLanded, which
// records the landed sha with the state.
func SetQueueBatchState(ctx context.Context, db orm.Context, id string, from, to core.QueueBatchState, u core.QueueBatchUpdate) (*core.QueueBatch, error) {
	if !from.CanTransitionTo(to) {
		return nil, fmt.Errorf("batch %s %s → %s: %w", id, from, to, core.ErrQueueBatchTransition)
	}
	if to == core.QueueBatchLanded {
		return nil, fmt.Errorf("batch %s: land it with MarkQueueBatchLanded: %w", id, core.ErrQueueBatchTransition)
	}
	set := []string{"state = ?"}
	args := []any{to}
	str := func(col, v string) {
		if v != "" {
			set = append(set, col+" = ?")
			args = append(args, v)
		}
	}
	doc := func(col string, v []byte) {
		if len(v) > 0 {
			set = append(set, col+" = ?::jsonb")
			args = append(args, string(v))
		}
	}
	str("candidate_sha", u.CandidateSHA)
	str("candidate_ref", u.CandidateRef)
	str("isolation_backend", string(u.IsolationBackend))
	str("gate_machine_id", u.GateMachineID)
	str("gate_runner", string(u.GateRunner))
	str("gate_run_id", u.GateRunID)
	doc("expected_trees_json", u.ExpectedTreesJSON)
	doc("verdict_json", u.VerdictJSON)
	doc("stages_json", u.StagesJSON)
	if u.CheckNames != nil {
		set = append(set, "check_names = ?::text[]")
		args = append(args, orm.Arg(u.CheckNames))
	}
	switch to {
	case core.QueueBatchGating:
		set = append(set, "finished_at = NULL")
		if from == core.QueueBatchUnproven {
			set = append(set, "attempt = attempt + 1")
		}
	default:
		set = append(set, "finished_at = now()")
	}
	args = append(args, id, from)
	out, err := scanQueueBatch(ctx, db.Bun().NewRaw(`UPDATE queue_batches SET `+strings.Join(set, ", ")+`, updated_at = now()
		WHERE id = ? AND state = ? RETURNING `+queueBatchColumns, args...))
	if err != nil {
		return nil, fmt.Errorf("set batch %s state: %w", id, err)
	}
	if out == nil {
		return nil, fmt.Errorf("batch %s is not %s: %w", id, from, core.ErrPipelineVersionConflict)
	}
	return out, nil
}

// MarkQueueBatchLanded records a passed batch as landed, with main's sha
// after its last squash merge: the one sha of the batch releases and
// main_health use. A batch that is not passed is
// core.ErrPipelineVersionConflict.
func MarkQueueBatchLanded(ctx context.Context, db orm.Context, id, landedSHA string) (*core.QueueBatch, error) {
	if landedSHA == "" {
		return nil, errors.New("mark queue batch landed: a sha is required")
	}
	// One statement, so the check and the move are atomic: a batch lands only
	// once it holds at least one entry and every entry it holds has landed
	// (review B1 on #3805). Otherwise its remaining entries would sit in a
	// landed batch, out of every later pick.
	out, err := scanQueueBatch(ctx, db.Bun().NewRaw(`UPDATE queue_batches b SET state = ?, landed_sha = ?, updated_at = now()
		WHERE b.id = ? AND b.state = ?
		  AND EXISTS (SELECT 1 FROM queue_entries e WHERE e.batch_id = b.id)
		  AND NOT EXISTS (SELECT 1 FROM queue_entries e WHERE e.batch_id = b.id AND e.state <> ?)
		RETURNING `+queueBatchColumns, core.QueueBatchLanded, landedSHA, id, core.QueueBatchPassed, core.QueueEntryLanded))
	if err != nil {
		return nil, fmt.Errorf("mark batch %s landed: %w", id, err)
	}
	if out == nil {
		return nil, fmt.Errorf("batch %s is not passed, holds no entry, or holds an entry that has not landed: %w", id, core.ErrPipelineVersionConflict)
	}
	return out, nil
}

// PatchQueueBatchVerdict merges patch, a JSON object, into the batch's
// verdict_json at the top level (jsonb ||: patch's keys replace the stored
// ones of the same name, every other key is kept), in whatever state the
// batch is. It is how the lander and the display record what they did
// (`unenforced`, `landing`, `integrity`, `display`) beside the scheduler's
// verdict without rewriting it. A batch that does not exist is
// core.ErrPipelineVersionConflict.
func PatchQueueBatchVerdict(ctx context.Context, db orm.Context, id string, patch json.RawMessage) (*core.QueueBatch, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(patch, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("patch batch %s verdict: the patch must be a JSON object", id)
	}
	out, err := scanQueueBatch(ctx, db.Bun().NewRaw(`UPDATE queue_batches
		SET verdict_json = COALESCE(verdict_json, '{}'::jsonb) || ?::jsonb, updated_at = now()
		WHERE id = ? RETURNING `+queueBatchColumns, string(patch), id))
	if err != nil {
		return nil, fmt.Errorf("patch batch %s verdict: %w", id, err)
	}
	if out == nil {
		return nil, fmt.Errorf("batch %s: %w", id, core.ErrPipelineVersionConflict)
	}
	return out, nil
}

const mainHealthColumns = `id, org_id, project_id, repo_id, sha, source, verdict, signature, batch_id, run_id, observed_at`

// InsertMainHealth records a verdict on a sha of main. Rows are only
// inserted.
func InsertMainHealth(ctx context.Context, db orm.Context, h core.MainHealthRecord) (*core.MainHealthRecord, error) {
	if h.ID == "" {
		h.ID = uuid.NewString()
	}
	var observed any // nil: the column's DEFAULT now()
	if !h.ObservedAt.IsZero() {
		observed = h.ObservedAt.UTC()
	}
	var rows []*core.MainHealthRecord
	if err := db.Bun().NewRaw(`INSERT INTO main_health
		(id, org_id, project_id, repo_id, sha, source, verdict, signature, batch_id, run_id, observed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), COALESCE(?::timestamptz, now()))
		RETURNING `+mainHealthColumns,
		h.ID, h.OrgID, h.ProjectID, h.RepoID, h.SHA, h.Source, h.Verdict, h.Signature, h.BatchID, h.RunID, observed).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("insert main health: %w", err)
	}
	return rows[0], nil
}

// LatestMainHealth returns the repository's newest verdict on main, or nil
// when it has none. It reads main_health_observed_idx.
func LatestMainHealth(ctx context.Context, db orm.Context, repoID string) (*core.MainHealthRecord, error) {
	var rows []*core.MainHealthRecord
	if err := db.Bun().NewRaw(`SELECT `+mainHealthColumns+` FROM main_health
		WHERE repo_id = ? ORDER BY observed_at DESC, id DESC LIMIT 1`, repoID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("latest main health: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

// prefixColumns qualifies each of a comma-separated column list with alias.
func prefixColumns(alias, columns string) string {
	cols := strings.Split(columns, ",")
	for i, c := range cols {
		cols[i] = alias + "." + strings.TrimSpace(c)
	}
	return strings.Join(cols, ", ")
}

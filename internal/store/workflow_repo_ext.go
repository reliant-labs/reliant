// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// The workflows queries: the forge-ORM port of internal/db's workflow store
// (P5b lane L2, internal/db/SCHEMA.md "Porting onto the forge ORM"). Every
// function takes (ctx, orm.Context) like the generated delegates, joins the
// transaction ctx carries, and speaks core types: rows are mapped at this
// boundary and no Workflow row leaves the package.
//
// By-key reads are the generated delegates. Lists are the bun builder rather
// than ListWorkflow: the generated List caps a call with no limit at 10 000
// rows, and these reads were unbounded. The recursive cascades are NewRaw, so
// a reviewer reads the SQL they run.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// InsertWorkflowIfAbsent writes w, doing nothing when its id already exists.
// Inline and loop child workflows reuse their parent's id, so the conflict is
// the expected idempotent path. DO NOTHING raises no error, so it never
// aborts the surrounding transaction the way a duplicate-key violation
// (SQLSTATE 25P02) would.
//
// It writes the columns a new run has: the outcome, the worker stamps and
// the failed-start stamp start NULL whatever w carries.
func InsertWorkflowIfAbsent(ctx context.Context, db orm.Context, w *core.Workflow) error {
	state := w.Status.State
	row := &Workflow{
		ID:              w.ID,
		ParentID:        w.ParentID,
		ChatID:          w.ChatID,
		WorkflowName:    w.WorkflowName,
		Thread:          w.Thread,
		State:           &state,
		StopReason:      w.Status.StopReason,
		SpawnedByNodeID: w.SpawnedByNodeID,
		LoopIteration:   w.LoopIteration,
		CreatedAt:       w.CreatedAt,
		CompletedAt:     w.CompletedAt,
		OwnerUserID:     w.OwnerUserID,
	}
	_, err := db.Bun().NewInsert().Model(row).
		ExcludeColumn(WorkflowFieldOutcome, WorkflowFieldWorkerStartedAt, WorkflowFieldWorkerStoppedAt, WorkflowFieldStartFailedAt, WorkflowFieldFailureDetail).
		On("CONFLICT (id) DO NOTHING").
		Returning("NULL").
		Exec(ctx)
	return err
}

// LoadWorkflow reads one workflow by id.
func LoadWorkflow(ctx context.Context, db orm.Context, id string) (*core.Workflow, error) {
	row, err := GetWorkflowByID(ctx, db, id)
	if err != nil {
		if svcerr.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s", svcerr.NotFound("workflow"), id)
		}
		return nil, err
	}
	return workflowToCore(row), nil
}

// FindWorkflowByThread is the workflow of a chat's thread, or nil when there
// is none.
func FindWorkflowByThread(ctx context.Context, db orm.Context, chatID, thread string) (*core.Workflow, error) {
	row := new(Workflow)
	err := db.Bun().NewSelect().Model(row).
		Where("chat_id = ?", chatID).
		Where("thread = ?", thread).
		Limit(1).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return workflowToCore(row), nil
}

// ListChatWorkflows is every workflow of a chat, oldest first.
func ListChatWorkflows(ctx context.Context, db orm.Context, chatID string) ([]*core.Workflow, error) {
	return selectWorkflows(ctx, db, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("chat_id = ?", chatID).Order("created_at ASC")
	})
}

// ListChildWorkflows is every direct child of a workflow, oldest first.
func ListChildWorkflows(ctx context.Context, db orm.Context, parentID string) ([]*core.Workflow, error) {
	return selectWorkflows(ctx, db, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("parent_id = ?", parentID).Order("created_at ASC")
	})
}

// ListRootWorkflows is a chat's root workflows, newest first.
func ListRootWorkflows(ctx context.Context, db orm.Context, chatID string) ([]*core.Workflow, error) {
	return selectWorkflows(ctx, db, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("chat_id = ?", chatID).Where("parent_id IS NULL").Order("created_at DESC")
	})
}

// ListWorkflowsByStatus is every workflow at one lifecycle, oldest first:
// startup recovery restarts workers for the active ones.
func ListWorkflowsByStatus(ctx context.Context, db orm.Context, status core.WorkflowStatus) ([]*core.Workflow, error) {
	return selectWorkflows(ctx, db, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("state = ?", status.State).Where("stop_reason = ?", status.StopReason).Order("created_at ASC")
	})
}

func selectWorkflows(ctx context.Context, db orm.Context, apply func(*bun.SelectQuery) *bun.SelectQuery) ([]*core.Workflow, error) {
	var rows []*Workflow
	if err := apply(db.Bun().NewSelect().Model(&rows)).Scan(ctx); err != nil {
		return nil, err
	}
	out := make([]*core.Workflow, len(rows))
	for i, row := range rows {
		out[i] = workflowToCore(row)
	}
	return out, nil
}

// rootWorkflowStatusSQL is a chat's effective lifecycle: ACTIVE if ANY real
// workflow (root or child) is active, STOPPED/PAUSED if any is paused and
// none active, otherwise the newest root's own lifecycle. Every workflows row
// is a real execution: thread lifecycle lives on threads.status.
const rootWorkflowStatusSQL = `
SELECT
    w.chat_id,
    CASE
        WHEN EXISTS (
            SELECT 1 FROM workflows w3
            WHERE w3.chat_id = w.chat_id
              AND w3.state = 2
        ) THEN 2
        WHEN EXISTS (
            SELECT 1 FROM workflows w4
            WHERE w4.chat_id = w.chat_id
              AND w4.state = 3 AND w4.stop_reason = 3
        ) THEN 3
        ELSE w.state
    END AS state,
    CASE
        WHEN EXISTS (
            SELECT 1 FROM workflows w3
            WHERE w3.chat_id = w.chat_id
              AND w3.state = 2
        ) THEN 0
        WHEN EXISTS (
            SELECT 1 FROM workflows w4
            WHERE w4.chat_id = w.chat_id
              AND w4.state = 3 AND w4.stop_reason = 3
        ) THEN 3
        ELSE w.stop_reason
    END AS stop_reason
FROM workflows w
WHERE w.parent_id IS NULL
  AND w.chat_id = ?0
ORDER BY w.created_at DESC
LIMIT 1`

// RootWorkflowStatusForChats is the effective lifecycle of each chat that
// has a root workflow; a chat with none is absent from the map.
func RootWorkflowStatusForChats(ctx context.Context, db orm.Context, chatIDs []string) (map[string]core.WorkflowStatus, error) {
	result := make(map[string]core.WorkflowStatus, len(chatIDs))
	for _, chatID := range chatIDs {
		var row struct {
			ChatID     string                  `bun:"chat_id"`
			State      core.WorkflowState      `bun:"state"`
			StopReason core.WorkflowStopReason `bun:"stop_reason"`
		}
		err := db.Bun().NewRaw(rootWorkflowStatusSQL, chatID).Scan(ctx, &row)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result[row.ChatID] = core.WorkflowStatus{State: row.State, StopReason: row.StopReason}
	}
	return result, nil
}

// setWorkflowStatus writes a lifecycle with its completed_at rule:
// stopped for good (any reason but paused) stamps it, active or pending
// clears it, and stopped-but-paused leaves it alone — the run has not ended.
func setWorkflowStatus(q *bun.UpdateQuery, status core.WorkflowStatus) *bun.UpdateQuery {
	return q.
		Set("state = ?", status.State).
		Set("stop_reason = ?", status.StopReason).
		Set(`completed_at = CASE
        WHEN ?0 = 3 AND ?1 <> 3 THEN NOW()
        WHEN ?0 IN (1, 2) THEN NULL
        ELSE completed_at
    END`, status.State, status.StopReason).
		// A row that is live again belongs to a new run of the chat (its
		// root workflow id is reused), which has not failed: the last run's
		// reason must not read as this one's.
		Set(`failure_detail = CASE WHEN ?0 IN (1, 2) THEN NULL ELSE failure_detail END`, status.State)
}

// UpdateWorkflowStatus moves a workflow to status. A missing workflow is
// svcerr.NotFound (and sql.ErrNoRows).
func UpdateWorkflowStatus(ctx context.Context, db orm.Context, id string, status core.WorkflowStatus) error {
	res, err := setWorkflowStatus(db.Bun().NewUpdate().Model((*Workflow)(nil)), status).
		Where("id = ?", id).
		Exec(ctx)
	return requireOneRow(res, err, "workflow")
}

// CompareAndSwapWorkflowStatus moves a workflow to newStatus only while it is
// at expected, reporting whether it did. The comparison is over the PAIR: two
// rows that are each STOPPED for different reasons are different statuses,
// and a swap expecting one must not win against the other.
func CompareAndSwapWorkflowStatus(ctx context.Context, db orm.Context, id string, newStatus, expected core.WorkflowStatus) (bool, error) {
	res, err := setWorkflowStatus(db.Bun().NewUpdate().Model((*Workflow)(nil)), newStatus).
		Where("id = ?", id).
		Where("state = ?", expected.State).
		Where("stop_reason = ?", expected.StopReason).
		Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// MarkWorkflowStartFailed stamps start_failed_at on a PENDING workflow whose
// start attempt failed, reporting whether it did. A workflow that already
// left PENDING (the run got there first) or already carries a stamp is left
// alone: the stamp is the time the start was first found failed.
func MarkWorkflowStartFailed(ctx context.Context, db orm.Context, id string) (bool, error) {
	res, err := db.Bun().NewUpdate().Model((*Workflow)(nil)).
		Set("start_failed_at = NOW()").
		Where("id = ?", id).
		Where("state = ?", core.WorkflowStatePending).
		Where("start_failed_at IS NULL").
		Exec(ctx)
	return rowsChanged(res, err)
}

// ClaimWorkflowStart clears a PENDING workflow's failed-start stamp before a
// retry of its start, reporting whether the workflow is still PENDING. False
// means the start is no longer this caller's to make: the run started, or a
// later trigger fire abandoned it (AbandonFailedWorkflowStart). One UPDATE,
// so it serializes with that abandon on the row lock: whichever commits
// first wins, and the other matches nothing.
//
// A root that holds a workflow-capacity slot is first stamped as a new start
// attempt, under its trigger event's row lock, BEFORE the root is claimed.
// That stamp is what keeps capacity recovery from releasing the slot while
// this start is in flight (ReleaseUnboundTriggerCapacity requires it to be
// older than core.StartAttemptBound). If recovery released the slot first,
// the start is no longer the caller's to make, and false is returned without
// claiming the root.
func ClaimWorkflowStart(ctx context.Context, db orm.Context, id string) (bool, error) {
	var released []bool
	if err := db.Bun().NewRaw(`UPDATE trigger_events SET capacity_start_attempt_at = clock_timestamp()
 WHERE capacity_workflow_id = ? AND outcome = 'launched' RETURNING capacity_released`, id).Scan(ctx, &released); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("stamp workflow capacity start attempt: %w", err)
	}
	for _, r := range released {
		if r {
			return false, nil
		}
	}
	res, err := db.Bun().NewUpdate().Model((*Workflow)(nil)).
		Set("start_failed_at = NULL").
		Where("id = ?", id).
		Where("state = ?", core.WorkflowStatePending).
		Exec(ctx)
	return rowsChanged(res, err)
}

// AbandonFailedWorkflowStart moves a PENDING workflow whose start failed to
// STOPPED/FAILED, reporting whether it did. False means it is not stranded:
// it left PENDING, or a retry of its start has claimed it (ClaimWorkflowStart
// cleared the stamp), so its run is starting.
func AbandonFailedWorkflowStart(ctx context.Context, db orm.Context, id string) (bool, error) {
	res, err := setWorkflowStatus(db.Bun().NewUpdate().Model((*Workflow)(nil)), core.Failed()).
		Where("id = ?", id).
		Where("state = ?", core.WorkflowStatePending).
		Where("start_failed_at IS NOT NULL").
		Exec(ctx)
	return rowsChanged(res, err)
}

// strandedStartsSQL is every PENDING root workflow whose launch committed (its
// chat has a launched trigger event) before cutoff and that no failed start
// has been recorded for. The launch time is the later of the root's creation
// and its event's: a branch's root is created when it is branched, long
// before its first send records the event and starts it.
var strandedStartsSQL = `
SELECT ` + columnsOf[Workflow]("w") + `
FROM workflows w
WHERE w.parent_id IS NULL
  AND w.state = ?0
  AND w.start_failed_at IS NULL
  AND w.created_at < ?1
  AND EXISTS (
      SELECT 1 FROM trigger_events e
      WHERE e.chat_id = w.chat_id
        AND e.outcome = ?2
        AND e.created_at < ?1
  )
ORDER BY w.created_at ASC
LIMIT ?3`

// ListStrandedWorkflowStarts is the PENDING roots whose launch committed
// before cutoff and whose start was never recorded as failed: either their
// start is still in flight (a slow one, or one the bound is shorter than) or
// whoever was starting them died between the commit and the Temporal start.
// The reconciler tells the two apart by asking Temporal.
func ListStrandedWorkflowStarts(ctx context.Context, db orm.Context, cutoff time.Time, limit int) ([]*core.Workflow, error) {
	var rows []*Workflow
	if err := db.Bun().NewRaw(strandedStartsSQL,
		core.WorkflowStatePending, cutoff, core.TriggerEventLaunched, limit,
	).Scan(ctx, &rows); err != nil {
		return nil, err
	}
	out := make([]*core.Workflow, len(rows))
	for i, row := range rows {
		out[i] = workflowToCore(row)
	}
	return out, nil
}

// rowsChanged reports whether an UPDATE matched a row.
func rowsChanged(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetWorkflowOutcome records the run's verdict, the Node.outcome of the
// terminal node it reached; "" clears it. Written once at completion and
// never reconciled from Temporal, unlike status: a graph that routes to its
// `failed` node is a COMPLETED Temporal execution, so the verdict has nowhere
// else to live. A missing workflow is svcerr.NotFound (and sql.ErrNoRows).
func SetWorkflowOutcome(ctx context.Context, db orm.Context, id, outcome string) error {
	var stored *string
	if outcome != "" {
		stored = &outcome
	}
	res, err := db.Bun().NewUpdate().Model((*Workflow)(nil)).
		Set("outcome = ?", stored).
		Where("id = ?", id).
		Exec(ctx)
	return requireOneRow(res, err, "workflow")
}

// SetWorkflowFailureDetail records why the run failed, on the run (see
// core.Workflow.FailureDetail). It is written in the transaction that writes
// the FAILED status; the next status that makes the row live again clears it
// (setWorkflowStatus). A missing workflow is svcerr.NotFound.
func SetWorkflowFailureDetail(ctx context.Context, db orm.Context, id, detail string) error {
	var stored *string
	if detail != "" {
		stored = &detail
	}
	res, err := db.Bun().NewUpdate().Model((*Workflow)(nil)).
		Set("failure_detail = ?", stored).
		Where("id = ?", id).
		Exec(ctx)
	return requireOneRow(res, err, "workflow")
}

// RenamePendingWorkflow renames a workflow that has not started yet.
func RenamePendingWorkflow(ctx context.Context, db orm.Context, id, workflowName string) error {
	res, err := db.Bun().NewUpdate().Model((*Workflow)(nil)).
		Set("workflow_name = ?", workflowName).
		Where("id = ?", id).
		Where("state = ?", core.WorkflowStatePending).
		Exec(ctx)
	if err := requireOneRow(res, err, "workflow"); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return svcerr.FailedPrecondition("cannot update workflow name: workflow not found or status is not pending")
		}
		return err
	}
	return nil
}

// cascadeTerminalStatusSQL moves every LIVE descendant (active, or paused
// 3/3) of a workflow to STOPPED with the caller's stop reason. The reason is
// the caller's, not a constant: a descendant of a cancelled run was
// cancelled, and a fixed "completed" laundered every terminated subtree into
// a finished one. Recursive, because a spawn's own spawns must end too; a
// one-level cascade left grandchildren live forever, keeping the chat
// "active" in chats_with_activity.
//
// P5b L1b: the thread half of this cascade, CascadeTerminalStatusToThreadSubtree,
// is a threads query (queries/threads.sql) and ports with the threads lane.
const cascadeTerminalStatusSQL = `
WITH RECURSIVE descendants AS (
    SELECT c.id FROM workflows c WHERE c.parent_id = ?0
    UNION ALL
    SELECT g.id FROM workflows g
    JOIN descendants d ON g.parent_id = d.id
)
UPDATE workflows AS t
SET state = 3, stop_reason = ?1, completed_at = NOW()
FROM descendants
WHERE t.id = descendants.id
  AND (t.state = 2 OR (t.state = 3 AND t.stop_reason = 3))`

// CascadeTerminalStatusToDescendants drains a finished run's subtree. Only
// the stop reason is passed: every descendant it touches becomes STOPPED by
// definition, and the reason is the part that must not be laundered.
func CascadeTerminalStatusToDescendants(ctx context.Context, db orm.Context, parentID string, reason core.WorkflowStopReason) error {
	_, err := db.Bun().NewRaw(cascadeTerminalStatusSQL, parentID, reason).Exec(ctx)
	return err
}

// reapOrphanedDescendantsSQL enforces the invariant the cascade asserts from
// the other direction: a workflow whose parent is terminal is not running.
// The cascade only runs when the write path that ended the parent remembers
// to call it, and every TerminateWorkflow path skips it (a hard kill never
// runs the completion handler), stranding the subtree at running/paused:
// the reconciler skips workflows with a parent_id. Anchored on ANY terminal
// parent, so a terminal mid-tree spawn drains its own subtree, and each row
// inherits its terminal ANCESTOR's stop reason, as the direct cascade writes.
// A paused parent (3/3) has not ended, so its children are left alone.
const reapOrphanedDescendantsSQL = `
WITH RECURSIVE descendants AS (
    SELECT c.id, p.stop_reason AS terminal_reason FROM workflows c
    JOIN workflows p ON c.parent_id = p.id
    WHERE p.state = 3 AND p.stop_reason <> 3
    UNION ALL
    SELECT g.id, d.terminal_reason FROM workflows g
    JOIN descendants d ON g.parent_id = d.id
)
UPDATE workflows AS t
SET state = 3, stop_reason = descendants.terminal_reason, completed_at = NOW()
FROM descendants
WHERE t.id = descendants.id
  AND (t.state = 2 OR (t.state = 3 AND t.stop_reason = 3))
RETURNING t.id AS workflow_id, t.chat_id, t.stop_reason`

// ReapOrphanedWorkflowDescendants ends every live workflow under a terminal
// ancestor and returns the rows it moved: whether a reap is a bug depends on
// how the run ended, so the reconciler needs the ids to ask Temporal.
func ReapOrphanedWorkflowDescendants(ctx context.Context, db orm.Context) ([]core.ReapedWorkflow, error) {
	var rows []struct {
		WorkflowID string                  `bun:"workflow_id"`
		ChatID     string                  `bun:"chat_id"`
		StopReason core.WorkflowStopReason `bun:"stop_reason"`
	}
	if err := db.Bun().NewRaw(reapOrphanedDescendantsSQL).Scan(ctx, &rows); err != nil {
		return nil, err
	}
	reaped := make([]core.ReapedWorkflow, len(rows))
	for i, row := range rows {
		reaped[i] = core.ReapedWorkflow{WorkflowID: row.WorkflowID, ChatID: row.ChatID, StopReason: row.StopReason}
	}
	return reaped, nil
}

// reviveSubtreeLiveAtSQL moves every DESCENDANT workflow that was live at a
// reset point back to active, AND the threads those rows own, AND the root's
// own thread, in one statement, counting each. The inverse of the two
// terminal cascades for a subtree a reset-and-replay is bringing back.
//
// Every sub-agent runs INLINE in the root's Temporal execution but owns its
// own workflows and threads rows, so killing the root stamped them failed,
// and replay rebuilds them without re-running the "started" activity (it is
// in the replayed history). Measured: six live agents shown failed, chat
// abe58f03, dev-docs/incidents/2026-09-29-reconciler-false-wedge.md.
//
// The predicate is a time window, the only thing that separates work the new
// run will redo from work it will merely replay: the row existed at the reset
// point (created_at <= T), and had not finished by it (completed_at NULL or
// > T). Only rows STOPPED for a reason other than paused move: paused rows
// are ResumeWorkflowsByChat's, and active/pending rows are already right.
// Recursive over parent_id, like the cascade.
//
// The thread half is a data-modifying CTE over the workflow half's RETURNING:
// it cannot re-derive the set afterwards (revived rows no longer match the
// terminal predicate), and one statement is one atomic write, so no
// reconciler pass sees revived workflows with failed threads. It is guarded
// to terminal thread statuses (3,4,5), as ReviveThread is.
//
// P5b L1b: this one statement writes threads too. It is ported here whole
// rather than split across lanes, because splitting it would break the
// atomicity above.
const reviveSubtreeLiveAtSQL = `
WITH RECURSIVE descendants AS (
    SELECT c.id FROM workflows c WHERE c.parent_id = ?0::text
    UNION ALL
    SELECT g.id FROM workflows g
    JOIN descendants d ON g.parent_id = d.id
),
revived_workflows AS (
    UPDATE workflows AS t
    SET state = 2, stop_reason = 0, completed_at = NULL
    FROM descendants
    WHERE t.id = descendants.id
      AND t.state = 3 AND t.stop_reason <> 3
      AND t.created_at <= ?1::timestamptz
      AND (t.completed_at IS NULL OR t.completed_at > ?1::timestamptz)
    RETURNING t.id
),
revived_threads AS (
    UPDATE threads AS th
    SET status = 2, completed_at = NULL
    WHERE th.status IN (3, 4, 5)
      AND (th.workflow_id IN (SELECT id FROM revived_workflows)
           OR th.id = ?0::text)
    RETURNING th.id
)
SELECT
    (SELECT count(*) FROM revived_workflows) AS workflows_revived,
    (SELECT count(*) FROM revived_threads) AS threads_revived`

// ReviveSubtreeLiveAt brings back the descendants a reset-and-replay is about
// to re-execute, and their threads, reporting how many of each it moved.
func ReviveSubtreeLiveAt(ctx context.Context, db orm.Context, rootWorkflowID string, at time.Time) (workflowsRevived, threadsRevived int64, err error) {
	var row struct {
		WorkflowsRevived int64 `bun:"workflows_revived"`
		ThreadsRevived   int64 `bun:"threads_revived"`
	}
	if err := db.Bun().NewRaw(reviveSubtreeLiveAtSQL, rootWorkflowID, at).Scan(ctx, &row); err != nil {
		return 0, 0, err
	}
	return row.WorkflowsRevived, row.ThreadsRevived, nil
}

// PauseRunningWorkflowsByChat pauses every active workflow of a chat, child
// workflows (agent threads) included, so chats_with_activity reports the
// chat as paused.
func PauseRunningWorkflowsByChat(ctx context.Context, db orm.Context, chatID string) error {
	_, err := db.Bun().NewUpdate().Model((*Workflow)(nil)).
		Set("state = ?", core.WorkflowStateStopped).
		Set("stop_reason = ?", core.StopReasonPaused).
		Where("chat_id = ?", chatID).
		Where("state = ?", core.WorkflowStateActive).
		Exec(ctx)
	return err
}

// ResumeWorkflowsByChat resumes every paused workflow of a chat.
func ResumeWorkflowsByChat(ctx context.Context, db orm.Context, chatID string) error {
	_, err := db.Bun().NewUpdate().Model((*Workflow)(nil)).
		Set("state = ?", core.WorkflowStateActive).
		Set("stop_reason = ?", core.StopReasonUnspecified).
		Where("chat_id = ?", chatID).
		Where("state = ?", core.WorkflowStateStopped).
		Where("stop_reason = ?", core.StopReasonPaused).
		Exec(ctx)
	return err
}

func workflowToCore(row *Workflow) *core.Workflow {
	w := &core.Workflow{
		ID:              row.ID,
		ParentID:        row.ParentID,
		ChatID:          row.ChatID,
		WorkflowName:    row.WorkflowName,
		Thread:          row.Thread,
		Status:          core.WorkflowStatus{StopReason: row.StopReason},
		SpawnedByNodeID: row.SpawnedByNodeID,
		LoopIteration:   row.LoopIteration,
		CreatedAt:       row.CreatedAt,
		CompletedAt:     row.CompletedAt,
		WorkerStartedAt: row.WorkerStartedAt,
		WorkerStoppedAt: row.WorkerStoppedAt,
		StartFailedAt:   row.StartFailedAt,
		Outcome:         row.Outcome,
		FailureDetail:   row.FailureDetail,
		OwnerUserID:     row.OwnerUserID,
	}
	if row.State != nil {
		w.Status.State = *row.State
	}
	return w
}

// requireOneRow turns an UPDATE that matched no row into missing(entity):
// svcerr.NotFound, and errors.Is(err, sql.ErrNoRows), the answer the :one
// queries these replace gave.
func requireOneRow(res sql.Result, err error, entity string) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return missing(entity)
	}
	return nil
}

// ignoreNotFound keeps a by-key write of a missing row the no-op its :exec
// predecessor was. Every generated miss is errors.Is(sql.ErrNoRows).
func ignoreNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

// WorkflowExtra carries fields on Workflow that are NOT columns — computed
// values, transient state, anything derived rather than stored. Workflow is
// embedded on WorkflowExtra (see workflow_orm.go), so a field added here is a field on
// every Workflow value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type WorkflowExtra struct {
}

// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// The step_executions queries: the forge-ORM port of internal/db's step store
// (P5b lane L2). Functions take (ctx, orm.Context), join the transaction ctx
// carries, and speak core types. Lists are the bun builder (the generated
// List caps an unlimited call at 10 000 rows; a busy workflow has more steps
// than that). The chat-wide reads are NewRaw: each is one hand-tuned statement
// whose shape the indexes were built for.
package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// InsertStepExecution records one step attempt. saved_message_id is a
// generated column, so whatever exec carries for it is not written.
func InsertStepExecution(ctx context.Context, db orm.Context, exec *core.StepExecution) error {
	return CreateStepExecution(ctx, db, &StepExecution{
		ID:            exec.ID,
		WorkflowID:    exec.WorkflowID,
		StepID:        exec.StepID,
		ActivityName:  exec.ActivityName,
		OutputJSON:    jsonbStorable(exec.OutputJSON),
		ExitCode:      exec.ExitCode,
		Success:       successFlag(exec.Success),
		DurationMs:    exec.DurationMs,
		CreatedAt:     exec.CreatedAt,
		LoopNodeID:    exec.LoopNodeID,
		LoopIteration: exec.LoopIteration,
		InputJSON:     jsonbStorable(exec.InputJSON),
		ErrorMessage:  validUTF8Ptr(exec.ErrorMessage),
		Attempt:       exec.Attempt,
		NodePath:      exec.NodePath,
	})
}

// jsonbStorable makes JSON text castable to jsonb. encoding/json writes a NUL
// in a string as the escape \u0000, which a text column stores but jsonb
// refuses ("unsupported Unicode escape sequence", SQLSTATE 22P05). The
// step_executions row casts output_json to jsonb in its generated
// saved_message_id column, so in prod one NUL in a SaveMessage step's tool
// output failed the whole INSERT and the step's history was lost; the run
// diagnostics read casts it too. Each escaped NUL becomes \ufffd (U+FFFD, as
// validUTF8 does for a raw NUL); an escaped backslash followed by "u0000" is
// text, not a NUL, and is kept. Anything else returns unchanged.
func jsonbStorable(text *string) *string {
	if text == nil || !strings.Contains(*text, `\u0000`) {
		return text
	}
	in := *text
	var b strings.Builder
	b.Grow(len(in))
	for i := 0; i < len(in); i++ {
		if in[i] != '\\' || i+1 == len(in) {
			b.WriteByte(in[i])
			continue
		}
		if strings.HasPrefix(in[i:], `\u0000`) {
			b.WriteString(`\ufffd`)
			i += len(`\u0000`) - 1
			continue
		}
		// Any other escape: copy it whole so its second byte (an escaped
		// backslash, say) is never read as the start of another escape.
		b.WriteString(in[i : i+2])
		i++
	}
	out := b.String()
	return &out
}

// LoadStepExecution reads one step attempt by id.
func LoadStepExecution(ctx context.Context, db orm.Context, id string) (*core.StepExecution, error) {
	row, err := GetStepExecutionByID(ctx, db, id)
	if err != nil {
		if svcerr.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s", svcerr.NotFound("step execution"), id)
		}
		return nil, err
	}
	return stepExecutionToCore(row), nil
}

// ListWorkflowStepExecutions is every step of a workflow, oldest first: the
// full history reconstruction.
func ListWorkflowStepExecutions(ctx context.Context, db orm.Context, workflowID string) ([]*core.StepExecution, error) {
	return selectStepExecutions(ctx, db, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("workflow_id = ?", workflowID).Order("created_at ASC")
	})
}

func selectStepExecutions(ctx context.Context, db orm.Context, apply func(*bun.SelectQuery) *bun.SelectQuery) ([]*core.StepExecution, error) {
	var rows []*StepExecution
	if err := apply(db.Bun().NewSelect().Model(&rows)).Scan(ctx); err != nil {
		return nil, err
	}
	return stepExecutionsToCore(rows), nil
}

// stepExecutionRecordsForChatSQL is the full record of a chat's steps —
// inputs, output and error included — for ChatService/ListStepExecutions,
// which the builder's Run tab reads when the author inspects one step.
//
// It is the one read that ships output_json for internal activities too
// (CallLLM's output IS the step's answer), affordable because it is scoped:
// given a node_path (?1), only that node and what ran inside it are read, and
// the newest rows win when the limit cuts. Rows written before node_path
// existed fall back to their step id.
const stepExecutionRecordsForChatSQL = `
SELECT se.id, se.workflow_id, se.step_id, se.activity_name,
       se.output_json, se.exit_code, se.success, se.duration_ms,
       se.loop_node_id, se.loop_iteration, se.created_at,
       se.input_json, se.error_message, se.attempt, se.node_path
FROM step_executions se
JOIN workflows w ON w.id = se.workflow_id
WHERE w.chat_id = ?0
  AND (
      ?1::text = ''
      OR se.node_path = ?1::text
      -- starts_with, not LIKE: node ids are full of underscores, which LIKE
      -- reads as wildcards.
      OR starts_with(se.node_path, ?1::text || '.')
      OR (se.node_path IS NULL AND (se.step_id = ?1::text
                                    OR se.step_id = ?1::text || '-save'))
  )
ORDER BY se.created_at DESC, se.id DESC
LIMIT ?2`

// ListStepExecutionRecordsForChat returns a chat's full step rows, newest
// first, at most limit; a non-empty nodePath scopes them to that node and
// everything that ran inside it.
func ListStepExecutionRecordsForChat(ctx context.Context, db orm.Context, chatID, nodePath string, limit int) ([]*core.StepExecution, error) {
	var rows []*StepExecution
	if err := db.Bun().NewRaw(stepExecutionRecordsForChatSQL, chatID, nodePath, limit).Scan(ctx, &rows); err != nil {
		return nil, err
	}
	return stepExecutionsToCore(rows), nil
}

// ListRunNodeExecutions reads one workflow subtree, never a different root
// run in the same chat. Inspect octet_length before any JSON cast: complete
// output/error values over 16 KiB, or over the 128 KiB cumulative scan budget,
// are omitted whole. Never take an unsanitized prefix/tail (it could split a
// secret). Only then extract streams, the safe skipped boolean and, for a
// save_message NODE, the text it wrote (a failure-outcome node's text is the
// run's explanation). Resolved inputs and arbitrary output fields never leave
// Postgres.
//
// A save_message node is a graph node like any other — a run that ends at a
// `outcome: failure` save_message has no other trace of where it ended — so
// only the "<node>-save" companion rows (the message an agent/LLM step wrote,
// already shown as that step) are dropped with the bookkeeping activities.
func ListRunNodeExecutions(ctx context.Context, db orm.Context, workflowID string, limit int) ([]*core.StepExecution, error) {
	if limit <= 0 || limit > 201 {
		limit = 201
	}
	var rows []*StepExecution
	err := db.Bun().NewRaw(`WITH RECURSIVE tree AS (
    SELECT id, chat_id FROM workflows WHERE id = ?0
    UNION ALL SELECT w.id, w.chat_id FROM workflows w JOIN tree t ON w.parent_id = t.id AND w.chat_id = t.chat_id
), picked AS (
 SELECT `+columnsOf[StepExecution]("se")+` FROM step_executions se JOIN tree t ON t.id = se.workflow_id
 WHERE se.activity_name <> ALL(ARRAY['WorkflowStatus', 'FetchThreadResult', 'Cleanup', 'WorkflowError']::text[])
   AND NOT (se.activity_name = 'SaveMessage' AND right(se.step_id, 5) = '-save')
 ORDER BY se.created_at DESC, se.id DESC LIMIT ?1
), budgeted AS (
 SELECT `+columnsOf[StepExecution]("picked")+`, sum(least(coalesce(octet_length(output_json), 0), 16385) + least(coalesce(octet_length(error_message), 0), 16385))
 OVER (ORDER BY created_at DESC, id DESC) AS scan_bytes FROM picked
)
SELECT se.id, se.workflow_id, se.step_id, se.activity_name,
       se.exit_code, se.success, se.duration_ms, se.created_at,
       se.loop_node_id, se.loop_iteration, se.attempt, se.node_path,
       CASE WHEN coalesce(octet_length(se.error_message), 0) = 0 THEN se.error_message
         WHEN octet_length(se.error_message) > 16384 OR se.scan_bytes > 131072
         THEN '[error omitted: safe diagnostic limit]' ELSE se.error_message END AS error_message,
       CASE WHEN octet_length(se.output_json) > 16384 OR se.scan_bytes > 131072
         THEN '{"_diagnostic_omitted":true}'
         WHEN se.output_json IS JSON OBJECT THEN json_build_object(
           'skipped', se.output_json::jsonb->'skipped' = 'true'::jsonb,
           'stdout', CASE WHEN se.activity_name LIKE '%ExecuteRunStep' OR se.exit_code IS NOT NULL THEN se.output_json::jsonb->>'stdout' END,
           'stderr', CASE WHEN se.activity_name LIKE '%ExecuteRunStep' OR se.exit_code IS NOT NULL THEN se.output_json::jsonb->>'stderr' END,
           'message', CASE WHEN se.activity_name = 'SaveMessage' THEN se.output_json::jsonb#>>'{message,text}' END)::text
         ELSE NULL END AS output_json
FROM budgeted se
ORDER BY se.created_at DESC, se.id DESC`, workflowID, limit).Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	return stepExecutionsToCore(rows), nil
}

// DeclaredFailureError is the error MarkDeclaredFailureNode records on the
// node a run ended at with outcome: failure. rundiagnostics appends the text
// a save_message node wrote.
const DeclaredFailureError = "declared the run's outcome: failure"

// MarkDeclaredFailureNode records, on the newest attempt of the top-level node
// nodeID of workflow workflowID, that it declared the run failed. A run that
// routes to an `outcome: failure` node completes with no error anywhere — the
// node's own activity succeeded — so without this every diagnostic surface
// (get_run, trigger health, the automation failure message) finds no failed
// node and says "declared failure without a recorded node error". A row that
// already carries an error keeps it. Reports whether a row was marked: a node
// that runs no activity (compute, a sub-workflow) has none.
func MarkDeclaredFailureNode(ctx context.Context, db orm.Context, workflowID, nodeID string) (bool, error) {
	res, err := db.Bun().NewRaw(`UPDATE step_executions SET success = 0, error_message = ?2
WHERE id = (
    SELECT id FROM step_executions
    WHERE workflow_id = ?0 AND step_id = ?1 AND (node_path IS NULL OR node_path = ?1)
      AND activity_name <> 'SkippedStep'
    ORDER BY created_at DESC, id DESC LIMIT 1)
  AND coalesce(error_message, '') = ''`, workflowID, nodeID, DeclaredFailureError).Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// chatStepExecutionsSQL is every step of every workflow of one chat, in one
// round trip, for ChatService/GetWorkflowExecutions (the FULL view). It
// replaced a per-workflow loop: 83 serial queries for the worst real chat,
// 2.61s of an 8-connection pool.
//
// output_json is read for almost no row, and that is the point: it is
// TOASTed out of line (905 MB against a 361 MB heap). A "-save" step's
// message id arrives as saved_message_id, a generated column (~42ms against
// ~520ms extracting it from output_json), and the output itself is selected
// ONLY for user-facing activities: of one chat's 32,023 steps, 32,013 are
// internal plumbing. The internal-activity list is a PARAMETER (?1), so the
// set lives in one Go place (workflowmodel.InternalActivities) and cannot
// drift from the frontend's INTERNAL_ACTIVITIES.
//
// ORDER BY (workflow_id, created_at) matches idx_step_executions_chat_read,
// which covers every scalar column below. Empty string, not NULL, is the
// withheld output: output_json is written by json.Marshal, so a real value is
// never empty. A LEFT JOIN to the row itself instead costs a primary-key
// lookup per row (146k buffers against 16k, ~200ms against ~45ms).
const chatStepExecutionsSQL = `
SELECT se.id, se.workflow_id, se.step_id, se.activity_name,
       se.exit_code, se.success, se.duration_ms,
       se.loop_node_id, se.loop_iteration, se.created_at,
       se.saved_message_id,
       COALESCE(
           CASE WHEN se.activity_name <> ALL(?1::text[])
                THEN se.output_json END,
           ''
       )::text AS output_json
FROM step_executions se
JOIN workflows w ON w.id = se.workflow_id
WHERE w.chat_id = ?0
ORDER BY se.workflow_id, se.created_at ASC`

// ListChatStepExecutions loads every step of every workflow of one chat,
// with OutputJSON only for the activities not in internalActivities.
func ListChatStepExecutions(ctx context.Context, db orm.Context, chatID string, internalActivities []string) ([]*core.ChatStepExecution, error) {
	if internalActivities == nil {
		// A nil array binds as NULL, and <> ALL(NULL) is NULL for every row.
		internalActivities = []string{}
	}
	return scanChatStepExecutions(ctx, db.Bun().NewRaw(chatStepExecutionsSQL, chatID, pgdialect.Array(internalActivities)))
}

// BasicChatStepExecutionsSQL is the steps the chat timeline renders, for
// ChatService/GetWorkflowExecutions in its BASIC view: each USER-FACING step,
// plus its "-save" sibling when that recorded a message (the step then
// renders AS that message). On chat 8bb0a875 that is 6 of 93,568 step rows:
// a few kB / ~6ms against 56.8 MB / ~1s for the full read.
//
// The activity list is a LITERAL, unlike chatStepExecutionsSQL's parameter:
// the planner proves a query is covered by a partial index only from a
// predicate it can see at plan time. With the literal it uses
// idx_step_executions_user_facing (96 kB); without it, a seq scan over every
// step row. The literal MUST equal workflowmodel.InternalActivities and the
// index predicate; TestUserFacingStepIndexPredicateMatchesInternalActivities
// pins all three.
//
// The second arm finds each visible step's "-save" sibling by
// (workflow_id, step_id || '-save', loop scope), so iteration 3's step never
// picks up iteration 2's save. Save rows carry no output_json, as in the FULL
// view.
//
// The loop scope is compared as ONE array, ARRAY[loop_node_id,
// loop_iteration::text], spelled exactly as idx_step_executions_save_scope's
// third key. Array equality treats NULL elements as equal, so it is the
// null-safe match a step outside any loop needs, and unlike IS NOT DISTINCT
// FROM it is an index condition. With IS NOT DISTINCT FROM the probe keyed
// only (workflow_id, step_id) and filtered the scope: an agent loop writes
// one execute_tools-save per turn under one step id, so every probe walked
// the run's whole history to keep one row (prod, chat 9fbaace8: 1,958 entries
// per probe, 1.3-3.9s per open). TestBasicStepSaveProbeUsesScopeIndex pins
// the index condition under a generic plan.
const BasicChatStepExecutionsSQL = `
SELECT u.id, u.workflow_id, u.step_id, u.activity_name,
       u.exit_code, u.success, u.duration_ms,
       u.loop_node_id, u.loop_iteration, u.created_at,
       u.saved_message_id, u.output_json::text AS output_json
FROM (
    SELECT se.id, se.workflow_id, se.step_id, se.activity_name,
           se.exit_code, se.success, se.duration_ms,
           se.loop_node_id, se.loop_iteration, se.created_at,
           se.saved_message_id,
           COALESCE(se.output_json, '') AS output_json
    FROM step_executions se
    JOIN workflows w ON w.id = se.workflow_id
    WHERE w.chat_id = ?0
      AND se.activity_name NOT IN (
          'WorkflowStatus', 'WorkflowError', 'Cleanup', 'FetchThreadResult',
          'FailStep', 'SaveMessage', 'CallLLM', 'Approval', 'ExecuteTools')
    UNION ALL
    SELECT sv.id, sv.workflow_id, sv.step_id, sv.activity_name,
           sv.exit_code, sv.success, sv.duration_ms,
           sv.loop_node_id, sv.loop_iteration, sv.created_at,
           sv.saved_message_id,
           '' AS output_json
    FROM step_executions vis
    JOIN workflows vw ON vw.id = vis.workflow_id
    JOIN step_executions sv
      ON sv.workflow_id = vis.workflow_id
     AND sv.step_id = vis.step_id || '-save'
     AND ARRAY[sv.loop_node_id, sv.loop_iteration::text]
         = ARRAY[vis.loop_node_id, vis.loop_iteration::text]
     AND sv.saved_message_id IS NOT NULL
    WHERE vw.chat_id = ?0
      AND vis.activity_name NOT IN (
          'WorkflowStatus', 'WorkflowError', 'Cleanup', 'FetchThreadResult',
          'FailStep', 'SaveMessage', 'CallLLM', 'Approval', 'ExecuteTools')
) u
ORDER BY u.workflow_id, u.created_at ASC`

// ListBasicChatStepExecutions loads only the steps the chat timeline renders.
func ListBasicChatStepExecutions(ctx context.Context, db orm.Context, chatID string) ([]*core.ChatStepExecution, error) {
	return scanChatStepExecutions(ctx, db.Bun().NewRaw(BasicChatStepExecutionsSQL, chatID))
}

// ─── Retention ───────────────────────────────────────────────────────────────
//
// step_executions was never pruned (prod 2026-11-10: 2.7M rows, 8.8 GB, most
// of it CallLLM / ExecuteTools / SaveMessage output out of line). What a
// finished run's steps are read for afterwards is the chat timeline
// (BasicChatStepExecutionsSQL: the user-facing steps and the "-save" rows that
// render them as messages, a handful per run) and diagnostics (get_run, the
// builder's Run tab: everything). Retention keeps the first exactly and lets
// the second degrade: it deletes a finished run's INTERNAL steps, except a
// "-save" row the timeline renders. The run's own row (workflows: state,
// outcome, failure_detail) is untouched, so its summary stays.
//
// The unit is a root run — what get_run and ListRunNodeExecutions read as one
// tree — that COMPLETED or was CANCELLED before the cutoff. Inside it, every
// workflow is judged on its own: only one that itself completed or was
// cancelled before the cutoff loses its internal steps. A FAILED workflow is
// resumable, a PAUSED one live, an ACTIVE or PENDING one running, and one that
// finished after the cutoff is too recent: their steps stay. A root that still
// holds any such workflow is DEFERRED (step_retention_deferred_roots) and
// revisited every pass until its whole tree has been pruned, so a child that
// finishes after its root was first visited is pruned in its turn. A FAILED
// root is never visited; if it is resumed and completes, its completed_at moves
// past the cursor and it is visited then.

// StepRetentionRoot is a root run whose internal steps may be pruned.
type StepRetentionRoot struct {
	ID          string    `bun:"id"`
	CompletedAt time.Time `bun:"completed_at"`
}

// stepRetentionRootsSQL: root runs that completed (stop reason 1) or were
// cancelled (4) before ?0, after (?1, ?2) in (completed_at, id) order, at most
// ?3. state 3 is STOPPED (TestStepRetentionLiteralsMatchTheEnums).
const stepRetentionRootsSQL = `
SELECT id, completed_at FROM workflows
 WHERE parent_id IS NULL AND state = 3 AND stop_reason IN (1, 4)
   AND completed_at < ?0
   AND (completed_at, id) > (?1, ?2)
 ORDER BY completed_at, id
 LIMIT ?3`

// StepRetentionRoots returns up to limit root runs that finished before
// cutoff, after (afterAt, afterID) in (completed_at, id) order.
func StepRetentionRoots(ctx context.Context, db orm.Context, cutoff, afterAt time.Time, afterID string, limit int) ([]StepRetentionRoot, error) {
	var out []StepRetentionRoot
	if err := db.Bun().NewRaw(stepRetentionRootsSQL, cutoff.UTC(), afterAt.UTC(), afterID, limit).Scan(ctx, &out); err != nil {
		return nil, fmt.Errorf("step retention roots: %w", err)
	}
	return out, nil
}

// stepRetentionTreeSQL is root run ?0's tree (the tree ListRunNodeExecutions
// reads), each workflow marked prunable when it itself completed (stop reason
// 1) or was cancelled (4) before ?1. Every other workflow — running, pending,
// paused, failed, finished since the cutoff, or with no completed_at — is not.
const stepRetentionTreeSQL = `
WITH RECURSIVE tree AS (
    SELECT id, chat_id FROM workflows WHERE id = ?0
    UNION ALL
    SELECT w.id, w.chat_id FROM workflows w JOIN tree t ON w.parent_id = t.id AND w.chat_id = t.chat_id
), judged AS (
    SELECT t.id, COALESCE(w.state = 3 AND w.stop_reason IN (1, 4) AND w.completed_at < ?1, false) AS prunable
      FROM tree t JOIN workflows w ON w.id = t.id
)`

// prunableStepsSQL selects the prunable steps of the prunable workflows of
// stepRetentionTreeSQL. The prunable list is model.InternalActivities, and the
// "-save" exemption is BasicChatStepExecutionsSQL's second arm verbatim: a
// save row whose user-facing step (same workflow, step id, loop scope)
// renders it in the chat timeline. Both literals are pinned by
// TestUserFacingStepIndexPredicateMatchesInternalActivities and
// TestStepRetentionLiteralsMatchTheEnums.
const prunableStepsSQL = `
    SELECT se.id FROM step_executions se
      JOIN judged j ON j.id = se.workflow_id AND j.prunable
     WHERE se.activity_name IN (
             'WorkflowStatus', 'WorkflowError', 'Cleanup', 'FetchThreadResult',
             'FailStep', 'SaveMessage', 'CallLLM', 'Approval', 'ExecuteTools')
       AND NOT (se.saved_message_id IS NOT NULL AND EXISTS (
             SELECT 1 FROM step_executions vis
              WHERE vis.workflow_id = se.workflow_id
                AND se.step_id = vis.step_id || '-save'
                AND ARRAY[se.loop_node_id, se.loop_iteration::text]
                    = ARRAY[vis.loop_node_id, vis.loop_iteration::text]
                AND vis.activity_name NOT IN (
                    'WorkflowStatus', 'WorkflowError', 'Cleanup', 'FetchThreadResult',
                    'FailStep', 'SaveMessage', 'CallLLM', 'Approval', 'ExecuteTools')))`

// pruneRunStepsSQL deletes up to ?2 of them. No SKIP LOCKED: a row another
// replica is deleting is waited for, so a batch never passes over a row whose
// delete then rolls back.
const pruneRunStepsSQL = stepRetentionTreeSQL + `
DELETE FROM step_executions WHERE id IN (` + prunableStepsSQL + `
     LIMIT ?2)`

// runStepsLeftSQL: whether any prunable step of the tree is left. A short
// batch does not prove there is none (rows another replica deleted under the
// LIMIT count for nothing), so the prune ends only on this.
const runStepsLeftSQL = stepRetentionTreeSQL + `
SELECT EXISTS (` + prunableStepsSQL + `)`

// runHasUnprunableWorkflowsSQL: whether the tree holds a workflow that is not
// prunable yet, so the root must be revisited.
const runHasUnprunableWorkflowsSQL = stepRetentionTreeSQL + `
SELECT EXISTS (SELECT 1 FROM judged WHERE NOT prunable)`

// PruneRunSteps deletes up to limit prunable steps of root run rootID's tree,
// as of cutoff, and returns how many.
func PruneRunSteps(ctx context.Context, db orm.Context, rootID string, cutoff time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("prune run steps: limit must be positive")
	}
	res, err := db.Bun().NewRaw(pruneRunStepsSQL, rootID, cutoff.UTC(), limit).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("prune run steps: %w", err)
	}
	return res.RowsAffected()
}

// RunStepsLeft reports whether root run rootID's tree still has a step
// PruneRunSteps would delete.
func RunStepsLeft(ctx context.Context, db orm.Context, rootID string, cutoff time.Time) (bool, error) {
	var left bool
	if err := db.Bun().NewRaw(runStepsLeftSQL, rootID, cutoff.UTC()).Scan(ctx, &left); err != nil {
		return false, fmt.Errorf("run steps left: %w", err)
	}
	return left, nil
}

// RunHasUnprunableWorkflows reports whether root run rootID's tree holds a
// workflow that is not prunable as of cutoff: the root must be revisited.
func RunHasUnprunableWorkflows(ctx context.Context, db orm.Context, rootID string, cutoff time.Time) (bool, error) {
	var pending bool
	if err := db.Bun().NewRaw(runHasUnprunableWorkflowsSQL, rootID, cutoff.UTC()).Scan(ctx, &pending); err != nil {
		return false, fmt.Errorf("run has unprunable workflows: %w", err)
	}
	return pending, nil
}

// DeferStepRetentionRoot records that root run rootID must be revisited.
func DeferStepRetentionRoot(ctx context.Context, db orm.Context, rootID string) error {
	if _, err := db.Exec(ctx, `INSERT INTO step_retention_deferred_roots (root_id) VALUES ($1) ON CONFLICT (root_id) DO NOTHING`, rootID); err != nil {
		return fmt.Errorf("defer step retention root: %w", err)
	}
	return nil
}

// ClearStepRetentionRoot records that root run rootID's tree is fully pruned.
func ClearStepRetentionRoot(ctx context.Context, db orm.Context, rootID string) error {
	if _, err := db.Exec(ctx, `DELETE FROM step_retention_deferred_roots WHERE root_id = $1`, rootID); err != nil {
		return fmt.Errorf("clear step retention root: %w", err)
	}
	return nil
}

// DeferredStepRetentionRoots returns up to limit deferred root runs after
// afterID, in id order.
func DeferredStepRetentionRoots(ctx context.Context, db orm.Context, afterID string, limit int) ([]string, error) {
	var ids []string
	if err := db.Bun().NewRaw(`SELECT root_id FROM step_retention_deferred_roots WHERE root_id > ?0 ORDER BY root_id LIMIT ?1`, afterID, limit).Scan(ctx, &ids); err != nil {
		return nil, fmt.Errorf("deferred step retention roots: %w", err)
	}
	return ids, nil
}

// chatStepExecutionRow is one row of the chat-wide step reads.
type chatStepExecutionRow struct {
	ID             string    `bun:"id"`
	WorkflowID     string    `bun:"workflow_id"`
	StepID         string    `bun:"step_id"`
	ActivityName   string    `bun:"activity_name"`
	ExitCode       *int64    `bun:"exit_code"`
	Success        *int64    `bun:"success"`
	DurationMs     *int64    `bun:"duration_ms"`
	LoopNodeID     *string   `bun:"loop_node_id"`
	LoopIteration  *int64    `bun:"loop_iteration"`
	CreatedAt      time.Time `bun:"created_at"`
	SavedMessageID *string   `bun:"saved_message_id"`
	OutputJSON     string    `bun:"output_json"`
}

func scanChatStepExecutions(ctx context.Context, q *bun.RawQuery) ([]*core.ChatStepExecution, error) {
	var rows []chatStepExecutionRow
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, err
	}
	out := make([]*core.ChatStepExecution, len(rows))
	for i, row := range rows {
		out[i] = &core.ChatStepExecution{
			ID:             row.ID,
			WorkflowID:     row.WorkflowID,
			StepID:         row.StepID,
			ActivityName:   row.ActivityName,
			ExitCode:       row.ExitCode,
			Success:        successBool(row.Success),
			DurationMs:     row.DurationMs,
			LoopNodeID:     row.LoopNodeID,
			LoopIteration:  row.LoopIteration,
			CreatedAt:      row.CreatedAt,
			SavedMessageID: row.SavedMessageID,
			OutputJSON:     row.OutputJSON,
		}
	}
	return out, nil
}

func stepExecutionsToCore(rows []*StepExecution) []*core.StepExecution {
	out := make([]*core.StepExecution, len(rows))
	for i, row := range rows {
		out[i] = stepExecutionToCore(row)
	}
	return out
}

func stepExecutionToCore(row *StepExecution) *core.StepExecution {
	return &core.StepExecution{
		ID:             row.ID,
		WorkflowID:     row.WorkflowID,
		StepID:         row.StepID,
		ActivityName:   row.ActivityName,
		OutputJSON:     row.OutputJSON,
		ExitCode:       row.ExitCode,
		Success:        successBool(row.Success),
		DurationMs:     row.DurationMs,
		LoopNodeID:     row.LoopNodeID,
		LoopIteration:  row.LoopIteration,
		CreatedAt:      row.CreatedAt,
		SavedMessageID: row.SavedMessageID,
		InputJSON:      row.InputJSON,
		ErrorMessage:   row.ErrorMessage,
		Attempt:        row.Attempt,
		NodePath:       row.NodePath,
	}
}

// successFlag stores a step's success as the column's 0/1 bigint.
func successFlag(success *bool) *int64 {
	if success == nil {
		return nil
	}
	var v int64
	if *success {
		v = 1
	}
	return &v
}

// successBool reads the 0/1 success flag back.
func successBool(v *int64) *bool {
	if v == nil {
		return nil
	}
	b := *v != 0
	return &b
}

// StepExecutionExtra carries fields on StepExecution that are NOT columns — computed
// values, transient state, anything derived rather than stored. StepExecution is
// embedded on StepExecutionExtra (see step_execution_orm.go), so a field added here is a field on
// every StepExecution value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type StepExecutionExtra struct {
}

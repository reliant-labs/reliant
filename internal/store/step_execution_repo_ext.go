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
	"database/sql"
	"fmt"
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
		OutputJSON:    nullStringPtr(exec.OutputJSON),
		ExitCode:      nullInt64Ptr(exec.ExitCode),
		Success:       successFlag(exec.Success),
		DurationMs:    nullInt64Ptr(exec.DurationMs),
		CreatedAt:     exec.CreatedAt,
		LoopNodeID:    nullStringPtr(exec.LoopNodeID),
		LoopIteration: nullInt64Ptr(exec.LoopIteration),
		InputJSON:     nullStringPtr(exec.InputJSON),
		ErrorMessage:  nullStringPtr(exec.ErrorMessage),
		Attempt:       nullInt32Ptr(exec.Attempt),
		NodePath:      nullStringPtr(exec.NodePath),
	})
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
// secret). Only then extract streams and the safe skipped boolean. Resolved
// inputs and arbitrary output fields never leave Postgres.
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
 WHERE se.activity_name <> ALL(ARRAY['WorkflowStatus', 'SaveMessage', 'FetchThreadResult', 'Cleanup', 'WorkflowError']::text[])
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
           'stderr', CASE WHEN se.activity_name LIKE '%ExecuteRunStep' OR se.exit_code IS NOT NULL THEN se.output_json::jsonb->>'stderr' END)::text
         ELSE NULL END AS output_json
FROM budgeted se
ORDER BY se.created_at DESC, se.id DESC`, workflowID, limit).Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	return stepExecutionsToCore(rows), nil
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

// basicChatStepExecutionsSQL is the steps the chat timeline renders, for
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
// (workflow_id, step_id || '-save', loop scope) through
// idx_step_executions_saves. Loop scope is matched with IS NOT DISTINCT FROM
// so iteration 3's step never picks up iteration 2's save. Save rows carry
// no output_json, as in the FULL view.
const basicChatStepExecutionsSQL = `
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
     AND sv.loop_node_id IS NOT DISTINCT FROM vis.loop_node_id
     AND sv.loop_iteration IS NOT DISTINCT FROM vis.loop_iteration
     AND sv.saved_message_id IS NOT NULL
    WHERE vw.chat_id = ?0
      AND vis.activity_name NOT IN (
          'WorkflowStatus', 'WorkflowError', 'Cleanup', 'FetchThreadResult',
          'FailStep', 'SaveMessage', 'CallLLM', 'Approval', 'ExecuteTools')
) u
ORDER BY u.workflow_id, u.created_at ASC`

// ListBasicChatStepExecutions loads only the steps the chat timeline renders.
func ListBasicChatStepExecutions(ctx context.Context, db orm.Context, chatID string) ([]*core.ChatStepExecution, error) {
	return scanChatStepExecutions(ctx, db.Bun().NewRaw(basicChatStepExecutionsSQL, chatID))
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
			ExitCode:       toNullInt64(row.ExitCode),
			Success:        toNullBool(row.Success),
			DurationMs:     toNullInt64(row.DurationMs),
			LoopNodeID:     toNullString(row.LoopNodeID),
			LoopIteration:  toNullInt64(row.LoopIteration),
			CreatedAt:      row.CreatedAt,
			SavedMessageID: toNullString(row.SavedMessageID),
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
		OutputJSON:     toNullString(row.OutputJSON),
		ExitCode:       toNullInt64(row.ExitCode),
		Success:        toNullBool(row.Success),
		DurationMs:     toNullInt64(row.DurationMs),
		LoopNodeID:     toNullString(row.LoopNodeID),
		LoopIteration:  toNullInt64(row.LoopIteration),
		CreatedAt:      row.CreatedAt,
		SavedMessageID: toNullString(row.SavedMessageID),
		InputJSON:      toNullString(row.InputJSON),
		ErrorMessage:   toNullString(row.ErrorMessage),
		Attempt:        toNullInt32(row.Attempt),
		NodePath:       toNullString(row.NodePath),
	}
}

// successFlag stores a step's success as the column's 0/1 bigint.
func successFlag(success sql.NullBool) *int64 {
	if !success.Valid {
		return nil
	}
	var v int64
	if success.Bool {
		v = 1
	}
	return &v
}

// toNullBool reads the 0/1 success flag back.
func toNullBool(v *int64) sql.NullBool {
	if v == nil {
		return sql.NullBool{}
	}
	return sql.NullBool{Bool: *v != 0, Valid: true}
}

func nullStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

func nullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

func nullInt32Ptr(v sql.NullInt32) *int32 {
	if !v.Valid {
		return nil
	}
	return &v.Int32
}

func toNullString(v *string) sql.NullString {
	if v == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *v, Valid: true}
}

func toNullInt64(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}

func toNullTime(v *time.Time) sql.NullTime {
	if v == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *v, Valid: true}
}

func toNullInt32(v *int32) sql.NullInt32 {
	if v == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: *v, Valid: true}
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

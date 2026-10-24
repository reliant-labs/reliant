// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ToolCall: the durable status row of every
// tool call, and the spawn read models joined onto it (P5b lane L1a). Every
// function takes and returns internal/db/core types.
//
// The two upserts run through orm.Context's raw passthrough with bound
// parameters: they carry the call's input and the result's content, which can
// be large, and bun's formatter would write them into the statement text — a
// new prepared statement, parsed whole, on every status transition. The
// reconciler and spawn reads are NewRaw: one statement each, read as SQL.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// upsertToolCallSQL writes a tool call, updating it in place on a retry.
//
// Activities that create/update a tool call retry on failure, so the write
// must be idempotent: a retry re-sending the same id updates the row in place
// instead of erroring on the primary key.
//
// Only in place for the SAME chat. The id is the model provider's, and a
// provider can hand two chats the same one; the row belongs to whichever chat
// recorded it first. A write for another chat updates nothing (0 rows), which
// UpsertToolCall reports as core.ErrToolCallIDInAnotherChat. chat_id was never
// in the SET list, so before this guard such a write produced a row that still
// claimed the first chat while carrying the second chat's thread, input and
// status.
const upsertToolCallSQL = `
INSERT INTO tool_calls (
    id, chat_id, thread_id, message_id, tool_name, input, status,
    error_message, child_workflow_id, background_process_id,
    requested_at, started_at, completed_at, created_at, updated_at,
    daemon_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16
)
ON CONFLICT (id) DO UPDATE SET
    thread_id = EXCLUDED.thread_id,
    message_id = EXCLUDED.message_id,
    tool_name = EXCLUDED.tool_name,
    input = EXCLUDED.input,
    status = EXCLUDED.status,
    error_message = EXCLUDED.error_message,
    child_workflow_id = EXCLUDED.child_workflow_id,
    background_process_id = EXCLUDED.background_process_id,
    daemon_id = EXCLUDED.daemon_id,
    started_at = EXCLUDED.started_at,
    completed_at = EXCLUDED.completed_at,
    updated_at = EXCLUDED.updated_at
WHERE tool_calls.chat_id = EXCLUDED.chat_id
`

// UpsertToolCall writes call, replacing the row with its id when that row is
// the same chat's. A row recorded for another chat is never replaced: the
// write fails with core.ErrToolCallIDInAnotherChat.
func UpsertToolCall(ctx context.Context, db orm.Context, call *core.ToolCall) error {
	res, err := db.Exec(ctx, upsertToolCallSQL,
		call.ID, call.ChatID, call.ThreadID, call.MessageID, call.ToolName, jsonbArg(call.Input), int32(call.Status),
		call.ErrorMessage, call.ChildWorkflowID, call.BackgroundProcessID,
		call.RequestedAt, call.StartedAt, call.CompletedAt, call.CreatedAt, call.UpdatedAt,
		call.DaemonID,
	)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		// The only way the upsert writes nothing: the id is held by another
		// chat's call.
		return fmt.Errorf("%w: id %q, writing for chat %q", core.ErrToolCallIDInAnotherChat, call.ID, call.ChatID)
	}
	return nil
}

// FindToolCall reads one call by id, or nil when no row holds it.
func FindToolCall(ctx context.Context, db orm.Context, id string) (*core.ToolCall, error) {
	row, err := GetToolCallByID(ctx, db, id)
	if err != nil {
		if isMissing(err) {
			return nil, nil
		}
		return nil, err
	}
	return toolCallFromRow(row), nil
}

// ListToolCallsByChat is every call of a chat, in request order.
func ListToolCallsByChat(ctx context.Context, db orm.Context, chatID string) ([]*core.ToolCall, error) {
	return toolCalls(ListToolCall(ctx, db,
		orm.WhereEq(ToolCallFieldChatID, chatID),
		orm.WithOrderBy(ToolCallFieldRequestedAt, orm.Asc)))
}

// ListLiveToolCallsByChat is a chat's non-terminal calls (pending, executing,
// backgrounded), in request order. The snapshot needs these even when their
// message is outside the window, because their status is still changing and
// no block it ships can carry it. The statuses are literals in the statement
// text, which is what lets the planner prove the partial idx_tool_calls_chat_live
// (a few dozen rows database-wide) answers it instead of scanning the chat's
// ~25k terminal calls.
func ListLiveToolCallsByChat(ctx context.Context, db orm.Context, chatID string) ([]*core.ToolCall, error) {
	return toolCalls(ListToolCall(ctx, db,
		orm.WhereEq(ToolCallFieldChatID, chatID),
		orm.WhereIn(ToolCallFieldStatus, []core.ToolCallStatus{core.ToolCallStatusPending, core.ToolCallStatusExecuting, core.ToolCallStatusBackgrounded}),
		orm.WithOrderBy(ToolCallFieldRequestedAt, orm.Asc)))
}

// ListToolCallsByIDs reads calls by primary key, in request order. The lookup
// that cannot miss: a tool-call block always carries its tool_call_id,
// whereas tool_calls.message_id is a link a writer has to remember to set.
func ListToolCallsByIDs(ctx context.Context, db orm.Context, ids []string) ([]*core.ToolCall, error) {
	if len(ids) == 0 {
		return []*core.ToolCall{}, nil
	}
	return toolCalls(ListToolCall(ctx, db,
		orm.WhereIn(ToolCallFieldID, ids),
		orm.WithOrderBy(ToolCallFieldRequestedAt, orm.Asc)))
}

// ListToolCallsByMessageIDs is every call whose message_id is in messageIDs,
// ordered by message then request time: the message read path's batch load.
func ListToolCallsByMessageIDs(ctx context.Context, db orm.Context, messageIDs []string) ([]*core.ToolCall, error) {
	if len(messageIDs) == 0 {
		return []*core.ToolCall{}, nil
	}
	return toolCalls(ListToolCall(ctx, db,
		orm.WhereIn(ToolCallFieldMessageID, messageIDs),
		orm.WithOrderBy(ToolCallFieldMessageID, orm.Asc),
		orm.WithOrderBy(ToolCallFieldRequestedAt, orm.Asc)))
}

// listStrandedSpawnToolCallsSQL: spawn tool calls whose child workflow is
// terminal but which never received a result — the join from a finished
// sub-agent back to its parent, broken.
//
// executeSpawnInline writes the child's terminal workflow status and the
// parent's tool-call result as two separate activities. A worker that dies
// between them leaves the child correctly recorded as finished and the parent's
// call stuck at pending (1) / executing (2) forever. Nothing revisits it:
// Cleanup only runs when a workflow reaches an abnormal terminal path, and it
// is scoped to the ENDING workflow's own thread — the stranded row belongs to
// the still-live PARENT's thread, so it is outside every scope that cleans.
// Observed on real data: a child failed at 22:16:54 (a worker restart is in the
// logs at 22:08) and its parent's spawn call was still "executing" 49 hours
// later, with the sub-agent's work silently dropped.
//
// Anchored on the same durable evidence callIsStillLive already trusts —
// tool_calls.child_workflow_id joined to the child's status — so a row
// returned here is dead by exactly the rule cleanup applies one call at a time.
// Fails CLOSED in the same direction: a child that is still LIVE — active, or
// stopped only because it is paused — is never returned, because fabricating a
// failure for a live spawn writes a lie into conversation history that no
// later pass can distinguish from a real one. A missing result is recoverable;
// an invented one is not.
var listStrandedSpawnToolCallsSQL = `
SELECT ` + columnsOf[ToolCall]("tc") + ` FROM tool_calls tc
JOIN workflows w ON w.id = tc.child_workflow_id
WHERE tc.tool_name = 'spawn'
  AND tc.status IN (1, 2)
  AND w.state = 3 AND w.stop_reason <> 3
  AND NOT EXISTS (
      SELECT 1 FROM tool_call_results r WHERE r.tool_call_id = tc.id
  )
ORDER BY tc.requested_at ASC
`

// ListStrandedSpawnToolCalls returns spawn calls whose child workflow is
// terminal but which never received a result (listStrandedSpawnToolCallsSQL).
func ListStrandedSpawnToolCalls(ctx context.Context, db orm.Context) ([]*core.ToolCall, error) {
	var rows []*ToolCall
	if err := db.Bun().NewRaw(listStrandedSpawnToolCallsSQL).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to list stranded spawn tool calls: %w", err)
	}
	return toolCallsFromRows(rows), nil
}

// listStrandedBackgroundSpawnToolCallsSQL is the async-spawn counterpart to
// listStrandedSpawnToolCallsSQL (spec: async-spawn-and-agent-messaging.md,
// §7.1). A background=true spawn (dispatchSpawnBackground/workflow.go) writes
// tool_calls.status = 6 (backgrounded) AND its tool_call_results row AT
// DISPATCH TIME, before the child has done any work -- so neither "status IN
// (1,2)" nor "NOT EXISTS(tool_call_results)" can ever match one, no matter how
// long its child has been finished with nothing delivered to the parent. The
// durable anchor moves to the mailbox: the detached goroutine that runs the
// child's turns reports its outcome by enqueueing a terminal-kind
// (2=completion, 3=cancelled, 4=failed) agent_messages row addressed to the
// parent thread, keyed by tool_call_id (see EnqueueAgentMessageIfAbsent). A
// worker that dies between the child reaching a terminal workflow status and
// that enqueue landing leaves the exact same silent-drop shape the sync repair
// exists for, just with the join anchor moved from tool_call_results to
// agent_messages.
//
// Same fail-closed discipline as the sync version: a child that is still LIVE
// — active, or stopped only because it is paused — is never returned, because
// fabricating a completion for a live spawn writes a lie into the parent's
// mailbox that no later pass can distinguish from a real one. A missing
// report is recoverable; an invented one is not.
//
// has_report: a terminal child whose report DID land is still returned while
// its row sits at status 6. The report and the status are written by
// different code (the detached goroutine enqueues; nothing on that path moves
// the row), so "reported" never implied "closed" — and filtering reported
// calls out made the close unreachable for exactly the spawns that finished
// normally. Observed: toolu_013CJA3i on chat 8bb0a875 still backgrounded two
// days after its child stopped and its kind=4 report was delivered. The
// caller enqueues only when has_report is false, and closes the row either
// way.
const listStrandedBackgroundSpawnToolCallsSQL = `
SELECT tc.id AS tool_call_id,
       tc.chat_id,
       tc.thread_id AS parent_thread_id,
       w.thread AS child_thread_id,
       w.state AS workflow_state,
       w.stop_reason AS workflow_stop_reason,
       EXISTS (
           SELECT 1 FROM agent_messages m
           WHERE m.chat_id = tc.chat_id AND m.tool_call_id = tc.id AND m.kind IN (2, 3, 4)
       ) AS has_report
FROM tool_calls tc
JOIN workflows w ON w.id = tc.child_workflow_id
WHERE tc.tool_name = 'spawn'
  AND tc.status = 6
  AND w.state = 3 AND w.stop_reason <> 3
ORDER BY tc.requested_at ASC
`

// ListStrandedBackgroundSpawnToolCalls returns backgrounded spawn calls whose
// child workflow is terminal (listStrandedBackgroundSpawnToolCallsSQL).
func ListStrandedBackgroundSpawnToolCalls(ctx context.Context, db orm.Context) ([]*core.StrandedBackgroundSpawn, error) {
	var rows []struct {
		ToolCallID         string  `bun:"tool_call_id"`
		ChatID             string  `bun:"chat_id"`
		ParentThreadID     *string `bun:"parent_thread_id"`
		ChildThreadID      string  `bun:"child_thread_id"`
		WorkflowState      int32   `bun:"workflow_state"`
		WorkflowStopReason int32   `bun:"workflow_stop_reason"`
		HasReport          bool    `bun:"has_report"`
	}
	if err := db.Bun().NewRaw(listStrandedBackgroundSpawnToolCallsSQL).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to list stranded background spawn tool calls: %w", err)
	}
	calls := make([]*core.StrandedBackgroundSpawn, 0, len(rows))
	for _, row := range rows {
		calls = append(calls, &core.StrandedBackgroundSpawn{
			ToolCallID:     row.ToolCallID,
			ChatID:         row.ChatID,
			ParentThreadID: row.ParentThreadID,
			ChildThreadID:  row.ChildThreadID,
			WorkflowStatus: core.WorkflowStatus{
				State:      core.WorkflowState(row.WorkflowState),
				StopReason: core.WorkflowStopReason(row.WorkflowStopReason),
			},
			HasReport: row.HasReport,
		})
	}
	return calls, nil
}

// listBackgroundedProcessToolCallsSQL: every backgrounded call whose outcome
// lives in a daemon's process table — status 6 with no child workflow. Spawns
// are excluded: their outcome is the child workflow, which
// ListStrandedBackgroundSpawnToolCalls reconciles.
//
// Read by the reconciler's background-process sweep, which asks each owning
// daemon whether the process is still running and closes the call when it is
// not. daemon_id and background_process_id are NULL on rows written before
// they were recorded; the sweep closes those only when the user's daemons
// prove the process gone, so they are returned too.
//
// Served by idx_tool_calls_chat_live (status IN (1,2,6)), which stays small
// once the sweep keeps status 6 honest.
const listBackgroundedProcessToolCallsSQL = `
SELECT tc.id, tc.chat_id, tc.tool_name, tc.background_process_id, tc.daemon_id,
       tc.requested_at, c.user_id
FROM tool_calls tc
JOIN chats c ON c.id = tc.chat_id
WHERE tc.status = 6
  AND tc.child_workflow_id IS NULL
ORDER BY tc.requested_at ASC
`

// ListBackgroundedProcessToolCalls returns backgrounded calls whose outcome
// lives in a daemon's process table (listBackgroundedProcessToolCallsSQL).
func ListBackgroundedProcessToolCalls(ctx context.Context, db orm.Context) ([]*core.BackgroundedProcessCall, error) {
	var rows []struct {
		ID                  string    `bun:"id"`
		ChatID              string    `bun:"chat_id"`
		ToolName            string    `bun:"tool_name"`
		BackgroundProcessID *string   `bun:"background_process_id"`
		DaemonID            *string   `bun:"daemon_id"`
		RequestedAt         time.Time `bun:"requested_at"`
		UserID              string    `bun:"user_id"`
	}
	if err := db.Bun().NewRaw(listBackgroundedProcessToolCallsSQL).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to list backgrounded process tool calls: %w", err)
	}
	calls := make([]*core.BackgroundedProcessCall, 0, len(rows))
	for _, row := range rows {
		calls = append(calls, &core.BackgroundedProcessCall{
			ToolCallID:          row.ID,
			ChatID:              row.ChatID,
			ToolName:            row.ToolName,
			UserID:              row.UserID,
			BackgroundProcessID: row.BackgroundProcessID,
			DaemonID:            row.DaemonID,
			RequestedAt:         row.RequestedAt,
		})
	}
	return calls, nil
}

// listSpawnToolCallIDsByChildThreadSQL: the spawn tool call behind each child
// thread in one chat, for the reconnect snapshot. It is what the
// background-work pill's cancel button addresses, and threads has no
// spawned_by_tool_call_id column to read it from.
//
// w.thread (not tc.child_workflow_id) is the join key into the thread, for the
// same reason listSpawnChildrenForThreadSQL uses it: for a RESUMED spawn,
// child_workflow_id names the workflow executing THIS call — a fresh id each
// resumption — while w.thread is the pre-existing thread being resumed, which
// is the thread the UI actually shows.
//
// A resumed spawn yields SEVERAL rows for one child thread (one per
// resumption, observed up to 6). requested_at ASC is load-bearing: the caller
// folds these into a map, so ascending order leaves the most recent call as
// the surviving value — which is the one still executing and therefore the one
// a cancel must address.
//
// Deliberately narrow. The snapshot needs ~1 row per spawn, and a busy chat
// holds ~10k tool calls whose input jsonb is large; selecting tool_calls.* and
// filtering in Go would haul all of that over the wire on every page load.
// Measured on the busiest real chat: 60 rows in 1.9ms via
// idx_tool_calls_child_workflow_id, against 10,622 tool calls.
const listSpawnToolCallIDsByChildThreadSQL = `
SELECT
    w.thread AS child_thread_id,
    tc.id AS tool_call_id
FROM tool_calls tc
JOIN workflows w ON w.id = tc.child_workflow_id
WHERE tc.chat_id = ?0
  AND tc.child_workflow_id IS NOT NULL
ORDER BY tc.requested_at ASC
`

// SpawnToolCallIDsByChildThread maps child thread id -> the spawn tool call
// that started it, for one chat (listSpawnToolCallIDsByChildThreadSQL). Rows
// arrive requested_at ASC, so overwriting leaves a resumed spawn's most recent
// call as the value: the one still executing, and the one a cancel must
// address.
func SpawnToolCallIDsByChildThread(ctx context.Context, db orm.Context, chatID string) (map[string]string, error) {
	var rows []struct {
		ChildThreadID string `bun:"child_thread_id"`
		ToolCallID    string `bun:"tool_call_id"`
	}
	if err := db.Bun().NewRaw(listSpawnToolCallIDsByChildThreadSQL, chatID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to list spawn tool call ids: %w", err)
	}
	byThread := make(map[string]string, len(rows))
	for _, row := range rows {
		if row.ChildThreadID == "" || row.ToolCallID == "" {
			continue
		}
		byThread[row.ChildThreadID] = row.ToolCallID
	}
	return byThread, nil
}

// listSpawnChildrenForThreadSQL: every spawn call a thread has issued, joined
// to the child's live workflow and thread rows. tool_calls.thread_id is the
// PARENT's thread — see listStrandedSpawnToolCallsSQL for the same join anchor
// — so this needs no new column. LEFT JOIN throughout: a child_workflow_id can
// be set before CreateWorkflowWithThread's activity has landed the
// workflow/thread rows (a narrow window right after dispatch), and a caller
// listing mid-race should see the call as pending rather than get an empty
// result.
//
// w.thread (not tc.child_workflow_id) is the join key into threads: for a
// RESUMED spawn (agent_id set), child_workflow_id names the workflow executing
// THIS call, which is a fresh id each resumption, while w.thread is the
// pre-existing thread being resumed — the one spawn_status/spawn_send must
// address. CreateWorkflowWithThread always sets Workflow.Thread to the child
// thread id, new or resumed, so this join is correct in both cases. Every
// joined column scans nullable: a missing/not-yet-created child produces NULLs.
//
// child_workflow_id is selected ALONGSIDE child_thread_id because for a
// resumed spawn they differ and both are needed: the thread is what a cancel
// signal names, while the workflow row id is what a status reconcile must
// CAS. Deriving either from the other is not possible — see spawn_stop.
//
// issuing_message_ordinal places the spawn in the parent's transcript. A chat
// BRANCHED from the parent inherits the parent's history only up to its fork
// point, so this is what decides which of the parent's spawns the branch
// inherited (ListInheritedSpawnChildren). messages.ordinal is per-thread, and
// the issuing message is always in tc.thread_id, so it compares directly with
// the fork point's ordinal. NULL when the call was recorded before its
// assistant message was finalized.
const listSpawnChildrenForThreadSQL = `
SELECT
    tc.id AS tool_call_id,
    tc.status AS tool_call_status,
    tc.input AS tool_input,
    tc.requested_at,
    tc.completed_at,
    tc.child_workflow_id,
    w.thread AS child_thread_id,
    w.state AS workflow_state,
    w.stop_reason AS workflow_stop_reason,
    w.completed_at AS workflow_completed_at,
    t.title AS thread_title,
    m.ordinal AS issuing_message_ordinal
FROM tool_calls tc
LEFT JOIN workflows w ON w.id = tc.child_workflow_id
LEFT JOIN threads t ON t.id = w.thread
LEFT JOIN messages m ON m.id = tc.message_id
WHERE tc.tool_name = 'spawn'
  AND tc.thread_id = ?0
ORDER BY tc.requested_at ASC
`

// ListSpawnChildren returns every spawn call issued BY threadID, in request
// order (listSpawnChildrenForThreadSQL).
func ListSpawnChildren(ctx context.Context, db orm.Context, threadID string) ([]*core.SpawnChild, error) {
	var rows []struct {
		ToolCallID            string          `bun:"tool_call_id"`
		ToolCallStatus        int32           `bun:"tool_call_status"`
		ToolInput             json.RawMessage `bun:"tool_input"`
		RequestedAt           time.Time       `bun:"requested_at"`
		CompletedAt           *time.Time      `bun:"completed_at"`
		ChildWorkflowID       *string         `bun:"child_workflow_id"`
		ChildThreadID         *string         `bun:"child_thread_id"`
		WorkflowState         *int32          `bun:"workflow_state"`
		WorkflowStopReason    *int32          `bun:"workflow_stop_reason"`
		WorkflowCompletedAt   *time.Time      `bun:"workflow_completed_at"`
		ThreadTitle           *string         `bun:"thread_title"`
		IssuingMessageOrdinal *int64          `bun:"issuing_message_ordinal"`
	}
	if err := db.Bun().NewRaw(listSpawnChildrenForThreadSQL, threadID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to list spawn children: %w", err)
	}
	children := make([]*core.SpawnChild, 0, len(rows))
	for _, row := range rows {
		child := &core.SpawnChild{
			ToolCallID:            row.ToolCallID,
			ToolCallStatus:        row.ToolCallStatus,
			ToolInput:             []byte(row.ToolInput),
			RequestedAt:           row.RequestedAt,
			CompletedAt:           row.CompletedAt,
			ChildThreadID:         row.ChildThreadID,
			ChildWorkflowID:       row.ChildWorkflowID,
			WorkflowCompleted:     row.WorkflowCompletedAt,
			ThreadTitle:           row.ThreadTitle,
			IssuingMessageOrdinal: row.IssuingMessageOrdinal,
		}
		// state and stop_reason arrive from the same LEFT JOIN, so either
		// both are present or the child's workflow row has not landed yet.
		if row.WorkflowState != nil {
			status := core.WorkflowStatus{State: core.WorkflowState(*row.WorkflowState)}
			if row.WorkflowStopReason != nil {
				status.StopReason = core.WorkflowStopReason(*row.WorkflowStopReason)
			}
			child.WorkflowStatus = &status
		}
		children = append(children, child)
	}
	return children, nil
}

// listLiveBackgroundSpawnsForWorkflowSQL: every background spawn issued
// anywhere inside one root execution that is still open — tool_calls.status =
// 6 (backgrounded) and no terminal report in its parent's mailbox. A background
// spawn is a goroutine inside the ROOT's Temporal execution — at every depth —
// so when that execution dies (the history-limit terminate is the case this
// exists for) these are exactly the spawns the coarse fresh restart must
// relaunch.
//
// Walks the workflow tree from the root: a spawn's child row carries the id of
// the workflow that ISSUED it as parent_id (the root for a top-level spawn, the
// spawning child's id for a nested one). Deliberately NOT filtered on the child
// rows' state: the reconciler's reap may already have marked them terminal,
// and that is an echo of the root dying, not of the child finishing. A spawn
// the stranded-spawn repair already closed has left status 6 and has a report,
// so it is not returned — its parent was told, and relaunching it would report
// twice. Ordered parents-before-children (depth), then by id, so a relaunch
// registers an issuing spawn before its own.
const listLiveBackgroundSpawnsForWorkflowSQL = `
WITH RECURSIVE tree AS (
    SELECT w.id, 0 AS depth FROM workflows w WHERE w.parent_id = ?0
    UNION ALL
    SELECT c.id, t.depth + 1 FROM workflows c JOIN tree t ON c.parent_id = t.id
)
SELECT tc.id AS tool_call_id,
       tc.thread_id AS parent_thread_id,
       tc.input AS tool_input,
       w.id AS child_workflow_id,
       w.thread AS child_thread_id,
       COALESCE(w.parent_id, '')::text AS issuing_workflow_id,
       t.depth::int AS depth
FROM tool_calls tc
JOIN workflows w ON w.id = tc.child_workflow_id
JOIN tree t ON t.id = w.id
WHERE tc.tool_name = 'spawn'
  AND tc.status = 6
  AND NOT EXISTS (
      SELECT 1 FROM agent_messages m
      WHERE m.chat_id = tc.chat_id AND m.tool_call_id = tc.id AND m.kind IN (2, 3, 4)
  )
ORDER BY t.depth ASC, tc.id ASC
`

// ListLiveBackgroundSpawns returns every background spawn issued anywhere in
// rootWorkflowID's execution that has not reported back, parents before
// children (listLiveBackgroundSpawnsForWorkflowSQL). A spawn with no parent
// thread on record is skipped: relaunching it would produce a report nobody
// can be addressed.
func ListLiveBackgroundSpawns(ctx context.Context, db orm.Context, rootWorkflowID string) ([]*core.LiveBackgroundSpawn, error) {
	var rows []struct {
		ToolCallID        string          `bun:"tool_call_id"`
		ParentThreadID    *string         `bun:"parent_thread_id"`
		ToolInput         json.RawMessage `bun:"tool_input"`
		ChildWorkflowID   string          `bun:"child_workflow_id"`
		ChildThreadID     string          `bun:"child_thread_id"`
		IssuingWorkflowID string          `bun:"issuing_workflow_id"`
		Depth             int             `bun:"depth"`
	}
	if err := db.Bun().NewRaw(listLiveBackgroundSpawnsForWorkflowSQL, rootWorkflowID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to list live background spawns: %w", err)
	}
	spawns := make([]*core.LiveBackgroundSpawn, 0, len(rows))
	for _, row := range rows {
		if row.ParentThreadID == nil || *row.ParentThreadID == "" {
			continue
		}
		spawns = append(spawns, &core.LiveBackgroundSpawn{
			ToolCallID:        row.ToolCallID,
			ParentThreadID:    *row.ParentThreadID,
			ToolInput:         []byte(row.ToolInput),
			ChildWorkflowID:   row.ChildWorkflowID,
			ChildThreadID:     row.ChildThreadID,
			IssuingWorkflowID: row.IssuingWorkflowID,
			Depth:             row.Depth,
		})
	}
	return spawns, nil
}

// latestSpawnRunForThreadSQL: the latest spawn run of the sub-agent on one
// thread — the spawn tool call that started it, joined to that run's workflow
// row, the thread, and the thread's own worktree. What a message needs to
// resume a sub-agent that stopped on a failure, read from durable rows alone,
// so it does not depend on the execution that saw it stop still being alive
// (see db.Repo.GetStoppedSpawnAgent).
//
// One thread can carry several runs: spawn(agent_id=...) resumes an agent as a
// new workflow row on the same thread, while a message resumes it on the row it
// stopped with. The newest row is the agent's current state; an older failure
// that was resumed since is history, not a stopped agent.
//
// spawn_depth counts the run's ancestors up its workflow tree: 1 for a spawn
// the root issued, 2 for one a top-level spawn issued, and so on. The walk is
// bounded so a malformed parent cycle cannot recurse forever.
const latestSpawnRunForThreadSQL = `
WITH RECURSIVE latest AS (
    SELECT w.id, w.parent_id
    FROM workflows w
    JOIN tool_calls tc ON tc.child_workflow_id = w.id AND tc.tool_name = 'spawn'
    WHERE w.chat_id = ?0 AND w.thread = ?1
    ORDER BY w.created_at DESC, w.id DESC
    LIMIT 1
), ancestors AS (
    SELECT p.id, p.parent_id, 1 AS depth
    FROM workflows p JOIN latest l ON p.id = l.parent_id
    UNION ALL
    SELECT p.id, p.parent_id, a.depth + 1
    FROM workflows p JOIN ancestors a ON p.id = a.parent_id
    WHERE a.depth < 64
)
SELECT tc.id AS tool_call_id,
       COALESCE(tc.thread_id, '')::text AS parent_thread_id,
       tc.input AS tool_input,
       w.id AS child_workflow_id,
       COALESCE(w.parent_id, '')::text AS issuing_workflow_id,
       w.state AS workflow_state,
       w.stop_reason AS workflow_stop_reason,
       t.status AS thread_status,
       t.origin AS thread_origin,
       COALESCE(wt.path, '')::text AS worktree_path,
       (SELECT COALESCE(MAX(a.depth), 0) FROM ancestors a)::int AS spawn_depth
FROM latest l
JOIN workflows w ON w.id = l.id
JOIN tool_calls tc ON tc.child_workflow_id = w.id AND tc.tool_name = 'spawn'
JOIN threads t ON t.id = w.thread
LEFT JOIN worktrees wt ON wt.id = t.worktree_id AND wt.deleted_at IS NULL
ORDER BY tc.requested_at DESC
LIMIT 1
`

// LatestSpawnRunForThread is the latest spawn run of the sub-agent on
// threadID in chatID (latestSpawnRunForThreadSQL), or nil when the thread has
// none.
func LatestSpawnRunForThread(ctx context.Context, db orm.Context, chatID, threadID string) (*core.SpawnRun, error) {
	var rows []struct {
		ToolCallID         string            `bun:"tool_call_id"`
		ParentThreadID     string            `bun:"parent_thread_id"`
		ToolInput          json.RawMessage   `bun:"tool_input"`
		ChildWorkflowID    string            `bun:"child_workflow_id"`
		IssuingWorkflowID  string            `bun:"issuing_workflow_id"`
		WorkflowState      int32             `bun:"workflow_state"`
		WorkflowStopReason int32             `bun:"workflow_stop_reason"`
		ThreadStatus       core.ThreadStatus `bun:"thread_status"`
		ThreadOrigin       string            `bun:"thread_origin"`
		WorktreePath       string            `bun:"worktree_path"`
		SpawnDepth         int               `bun:"spawn_depth"`
	}
	if err := db.Bun().NewRaw(latestSpawnRunForThreadSQL, chatID, threadID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to read the latest spawn run of thread %s: %w", threadID, err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	row := rows[0]
	return &core.SpawnRun{
		ToolCallID:        row.ToolCallID,
		ParentThreadID:    row.ParentThreadID,
		ToolInput:         []byte(row.ToolInput),
		ChildWorkflowID:   row.ChildWorkflowID,
		IssuingWorkflowID: row.IssuingWorkflowID,
		WorkflowStatus: core.WorkflowStatus{
			State:      core.WorkflowState(row.WorkflowState),
			StopReason: core.WorkflowStopReason(row.WorkflowStopReason),
		},
		ThreadStatus: row.ThreadStatus,
		ThreadOrigin: core.ThreadOrigin(row.ThreadOrigin),
		WorktreePath: row.WorktreePath,
		SpawnDepth:   row.SpawnDepth,
	}, nil
}

// ListStoppedSpawnRecords reads all failed latest spawn runs, ancestry and
// report-slot counters in one round trip. Ranking happens before status
// filtering, so an old failure behind a completed/resumed run is not revived.
func ListStoppedSpawnRecords(ctx context.Context, db orm.Context, chatID string) ([]core.StoppedSpawnRecord, error) {
	var records []core.StoppedSpawnRecord
	err := db.Bun().NewRaw(`WITH RECURSIVE latest AS (
        SELECT DISTINCT ON (w.thread) w.id, w.thread, w.parent_id, w.state, w.stop_reason, w.created_at
        FROM workflows w
        WHERE w.chat_id = ?0
          AND EXISTS (SELECT 1 FROM tool_calls tc WHERE tc.child_workflow_id = w.id AND tc.tool_name = 'spawn')
        ORDER BY w.thread, w.created_at DESC, w.id DESC
    ), failed AS (
        SELECT l.id, l.thread, l.parent_id FROM latest l JOIN threads t ON t.id = l.thread
        WHERE l.state = 3 AND l.stop_reason = 2 AND t.status = 4 AND t.origin = 'spawn'
    ), ancestors AS (
        SELECT f.id AS child, p.id, p.parent_id, 1 AS depth FROM failed f JOIN workflows p ON p.id = f.parent_id
        UNION ALL
        SELECT a.child, p.id, p.parent_id, a.depth + 1 FROM ancestors a JOIN workflows p ON p.id = a.parent_id WHERE a.depth < 64
    )
    SELECT tc.id AS tool_call_id, tc.thread_id AS parent_thread_id, tc.input::text AS tool_input,
           f.thread AS child_thread_id, f.id AS child_workflow_id, f.parent_id AS issuing_workflow_id,
           COALESCE((SELECT MAX(depth) FROM ancestors a WHERE a.child = f.id), 0)::int AS spawn_depth,
           COALESCE(wt.path, '') AS worktree_path,
           COALESCE((SELECT MAX(substring(am.tool_call_id FROM length(tc.id) + 9)::int)
                     FROM agent_messages am WHERE am.chat_id = ?0 AND am.kind IN (2,3,4)
                       AND starts_with(am.tool_call_id, tc.id || '#resume-')
                       AND substring(am.tool_call_id FROM length(tc.id) + 9) ~ '^[0-9]+$'), 0)::int AS resumes,
           COALESCE((SELECT am.body FROM agent_messages am
                     WHERE am.chat_id = ?0 AND am.kind = 4 AND NOT am.synthesized
                       AND (am.tool_call_id = tc.id OR starts_with(am.tool_call_id, tc.id || '#resume-'))
                     ORDER BY am.created_at DESC, am.id DESC LIMIT 1), '') AS last_failure
    FROM failed f JOIN threads t ON t.id = f.thread
    JOIN LATERAL (SELECT tc.id,tc.thread_id,tc.input FROM tool_calls tc WHERE tc.child_workflow_id=f.id AND tc.tool_name='spawn' ORDER BY tc.requested_at DESC LIMIT 1) tc ON true
    LEFT JOIN worktrees wt ON wt.id=t.worktree_id AND wt.deleted_at IS NULL
    WHERE tc.thread_id IS NOT NULL
    ORDER BY spawn_depth, f.thread`, chatID).Scan(ctx, &records)
	return records, err
}

// LatestSpawnFailureReport is the body of the latest real failure report a
// spawn's runs have written in chatID, under its own slot (toolCallID) or a
// resumed run's (toolCallID#resume-N); "" when it has none. A reconciler's
// synthesized placeholder is not one: no run wrote it.
func LatestSpawnFailureReport(ctx context.Context, db orm.Context, chatID, toolCallID string) (string, error) {
	var bodies []string
	err := db.Bun().NewSelect().Model((*AgentMessage)(nil)).
		ColumnExpr("body").
		Where("chat_id = ?", chatID).
		Where("kind = 4").
		Where("NOT synthesized").
		Where("(tool_call_id = ? OR starts_with(tool_call_id, ?))", toolCallID, toolCallID+"#resume-").
		OrderExpr("created_at DESC, id DESC").
		Limit(1).
		Scan(ctx, &bodies)
	if err != nil {
		return "", fmt.Errorf("failed to read the latest failure report of %s: %w", toolCallID, err)
	}
	if len(bodies) == 0 {
		return "", nil
	}
	return bodies[0], nil
}

// SpawnResumeReportIDs is the report slots a spawn's resumed runs have
// written in chatID: the terminal mailbox reports whose id starts with
// slotPrefix (<tool_call_id>#resume-, see runtime.SpawnHandoff.ReportID). The
// next resume numbers its slot past the highest: a slot already holding a
// report drops a second one as a duplicate
// (idx_agent_messages_one_terminal_report_per_chat_spawn).
func SpawnResumeReportIDs(ctx context.Context, db orm.Context, chatID, slotPrefix string) ([]string, error) {
	var ids []string
	err := db.Bun().NewSelect().Model((*AgentMessage)(nil)).
		ColumnExpr("tool_call_id").
		Where("chat_id = ?", chatID).
		Where("kind IN (2, 3, 4)").
		Where("starts_with(tool_call_id, ?)", slotPrefix).
		Scan(ctx, &ids)
	if err != nil {
		return nil, fmt.Errorf("failed to list the resume reports of %s: %w", slotPrefix, err)
	}
	return ids, nil
}

// jsonbArg is a jsonb parameter: NULL for nil input (still streaming), the
// document's text otherwise.
func jsonbArg(doc []byte) any {
	if doc == nil {
		return nil
	}
	return string(doc)
}

func toolCalls(rows []*ToolCall, err error) ([]*core.ToolCall, error) {
	if err != nil {
		return nil, err
	}
	return toolCallsFromRows(rows), nil
}

func toolCallsFromRows(rows []*ToolCall) []*core.ToolCall {
	out := make([]*core.ToolCall, len(rows))
	for i, row := range rows {
		out[i] = toolCallFromRow(row)
	}
	return out
}

func toolCallFromRow(row *ToolCall) *core.ToolCall {
	var input []byte
	if row.Input != nil {
		input = []byte(row.Input)
	}
	return &core.ToolCall{
		ID:                  row.ID,
		ChatID:              row.ChatID,
		ThreadID:            row.ThreadID,
		MessageID:           row.MessageID,
		ToolName:            row.ToolName,
		Input:               input,
		Status:              row.Status,
		ErrorMessage:        row.ErrorMessage,
		ChildWorkflowID:     row.ChildWorkflowID,
		BackgroundProcessID: row.BackgroundProcessID,
		DaemonID:            row.DaemonID,
		RequestedAt:         row.RequestedAt,
		StartedAt:           row.StartedAt,
		CompletedAt:         row.CompletedAt,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
	}
}

// ToolCallExtra carries fields on ToolCall that are NOT columns — computed
// values, transient state, anything derived rather than stored. ToolCall is
// embedded on ToolCallExtra (see tool_call_orm.go), so a field added here is a field on
// every ToolCall value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ToolCallExtra struct {
}

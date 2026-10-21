// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for Thread. The generated thread_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: threadRepo (a *crud.Repo[Thread]) and the
//     package-level GetThreadByID / ListThread / CreateThread / UpdateThread / DeleteThread
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     Thread struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindThreadBy(ctx context.Context, db orm.Context, /* args */) ([]*Thread, error) {
//		var rows []*Thread
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("chat_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveThreads(ctx context.Context, db orm.Context) ([]*Thread, error) {
//		return ListThread(ctx, db, orm.WhereEq("chat_id", true))
//	}
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The threads queries (P5b L1b). A by-key miss is reported as a wrapped
// sql.ErrNoRows, the answer the sqlc queries gave: internal/threads tells a
// missing thread or context window from a failed read with
// errors.Is(err, sql.ErrNoRows).

// noRows wraps a by-key miss — whichever of forge's two spellings it came
// as (see chatIsMissing) — as sql.ErrNoRows under what.
func noRows(what string, err error) error {
	if chatIsMissing(err) {
		return fmt.Errorf("%s: %w", what, sql.ErrNoRows)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// InsertThread stores a new thread and returns it as stored. An unset origin
// is inferred (origin is NOT NULL: a thread with a parent is far more likely a
// spawn than a root), and an unset status is running.
func InsertThread(ctx context.Context, db orm.Context, t *core.Thread) (*core.Thread, error) {
	row := threadToRow(t)
	if err := CreateThread(ctx, db, row); err != nil {
		return nil, fmt.Errorf("failed to create thread: %w", err)
	}
	return threadFromRow(row), nil
}

// GetThread returns the thread; a miss is a wrapped sql.ErrNoRows.
func GetThread(ctx context.Context, db orm.Context, id string) (*core.Thread, error) {
	row, err := GetThreadByID(ctx, db, id)
	if err != nil {
		return nil, noRows("failed to get thread", err)
	}
	return threadFromRow(row), nil
}

// GetThreadByWorkflow returns the thread a workflow runs on; a miss is a
// wrapped sql.ErrNoRows.
func GetThreadByWorkflow(ctx context.Context, db orm.Context, workflowID string) (*core.Thread, error) {
	row := new(Thread)
	err := db.Bun().NewSelect().Model(row).Where("workflow_id = ?", workflowID).Limit(1).Scan(ctx)
	if err != nil {
		return nil, noRows("failed to get thread by workflow", err)
	}
	return threadFromRow(row), nil
}

// GetRootThread returns the chat's root thread (the first with no parent); a
// miss is a wrapped sql.ErrNoRows.
func GetRootThread(ctx context.Context, db orm.Context, chatID string) (*core.Thread, error) {
	row := new(Thread)
	err := db.Bun().NewSelect().Model(row).
		Where("chat_id = ?", chatID).
		Where("parent_thread_id IS NULL").
		OrderExpr("created_at ASC").
		Limit(1).
		Scan(ctx)
	if err != nil {
		return nil, noRows("failed to get root thread", err)
	}
	return threadFromRow(row), nil
}

// GetThreadWithParent returns the thread and its parent thread's chat id (nil
// without a parent), for fork chain resolution.
func GetThreadWithParent(ctx context.Context, db orm.Context, id string) (*core.Thread, *string, error) {
	var row struct {
		Thread
		ParentChatID *string `bun:"parent_chat_id"`
	}
	err := db.Bun().NewRaw(`
SELECT t.*, pt.chat_id AS parent_chat_id
FROM threads t
LEFT JOIN threads pt ON t.parent_thread_id = pt.id
WHERE t.id = ?0`, id).Scan(ctx, &row)
	if err != nil {
		return nil, nil, noRows("failed to get thread with parent", err)
	}
	return threadFromRow(&row.Thread), row.ParentChatID, nil
}

// The thread lists below have no natural bound, so they are built on the
// builder rather than the generated List, which caps a limit-less list at its
// default page (forge friction).

// ListThreadsByConversation returns the chat's threads, oldest first.
func ListThreadsByConversation(ctx context.Context, db orm.Context, chatID string) ([]*core.Thread, error) {
	var rows []*Thread
	err := db.Bun().NewSelect().Model(&rows).Where("chat_id = ?", chatID).OrderExpr("created_at ASC").Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list threads: %w", err)
	}
	return threadsFromRows(rows), nil
}

// ListChildThreads returns the threads forked directly from parentThreadID,
// oldest first.
func ListChildThreads(ctx context.Context, db orm.Context, parentThreadID string) ([]*core.Thread, error) {
	var rows []*Thread
	err := db.Bun().NewSelect().Model(&rows).Where("parent_thread_id = ?", parentThreadID).OrderExpr("created_at ASC").Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list child threads: %w", err)
	}
	return threadsFromRows(rows), nil
}

// ListThreadsByOrigin returns the chat's threads of one origin (every spawn
// thread, say), oldest first.
func ListThreadsByOrigin(ctx context.Context, db orm.Context, chatID string, origin core.ThreadOrigin) ([]*core.Thread, error) {
	var rows []*Thread
	err := db.Bun().NewSelect().Model(&rows).
		Where("chat_id = ?", chatID).
		Where("origin = ?", origin).
		OrderExpr("created_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list threads by origin: %w", err)
	}
	return threadsFromRows(rows), nil
}

// CountThreadsInConversation counts the chat's threads.
func CountThreadsInConversation(ctx context.Context, db orm.Context, chatID string) (int64, error) {
	return CountThread(ctx, db, orm.WhereEq(ThreadFieldChatID, chatID))
}

// updateThread writes the named columns of the thread row and returns the
// stored row (the masked update reads back every column it did not write); a
// miss is a wrapped sql.ErrNoRows.
func updateThread(ctx context.Context, db orm.Context, row *Thread, what string, fields ...string) (*core.Thread, error) {
	if err := UpdateThreadMasked(ctx, db, row, fields); err != nil {
		return nil, noRows(what, err)
	}
	return threadFromRow(row), nil
}

// UpdateThreadWorkflow points the thread at workflowID ("" clears it).
func UpdateThreadWorkflow(ctx context.Context, db orm.Context, threadID, workflowID string) (*core.Thread, error) {
	row := &Thread{ID: threadID}
	if workflowID != "" {
		row.WorkflowID = &workflowID
	}
	return updateThread(ctx, db, row, "failed to update thread workflow", ThreadFieldWorkflowID)
}

// UpdateThreadForkPoint moves the thread's fork point, when the fork's target
// message changes (the parent compacts, say).
func UpdateThreadForkPoint(ctx context.Context, db orm.Context, threadID string, forkAtMessageID *string) (*core.Thread, error) {
	row := &Thread{ID: threadID, ForkAtMessageID: forkAtMessageID}
	return updateThread(ctx, db, row, "failed to update thread fork point", ThreadFieldForkAtMessageID)
}

// UpdateThreadStatus records the thread's lifecycle. completedAt is only
// meaningful for a terminal status; a caller moving a thread back to running
// passes nil, which clears it.
func UpdateThreadStatus(ctx context.Context, db orm.Context, threadID string, status core.ThreadStatus, completedAt *time.Time) (*core.Thread, error) {
	row := &Thread{ID: threadID, Status: &status, CompletedAt: completedAt}
	return updateThread(ctx, db, row, "failed to update thread status", ThreadFieldStatus, ThreadFieldCompletedAt)
}

// reviveThreadSQL moves a thread back to running because a new run has
// started on it: the exact inverse of the terminal cascade below.
//
// A chat's MAIN thread is reused for every turn: SendMessage starts a fresh
// Temporal run under the same workflow ID and the same thread ID. The end of
// turn N stamps the thread terminal, and WorkflowStatusActivity's "started"
// arm moves the WORKFLOW row back to running for turn N+1 — but nothing moved
// the THREAD, so from the second turn onward every chat ran with a live
// workflow behind a thread row still reading completed. That is what made
// SendAgentMessage refuse a live agent with "This agent has already finished".
//
// completed_at goes back to NULL with the status: a revived thread that keeps
// the completion time of a turn that already ended reads as finished while it
// is executing. Guarded to terminal statuses (3,4,5, and the retired 7), so it
// is a no-op for a thread already running or paused rather than a write that
// clears a live thread's bookkeeping.
const reviveThreadSQL = `
UPDATE threads
SET status = 2, completed_at = NULL
WHERE id = ?0
  AND status IN (3, 4, 5, 7)`

// ReviveThread moves a terminal thread back to running and reports the rows
// it moved, so the caller can log a real revival without a second read.
func ReviveThread(ctx context.Context, db orm.Context, threadID string) (int64, error) {
	res, err := db.Bun().NewRaw(reviveThreadSQL, threadID).Exec(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// cascadeTerminalStatusToThreadSubtreeSQL is the thread half of the terminal
// cascade, the counterpart of the workflow cascade (CascadeTerminalStatusTo
// Descendants). threads.status is otherwise written only by
// ThreadStatusActivity on the live path, so an abnormal exit (terminate, reap,
// a status write that races past the thread's own "completed" call) left the
// thread running forever, with completed_at NULL — 288 such threads measured
// on the live DB (dev-docs/incidents/2026-08-12-spawn-history-cap.md). It is
// not cosmetic: ListThreadsWithOrphanedAgentMessages only matches threads
// already terminal, so a thread stuck at running hides its own orphaned
// mailbox from that sweep.
//
// Every caller pairs the workflow's own status write with the descendant
// cascade; this mirrors that pair in one statement. The subtree starts AT the
// workflow (its own thread closes too) and walks parent_id recursively.
// status IN (2, 6) is the workflow cascade's fail-closed guard: an
// already-terminal thread is left alone.
const cascadeTerminalStatusToThreadSubtreeSQL = `
WITH RECURSIVE subtree AS (
    SELECT ?0::text AS id
    UNION ALL
    SELECT c.id FROM workflows c JOIN subtree s ON c.parent_id = s.id
)
UPDATE threads AS t
SET status = ?1::int,
    completed_at = NOW()
FROM subtree
WHERE t.workflow_id = subtree.id
  AND t.status IN (2, 6)`

// CascadeTerminalStatusToThreadSubtree closes every live thread owned by
// workflowID or any of its descendants at status.
func CascadeTerminalStatusToThreadSubtree(ctx context.Context, db orm.Context, workflowID string, status core.ThreadStatus) error {
	_, err := db.Bun().NewRaw(cascadeTerminalStatusToThreadSubtreeSQL, workflowID, status).Exec(ctx)
	return err
}

// reapOrphanedThreadsSQL enforces the invariant the cascade asserts from the
// other direction: a thread whose WORKFLOW is terminal is not running. The
// thread-status mirror of ReapOrphanedWorkflowDescendants: the cascade runs
// only when the write path that ended a workflow remembers to call it, and a
// forgotten one strands the thread forever, because nothing else revisits a
// threads row.
//
// Fails CLOSED: a thread whose workflow is still LIVE — active, or stopped
// only because it is paused — is never touched, because inventing a terminal
// status for live work is unrecoverable (it would mark an in-flight agent's
// mailbox resolvable and its conversation finished), while a thread that
// stays stranded a little longer is not.
//
// The thread inherits the workflow's stop REASON in the thread vocabulary,
// the SQL twin of core.ThreadStatusForStopReason: a repaired cancel must not
// read as a repaired success. It returns each thread it moved and the
// workflow that owns it: a reap is evidence that some write path ended a
// workflow without cascading, and the reconciler needs the ids to ask
// Temporal and to log something actionable.
const reapOrphanedThreadsSQL = `
UPDATE threads AS t
SET status = CASE w.stop_reason
        WHEN 1 THEN 3  -- COMPLETED -> thread completed
        WHEN 2 THEN 4  -- FAILED    -> thread failed
        WHEN 4 THEN 5  -- CANCELLED -> thread cancelled
    END,
    completed_at = NOW()
FROM workflows w
WHERE t.workflow_id = w.id
  AND w.state = 3 AND w.stop_reason IN (1, 2, 4)
  AND t.status IN (2, 6)
RETURNING t.id AS thread_id, t.chat_id, w.id AS workflow_id, t.status`

// ReapOrphanedThreads ends running/paused threads whose workflow is already
// terminal, at the workflow's own status, returning the rows repaired.
func ReapOrphanedThreads(ctx context.Context, db orm.Context) ([]core.ReapedThread, error) {
	var rows []struct {
		ThreadID   string            `bun:"thread_id"`
		ChatID     string            `bun:"chat_id"`
		WorkflowID string            `bun:"workflow_id"`
		Status     core.ThreadStatus `bun:"status"`
	}
	if err := db.Bun().NewRaw(reapOrphanedThreadsSQL).Scan(ctx, &rows); err != nil {
		return nil, err
	}
	reaped := make([]core.ReapedThread, len(rows))
	for i, row := range rows {
		reaped[i] = core.ReapedThread{ThreadID: row.ThreadID, ChatID: row.ChatID, WorkflowID: row.WorkflowID, Status: row.Status}
	}
	return reaped, nil
}

// RemoveThread deletes the thread; one that does not exist is already gone.
func RemoveThread(ctx context.Context, db orm.Context, id string) error {
	if err := DeleteThread(ctx, db, id); err != nil && !chatIsMissing(err) {
		return err
	}
	return nil
}

// DeleteThreadsByConversation deletes every thread of the chat.
func DeleteThreadsByConversation(ctx context.Context, db orm.Context, chatID string) error {
	_, err := db.Bun().NewDelete().Model((*Thread)(nil)).Where("chat_id = ?", chatID).Exec(ctx)
	return err
}

// threadToRow maps a core.Thread onto the row InsertThread writes.
func threadToRow(t *core.Thread) *Thread {
	origin := t.Origin
	if origin == "" {
		origin = core.ThreadOriginNode
		if t.ParentThreadID == nil {
			origin = core.ThreadOriginMain
		}
	}
	status := t.Status
	if status == 0 {
		status = core.ThreadStatusRunning
	}
	return &Thread{
		ID:              t.ID,
		ChatID:          t.ChatID,
		ParentThreadID:  t.ParentThreadID,
		WorkflowID:      t.WorkflowID,
		CreatedAt:       t.CreatedAt,
		Title:           t.Title,
		Origin:          origin,
		OriginNodeID:    t.OriginNodeID,
		Status:          &status,
		CompletedAt:     t.CompletedAt,
		ForkAtMessageID: t.ForkAtMessageID,
		WorktreeID:      t.WorktreeID,
	}
}

func threadFromRow(row *Thread) *core.Thread {
	t := &core.Thread{
		ID:              row.ID,
		ChatID:          row.ChatID,
		ParentThreadID:  row.ParentThreadID,
		ForkAtMessageID: row.ForkAtMessageID,
		WorkflowID:      row.WorkflowID,
		Title:           row.Title,
		CreatedAt:       row.CreatedAt,
		Origin:          row.Origin,
		OriginNodeID:    row.OriginNodeID,
		CompletedAt:     row.CompletedAt,
		WorktreeID:      row.WorktreeID,
	}
	if row.Status != nil {
		t.Status = *row.Status
	}
	return t
}

func threadsFromRows(rows []*Thread) []*core.Thread {
	out := make([]*core.Thread, len(rows))
	for i, row := range rows {
		out[i] = threadFromRow(row)
	}
	return out
}

// ThreadExtra carries fields on Thread that are NOT columns — computed
// values, transient state, anything derived rather than stored. Thread is
// embedded on ThreadExtra (see thread_orm.go), so a field added here is a field on
// every Thread value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ThreadExtra struct {
}

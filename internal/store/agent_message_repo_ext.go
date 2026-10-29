// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for AgentMessage. The generated agent_message_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: agentMessageRepo (a *crud.Repo[AgentMessage]) and the
//     package-level GetAgentMessageByID / ListAgentMessage / CreateAgentMessage / UpdateAgentMessage / DeleteAgentMessage
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     AgentMessage struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindAgentMessageBy(ctx context.Context, db orm.Context, /* args */) ([]*AgentMessage, error) {
//		var rows []*AgentMessage
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("chat_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveAgentMessages(ctx context.Context, db orm.Context) ([]*AgentMessage, error) {
//		return ListAgentMessage(ctx, db, orm.WhereEq("chat_id", true))
//	}
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The agent_messages queries (P5b L1b): the mailbox a thread is drained from
// at each agent loop boundary. Every write is guarded on the row's status,
// because the writers race each other — two drains on one thread, a user's
// cancel against the agent's own drain, the reconciler against a live spawn
// report — and the guard, not a read-then-write in Go, is what decides who
// wins (specs/interrupt-pause-spec.md).
//
// The three statements on the drain's path — the queued read, its count and
// the preemption mark — run on every agent loop step, so they go through
// orm.Context's raw passthrough with bound $N parameters: bun would format the
// thread id into the text, a new prepared statement per thread under pgx's
// statement cache (internal/db/SCHEMA.md).

// agentMessageColumns is the column list the passthrough reads scan, in
// scanAgentMessage's order.
const agentMessageColumns = `id, chat_id, from_thread_id, to_thread_id, kind, body, tool_call_id,
    status, created_at, delivered_at, delivered_message_id, attachments, synthesized, subject`

// insertAgentMessageSQL is the insert every enqueue shares; each appends its
// own conflict clause. attachments is NULL for none (see
// agentMessageAttachments).
const insertAgentMessageSQL = `
INSERT INTO agent_messages (
    id, chat_id, from_thread_id, to_thread_id, kind, body, tool_call_id,
    status, created_at, attachments, synthesized, subject
) VALUES (?0, ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9::jsonb, ?10, ?11)`

func agentMessageInsertArgs(msg *core.AgentMessage) ([]any, error) {
	attachments, err := agentMessageAttachments(msg.Attachments)
	if err != nil {
		return nil, err
	}
	return []any{msg.ID, msg.ChatID, msg.FromThreadID, msg.ToThreadID, msg.Kind, msg.Body, msg.ToolCallID,
		msg.Status, msg.CreatedAt, attachments, msg.Synthesized, msg.Subject}, nil
}

// EnqueueAgentMessage queues msg.
func EnqueueAgentMessage(ctx context.Context, db orm.Context, msg *core.AgentMessage) error {
	args, err := agentMessageInsertArgs(msg)
	if err != nil {
		return err
	}
	_, err = db.Bun().NewRaw(insertAgentMessageSQL, args...).Exec(ctx)
	return err
}

// EnqueueAgentMessageOnce queues msg unless a row already holds its id, and
// reports which happened. A human send names its row (the client's message
// id), so a repeat of that send arrives with an id that is taken; ON CONFLICT
// (id) DO NOTHING is what lets exactly one of two racing repeats insert.
func EnqueueAgentMessageOnce(ctx context.Context, db orm.Context, msg *core.AgentMessage) (bool, error) {
	args, err := agentMessageInsertArgs(msg)
	if err != nil {
		return false, err
	}
	res, err := db.Bun().NewRaw(insertAgentMessageSQL+"\nON CONFLICT (id) DO NOTHING", args...).Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// EnqueueAgentMessageIfAbsent is the reconciler's PLACEHOLDER write for a
// stranded background spawn (spec §7.1): it queues msg, a terminal report,
// unless the spawn's slot (chat_id, tool_call_id) already holds one. ON
// CONFLICT against idx_agent_messages_one_terminal_report_per_chat_spawn is
// what makes this safe under concurrency — two reconciler passes, or a sweep
// racing the spawn's own delayed report, both attempt the same insert and
// Postgres serializes them at the row lock — where a SELECT-then-INSERT in Go
// would leave exactly the window a concurrent pass lands in.
//
// It reports whether this call's row landed. A slot held by this spawn's
// report is the ordinary "already reported" outcome; one held by a different
// spawn is core.ErrSpawnReportSlotTaken. A real report later supersedes the
// placeholder (EnqueueSpawnReport).
func EnqueueAgentMessageIfAbsent(ctx context.Context, db orm.Context, msg *core.AgentMessage) (bool, error) {
	args, err := agentMessageInsertArgs(msg)
	if err != nil {
		return false, err
	}
	res, err := db.Bun().NewRaw(insertAgentMessageSQL+"\nON CONFLICT (chat_id, tool_call_id) WHERE kind IN (2, 3, 4) DO NOTHING", args...).Exec(ctx)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return n > 0, err
	}
	return false, requireSlotHeldBySameSpawn(ctx, db, msg)
}

// enqueueSpawnReportSQL writes a REAL terminal spawn report. Unlike the
// placeholder write above (DO NOTHING), a real report replaces a placeholder
// the reconciler synthesized for the same spawn — re-queued even if the
// placeholder was delivered: the parent was told "result lost, go check
// spawn_status" and should also receive the outcome
// (dev-docs/incidents/2026-10-04-spawn-report-collision.md).
//
// The row takes the NEW id. A drain lists queued rows outside its transaction
// and then claims them by id, so a drain that listed the placeholder just
// before this supersede still holds the placeholder's id and stale body;
// keeping the id would let that claim take the real report, write the
// placeholder text, and mark the real report delivered unseen. With a fresh id
// the stale claim matches nothing, which the drain already treats as "batch
// taken, re-read next boundary". Nothing references agent_messages.id.
//
// WHERE agent_messages.synthesized protects a real report: against one the DO
// UPDATE matches nothing and no row returns (the slot is held). The thread
// predicates keep a placeholder for one spawn from being superseded by a
// different spawn that reused its id in the same chat. xmax = 0 is true only
// for a fresh insert, telling inserted from superseded.
const enqueueSpawnReportSQL = `
INSERT INTO agent_messages (
    id, chat_id, from_thread_id, to_thread_id, kind, body, tool_call_id,
    status, created_at, attachments, synthesized
) VALUES (?0, ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9::jsonb, false)
ON CONFLICT (chat_id, tool_call_id) WHERE kind IN (2, 3, 4) DO UPDATE SET
    id = EXCLUDED.id,
    from_thread_id = EXCLUDED.from_thread_id,
    to_thread_id = EXCLUDED.to_thread_id,
    kind = EXCLUDED.kind,
    body = EXCLUDED.body,
    attachments = EXCLUDED.attachments,
    status = EXCLUDED.status,
    created_at = EXCLUDED.created_at,
    delivered_at = NULL,
    delivered_message_id = NULL,
    synthesized = false
WHERE agent_messages.synthesized
  AND agent_messages.from_thread_id = EXCLUDED.from_thread_id
  AND agent_messages.to_thread_id = EXCLUDED.to_thread_id
RETURNING (xmax = 0) AS inserted`

// EnqueueSpawnReport writes a real terminal spawn report and says what it
// did: inserted it, superseded this spawn's placeholder, or found this
// spawn's report already in the slot (a retry: no error). A slot held by a
// different spawn is core.ErrSpawnReportSlotTaken.
func EnqueueSpawnReport(ctx context.Context, db orm.Context, msg *core.AgentMessage) (core.SpawnReportOutcome, error) {
	args, err := agentMessageInsertArgs(msg)
	if err != nil {
		return 0, err
	}
	var inserted []bool
	if err := db.Bun().NewRaw(enqueueSpawnReportSQL, args[:10]...).Scan(ctx, &inserted); err != nil {
		return 0, err
	}
	switch {
	case len(inserted) == 0:
		if err := requireSlotHeldBySameSpawn(ctx, db, msg); err != nil {
			return 0, err
		}
		return core.SpawnReportAlreadyReported, nil
	case inserted[0]:
		return core.SpawnReportInserted, nil
	default:
		return core.SpawnReportSuperseded, nil
	}
}

// requireSlotHeldBySameSpawn is called when a terminal report could not take
// msg's slot. The report holding it is either this spawn's (a retry, or the
// sweep racing the live report: nothing to do) or a different spawn's that
// reused the tool call id in the same chat, whose report cannot be stored. The
// latter must surface: answering "already reported" is how a sub-agent's
// result used to vanish without a trace.
func requireSlotHeldBySameSpawn(ctx context.Context, db orm.Context, msg *core.AgentMessage) error {
	held := new(AgentMessage)
	err := db.Bun().NewSelect().Model(held).
		Where("chat_id = ?", msg.ChatID).
		Where("tool_call_id = ?", msg.ToolCallID).
		Where("kind IN (2, 3, 4)").
		Scan(ctx)
	if err != nil {
		return fmt.Errorf("read the report holding spawn slot %v: %w", agentMessageToolCallID(msg.ToolCallID), err)
	}
	if held.FromThreadID != msg.FromThreadID || held.ToThreadID != msg.ToThreadID {
		return fmt.Errorf("%w: chat %q, tool call %q is held by the report from thread %q to %q; this one is from %q to %q",
			core.ErrSpawnReportSlotTaken, msg.ChatID, agentMessageToolCallID(held.ToolCallID),
			held.FromThreadID, held.ToThreadID, msg.FromThreadID, msg.ToThreadID)
	}
	return nil
}

// GetAgentMessage returns the row with this id, whatever its status, or
// (nil, nil) when there is none.
func GetAgentMessage(ctx context.Context, db orm.Context, id string) (*core.AgentMessage, error) {
	row, err := GetAgentMessageByID(ctx, db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return agentMessageFromRow(row), nil
}

// DeleteUndeliveredAgentMessage removes the row with this id in this chat
// only if it was never read (its thread's loop exited before a drain reached
// it), and reports whether it did: a repeat of the send that wrote it is a
// fresh send and has to be able to take its id. A queued or delivered row is
// never touched.
func DeleteUndeliveredAgentMessage(ctx context.Context, db orm.Context, id, chatID string) (bool, error) {
	res, err := db.Bun().NewDelete().Model((*AgentMessage)(nil)).
		Where("id = ?", id).
		Where("chat_id = ?", chatID).
		Where("status = ?", core.AgentMessageStatusUndelivered).
		Exec(ctx)
	return rowsTouched(res, err)
}

// ListQueuedAgentMessagesForThread returns the thread's queued messages in
// the order they were sent — delivery order is load-bearing.
func ListQueuedAgentMessagesForThread(ctx context.Context, db orm.Context, toThreadID string) ([]*core.AgentMessage, error) {
	rows, err := db.Query(ctx, `SELECT `+agentMessageColumns+`
FROM agent_messages
WHERE to_thread_id = $1 AND status = 1
ORDER BY created_at ASC`, toThreadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.AgentMessage
	for rows.Next() {
		msg, err := scanAgentMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

// CountQueuedAgentMessagesForThread counts the thread's queued messages.
func CountQueuedAgentMessagesForThread(ctx context.Context, db orm.Context, toThreadID string) (int64, error) {
	var n int64
	err := db.QueryRow(ctx, `SELECT count(*) FROM agent_messages WHERE to_thread_id = $1 AND status = 1`, toThreadID).Scan(&n)
	return n, err
}

// MarkQueuedAgentMessagesPreempting marks every queued message for the thread
// that has not yet cut a waiting tool call short, and reports how many it
// marked. Non-zero is execute_tools' signal to end the thread's preemptable
// calls (ToolDefinition.Preemptable) so the agent reads its mail now.
//
// Marking rather than counting bounds preemption to once per message: a
// message the drain cannot deliver stays queued, and counted it would cut
// every wait short for as long as it stayed there. One statement, so two
// calls polling the same thread cannot both claim one message.
func MarkQueuedAgentMessagesPreempting(ctx context.Context, db orm.Context, toThreadID string, preemptedAt time.Time) (int64, error) {
	res, err := db.Exec(ctx, `UPDATE agent_messages SET preempted_at = $2
WHERE to_thread_id = $1 AND status = 1 AND preempted_at IS NULL`, toThreadID, preemptedAt)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// markAgentMessagesDeliveredSQL moves queued rows to delivered and returns
// the ids it actually moved. Both halves matter: the drain reads its rows
// before it writes them, so two drains racing on one thread can select the
// same rows. The status = 1 guard makes the loser's UPDATE match nothing
// instead of silently re-delivering rows already delivered and repointing
// delivered_message_id at a duplicate envelope; RETURNING lets it SEE that it
// lost, so it abandons its own inserts rather than writing the same messages
// into the transcript twice (specs/interrupt-pause-spec.md).
const markAgentMessagesDeliveredSQL = `
UPDATE agent_messages SET
    status = 2,
    delivered_at = ?0,
    delivered_message_id = ?1
WHERE id = ANY(?2::text[]) AND status = 1
RETURNING id`

// MarkAgentMessagesDelivered claims a batch of queued rows as delivered at
// deliveredAt into deliveredMessageID and returns the ids it moved (nil when
// it moved none). An empty deliveredMessageID claims the rows with
// delivered_message_id left NULL: the column is a foreign key into messages,
// and a claim happens BEFORE the envelope it will point at exists; the caller
// backfills it (SetAgentMessagesDeliveredMessageID) in the same transaction.
func MarkAgentMessagesDelivered(ctx context.Context, db orm.Context, ids []string, deliveredAt time.Time, deliveredMessageID string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var envelope *string
	if deliveredMessageID != "" {
		envelope = &deliveredMessageID
	}
	var claimed []string
	if err := db.Bun().NewRaw(markAgentMessagesDeliveredSQL, deliveredAt, envelope, pgdialect.Array(ids)).Scan(ctx, &claimed); err != nil {
		return nil, err
	}
	if len(claimed) == 0 {
		return nil, nil
	}
	return claimed, nil
}

// SetAgentMessagesDeliveredMessageID backfills the envelope pointer on rows
// this drain already claimed. Separate from the claim because the claim
// decides whether to write an envelope at all, and the envelope's id is only
// known after. Unguarded on status by design: the rows are already delivered
// by the caller's own claim, so the queued guard would match nothing.
func SetAgentMessagesDeliveredMessageID(ctx context.Context, db orm.Context, ids []string, deliveredMessageID string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := db.Bun().NewRaw(`UPDATE agent_messages SET delivered_message_id = ?0 WHERE id = ANY(?1::text[])`,
		deliveredMessageID, pgdialect.Array(ids)).Exec(ctx)
	return err
}

// CancelQueuedAgentMessage deletes the mailbox row (id, chatID) only if it is
// still queued, and reports whether it did. The caller and the agent's own
// drain race for the same row, so the deletion is conditioned on status in
// the statement rather than checked-then-deleted in Go: false, with the row
// untouched, means it was already delivered (or never existed, or belongs to
// another chat).
func CancelQueuedAgentMessage(ctx context.Context, db orm.Context, id, chatID string) (bool, error) {
	res, err := db.Bun().NewDelete().Model((*AgentMessage)(nil)).
		Where("id = ?", id).
		Where("chat_id = ?", chatID).
		Where("status = ?", core.AgentMessageStatusQueued).
		Exec(ctx)
	return rowsTouched(res, err)
}

// ClaimQueuedAgentMessagesForThread atomically takes the thread's still-queued
// HUMAN messages (one, when messageID is set) and returns exactly the rows it
// took, oldest first: the "flush the queue back into the composer" primitive
// behind Send now / Send all. DELETE … RETURNING is one statement, so there
// is no window between deciding to take a row and taking it — a row the drain
// won first is simply absent, and the caller can never resend something the
// agent already received. Kind 5 (human message) scopes it to the user's own
// messages: a peer agent's spawn_send is not the human's to reclaim.
func ClaimQueuedAgentMessagesForThread(ctx context.Context, db orm.Context, toThreadID, chatID, messageID string) ([]*core.AgentMessage, error) {
	q := db.Bun().NewDelete().Model((*AgentMessage)(nil)).
		Where("to_thread_id = ?", toThreadID).
		Where("chat_id = ?", chatID).
		Where("status = ?", core.AgentMessageStatusQueued).
		Where("kind = ?", core.AgentMessageKindHumanMessage)
	if messageID != "" {
		q = q.Where("id = ?", messageID)
	}
	var rows []*AgentMessage
	if _, err := q.Returning("?TableColumns").Exec(ctx, &rows); err != nil {
		return nil, err
	}
	claimed := make([]*core.AgentMessage, len(rows))
	for i, row := range rows {
		claimed[i] = agentMessageFromRow(row)
	}
	// DELETE … RETURNING makes no ordering promise. The caller resends these
	// as conversation turns, where order is the meaning.
	sort.SliceStable(claimed, func(i, j int) bool { return claimed[i].CreatedAt.Before(claimed[j].CreatedAt) })
	return claimed, nil
}

// MarkQueuedAgentMessagesUndeliveredForThread resolves a dead thread's
// mailbox: every row still queued for a thread whose loop has exited becomes
// undelivered, because no future loop boundary will drain it. Delivery
// happens only at a loop-step boundary, so a message queued into a running
// thread whose loop then exits first is undeliverable by construction — a
// race no enqueue-time check can close. Guarded on queued, which makes it
// idempotent and raceless where it matters: it can overlap the thread's own
// final drain, and a row that drain delivered must never be relabelled.
// delivered_at stays NULL: its absence is the record that no delivery
// occurred. Returns how many rows it moved.
//
// A thread with a pending blocking question is not dead, whatever its status:
// a failed run is resumed from that question (question_close.go — FAILED keeps
// its questions answerable), and the call_llm after the answer drains the
// mailbox. The answer to an async question is queued there on purpose while
// its thread is parked on a blocking one (question/async_answer.go), so such a
// mailbox is left alone. A completed or cancelled thread has no pending
// blocking question — UpdateThreadStatus closes them in the same write — so
// its mail is still resolved.
func MarkQueuedAgentMessagesUndeliveredForThread(ctx context.Context, db orm.Context, toThreadID string) (int64, error) {
	res, err := db.Bun().NewUpdate().Model((*AgentMessage)(nil)).
		Set("status = ?", core.AgentMessageStatusUndelivered).
		Where("to_thread_id = ?", toThreadID).
		Where("status = ?", core.AgentMessageStatusQueued).
		Where(threadHasNoPendingBlockingQuestion, toThreadID).
		Exec(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ListThreadsWithOrphanedAgentMessages returns every thread that is ALREADY
// terminal but still has queued mailbox rows: the backlog the live path
// above missed (rows from before it existed, or a process that died between
// writing the terminal status and resolving the mailbox). Fails CLOSED: a
// thread still running or in any non-terminal state is never returned, so a
// message waiting for a live agent's next boundary can never be swept.
// Terminal thread statuses: 3 completed, 4 failed, 5 cancelled, 7 the retired
// expired (core.ThreadStatusIsTerminal).
//
// A thread parked on a pending blocking question is not orphaned (see
// MarkQueuedAgentMessagesUndeliveredForThread): its question resumes it.
func ListThreadsWithOrphanedAgentMessages(ctx context.Context, db orm.Context) ([]string, error) {
	var ids []string
	err := db.Bun().NewRaw(`
SELECT DISTINCT m.to_thread_id
FROM agent_messages m
JOIN threads t ON t.id = m.to_thread_id
WHERE m.status = 1
  AND t.status IN (3, 4, 5, 7)
  AND NOT EXISTS (
      SELECT 1 FROM questions q
      WHERE q.thread_id = t.id AND q.status = 1 AND NOT q.async)`).Scan(ctx, &ids)
	return ids, err
}

// threadHasNoPendingBlockingQuestion is the WHERE clause, over a thread id,
// that keeps a parked thread's mailbox (MarkQueuedAgentMessagesUndeliveredForThread).
const threadHasNoPendingBlockingQuestion = `NOT EXISTS (
    SELECT 1 FROM questions q
    WHERE q.thread_id = ? AND q.status = 1 AND NOT q.async)`

// scanAgentMessage reads one row of agentMessageColumns.
func scanAgentMessage(rows *sql.Rows) (*core.AgentMessage, error) {
	var (
		msg         core.AgentMessage
		attachments []byte
	)
	if err := rows.Scan(&msg.ID, &msg.ChatID, &msg.FromThreadID, &msg.ToThreadID, &msg.Kind, &msg.Body, &msg.ToolCallID,
		&msg.Status, &msg.CreatedAt, &msg.DeliveredAt, &msg.DeliveredMessageID, &attachments, &msg.Synthesized,
		&msg.Subject); err != nil {
		return nil, err
	}
	msg.Attachments = agentMessageAttachmentIDs(attachments)
	return &msg, nil
}

func agentMessageFromRow(row *AgentMessage) *core.AgentMessage {
	return &core.AgentMessage{
		ID:                 row.ID,
		ChatID:             row.ChatID,
		FromThreadID:       row.FromThreadID,
		ToThreadID:         row.ToThreadID,
		Kind:               row.Kind,
		Body:               row.Body,
		Attachments:        agentMessageAttachmentIDs(row.Attachments),
		ToolCallID:         row.ToolCallID,
		Status:             row.Status,
		CreatedAt:          row.CreatedAt,
		DeliveredAt:        row.DeliveredAt,
		DeliveredMessageID: row.DeliveredMessageID,
		Synthesized:        row.Synthesized,
		Subject:            row.Subject,
	}
}

// agentMessageAttachments encodes attachment ids for the jsonb column. The
// column is read and written whole, never queried by element, so jsonb needs
// no array support; no attachments is NULL rather than "[]", which every
// reader takes as none.
func agentMessageAttachments(ids []string) (*string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("encode agent message attachments: %w", err)
	}
	s := string(encoded)
	return &s, nil
}

func agentMessageAttachmentIDs(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil
	}
	return ids
}

func agentMessageToolCallID(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

// rowsTouched reports whether a guarded write matched a row.
func rowsTouched(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// AgentMessageExtra carries fields on AgentMessage that are NOT columns — computed
// values, transient state, anything derived rather than stored. AgentMessage is
// embedded on AgentMessageExtra (see agent_message_orm.go), so a field added here is a field on
// every AgentMessage value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type AgentMessageExtra struct {
}

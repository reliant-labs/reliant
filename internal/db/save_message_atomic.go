// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lib/pq"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// AtomicMessageWrite is everything one SaveMessage persists: the message row,
// its content blocks, and the chat_update that announces it.
//
// Ordinal and Seq are deliberately ABSENT — they are allocated by the database
// as part of the write and returned. A caller cannot supply them, which is what
// makes the read-then-insert race structurally impossible rather than merely
// avoided by convention.
type AtomicMessageWrite struct {
	MessageID       string
	ChatID          string
	ThreadID        string
	ContextWindowID string
	Role            reliantv1.MessageRole
	DisplayStyle    *reliantv1.DisplayStyle
	Model           *string
	Agent           *string
	TokenCount      *int
	Cost            *float64
	WorkflowID      *string
	RunID           *string
	NodeID          *string
	NodePath        *string
	ActivityID      *string
	CreatedAt       time.Time

	Blocks []MessageContentBlock

	// ChatUpdateData is the chat_update payload. It is built by the caller
	// EXCEPT for ordinal and seq, which do not exist until this statement
	// runs; BuildChatUpdateData patches them in from the RETURNING values.
	ChatUpdateData   func(ordinal, seq, updateSeq int64) (string, error)
	ChatUpdateType   reliantv1.ChatUpdateType
	ChatUpdateEntity string
}

// AtomicMessageResult reports the numbers the database assigned.
type AtomicMessageResult struct {
	Ordinal         int64
	Seq             int64
	UpdateSeq       int64
	ChatUpdate      ChatUpdate
	ChatUpdateBytes string
}

// saveMessageAtomicSQL writes a message, its content blocks and its
// chat_update in ONE statement.
//
// # Why one statement
//
// This replaces a SERIALIZABLE transaction that made 5+N network round trips:
// MAX(ordinal) scan, a WITH RECURSIVE fork-chain walk for seq, the message
// insert, one insert PER content block in a Go loop, then the counter upsert
// and ledger insert. Locks were held across every one of those round trips, so
// a transaction doing microseconds of real work stayed open for seconds — and
// the longer it was open, the wider the window for another writer on the same
// chat to collide. Measured on one dev session: 881 SQLSTATE 40001 conflicts,
// 35 of which exhausted all 7 retry attempts and surfaced to users as errors.
//
// Collapsing to a single statement means the locks are held for the duration of
// one statement. unnest() is what removes the per-block round trips: N content
// blocks are passed as parallel arrays and inserted in one go.
//
// # Why the counters
//
// ordinal and seq come from message_order_counters rather than MAX()+1. A
// MAX() read followed by an INSERT into the range just read takes an SSI
// predicate lock over that range, so concurrent savers abort each other. An
// upsert on a single counter row takes a row lock instead: a concurrent writer
// WAITS microseconds rather than aborting. See migration
// 20260901000000_message_order_counters.sql.
//
// # Why this is safe at READ COMMITTED
//
// Every allocation here is an atomic upsert-returning on ONE row. At READ
// COMMITTED a concurrent writer blocks on the row lock and then re-reads the
// committed row (EvalPlanQual), so it observes N and takes N+1 — no lost
// update, and no 40001. The row lock is still held until COMMIT, so sequence
// order still cannot diverge from commit order: the client's `WHERE
// sequence_number > cursor` resume contract is preserved exactly.
//
// What READ COMMITTED gives up is cross-statement stability, and this
// transaction has exactly one statement, so "between statements" has no
// meaning here.
const saveMessageAtomicSQL = `
WITH
-- Both allocators are SELF-HEALING: they take the greater of the counter's
-- next value and the table's actual high-water mark.
--
-- This is not belt-and-braces, it is required. Three production paths still
-- insert messages directly with their own allocation — the branch repair in
-- chat_branch.go, the orphan repair in cleanup.go, and the denial message in
-- approval.go. A row written by any of them advances the table past the
-- counter, and a counter that then handed out its own stale next value would
-- collide with messages_chat_seq_key (observed: SQLSTATE 23505 under exactly
-- this mix). Healing here means those writers stay correct without every one
-- of them having to know the counter exists.
--
-- The subquery is an indexed MAX on a single chat/thread, not the recursive
-- fork walk this replaced, and it is evaluated once per write rather than
-- scanning a range the insert then lands in — so it does not reintroduce the
-- SSI predicate conflict that motivated the change.
ord AS (
    INSERT INTO message_order_counters (counter_kind, scope_id, last_assigned)
    VALUES ('ordinal', $1, COALESCE((SELECT MAX(ordinal) FROM messages WHERE thread_id = $1), -1) + 1)
    ON CONFLICT (counter_kind, scope_id) DO UPDATE
    SET last_assigned = GREATEST(
            message_order_counters.last_assigned + 1,
            COALESCE((SELECT MAX(ordinal) FROM messages WHERE thread_id = $1), -1) + 1
        )
    RETURNING last_assigned
),
sq AS (
    INSERT INTO message_order_counters (counter_kind, scope_id, last_assigned)
    VALUES ('seq', $2, COALESCE((SELECT MAX(seq) FROM messages WHERE chat_id = $2), -1) + 1)
    ON CONFLICT (counter_kind, scope_id) DO UPDATE
    SET last_assigned = GREATEST(
            message_order_counters.last_assigned + 1,
            COALESCE((SELECT MAX(seq) FROM messages WHERE chat_id = $2), -1) + 1
        )
    RETURNING last_assigned
),
upd AS (
    INSERT INTO update_stream_counters (stream_kind, stream_id, last_assigned_seq)
    VALUES ('chat', $2, 1)
    ON CONFLICT (stream_kind, stream_id) DO UPDATE
    SET last_assigned_seq = update_stream_counters.last_assigned_seq + 1
    RETURNING last_assigned_seq
),
msg AS (
    INSERT INTO messages (
        id, chat_id, ordinal, seq, thread_id, context_window_id,
        node_id, node_path, role, display_style, model, agent,
        token_count, cost, workflow_id, run_id, activity_id,
        created_at, updated_at
    )
    SELECT
        $3, $2, ord.last_assigned, sq.last_assigned, $1, $4,
        $5, $6, $7, $8, $9, $10,
        $11, $12, $13, $14, $15,
        $16, $16
    FROM ord, sq
),
blocks AS (
    INSERT INTO message_content_blocks (
        id, message_id, position, block_type, content,
        tool_name, tool_input, tool_call_id, thought_signature, is_error,
        version, activity_id, workflow_run_id, attempt_number,
        created_at, updated_at
    )
    SELECT
        b.id, $3, b.position, b.block_type, b.content,
        b.tool_name, b.tool_input, b.tool_call_id, b.thought_signature, b.is_error,
        b.version, b.activity_id, b.workflow_run_id, b.attempt_number,
        $16, $16
    FROM unnest(
        $17::text[], $18::bigint[], $19::int[], $20::text[],
        $21::text[], $22::text[], $23::text[], $24::text[], $25::boolean[],
        $26::bigint[], $27::text[], $28::text[], $29::bigint[]
    ) AS b(
        id, position, block_type, content,
        tool_name, tool_input, tool_call_id, thought_signature, is_error,
        version, activity_id, workflow_run_id, attempt_number
    )
)
SELECT ord.last_assigned, sq.last_assigned, upd.last_assigned_seq
FROM ord, sq, upd
`

// insertChatUpdateSQL writes the ledger row using the sequence already
// allocated by saveMessageAtomicSQL.
//
// This is a second statement only because its JSON payload embeds the ordinal
// and seq that the first statement allocates — the value cannot be built until
// those numbers exist. It is a plain INSERT with no reads, so it takes no
// predicate locks and cannot raise a serialization conflict.
const insertChatUpdateSQL = `
INSERT INTO chat_updates (
    id, chat_id, sequence_number, update_type, entity_id, data, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7)
`

// SaveMessageAtomic performs the whole message write in one transaction whose
// body is a single round trip for the durable rows.
//
// It runs at READ COMMITTED deliberately. See saveMessageAtomicSQL for why
// that is safe here and what it buys: this write path is the hottest
// SERIALIZABLE transaction in the system and was responsible for the
// overwhelming majority of observed 40001 conflicts.
func (r *Repo) SaveMessageAtomic(ctx context.Context, w AtomicMessageWrite) (*AtomicMessageResult, error) {
	if w.MessageID == "" {
		return nil, fmt.Errorf("message ID is required")
	}
	if w.ChatID == "" {
		return nil, fmt.Errorf("chat ID is required")
	}
	if w.ThreadID == "" {
		return nil, fmt.Errorf("thread ID is required")
	}

	cols := newBlockColumns(w.Blocks)

	var result AtomicMessageResult
	err := r.RunTxWithOptions(ctx, TxOptions{Isolation: IsolationReadCommitted}, func(txCtx context.Context) error {
		row := r.DB.DB(txCtx).QueryRowContext(txCtx, saveMessageAtomicSQL,
			w.ThreadID,
			w.ChatID,
			w.MessageID,
			w.ContextWindowID,
			nullString(w.NodeID),
			nullString(w.NodePath),
			int32(w.Role),
			nullDisplayStyle(w.DisplayStyle),
			nullString(w.Model),
			nullString(w.Agent),
			nullInt(w.TokenCount),
			nullFloat(w.Cost),
			nullString(w.WorkflowID),
			nullString(w.RunID),
			nullString(w.ActivityID),
			w.CreatedAt,
			cols.ids, cols.positions, cols.blockTypes, cols.contents,
			cols.toolNames, cols.toolInputs, cols.toolCallIDs, cols.thoughtSignatures, cols.isErrors,
			cols.versions, cols.activityIDs, cols.workflowRunIDs, cols.attemptNumbers,
		)

		if err := row.Scan(&result.Ordinal, &result.Seq, &result.UpdateSeq); err != nil {
			return fmt.Errorf("failed to write message: %w", err)
		}

		data, err := w.ChatUpdateData(result.Ordinal, result.Seq, result.UpdateSeq)
		if err != nil {
			return fmt.Errorf("failed to build chat_update data: %w", err)
		}

		updateID := fmt.Sprintf("%s-%d", w.ChatID, result.UpdateSeq)
		// UTC, not local: chat_updates.created_at drops the offset and keeps
		// the wall clock, so a local time.Now() is stored as its local reading
		// and read back as RFC3339 with a "Z" that lies about the instant.
		createdAt := time.Now().UTC()

		if _, err := r.DB.DB(txCtx).ExecContext(txCtx, insertChatUpdateSQL,
			updateID, w.ChatID, result.UpdateSeq, int32(w.ChatUpdateType),
			w.ChatUpdateEntity, data, createdAt,
		); err != nil {
			return fmt.Errorf("failed to create chat_update: %w", err)
		}

		result.ChatUpdate = ChatUpdate{
			ID:             updateID,
			ChatID:         w.ChatID,
			SequenceNumber: result.UpdateSeq,
			UpdateType:     w.ChatUpdateType,
			EntityID:       w.ChatUpdateEntity,
			Data:           json.RawMessage(data),
			CreatedAt:      createdAt,
		}
		result.ChatUpdateBytes = data

		// Publish only after the transaction commits. Notifying from inside
		// would announce a sequence number a reader could not yet SELECT, and
		// on rollback would announce one that never existed at all.
		if r.onChatUpdate != nil {
			committed := result.ChatUpdate
			chatID := w.ChatID
			if err := runAfterCommit(txCtx, func() {
				r.onChatUpdate(chatID, committed.SequenceNumber, committed)
			}); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return &result, nil
}

// blockColumns is the column-major form of []MessageContentBlock that unnest()
// consumes: N blocks become one array per column rather than N inserts.
type blockColumns struct {
	ids               pq.StringArray
	positions         pq.Int64Array
	blockTypes        pq.Int32Array
	contents          pq.StringArray
	toolNames         pq.StringArray
	toolInputs        pq.StringArray
	toolCallIDs       pq.StringArray
	thoughtSignatures pq.StringArray
	isErrors          pq.BoolArray
	versions          pq.Int64Array
	activityIDs       pq.StringArray
	workflowRunIDs    pq.StringArray
	attemptNumbers    pq.Int64Array
}

func newBlockColumns(blocks []MessageContentBlock) blockColumns {
	n := len(blocks)
	c := blockColumns{
		ids:               make(pq.StringArray, n),
		positions:         make(pq.Int64Array, n),
		blockTypes:        make(pq.Int32Array, n),
		contents:          make(pq.StringArray, n),
		toolNames:         make(pq.StringArray, n),
		toolInputs:        make(pq.StringArray, n),
		toolCallIDs:       make(pq.StringArray, n),
		thoughtSignatures: make(pq.StringArray, n),
		isErrors:          make(pq.BoolArray, n),
		versions:          make(pq.Int64Array, n),
		activityIDs:       make(pq.StringArray, n),
		workflowRunIDs:    make(pq.StringArray, n),
		attemptNumbers:    make(pq.Int64Array, n),
	}
	for i, b := range blocks {
		c.ids[i] = b.ID
		c.positions[i] = int64(b.Position)
		c.blockTypes[i] = int32(b.BlockType)
		c.contents[i] = derefString(b.Content)
		c.toolNames[i] = derefString(b.ToolName)
		c.toolInputs[i] = derefString(b.ToolInput)
		c.toolCallIDs[i] = derefString(b.ToolCallID)
		c.thoughtSignatures[i] = derefString(b.ThoughtSignature)
		c.isErrors[i] = b.IsError != nil && *b.IsError
		c.versions[i] = int64(derefInt(b.Version))
		c.activityIDs[i] = derefString(b.ActivityID)
		c.workflowRunIDs[i] = derefString(b.WorkflowRunID)
		c.attemptNumbers[i] = int64(b.AttemptNumber)
	}
	return c
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefInt(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}

func nullString(s *string) sql.NullString {
	if s == nil || *s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: *s, Valid: true}
}

func nullInt(i *int) sql.NullInt64 {
	if i == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*i), Valid: true}
}

func nullFloat(f *float64) sql.NullFloat64 {
	if f == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *f, Valid: true}
}

func nullDisplayStyle(d *reliantv1.DisplayStyle) sql.NullInt32 {
	if d == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: int32(*d), Valid: true}
}

// Copyright (c) 2025 Reliant Labs
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
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

	// ProviderAccountID is the provider account that served an assistant
	// message (messages.provider_account_id); nil for every other message.
	ProviderAccountID *string

	// SenderThreadID and SenderKind (messages.sender_thread_id/sender_kind)
	// attribute a message another agent wrote; nil for the human and for the
	// model's own output.
	SenderThreadID *string
	SenderKind     *reliantv1.AgentMessageKind
	// SenderSubject (messages.sender_subject) is the sending agent's one-line
	// subject; nil when it gave none.
	SenderSubject *string

	Blocks []core.MessageContentBlock

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
	ChatUpdate      core.ChatUpdate
	ChatUpdateBytes string
}

// saveMessageAtomicSQL writes a message and its content blocks, and allocates
// the message's ordinal, seq and chat update sequence, in ONE statement.
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
// blocks are passed as parallel arrays and inserted in one go. It stays hand
// SQL in the ORM port for the same reason: no builder chain expresses "one
// statement holding row locks for one statement" more clearly than the SQL.
//
// # Why the counters
//
// ordinal and seq come from message_order_counters (messageOrderCTEs), never
// from MAX()+1. A MAX() read followed by an INSERT into the range just read
// takes an SSI predicate lock over that range, so concurrent savers abort each
// other. An upsert on a single counter row takes a row lock instead: a
// concurrent writer WAITS microseconds rather than aborting. The counters are
// authoritative: every other message writer allocates from them too, or
// raises them to the numbers it wrote (InsertMessage).
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
// What READ COMMITTED gives up is cross-statement stability, and the only
// other statement in the transaction is the ledger insert, which reads
// nothing.
//
// It runs through orm.Context's raw passthrough with real bind parameters
// ($N), not through bun's NewRaw, which would format every value — the ids,
// the block contents — into the text: a new statement for every message,
// parsed and planned on every save, where this text is prepared once per
// connection and reused. On the hottest write path that difference measured
// +40% per save (P1 benchmark, research/forge-migration/08-p5b-spike.md).
const saveMessageAtomicSQL = `
WITH
` + messageOrderCTEs + `,
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
        created_at, updated_at, provider_account_id,
        sender_thread_id, sender_kind, sender_subject
    )
    SELECT
        $3, $2, ord.last_assigned, sq.last_assigned, $1, $4,
        $5, $6, $7, $8, $9, $10,
        $11, $12, $13, $14, $15,
        $16, $16, $31,
        $32, $33, $34
    FROM ord, sq
),
blocks AS (
    INSERT INTO message_content_blocks (
        id, message_id, position, block_type, content,
        tool_name, tool_input, tool_call_id, thought_signature, is_error,
        version, activity_id, workflow_run_id, attempt_number,
        phase,
        created_at, updated_at
    )
    SELECT
        b.id, $3, b.position, b.block_type, b.content,
        b.tool_name, b.tool_input, b.tool_call_id, b.thought_signature, b.is_error,
        b.version, b.activity_id, b.workflow_run_id, b.attempt_number,
        -- NULLIF: the column-major arrays carry "" for an absent value (see
        -- blockColumns), but phase is an enum where "absent" and "empty" are
        -- different answers — a stored "" would be resent to the provider as
        -- an invalid phase. Only this column needs it; the rest are free text.
        NULLIF(b.phase, ''),
        $16, $16
    FROM unnest(
        $17::text[], $18::bigint[], $19::int[], $20::text[],
        $21::text[], $22::text[], $23::text[], $24::text[], $25::boolean[],
        $26::bigint[], $27::text[], $28::text[], $29::bigint[], $30::text[]
    ) AS b(
        id, position, block_type, content,
        tool_name, tool_input, tool_call_id, thought_signature, is_error,
        version, activity_id, workflow_run_id, attempt_number, phase
    )
)
SELECT ord.last_assigned, sq.last_assigned, upd.last_assigned_seq
FROM ord, sq, upd
`

// SaveMessageAtomic performs the whole message write: the durable rows in one
// round trip (saveMessageAtomicSQL), then the chat_update ledger row through
// the generated CreateChatUpdate. The ledger insert is a second statement only
// because its payload embeds the ordinal and seq the first one allocates; it
// is a plain INSERT with no reads, so it takes no predicate locks and cannot
// raise a serialization conflict.
//
// It runs in a READ COMMITTED transaction (see saveMessageAtomicSQL for why
// that is safe), or joins the one ctx carries. The block contents of w are
// made valid UTF-8 in place. Publishing the chat_update is the caller's, after
// the commit: a notification from inside would announce a sequence number a
// reader could not yet SELECT.
func SaveMessageAtomic(ctx context.Context, db orm.Context, w AtomicMessageWrite) (*AtomicMessageResult, error) {
	if w.MessageID == "" {
		return nil, errors.New("message ID is required")
	}
	if w.ChatID == "" {
		return nil, errors.New("chat ID is required")
	}
	if w.ThreadID == "" {
		return nil, errors.New("thread ID is required")
	}
	for i := range w.Blocks {
		NormalizeContentBlock(&w.Blocks[i])
	}
	cols := newBlockColumns(w.Blocks)

	var result AtomicMessageResult
	err := db.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts), func(txCtx context.Context) error {
		result = AtomicMessageResult{}
		err := db.QueryRow(txCtx, saveMessageAtomicSQL,
			w.ThreadID,
			w.ChatID,
			w.MessageID,
			w.ContextWindowID,
			emptyNilPtr(w.NodeID),
			emptyNilPtr(w.NodePath),
			int32(w.Role),
			displayStyleValue(w.DisplayStyle),
			emptyNilPtr(w.Model),
			emptyNilPtr(w.Agent),
			intToInt64Ptr(w.TokenCount),
			w.Cost,
			emptyNilPtr(w.WorkflowID),
			emptyNilPtr(w.RunID),
			emptyNilPtr(w.ActivityID),
			w.CreatedAt,
			cols.ids, cols.positions, cols.blockTypes, cols.contents,
			cols.toolNames, cols.toolInputs, cols.toolCallIDs,
			cols.thoughtSignatures, cols.isErrors,
			cols.versions, cols.activityIDs, cols.workflowRunIDs,
			cols.attemptNumbers, cols.phases,
			emptyNilPtr(w.ProviderAccountID),
			emptyNilPtr(w.SenderThreadID),
			senderKindValue(w.SenderKind),
			emptyNilPtr(w.SenderSubject),
		).Scan(&result.Ordinal, &result.Seq, &result.UpdateSeq)
		if err != nil {
			return fmt.Errorf("failed to write message: %w", err)
		}

		data, err := w.ChatUpdateData(result.Ordinal, result.Seq, result.UpdateSeq)
		if err != nil {
			return fmt.Errorf("failed to build chat_update data: %w", err)
		}
		update := &ChatUpdate{
			ID:             fmt.Sprintf("%s-%d", w.ChatID, result.UpdateSeq),
			ChatID:         w.ChatID,
			SequenceNumber: result.UpdateSeq,
			UpdateType:     w.ChatUpdateType,
			EntityID:       w.ChatUpdateEntity,
			Data:           data,
			CreatedAt:      time.Now().UTC(),
		}
		if err := CreateChatUpdate(txCtx, db, update); err != nil {
			return fmt.Errorf("failed to create chat_update: %w", err)
		}
		result.ChatUpdate = core.ChatUpdate{
			ID:             update.ID,
			ChatID:         update.ChatID,
			SequenceNumber: update.SequenceNumber,
			UpdateType:     update.UpdateType,
			EntityID:       update.EntityID,
			Data:           json.RawMessage(data),
			CreatedAt:      update.CreatedAt,
		}
		result.ChatUpdateBytes = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// blockColumns is the column-major form of []core.MessageContentBlock that
// unnest() consumes: N blocks become one array per column rather than N
// inserts. An absent text value is "" (see the NULLIF on phase).
type blockColumns struct {
	ids, contents, toolNames, toolInputs, toolCallIDs, thoughtSignatures []string
	activityIDs, workflowRunIDs, phases                                  []string
	positions, versions, attemptNumbers                                  []int64
	blockTypes                                                           []int32
	isErrors                                                             []bool
}

func newBlockColumns(blocks []core.MessageContentBlock) blockColumns {
	n := len(blocks)
	c := blockColumns{
		ids: make([]string, n), contents: make([]string, n), toolNames: make([]string, n),
		toolInputs: make([]string, n), toolCallIDs: make([]string, n), thoughtSignatures: make([]string, n),
		activityIDs: make([]string, n), workflowRunIDs: make([]string, n), phases: make([]string, n),
		positions: make([]int64, n), versions: make([]int64, n), attemptNumbers: make([]int64, n),
		blockTypes: make([]int32, n), isErrors: make([]bool, n),
	}
	for i, b := range blocks {
		c.ids[i] = b.ID
		c.positions[i] = int64(b.Position)
		c.blockTypes[i] = int32(b.BlockType)
		c.contents[i] = deref(b.Content)
		c.toolNames[i] = deref(b.ToolName)
		c.toolInputs[i] = deref(b.ToolInput)
		c.toolCallIDs[i] = deref(b.ToolCallID)
		c.thoughtSignatures[i] = deref(b.ThoughtSignature)
		c.isErrors[i] = b.IsError != nil && *b.IsError
		if b.Version != nil {
			c.versions[i] = int64(*b.Version)
		}
		c.activityIDs[i] = deref(b.ActivityID)
		c.workflowRunIDs[i] = deref(b.WorkflowRunID)
		c.attemptNumbers[i] = int64(b.AttemptNumber)
		c.phases[i] = deref(b.Phase)
	}
	return c
}

// senderKindValue is the sender_kind column value: NULL, or the enum's number.
func senderKindValue(k *reliantv1.AgentMessageKind) *int32 {
	if k == nil {
		return nil
	}
	v := int32(*k)
	return &v
}

// displayStyleValue is the display_style column value: NULL, or the enum's
// number.
func displayStyleValue(d *reliantv1.DisplayStyle) *int32 {
	if d == nil {
		return nil
	}
	v := int32(*d)
	return &v
}

// emptyNilPtr maps nil and "" to SQL NULL, and makes a value valid UTF-8.
func emptyNilPtr(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return validUTF8Ptr(s)
}

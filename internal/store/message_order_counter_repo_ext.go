// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for MessageOrderCounter: the allocators of
// every number a conversation is ordered by, keyed by (counter_kind,
// scope_id):
//
//	'ordinal'        a message's position in its thread  (scope: thread id)
//	'seq'            a message's position in its chat    (scope: chat id)
//	'context_window' a context window's sequence         (scope: thread id)
//
// The counters are the ONLY source of these numbers. Nothing numbers a row by
// reading the highest number already written: MAX()+1 hands two concurrent
// writers the same number (one then fails the unique key), and under
// SERIALIZABLE its range read is a predicate lock that aborts writers that
// share nothing but an index page. An allocation is an upsert on one counter
// row instead, so a concurrent writer waits on the row lock and takes the next
// number. Numbers are unique and increase in commit order; a rolled-back or
// abandoned allocation leaves a gap, and nothing reads them as dense.
//
// Every row that carries one of these numbers raises its counter as it is
// written (InsertMessage, InsertContextWindow, SeedSeqCounterForFork), so a
// counter is never behind a row, whoever chose the row's number.
//
// Read a counter with the generated GetMessageOrderCounterByPK.
package store

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"
)

// Counter kinds (message_order_counters_kind_check).
const (
	CounterOrdinal       = "ordinal"
	CounterSeq           = "seq"
	CounterContextWindow = "context_window"
)

// messageOrderCTEs allocate a message's ordinal ($1 = thread id) and seq ($2
// = chat id) as the CTEs `ord` and `sq`, each RETURNING last_assigned. A
// scope's first number is 0. SaveMessageAtomic's one statement and
// AllocateMessageOrder share them.
const messageOrderCTEs = `
ord AS (
    INSERT INTO message_order_counters (counter_kind, scope_id, last_assigned)
    VALUES ('ordinal', $1, 0)
    ON CONFLICT (counter_kind, scope_id) DO UPDATE
    SET last_assigned = message_order_counters.last_assigned + 1
    RETURNING last_assigned
),
sq AS (
    INSERT INTO message_order_counters (counter_kind, scope_id, last_assigned)
    VALUES ('seq', $2, 0)
    ON CONFLICT (counter_kind, scope_id) DO UPDATE
    SET last_assigned = message_order_counters.last_assigned + 1
    RETURNING last_assigned
)`

const allocateMessageOrderSQL = `
WITH
` + messageOrderCTEs + `
SELECT ord.last_assigned, sq.last_assigned FROM ord, sq
`

// AllocateMessageOrder takes the next ordinal of threadID and the next seq of
// chatID, for a message the caller writes in the same transaction: the
// counter rows stay locked until it commits, so numbers increase in commit
// order, and a rollback hands them back.
func AllocateMessageOrder(ctx context.Context, db orm.Context, threadID, chatID string) (ordinal, seq int64, err error) {
	if err := db.QueryRow(ctx, allocateMessageOrderSQL, threadID, chatID).Scan(&ordinal, &seq); err != nil {
		return 0, 0, fmt.Errorf("allocate message order: %w", err)
	}
	return ordinal, seq, nil
}

// raiseCounterSQL lifts one counter to at least a number a row was just
// written with. GREATEST, never an assignment: a counter that is ahead stays
// ahead, and lowering one would hand out a number already in use.
const raiseCounterSQL = `
INSERT INTO message_order_counters (counter_kind, scope_id, last_assigned)
VALUES ($1, $2, $3)
ON CONFLICT (counter_kind, scope_id) DO UPDATE
SET last_assigned = GREATEST(message_order_counters.last_assigned, EXCLUDED.last_assigned)
`

func raiseCounter(ctx context.Context, db orm.Context, kind, scopeID string, to int64) error {
	if _, err := db.Exec(ctx, raiseCounterSQL, kind, scopeID, to); err != nil {
		return fmt.Errorf("raise %s counter: %w", kind, err)
	}
	return nil
}

// allocateContextWindowSequenceSQL takes a thread's next window sequence. A
// thread's first window (its creation, or its fork) raises the counter to that
// window's sequence (InsertContextWindow), so the row exists for every thread
// that has a window; one without it has no window to follow.
const allocateContextWindowSequenceSQL = `
UPDATE message_order_counters
SET last_assigned = last_assigned + 1
WHERE counter_kind = 'context_window' AND scope_id = $1
RETURNING last_assigned
`

// AllocateContextWindowSequence takes the sequence of threadID's next context
// window (a compaction boundary). It refuses a thread with no window yet.
func AllocateContextWindowSequence(ctx context.Context, db orm.Context, threadID string) (int64, error) {
	var seq int64
	if err := db.QueryRow(ctx, allocateContextWindowSequenceSQL, threadID).Scan(&seq); err != nil {
		return 0, fmt.Errorf("allocate context window sequence of thread %s: %w", threadID, err)
	}
	return seq, nil
}

// seedSeqCounterFromParentSQL raises a branch's seq counter ($1) to the
// highest seq counter of the chats that own the windows of its fork chain,
// from the window $2 up through its parents.
//
// A branched chat displays its parent's messages but does not own those rows —
// they keep the parent's chat_id and are resolved on read by walking the
// window chain. So a fresh branch's own counter would start at 0, placing
// every message the user sends numerically BENEATH the inherited messages
// rendered above it. A parent's counter is at least every seq the parent
// shows (its own fork seeded it the same way), so the chain's counters cover
// everything the branch inherits. It walks windows and reads counters; it
// reads no message.
const seedSeqCounterFromParentSQL = `
WITH RECURSIVE chain AS (
    SELECT cw.id, cw.parent_context_window_id, cw.thread_id
    FROM context_windows cw
    WHERE cw.id = $2
    UNION
    SELECT parent.id, parent.parent_context_window_id, parent.thread_id
    FROM context_windows parent
    JOIN chain ON chain.parent_context_window_id = parent.id
)
INSERT INTO message_order_counters (counter_kind, scope_id, last_assigned)
SELECT 'seq', $1, MAX(c.last_assigned)
FROM chain
JOIN threads t ON t.id = chain.thread_id
JOIN message_order_counters c ON c.counter_kind = 'seq' AND c.scope_id = t.chat_id
HAVING MAX(c.last_assigned) IS NOT NULL
ON CONFLICT (counter_kind, scope_id) DO UPDATE
SET last_assigned = GREATEST(message_order_counters.last_assigned, EXCLUDED.last_assigned)
`

// SeedSeqCounterForFork raises chatID's seq counter to cover the history it
// inherits through forkAtContextWindowID (seedSeqCounterFromParentSQL). Safe
// to repeat: GREATEST keeps it monotonic, so a retry or a duplicate fork
// cannot move the counter backwards.
func SeedSeqCounterForFork(ctx context.Context, db orm.Context, chatID, forkAtContextWindowID string) error {
	if _, err := db.Exec(ctx, seedSeqCounterFromParentSQL, chatID, forkAtContextWindowID); err != nil {
		return fmt.Errorf("failed to seed seq counter for fork: %w", err)
	}
	return nil
}

// MessageOrderCounterExtra carries fields on MessageOrderCounter that are NOT columns — computed
// values, transient state, anything derived rather than stored. MessageOrderCounter is
// embedded on MessageOrderCounterExtra (see message_order_counter_orm.go), so a field added here is a field on
// every MessageOrderCounter value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type MessageOrderCounterExtra struct {
}

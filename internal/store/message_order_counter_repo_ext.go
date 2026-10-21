// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for MessageOrderCounter: the per-thread
// ordinal and per-chat seq allocators (counter_kind 'ordinal' / 'seq',
// keyed by (counter_kind, scope_id)). SaveMessageAtomic (save_message_atomic.go)
// allocates from them inside its one statement; a fork seeds them here. Read
// one with the generated GetMessageOrderCounterByPK.
package store

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"
)

// seedSeqCounterFromForkChainSQL raises a chat's seq counter to cover the
// history it INHERITS through a context-window fork chain.
//
// A branched chat displays its parent's messages but does not own those rows —
// they keep the parent's chat_id and are resolved on read by walking the
// window chain. So a fresh branch's own rows are empty and its counter would
// start at 0, placing every message the user sends numerically BENEATH the
// hundreds of inherited messages rendered above it. Seeding at fork time buys
// NextSeqInChat's guarantee for the cost of ONE walk per fork instead of one
// per message saved forever after.
//
// GREATEST, never a bare assignment: the counter may already be ahead (a
// re-fork, or a branch that has since saved its own messages), and lowering it
// would hand out a number already in use.
const seedSeqCounterFromForkChainSQL = `
WITH RECURSIVE chain AS (
    SELECT cw.id, cw.parent_context_window_id
    FROM context_windows cw
    WHERE cw.id = ?1
    UNION
    SELECT parent.id, parent.parent_context_window_id
    FROM context_windows parent
    JOIN chain ON chain.parent_context_window_id = parent.id
),
high AS (
    SELECT COALESCE(MAX(m.seq), -1) AS max_seq
    FROM messages m
    WHERE m.context_window_id = ANY(ARRAY(SELECT id FROM chain))
)
INSERT INTO message_order_counters (counter_kind, scope_id, last_assigned)
SELECT 'seq', ?0, high.max_seq
FROM high
WHERE high.max_seq >= 0
ON CONFLICT (counter_kind, scope_id) DO UPDATE
SET last_assigned = GREATEST(
        message_order_counters.last_assigned,
        EXCLUDED.last_assigned
    )
`

// SeedSeqCounterForFork raises chatID's seq allocator to cover everything
// reachable through forkAtContextWindowID's chain. Safe to repeat: GREATEST
// keeps it monotonic, so a retry or a duplicate fork cannot move the counter
// backwards.
func SeedSeqCounterForFork(ctx context.Context, db orm.Context, chatID, forkAtContextWindowID string) error {
	if _, err := db.Bun().NewRaw(seedSeqCounterFromForkChainSQL, chatID, forkAtContextWindowID).Exec(ctx); err != nil {
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

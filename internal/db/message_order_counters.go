// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
)

// seedSeqCounterFromForkChainSQL raises a chat's seq counter to cover the
// history it INHERITS through a context-window fork chain.
//
// A branched chat displays its parent's messages but does not own those rows —
// they keep the parent's chat_id and are resolved on read by walking the
// window chain. So a fresh branch's own rows are empty and its counter would
// start at 0, placing every message the user sends numerically BENEATH the
// hundreds of inherited messages rendered above it. The reply saves and
// streams correctly but renders at the top of the transcript, which reads as
// "my message never arrived".
//
// This is the defect GetNextSeqByChat's per-save recursive walk existed to
// prevent. Seeding at fork time buys the same guarantee for the cost of ONE
// walk per fork instead of one per message saved forever after.
//
// GREATEST, never a bare assignment: the counter may already be ahead (a
// re-fork, or a branch that has since saved its own messages), and lowering it
// would hand out a number already in use.
const seedSeqCounterFromForkChainSQL = `
WITH RECURSIVE chain AS (
    SELECT cw.id, cw.parent_context_window_id
    FROM context_windows cw
    WHERE cw.id = $2
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
SELECT 'seq', $1, high.max_seq
FROM high
WHERE high.max_seq >= 0
ON CONFLICT (counter_kind, scope_id) DO UPDATE
SET last_assigned = GREATEST(
        message_order_counters.last_assigned,
        EXCLUDED.last_assigned
    )
`

// SeedSeqCounterForFork raises chatID's seq allocator to cover everything
// reachable through forkAtContextWindowID's chain.
//
// Called when a thread is forked. Safe to call repeatedly: the GREATEST keeps
// it monotonic, so a retry or a duplicate fork cannot move the counter
// backwards.
func (r *Repo) SeedSeqCounterForFork(ctx context.Context, chatID, forkAtContextWindowID string) error {
	if chatID == "" {
		return fmt.Errorf("chat ID is required")
	}
	if forkAtContextWindowID == "" {
		return fmt.Errorf("fork context window ID is required")
	}

	if _, err := r.DB.DB(ctx).ExecContext(ctx, seedSeqCounterFromForkChainSQL, chatID, forkAtContextWindowID); err != nil {
		return fmt.Errorf("failed to seed seq counter for fork: %w", err)
	}
	return nil
}

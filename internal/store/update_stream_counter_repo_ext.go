// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for UpdateStreamCounter: one transactional
// sequence allocator per logical update stream (stream_kind 'chat' or 'user',
// keyed by (stream_kind, stream_id)). SaveMessageAtomic allocates a chat's
// next update inside its one statement; every other chat_updates and
// user_updates writer allocates here. Read a counter with the generated
// GetUpdateStreamCounterByPK.
package store

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"
)

// Update stream kinds (update_stream_counters_kind_check).
const (
	UpdateStreamChat = "chat"
	UpdateStreamUser = "user"
)

// allocateUpdateSequenceSQL advances one stream's counter and returns the
// number it assigned. Bound parameters through the raw passthrough, not bun's
// formatter: it runs for every chat and user update, and a formatted text is a
// new prepared statement per stream id.
const allocateUpdateSequenceSQL = `
INSERT INTO update_stream_counters (stream_kind, stream_id, last_assigned_seq)
VALUES ($1, $2, 1)
ON CONFLICT (stream_kind, stream_id) DO UPDATE
SET last_assigned_seq = update_stream_counters.last_assigned_seq + 1
RETURNING last_assigned_seq
`

// AllocateUpdateSequence advances one logical stream's transactional counter.
//
// The counter update and the ledger insert MUST share a transaction. The row
// lock is what prevents N+1 from committing before N, and rolling the
// transaction back must roll both the counter and ledger row back together.
// Refusing outside a transaction makes it impossible for a caller to consume a
// cursor outside the durable write.
func AllocateUpdateSequence(ctx context.Context, db orm.Context, streamKind, streamID string) (int64, error) {
	if !orm.InTx(ctx) {
		return 0, fmt.Errorf("allocate %s update sequence: transaction required", streamKind)
	}
	var next int64
	if err := db.QueryRow(ctx, allocateUpdateSequenceSQL, streamKind, streamID).Scan(&next); err != nil {
		return 0, fmt.Errorf("allocate %s update sequence: %w", streamKind, err)
	}
	return next, nil
}

// UpdateStreamCounterExtra carries fields on UpdateStreamCounter that are NOT columns — computed
// values, transient state, anything derived rather than stored. UpdateStreamCounter is
// embedded on UpdateStreamCounterExtra (see update_stream_counter_orm.go), so a field added here is a field on
// every UpdateStreamCounter value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type UpdateStreamCounterExtra struct {
}

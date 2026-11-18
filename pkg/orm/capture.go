package orm

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/querycapture"
)

// ─── query capture (FORGE_QUERY_CAPTURE_DIR) ──────────────────────────────
//
// A Client constructed while FORGE_QUERY_CAPTURE_DIR is set records the shape
// of every statement it runs (forge/pkg/querycapture), for
// `forge db analyze queries`. Unset, the Client's recorder is nil, no hook is
// installed, and each seam below costs one nil check.
//
// What is recorded is the statement as SENT, because that is what Postgres
// plans: with its values as $n where binding (bind.go) turned them into
// parameters. A bun query hook alone sees the wrong text — bun runs its hooks
// on the statement it rendered with every value inline, before bindConn binds
// it — and never sees pkg/crud's by-PK statements, which run beside bun on
// the raw passthrough. So there are three seams:
//
//   - bindConn records the bound statement of every bun builder query
//     (Client.Bun(), BunDB(), Tx.Bun(), in and out of transactions);
//   - Client.Exec/Query/QueryRow and Tx.Exec/Query/QueryRow record the raw
//     passthrough (hand-written SQL, pkg/crud's by-PK statements);
//   - captureHook records what reaches neither: bun's own raw
//     ExecContext/QueryContext/QueryRowContext (db.Bun().ExecContext), which
//     bun formats with the values inline and runs on the bare connection.
//
// captureHook puts a slot in the ctx of each statement bun runs; bindConn
// marks it when it records the statement, and the hook records the event's
// text only when nothing did.

// captureSlotKey is the ctx key of the statement's captureSlot.
type captureSlotKey struct{}

// captureSlot says whether bindConn recorded the statement bun is running.
type captureSlot struct{ recorded bool }

// captureHook is the bun query hook a capturing Client installs.
type captureHook struct{ rec *querycapture.Recorder }

func (captureHook) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return context.WithValue(ctx, captureSlotKey{}, &captureSlot{})
}

func (h captureHook) AfterQuery(ctx context.Context, e *bun.QueryEvent) {
	if slot, ok := ctx.Value(captureSlotKey{}).(*captureSlot); ok && slot.recorded {
		return
	}
	h.rec.Record(e.Query)
}

// recordSent records a statement bindConn is sending, and marks the slot of
// the bun query running it so captureHook does not record bun's inline text
// too.
func recordSent(ctx context.Context, rec *querycapture.Recorder, query string) {
	if slot, ok := ctx.Value(captureSlotKey{}).(*captureSlot); ok {
		slot.recorded = true
	}
	rec.Record(query)
}

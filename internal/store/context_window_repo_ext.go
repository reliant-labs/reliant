// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ContextWindow. The generated context_window_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: contextWindowRepo (a *crud.Repo[ContextWindow]) and the
//     package-level GetContextWindowByID / ListContextWindow / CreateContextWindow / UpdateContextWindow / DeleteContextWindow
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ContextWindow struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindContextWindowBy(ctx context.Context, db orm.Context, /* args */) ([]*ContextWindow, error) {
//		var rows []*ContextWindow
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("thread_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveContextWindows(ctx context.Context, db orm.Context) ([]*ContextWindow, error) {
//		return ListContextWindow(ctx, db, orm.WhereEq("thread_id", true))
//	}
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The context_windows queries (P5b L1b). A context window is the atomic unit
// of what is sent to the LLM; a thread's windows are numbered by sequence,
// and compaction opens the next one. A by-key miss is a wrapped
// sql.ErrNoRows, as for threads (see noRows).

// InsertContextWindow stores a new context window and returns it as stored.
func InsertContextWindow(ctx context.Context, db orm.Context, cw *core.ContextWindow) (*core.ContextWindow, error) {
	row := contextWindowToRow(cw)
	if err := CreateContextWindow(ctx, db, row); err != nil {
		return nil, fmt.Errorf("failed to create context window: %w", err)
	}
	return contextWindowFromRow(row), nil
}

// GetContextWindow returns the context window; a miss is a wrapped
// sql.ErrNoRows.
func GetContextWindow(ctx context.Context, db orm.Context, id string) (*core.ContextWindow, error) {
	row, err := GetContextWindowByID(ctx, db, id)
	if err != nil {
		return nil, noRows("failed to get context window", "context window", err)
	}
	return contextWindowFromRow(row), nil
}

// GetLatestContextWindow returns the thread's newest context window (highest
// sequence); a thread with none is a wrapped sql.ErrNoRows.
func GetLatestContextWindow(ctx context.Context, db orm.Context, threadID string) (*core.ContextWindow, error) {
	row := new(ContextWindow)
	err := db.Bun().NewSelect().Model(row).
		Where("thread_id = ?", threadID).
		OrderExpr("sequence DESC").
		Limit(1).
		Scan(ctx)
	if err != nil {
		return nil, noRows("failed to get latest context window", "context window", err)
	}
	return contextWindowFromRow(row), nil
}

// GetContextWindowBySequence returns the thread's window at sequence; a miss
// is a wrapped sql.ErrNoRows.
func GetContextWindowBySequence(ctx context.Context, db orm.Context, threadID string, sequence int) (*core.ContextWindow, error) {
	row := new(ContextWindow)
	err := db.Bun().NewSelect().Model(row).
		Where("thread_id = ?", threadID).
		Where("sequence = ?", sequence).
		Scan(ctx)
	if err != nil {
		return nil, noRows("failed to get context window by sequence", "context window", err)
	}
	return contextWindowFromRow(row), nil
}

// ListContextWindowsByThread returns the thread's windows in sequence order.
// Unbounded, so the builder, not the generated List and its default cap.
func ListContextWindowsByThread(ctx context.Context, db orm.Context, threadID string) ([]*core.ContextWindow, error) {
	var rows []*ContextWindow
	err := db.Bun().NewSelect().Model(&rows).Where("thread_id = ?", threadID).OrderExpr("sequence ASC").Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list context windows: %w", err)
	}
	out := make([]*core.ContextWindow, len(rows))
	for i, row := range rows {
		out[i] = contextWindowFromRow(row)
	}
	return out, nil
}

// GetMaxSequenceForThread returns the thread's highest window sequence, -1
// when it has none. It replaced the messages-side
// GetLatestContextSequenceByThread (P5b L1a), and is read wherever a new
// window is numbered, so it binds its argument (the raw passthrough's $1).
func GetMaxSequenceForThread(ctx context.Context, db orm.Context, threadID string) (int, error) {
	var seq int
	err := db.QueryRow(ctx, `SELECT COALESCE(MAX(sequence), -1) FROM context_windows WHERE thread_id = $1`, threadID).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("failed to get max sequence: %w", err)
	}
	return seq, nil
}

// GetContextWindowWithThread returns the window with its thread's chat id,
// parent thread id and fork point, for fork resolution.
func GetContextWindowWithThread(ctx context.Context, db orm.Context, id string) (*core.ContextWindow, string, *string, *string, error) {
	var row struct {
		ContextWindow
		ChatID                string  `bun:"chat_id"`
		ParentThreadID        *string `bun:"parent_thread_id"`
		ThreadForkAtMessageID *string `bun:"thread_fork_at_message_id"`
	}
	err := db.Bun().NewRaw(`
SELECT cw.*, t.chat_id, t.parent_thread_id, t.fork_at_message_id AS thread_fork_at_message_id
FROM context_windows cw
JOIN threads t ON t.id = cw.thread_id
WHERE cw.id = ?0`, id).Scan(ctx, &row)
	if err != nil {
		return nil, "", nil, nil, noRows("failed to get context window with thread", "context window", err)
	}
	return contextWindowFromRow(&row.ContextWindow), row.ChatID, row.ParentThreadID, row.ThreadForkAtMessageID, nil
}

// ListForkedThreadIDs returns which of threadIDs are forks: their initial
// (sequence 0) context window links to a parent window. One round trip for a
// whole chat, where GetContextWindowBySequence(thread, 0) cost one per
// workflow.
func ListForkedThreadIDs(ctx context.Context, db orm.Context, threadIDs []string) ([]string, error) {
	var ids []string
	err := db.Bun().NewRaw(`
SELECT thread_id FROM context_windows
WHERE thread_id = ANY(?0::text[])
  AND sequence = 0
  AND parent_context_window_id IS NOT NULL`, pgdialect.Array(threadIDs)).Scan(ctx, &ids)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// SetCompactionSummaryMessage records the message that summarizes the window
// (compaction), returning the stored window; a miss is a wrapped
// sql.ErrNoRows.
func SetCompactionSummaryMessage(ctx context.Context, db orm.Context, cwID, messageID string) (*core.ContextWindow, error) {
	row := &ContextWindow{ID: cwID}
	if messageID != "" {
		row.CompactionSummaryMessageID = &messageID
	}
	if err := UpdateContextWindowMasked(ctx, db, row, []string{ContextWindowFieldCompactionSummaryMessageID}); err != nil {
		return nil, noRows("failed to set compaction summary message", "context window", err)
	}
	return contextWindowFromRow(row), nil
}

// RemoveContextWindow deletes the window; one that does not exist is already
// gone.
func RemoveContextWindow(ctx context.Context, db orm.Context, id string) error {
	if err := DeleteContextWindow(ctx, db, id); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

// DeleteContextWindowsByThread deletes every window of the thread.
func DeleteContextWindowsByThread(ctx context.Context, db orm.Context, threadID string) error {
	_, err := db.Bun().NewDelete().Model((*ContextWindow)(nil)).Where("thread_id = ?", threadID).Exec(ctx)
	return err
}

func contextWindowToRow(cw *core.ContextWindow) *ContextWindow {
	return &ContextWindow{
		ID:                         cw.ID,
		ThreadID:                   cw.ThreadID,
		Sequence:                   int64(cw.Sequence),
		CompactionSummaryMessageID: cw.CompactionSummaryMessageID,
		CreatedAt:                  cw.CreatedAt,
		ParentContextWindowID:      cw.ParentContextWindowID,
		ForkAtMessageID:            cw.ForkAtMessageID,
	}
}

func contextWindowFromRow(row *ContextWindow) *core.ContextWindow {
	return &core.ContextWindow{
		ID:                         row.ID,
		ThreadID:                   row.ThreadID,
		Sequence:                   int(row.Sequence),
		ParentContextWindowID:      row.ParentContextWindowID,
		ForkAtMessageID:            row.ForkAtMessageID,
		CompactionSummaryMessageID: row.CompactionSummaryMessageID,
		CreatedAt:                  row.CreatedAt,
	}
}

// ContextWindowExtra carries fields on ContextWindow that are NOT columns — computed
// values, transient state, anything derived rather than stored. ContextWindow is
// embedded on ContextWindowExtra (see context_window_orm.go), so a field added here is a field on
// every ContextWindow value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ContextWindowExtra struct {
}

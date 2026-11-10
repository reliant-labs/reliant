package db

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/store"
)

// Ledger retention: the worker's hourly pass over the two tables that only
// ever grew (prod reliant-pg, 2026-11-10: chat_updates 10 GB, step_executions
// 8.8 GB, zero deletes). Each pass works in small batches, each its own
// statement on the pool, so no lock or snapshot is held for longer than a
// batch and the WAL is written a little at a time. Every replica runs it; the
// work is idempotent and a batch another replica holds is skipped.
//
// What is kept, and why each reader still reads what it did, is
// store.CompactChatUpdates' and store.PruneRunSteps' section comments.

const (
	// chatCompactionBatch is the most chat updates one compaction batch
	// covers: three short statements (read, raise the watermark, delete).
	chatCompactionBatch = 1000
	// chatCompactionPage is how many due chats one lookup returns.
	chatCompactionPage = 100
	// stepPruneBatch is the most step rows one delete takes. Smaller than the
	// chat batch: a step's output is out of line, so each row's delete also
	// frees its TOAST chunks.
	stepPruneBatch = 500
	// stepRetentionPage is how many root runs one lookup returns.
	stepRetentionPage = 100
	// stepRetentionCursorName is the step prune's ledger_retention_cursors row.
	stepRetentionCursorName = "step_executions"
)

// LedgerRetentionPause is the pause between two batches of a pass, so a large
// backlog (the first pass on prod deletes millions of rows) is paced rather
// than run flat out against the primary and its replica.
type LedgerRetentionPause time.Duration

func (p LedgerRetentionPause) wait(ctx context.Context) error {
	if p <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(time.Duration(p))
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ChatCompactionResult is what one CompactChatUpdates pass did.
type ChatCompactionResult struct {
	// Chats is how many chats it compacted.
	Chats int
	// Examined is how many aged updates it covered, Deleted how many of
	// those it deleted.
	Examined int64
	Deleted  int64
}

// CompactChatUpdates compacts every chat's updates written before cutoff
// (store.CompactChatUpdates) and returns what it did, also on error.
func (r *Repo) CompactChatUpdates(ctx context.Context, cutoff time.Time, pause LedgerRetentionPause) (ChatCompactionResult, error) {
	var res ChatCompactionResult
	ctx = orm.Detached(ctx)
	after := ""
	for {
		due, err := store.ChatsDueForCompaction(ctx, r.ORM(), cutoff, after, chatCompactionPage)
		if err != nil {
			return res, err
		}
		for _, chat := range due {
			through := chat.Through
			for {
				if err := ctx.Err(); err != nil {
					return res, err
				}
				batch, err := store.CompactChatUpdates(ctx, r.ORM(), chat.ChatID, through, cutoff, chatCompactionBatch)
				res.Examined += int64(batch.Examined)
				res.Deleted += batch.Deleted
				if err != nil {
					return res, fmt.Errorf("compact chat %s: %w", chat.ChatID, err)
				}
				through = batch.Through
				if batch.Done {
					break
				}
				if err := pause.wait(ctx); err != nil {
					return res, err
				}
			}
			res.Chats++
			after = chat.ChatID
			if err := pause.wait(ctx); err != nil {
				return res, err
			}
		}
		if len(due) < chatCompactionPage {
			return res, nil
		}
	}
}

// StepRetentionResult is what one PruneStepExecutions pass did.
type StepRetentionResult struct {
	// Runs is how many root runs it pruned, Deleted how many step rows.
	Runs    int
	Deleted int64
}

// PruneStepExecutions deletes the internal steps of every root run that
// completed or was cancelled before cutoff (store.PruneRunSteps), resuming
// where the last pass stopped, and returns what it did, also on error. A run
// is recorded done only once all of it is pruned, so an error retries it.
func (r *Repo) PruneStepExecutions(ctx context.Context, cutoff time.Time, pause LedgerRetentionPause) (StepRetentionResult, error) {
	var res StepRetentionResult
	ctx = orm.Detached(ctx)
	at, id, err := store.ReadLedgerRetentionCursor(ctx, r.ORM(), stepRetentionCursorName)
	if err != nil {
		return res, err
	}
	for {
		roots, err := store.StepRetentionRoots(ctx, r.ORM(), cutoff, at, id, stepRetentionPage)
		if err != nil {
			return res, err
		}
		for _, root := range roots {
			for {
				if err := ctx.Err(); err != nil {
					return res, err
				}
				n, err := store.PruneRunSteps(ctx, r.ORM(), root.ID, stepPruneBatch)
				res.Deleted += n
				if err != nil {
					return res, fmt.Errorf("prune run %s: %w", root.ID, err)
				}
				if n < stepPruneBatch {
					break
				}
				if err := pause.wait(ctx); err != nil {
					return res, err
				}
			}
			if err := store.AdvanceLedgerRetentionCursor(ctx, r.ORM(), stepRetentionCursorName, root.CompletedAt, root.ID); err != nil {
				return res, err
			}
			res.Runs++
			at, id = root.CompletedAt, root.ID
		}
		if len(roots) < stepRetentionPage {
			return res, nil
		}
	}
}

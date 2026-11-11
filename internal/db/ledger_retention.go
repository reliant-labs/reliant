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
			through := chat.Done
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
	// Runs is how many root runs it visited (new and deferred), Deferred how
	// many of them still hold a workflow that is not prunable yet, and
	// Deleted how many step rows it deleted.
	Runs     int
	Deferred int
	Deleted  int64
}

// PruneStepExecutions deletes the internal steps of every root run that
// completed or was cancelled before cutoff (store.PruneRunSteps): first the
// runs an earlier pass deferred, then the new ones after the cursor. It
// returns what it did, also on error. A run is recorded — deferred or done —
// before the cursor passes it, so an error or a stop retries it.
func (r *Repo) PruneStepExecutions(ctx context.Context, cutoff time.Time, pause LedgerRetentionPause) (StepRetentionResult, error) {
	var res StepRetentionResult
	ctx = orm.Detached(ctx)

	after := ""
	for {
		deferred, err := store.DeferredStepRetentionRoots(ctx, r.ORM(), after, stepRetentionPage)
		if err != nil {
			return res, err
		}
		for _, root := range deferred {
			if err := r.pruneRunSteps(ctx, root, cutoff, pause, &res); err != nil {
				return res, err
			}
			after = root
		}
		if len(deferred) < stepRetentionPage {
			break
		}
	}

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
			if err := r.pruneRunSteps(ctx, root.ID, cutoff, pause, &res); err != nil {
				return res, err
			}
			if err := store.AdvanceLedgerRetentionCursor(ctx, r.ORM(), stepRetentionCursorName, root.CompletedAt, root.ID); err != nil {
				return res, err
			}
			at, id = root.CompletedAt, root.ID
		}
		if len(roots) < stepRetentionPage {
			return res, nil
		}
	}
}

// pruneRunSteps prunes one root run's tree until none of its prunable steps
// is left, then records it deferred (part of the tree is not prunable yet) or
// done.
func (r *Repo) pruneRunSteps(ctx context.Context, rootID string, cutoff time.Time, pause LedgerRetentionPause, res *StepRetentionResult) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := store.PruneRunSteps(ctx, r.ORM(), rootID, cutoff, stepPruneBatch)
		res.Deleted += n
		if err != nil {
			return fmt.Errorf("prune run %s: %w", rootID, err)
		}
		if n < stepPruneBatch {
			left, err := store.RunStepsLeft(ctx, r.ORM(), rootID, cutoff)
			if err != nil {
				return fmt.Errorf("prune run %s: %w", rootID, err)
			}
			if !left {
				break
			}
		}
		if err := pause.wait(ctx); err != nil {
			return err
		}
	}
	pending, err := store.RunHasUnprunableWorkflows(ctx, r.ORM(), rootID, cutoff)
	if err != nil {
		return fmt.Errorf("prune run %s: %w", rootID, err)
	}
	res.Runs++
	if pending {
		res.Deferred++
		return store.DeferStepRetentionRoot(ctx, r.ORM(), rootID)
	}
	return store.ClearStepRetentionRoot(ctx, r.ORM(), rootID)
}

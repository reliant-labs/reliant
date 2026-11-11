// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/store"
)

// ChatUpdateHeadsBackfillReport is what one backfill pass did.
type ChatUpdateHeadsBackfillReport struct {
	// Backfilled is the chats made complete.
	Backfilled int
	// Failed is the chats whose backfill returned an error; the next pass
	// retries them.
	Failed int
}

// BackfillIncompleteChatUpdateHeads walks every chat whose snapshot heads its
// stream counter does not vouch for (store.ChatsWithIncompleteHeads), in id
// order, batch ids at a time, and backfills each (store.BackfillChatUpdateHeads),
// waiting pause between chats so the pass never competes with live traffic
// for long. A chat that fails is logged and skipped; the work list is the
// counters' state, so the next pass finds it again, and a pass stopped at any
// point (ctx, a restart) loses nothing. dev-docs/design/chat-update-heads.md,
// "Backfill".
func (r *Repo) BackfillIncompleteChatUpdateHeads(ctx context.Context, batch int, pause time.Duration) (ChatUpdateHeadsBackfillReport, error) {
	var report ChatUpdateHeadsBackfillReport
	if batch <= 0 {
		return report, fmt.Errorf("backfill chat update heads: batch must be positive, got %d", batch)
	}
	after := ""
	for {
		chats, err := store.ChatsWithIncompleteHeads(ctx, r.ORM(), after, batch)
		if err != nil {
			return report, fmt.Errorf("backfill chat update heads: list chats: %w", err)
		}
		if len(chats) == 0 {
			return report, nil
		}
		for _, chatID := range chats {
			if err := ctx.Err(); err != nil {
				return report, err
			}
			did, err := store.BackfillChatUpdateHeads(ctx, r.ORM(), chatID)
			switch {
			case err != nil:
				report.Failed++
				logging.WarnContext(ctx, "backfilling a chat's snapshot heads failed", "chatID", chatID, "error", err)
			case did:
				report.Backfilled++
			}
			if pause > 0 {
				select {
				case <-ctx.Done():
					return report, ctx.Err()
				case <-time.After(pause):
				}
			}
		}
		after = chats[len(chats)-1]
	}
}

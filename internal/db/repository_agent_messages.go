// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"

	"github.com/reliant-labs/reliant/internal/logging"
)

// announceAgentMailboxChanged tells the chat's open clients that one of its
// threads changed its mailbox, so the pending-queue strip re-reads it instead
// of polling. It is best-effort: the row is the source of truth and the client
// keeps a fallback poll, so a failed announcement degrades latency, not
// correctness. The enqueue paths announce through agentmailbox.Mailbox; this is
// the stop path's (CancelStoppedSpawn), which resolves rows inside its own
// transaction.
func (r *Repo) announceAgentMailboxChanged(ctx context.Context, chatID string) {
	if err := r.UpdateWriter().EmitChatRefetch(ctx, chatID, RefetchAgentMailbox); err != nil {
		logging.WarnContext(ctx, "failed to announce agent mailbox change", "chatID", chatID, "error", err)
	}
}

// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// ChatWorkflowLookup returns a resolver from chat id to the Temporal workflow
// id driving that chat, for temporal.NewAgentMessageNotifier.
//
// A chat's workflow is normally created with the chat id as its identity, but
// chats.workflow_id is authoritative — a run restarted after the history limit
// (see HistoryLimitRestartMessage) carries a different id, and signalling the
// chat id would silently reach nothing.
//
// Returns ok=false rather than an error when the row cannot be read: the
// caller's fallback (address the workflow by chat id) is the right behavior
// for a doorbell that is best-effort anyway.
func ChatWorkflowLookup(repo db.Repository) func(ctx context.Context, chatID string) (string, bool) {
	return func(ctx context.Context, chatID string) (string, bool) {
		if repo == nil {
			return "", false
		}
		chat, err := repo.GetChat(ctx, chatID)
		if err != nil || chat == nil || chat.WorkflowID == nil || *chat.WorkflowID == "" {
			return "", false
		}
		return *chat.WorkflowID, true
	}
}

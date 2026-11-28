// forge:deps — consumer-declared interfaces for this package's Deps.
// Copyright (c) 2025 Reliant Labs

package agentmailbox

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// Store writes the agent_messages rows. store.AgentMessageExtStore satisfies
// it.
type Store interface {
	EnqueueAgentMessage(ctx context.Context, msg *db.AgentMessage) error
	EnqueueAgentMessageOnce(ctx context.Context, msg *db.AgentMessage) (bool, error)
	EnqueueAgentMessageIfAbsent(ctx context.Context, msg *db.AgentMessage) (bool, error)
	EnqueueSpawnReport(ctx context.Context, msg *db.AgentMessage) (db.SpawnReportOutcome, error)
}

// Announcer tells a chat's open clients that its mailbox changed.
// chatupdates.Service satisfies it: the announcement is a chat update,
// published after commit.
type Announcer interface {
	EmitChatRefetch(ctx context.Context, chatID string, refetchType db.RefetchType) error
}

// Deps is what a Mailbox is built from. Plain Deps, not a forge contract
// package: it is built by hand by the composition root and the worker.
type Deps struct {
	// Store writes the rows.
	Store Store
	// Announcer announces each change to the chat's clients.
	Announcer Announcer
}

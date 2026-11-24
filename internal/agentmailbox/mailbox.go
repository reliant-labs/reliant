// Copyright (c) 2025 Reliant Labs
//
// Package agentmailbox queues messages for a thread's mailbox and tells the
// chat's open clients when the mailbox gained a row.
//
// Every enqueue path funnels through it: the human composer
// (SendAgentMessage), spawn_send, spawn completion reports and the
// reconciler's synthesized placeholders. The announcement lives here rather
// than at each caller because a path that forgot to announce would leave the
// pending-queue strip blind to its rows until the client's slow fallback poll.
//
// Leaving the mailbox needs no announcement from here. A drain already
// publishes AGENT_MESSAGES_DRAINED in its own transaction, a user claim or
// cancel is client-initiated and applied locally, and a thread going terminal
// is read once on the client's running-to-idle edge.
//
// Built by hand (agentmailbox.New(Deps)): never a node compose.go constructs.
//
//forge:exclude-contract: a concrete enqueue service built by hand with a plain Deps; its callers take the narrow roles they need, not a Service interface
package agentmailbox

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Mailbox queues agent messages and announces each change.
type Mailbox struct {
	store     Store
	announcer Announcer
}

// New builds a Mailbox from deps.
func New(deps Deps) *Mailbox {
	return &Mailbox{store: deps.Store, announcer: deps.Announcer}
}

// announce is best-effort by design: the row is the source of truth and the
// client keeps a fallback poll, so a failed announcement degrades latency, not
// correctness. It is a chat-scoped refetch (chat_updates), so only clients
// viewing this chat receive it, and it joins the caller's transaction when
// there is one: the client is told only after the row it should read has
// committed.
func (m *Mailbox) announce(ctx context.Context, chatID string) {
	if err := m.announcer.EmitChatRefetch(ctx, chatID, db.RefetchAgentMailbox); err != nil {
		logging.WarnContext(ctx, "failed to announce agent mailbox change", "chatID", chatID, "error", err)
	}
}

func validate(msg *db.AgentMessage) error {
	if msg == nil {
		return fmt.Errorf("agent message cannot be nil")
	}
	if msg.ID == "" {
		return fmt.Errorf("agent message ID is required")
	}
	if msg.ChatID == "" {
		return fmt.Errorf("chat ID is required")
	}
	if msg.FromThreadID == "" {
		return fmt.Errorf("from thread ID is required")
	}
	if msg.ToThreadID == "" {
		return fmt.Errorf("to thread ID is required")
	}
	return nil
}

// EnqueueAgentMessage queues msg.
func (m *Mailbox) EnqueueAgentMessage(ctx context.Context, msg *db.AgentMessage) error {
	if err := validate(msg); err != nil {
		return err
	}
	if err := m.store.EnqueueAgentMessage(ctx, msg); err != nil {
		return err
	}
	m.announce(ctx, msg.ChatID)
	return nil
}

// EnqueueAgentMessageOnce is EnqueueAgentMessage for a row whose id the caller
// does not control: a human send names its row (the client's message id), so a
// repeat of that send arrives with an id that is already taken. It reports
// inserted=false, and writes and announces nothing, when the id is. Concurrent
// repeats are arbitrated by the primary key, not by a read.
func (m *Mailbox) EnqueueAgentMessageOnce(ctx context.Context, msg *db.AgentMessage) (bool, error) {
	if err := validate(msg); err != nil {
		return false, err
	}
	inserted, err := m.store.EnqueueAgentMessageOnce(ctx, msg)
	if err != nil {
		return false, err
	}
	if inserted {
		m.announce(ctx, msg.ChatID)
	}
	return inserted, nil
}

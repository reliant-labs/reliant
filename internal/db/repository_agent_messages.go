// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func (r *Repo) EnqueueAgentMessage(ctx context.Context, msg *AgentMessage) error {
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
	return r.agentMessages.EnqueueAgentMessage(ctx, msg)
}

// EnqueueAgentMessageIfAbsent is EnqueueAgentMessage's conditional sibling,
// used only by the stranded-background-spawn reconciler sweep: msg.Kind must
// be a terminal kind (Completion, Cancelled, or Failed), and msg.ToolCallID
// must be set — those are exactly the rows the unique constraint
// (idx_agent_messages_one_terminal_report_per_spawn) applies to.
func (r *Repo) EnqueueAgentMessageIfAbsent(ctx context.Context, msg *AgentMessage) (bool, error) {
	if msg == nil {
		return false, fmt.Errorf("agent message cannot be nil")
	}
	if msg.ID == "" {
		return false, fmt.Errorf("agent message ID is required")
	}
	if msg.ChatID == "" {
		return false, fmt.Errorf("chat ID is required")
	}
	if msg.FromThreadID == "" {
		return false, fmt.Errorf("from thread ID is required")
	}
	if msg.ToThreadID == "" {
		return false, fmt.Errorf("to thread ID is required")
	}
	if msg.ToolCallID == nil || *msg.ToolCallID == "" {
		return false, fmt.Errorf("tool call ID is required")
	}
	switch msg.Kind {
	case core.AgentMessageKindCompletion, core.AgentMessageKindCancelled, core.AgentMessageKindFailed:
	default:
		return false, fmt.Errorf("kind must be a terminal kind (completion, cancelled, or failed), got %d", msg.Kind)
	}
	return r.agentMessages.EnqueueAgentMessageIfAbsent(ctx, msg)
}

func (r *Repo) ListQueuedAgentMessagesForThread(ctx context.Context, toThreadID string) ([]*AgentMessage, error) {
	if toThreadID == "" {
		return nil, fmt.Errorf("to thread ID cannot be empty")
	}
	return r.agentMessages.ListQueuedAgentMessagesForThread(ctx, toThreadID)
}

func (r *Repo) MarkAgentMessagesDelivered(ctx context.Context, ids []string, deliveredAt time.Time, deliveredMessageID string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	// deliveredMessageID may be empty: a drain claims its rows before the
	// envelope exists and backfills the pointer afterwards.
	return r.agentMessages.MarkAgentMessagesDelivered(ctx, ids, deliveredAt, deliveredMessageID)
}

func (r *Repo) SetAgentMessagesDeliveredMessageID(ctx context.Context, ids []string, deliveredMessageID string) error {
	if len(ids) == 0 {
		return nil
	}
	if deliveredMessageID == "" {
		return fmt.Errorf("delivered message ID is required")
	}
	return r.agentMessages.SetAgentMessagesDeliveredMessageID(ctx, ids, deliveredMessageID)
}

func (r *Repo) CountQueuedAgentMessagesForThread(ctx context.Context, toThreadID string) (int64, error) {
	if toThreadID == "" {
		return 0, fmt.Errorf("to thread ID cannot be empty")
	}
	return r.agentMessages.CountQueuedAgentMessagesForThread(ctx, toThreadID)
}

// CancelQueuedAgentMessage deletes a mailbox row only if it is still
// queued — see core.AgentMessageStore for the race this guards against.
func (r *Repo) CancelQueuedAgentMessage(ctx context.Context, id, chatID string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("message ID cannot be empty")
	}
	if chatID == "" {
		return false, fmt.Errorf("chat ID cannot be empty")
	}
	return r.agentMessages.CancelQueuedAgentMessage(ctx, id, chatID)
}

// ClaimQueuedAgentMessagesForThread takes queued human messages off a thread's
// mailbox and returns exactly what it took — see core.AgentMessageStore for
// why this is one statement rather than cancel-then-send.
//
// messageID is optional: pass "" to claim the whole queue.
func (r *Repo) ClaimQueuedAgentMessagesForThread(ctx context.Context, toThreadID, chatID, messageID string) ([]*AgentMessage, error) {
	if toThreadID == "" {
		return nil, fmt.Errorf("to thread ID cannot be empty")
	}
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	return r.agentMessages.ClaimQueuedAgentMessagesForThread(ctx, toThreadID, chatID, messageID)
}

// MarkQueuedAgentMessagesUndeliveredForThread resolves the mailbox of a thread
// whose loop has exited — see core.AgentMessageStore for why a stranded row is
// marked rather than deleted or left pending.
func (r *Repo) MarkQueuedAgentMessagesUndeliveredForThread(ctx context.Context, toThreadID string) (int64, error) {
	if toThreadID == "" {
		return 0, fmt.Errorf("to thread ID cannot be empty")
	}
	return r.agentMessages.MarkQueuedAgentMessagesUndeliveredForThread(ctx, toThreadID)
}

// ListThreadsWithOrphanedAgentMessages returns threads that are already
// terminal but still have queued mailbox rows.
func (r *Repo) ListThreadsWithOrphanedAgentMessages(ctx context.Context) ([]string, error) {
	return r.agentMessages.ListThreadsWithOrphanedAgentMessages(ctx)
}

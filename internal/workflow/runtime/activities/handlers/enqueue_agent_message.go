// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"go.temporal.io/sdk/temporal"
)

// EnqueueAgentMessageInput is the input for the EnqueueAgentMessage activity.
type EnqueueAgentMessageInput struct {
	ChatID       string `json:"chat_id" reliant:"-"`
	FromThreadID string `json:"from_thread_id"`
	ToThreadID   string `json:"to_thread_id"`
	// Kind is the wire value of core.AgentMessageKind (2=completion,
	// 3=cancelled, 4=failed; 1=message is sent via spawn_send, not here).
	Kind int32  `json:"kind"`
	Body string `json:"body"`
	// ToolCallID is the spawn call that owns the subject agent, when known.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ActivityThread is the thread this activity runs on: the sender. A
// background spawn reports its outcome from its own goroutine, so a failure
// here is that spawn's — not the parent's, and not every thread's. Without it
// the input carries no "thread" and its errors were written thread-less, which
// rendered them in a spawn view that did not yet exist when they happened.
func (in EnqueueAgentMessageInput) ActivityThread() string { return in.FromThreadID }

// EnqueueAgentMessageOutput reports the id of the enqueued row.
type EnqueueAgentMessageOutput struct {
	ID string `json:"id"`
}

// EnqueueAgentMessageActivity is the deterministic boundary a workflow
// goroutine uses to queue a mailbox entry (spec §5.1). Workflow code cannot
// touch the repository directly; this activity is the only path.
//
// Used by a detached (background=true) spawn to notify its parent's mailbox
// of completion/cancellation/failure once the child's own execution ends —
// the parent's mailbox is drained by its own next CallLLM, never written into
// directly.
type EnqueueAgentMessageActivity struct {
	repo db.Repository
}

// NewEnqueueAgentMessageActivity creates a new EnqueueAgentMessageActivity.
func NewEnqueueAgentMessageActivity(repo db.Repository) *EnqueueAgentMessageActivity {
	return &EnqueueAgentMessageActivity{repo: repo}
}

func (a *EnqueueAgentMessageActivity) Name() string        { return "EnqueueAgentMessage" }
func (a *EnqueueAgentMessageActivity) DisplayName() string { return "Enqueue Agent Message" }
func (a *EnqueueAgentMessageActivity) Description() string {
	return "Queue a mailbox entry for delivery to another thread"
}
func (a *EnqueueAgentMessageActivity) Category() schema.ActivityCategory {
	return schema.CategoryMessageProcessing
}

func (a *EnqueueAgentMessageActivity) Execute(ctx context.Context, input EnqueueAgentMessageInput) (EnqueueAgentMessageOutput, error) {
	if input.ToThreadID == "" {
		return EnqueueAgentMessageOutput{}, fmt.Errorf("to_thread_id is required")
	}
	if input.FromThreadID == "" {
		return EnqueueAgentMessageOutput{}, fmt.Errorf("from_thread_id is required")
	}
	kind := core.AgentMessageKind(input.Kind)
	if kind == core.AgentMessageKindUnspecified {
		return EnqueueAgentMessageOutput{}, fmt.Errorf("kind is required")
	}

	msg := &core.AgentMessage{
		ID:           uuid.New().String(),
		ChatID:       input.ChatID,
		FromThreadID: input.FromThreadID,
		ToThreadID:   input.ToThreadID,
		Kind:         kind,
		Body:         input.Body,
		Status:       core.AgentMessageStatusQueued,
		CreatedAt:    time.Now().UTC(),
	}
	if input.ToolCallID != "" {
		msg.ToolCallID = &input.ToolCallID
	}

	// Terminal spawn reports go through EnqueueSpawnReport: a reconciler may
	// already have synthesized a placeholder for this spawn, and a plain INSERT
	// would die on idx_agent_messages_one_terminal_report_per_chat_spawn (23505),
	// leaving the real outcome lost. See
	// dev-docs/incidents/2026-10-04-spawn-report-collision.md.
	//
	// The slot is (chat, tool call id). A slot held by a DIFFERENT spawn -- a
	// provider reused the id in this chat, or (until the contract migration
	// drops the chat-blind index) in another chat -- is an error, not "already
	// reported": the parent will not receive this report, and the failed
	// activity is what says so (the spawn goroutine logs it and the run
	// history keeps it).
	if input.ToolCallID != "" && isTerminalSpawnReportKind(kind) {
		outcome, err := a.repo.EnqueueSpawnReport(ctx, msg)
		if errors.Is(err, core.ErrSpawnReportSlotTaken) {
			logging.Error("[EnqueueAgentMessage] another spawn already reported under this tool call id; this report cannot be delivered",
				"chat_id", input.ChatID, "tool_call_id", input.ToolCallID,
				"from_thread_id", input.FromThreadID, "to_thread_id", input.ToThreadID, "error", err)
			return EnqueueAgentMessageOutput{}, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("spawn report not delivered: %v", err), "SpawnReportSlotTaken", err)
		}
		if err != nil {
			return EnqueueAgentMessageOutput{}, fmt.Errorf("failed to enqueue spawn report: %w", err)
		}
		switch outcome {
		case core.SpawnReportSuperseded:
			logging.Info("[EnqueueAgentMessage] real spawn report superseded a synthesized placeholder",
				"tool_call_id", input.ToolCallID)
		case core.SpawnReportAlreadyReported:
			// Idempotent: also what a retry after a lost commit response sees.
			logging.Debug("[EnqueueAgentMessage] spawn already reported; no-op",
				"tool_call_id", input.ToolCallID)
		}
		return EnqueueAgentMessageOutput{ID: msg.ID}, nil
	}

	if err := a.repo.EnqueueAgentMessage(ctx, msg); err != nil {
		return EnqueueAgentMessageOutput{}, fmt.Errorf("failed to enqueue agent message: %w", err)
	}

	return EnqueueAgentMessageOutput{ID: msg.ID}, nil
}

func isTerminalSpawnReportKind(kind core.AgentMessageKind) bool {
	switch kind {
	case core.AgentMessageKindCompletion, core.AgentMessageKindCancelled, core.AgentMessageKindFailed:
		return true
	}
	return false
}

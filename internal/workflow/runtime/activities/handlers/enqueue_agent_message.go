// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
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

	if err := a.repo.EnqueueAgentMessage(ctx, msg); err != nil {
		return EnqueueAgentMessageOutput{}, fmt.Errorf("failed to enqueue agent message: %w", err)
	}

	return EnqueueAgentMessageOutput{ID: msg.ID}, nil
}

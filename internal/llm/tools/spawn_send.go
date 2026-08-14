// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type SpawnSendParams struct {
	AgentID string `json:"agent_id" jsonschema:"required,description=The agent_id (thread id) to send a message to: one of your sub-agents, or your own parent"`
	Message string `json:"message" jsonschema:"required,description=The message to deliver"`
}

const (
	SpawnSendToolName    = "spawn_send"
	spawnSendDescription = `Send a message to a running sub-agent you spawned, or to your own parent agent.

THE RECEIPT IS HONEST — READ IT LITERALLY:
This QUEUES the message for delivery at the target's next turn. It does NOT
mean the target has read it, and it does NOT mean the target has acted on it.
Do not proceed as though your instruction has already taken effect — if you
need to know whether it landed, check back with spawn_status or wait for a
response.

v1 is parent \u2194 child only: you may message a direct sub-agent (a spawn of
this thread) or your own parent. Messaging a sibling or unrelated agent is
rejected.

If the target has already finished, this FAILS rather than silently doing
nothing \u2014 use spawn(agent_id=...) to resume it instead.`
)

// AgentMessageNotifier wakes the workflow that owns a thread after a message
// has been queued into that thread's mailbox.
//
// Declared here as a one-method interface rather than taking a Temporal
// client, because a tool has no business knowing what a workflow engine is:
// the implementation lives next to the wiring (internal/workflow), and tests
// substitute a recorder. A nil notifier is valid and means "no way to ring
// the doorbell here" — the daemon runtime builds a factory with no Temporal
// connection at all, and spawn_send must keep working there.
type AgentMessageNotifier interface {
	// NotifyAgentMessageQueued is best-effort by contract: the mailbox row
	// is already durable when it is called, so an error means a late
	// delivery, never a lost message.
	NotifyAgentMessageQueued(ctx context.Context, chatID, toThreadID string)
}

type spawnSendTool struct {
	repo     db.Repository
	notifier AgentMessageNotifier
}

func NewSpawnSendTool(repo db.Repository, notifier AgentMessageNotifier) Tool {
	return NewToolWrapper[SpawnSendParams, ToolResponse](&spawnSendTool{repo: repo, notifier: notifier})
}

func (s *spawnSendTool) Name() string {
	return SpawnSendToolName
}

func (s *spawnSendTool) Description() string {
	return spawnSendDescription
}

func (s *spawnSendTool) RequiresPermission(params SpawnSendParams) (bool, error) {
	return false, nil
}

func (s *spawnSendTool) Execute(rctx *rctx.ToolContext, params SpawnSendParams) (ToolResponse, error) {
	if s.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}
	threadID := rctx.Thread
	if threadID == "" {
		return NewTextErrorResponse("Thread context required"), nil
	}
	if params.AgentID == "" {
		return NewTextErrorResponse("agent_id is required"), nil
	}
	if params.Message == "" {
		return NewTextErrorResponse("message is required"), nil
	}
	if params.AgentID == threadID {
		return NewTextErrorResponse("agent_id cannot be this thread itself"), nil
	}

	relationship, err := spawnSendRelationship(rctx, s.repo, threadID, params.AgentID)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	if relationship == spawnRelationshipNone {
		return NewTextErrorResponse(fmt.Sprintf(
			"agent_id %q is neither a sub-agent you spawned nor your own parent. "+
				"spawn_send is parent\u2194child only in v1 — messaging a sibling or unrelated agent is not supported. "+
				"Use spawn_status to see your sub-agents.", params.AgentID)), nil
	}

	target, err := s.repo.GetThread(rctx.Context, params.AgentID)
	if err != nil || target == nil {
		return NewTextErrorResponse(fmt.Sprintf("Target agent %q could not be found.", params.AgentID)), nil
	}
	if core.ThreadStatusIsTerminal(target.Status) {
		return NewTextErrorResponse(fmt.Sprintf(
			"Agent %q has already finished (status: %s) — its loop has exited and there is nothing to deliver into. "+
				"Use spawn(agent_id=%q, ...) to resume it instead; spawn_send never silently resurrects a finished agent.",
			params.AgentID, core.ThreadStatusLabel(target.Status), params.AgentID)), nil
	}

	title := params.AgentID
	if target.Title != nil && *target.Title != "" {
		title = *target.Title
	}

	msg := &core.AgentMessage{
		ID:           uuid.New().String(),
		ChatID:       rctx.ChatID,
		FromThreadID: threadID,
		ToThreadID:   params.AgentID,
		Kind:         core.AgentMessageKindMessage,
		Body:         params.Message,
		Status:       core.AgentMessageStatusQueued,
		CreatedAt:    time.Now(),
	}
	if err := s.repo.EnqueueAgentMessage(rctx.Context, msg); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to queue message: %v", err)), nil
	}

	// Wake the recipient. Without this the row waits for the target to reach
	// an agent-loop step boundary on its own, and a parent that has fanned
	// work out to sub-agents is parked in awaitLiveDetachedSpawns and reaches
	// none — so a child reporting up to a waiting parent would not be read
	// until some OTHER child happened to finish. That is the same deadlock
	// the human send path had, and the child→parent direction is the common
	// case for it.
	if s.notifier != nil {
		s.notifier.NotifyAgentMessageQueued(rctx.Context, rctx.ChatID, params.AgentID)
	}

	return NewTextResponse(fmt.Sprintf(
		"Queued for delivery to %q (%s). It will be read at that agent's next turn. "+
			"It has NOT been read yet — do not assume it has acted on this.",
		title, params.AgentID)), nil
}

type spawnRelationship int

const (
	spawnRelationshipNone spawnRelationship = iota
	spawnRelationshipChild
	spawnRelationshipParent
)

// spawnSendRelationship determines whether targetThreadID is a direct spawn
// child of callerThreadID, or callerThreadID's own parent. Any other
// relationship (sibling, cousin, unrelated) is rejected — v1 is parent<->child
// only per spec.
func spawnSendRelationship(rctx *rctx.ToolContext, repo db.Repository, callerThreadID, targetThreadID string) (spawnRelationship, error) {
	children, err := repo.ListSpawnChildren(rctx.Context, callerThreadID)
	if err != nil {
		return spawnRelationshipNone, fmt.Errorf("failed to verify agent relationship: %w", err)
	}
	for _, child := range children {
		if child.ChildThreadID != nil && *child.ChildThreadID == targetThreadID {
			return spawnRelationshipChild, nil
		}
	}

	caller, err := repo.GetThread(rctx.Context, callerThreadID)
	if err != nil || caller == nil {
		return spawnRelationshipNone, fmt.Errorf("failed to resolve calling thread: %w", err)
	}
	if caller.ParentThreadID != nil && *caller.ParentThreadID == targetThreadID {
		return spawnRelationshipParent, nil
	}

	return spawnRelationshipNone, nil
}

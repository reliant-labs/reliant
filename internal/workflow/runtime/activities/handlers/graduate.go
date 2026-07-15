// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// transitionChatMessage is the short system line posted to the chat when a
// completed workflow hands the conversation off to its transition target.
const transitionChatMessage = "Project ready — continuing in interactive chat."

// TransitionChatOnCompletion permanently switches a chat to its just-completed
// ROOT workflow's declared `transition_to` target (a workflow ref, e.g.
// "builtin://agent"). It lets a one-shot pipeline (e.g. forge-one-shot) hand the
// conversation off to a plain interactive agent so the user can keep talking
// after the pipeline ends.
//
// It reuses the existing chat workflow_name update path (mutate chat + UpdateChat)
// and is designed to be called INSIDE the same transaction/commit as the
// workflow-status write, so the switch and the completion land atomically.
//
// Safety / idempotency:
//   - No target declared, or target == the completed workflow (self-cycle
//     guard): no-op, returns "".
//   - The chat has already moved on (already transitioned, or a stale/branched
//     workflow completed): no-op. Only the chat's CURRENTLY-ACTIVE workflow
//     transitions, which makes retries and concurrent completion writers safe.
//   - Only a genuine DB failure (chat read/update) returns a non-nil error, so
//     the caller's transaction rolls back and Temporal retries the whole
//     completion atomically. A missing/undeclared/self-referential target, or a
//     definition that cannot be loaded, is a logged no-op with nil error and
//     never wedges completion.
//
// Returns the transition target ref when the chat was switched ("" when it was
// not).
func TransitionChatOnCompletion(ctx context.Context, repo db.Repository, chatID, completedWorkflowName string) (string, error) {
	if repo == nil || chatID == "" || completedWorkflowName == "" {
		return "", nil
	}

	target := loadTransitionTarget(ctx, repo, chatID, completedWorkflowName)
	if target == "" || target == completedWorkflowName {
		// No target declared, or self-cycle guard: nothing to do.
		return "", nil
	}

	chat, err := repo.GetChat(ctx, chatID)
	if err != nil {
		return "", fmt.Errorf("transition_to: load chat %s: %w", chatID, err)
	}

	// Only transition the chat's ACTIVE workflow. If it already moved on —
	// because it was already transitioned (retry) or because a stale/branched
	// workflow completed — leave it untouched. This is the idempotency guard.
	if chat.WorkflowName == nil || *chat.WorkflowName != completedWorkflowName {
		return "", nil
	}

	chat.WorkflowName = &target
	chat.UpdatedAt = time.Now().UTC()
	if err := repo.UpdateChat(ctx, chat); err != nil {
		return "", fmt.Errorf("transition_to: switch chat %s workflow %q -> %q: %w", chatID, completedWorkflowName, target, err)
	}

	logging.Info("[Transition] Chat transitioned to target workflow on completion",
		"chatID", chatID,
		"from", completedWorkflowName,
		"transition_to", target,
	)
	return target, nil
}

// loadTransitionTarget loads the completed workflow's definition and returns its
// `transition_to` field. Any load/parse failure is a logged no-op ("") —
// transition is best-effort relative to completion and must never wedge the
// status write.
func loadTransitionTarget(ctx context.Context, repo db.Repository, chatID, completedWorkflowName string) string {
	loader := NewLoadWorkflowActivity(repo)

	// Builtins resolve from the embedded FS and need no chat/project context;
	// only custom workflows require resolving user/project to locate the draft.
	var wfCtx *workflowContext
	if !strings.HasPrefix(completedWorkflowName, "builtin://") {
		if c, err := loader.resolveWorkflowContext(ctx, chatID); err == nil {
			wfCtx = c
		}
	}
	if wfCtx == nil {
		wfCtx = &workflowContext{}
	}

	def, err := loader.loadWorkflowByName(ctx, completedWorkflowName, wfCtx)
	if err != nil || def == nil {
		logging.Warn("[Transition] Could not load completed workflow definition; skipping transition",
			"workflowName", completedWorkflowName,
			"chatID", chatID,
			"error", err,
		)
		return ""
	}
	return def.GetTransitionTo()
}

// EmitTransitionMessage posts a short system line to the chat announcing the
// switch to the transition-target workflow, so the frontend reflects the change.
// This is a best-effort UI signal: SaveMessageToThread creates a chat_update that
// the frontend streams, and callers already emit a workflow-executions refetch on
// completion. Failures are logged, never surfaced — the authoritative state
// switch already committed in TransitionChatOnCompletion.
func EmitTransitionMessage(ctx context.Context, repo db.Repository, chatID, thread, workflowID, target string) {
	if repo == nil || chatID == "" || thread == "" {
		return
	}
	var wfID *string
	if workflowID != "" {
		wfID = &workflowID
	}
	if _, err := repo.SaveMessageToThread(
		ctx,
		chatID,
		thread,
		int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM),
		transitionChatMessage,
		wfID,
		nil,
		nil,
	); err != nil {
		logging.Warn("[Transition] Failed to post transition message",
			"chatID", chatID,
			"transition_to", target,
			"error", err,
		)
	}
}

// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// Every workflow and scenario tool declares its `id` parameter with the same
// optional description — struct tags must be literals, so the text is repeated
// rather than shared:
//
//	ID string `json:"id,omitempty" jsonschema:"description=Workflow UUID, slug, or name. Optional — defaults to the workflow this chat is editing."`
//
// resolveWorkflowDraft finds the workflow draft a tool call is about.
//
// The chat-context fallback is the point of this function. Before it, the only
// way a tool learned which workflow it was editing was a system message the
// frontend injected on every turn ("You are operating on workflow <uuid>"),
// which meant the tools worked in exactly one UI and nowhere else. The draft is
// already keyed by chat in the database, so the binding is a lookup rather than
// something the prompt has to carry.
//
// Resolution order:
//  1. idOrName is a UUID          -> by primary key
//  2. idOrName is anything else   -> by slug, then by name, scoped to the user
//  3. idOrName is empty           -> the draft bound to this chat
func resolveWorkflowDraft(ctx *rctx.ToolContext, repo db.Repository, idOrName string) (*db.WorkflowDraft, error) {
	if repo == nil {
		return nil, fmt.Errorf("this tool requires a database connection and is not available in daemon-only mode")
	}

	idOrName = strings.TrimSpace(idOrName)

	if idOrName != "" {
		if _, err := uuid.Parse(idOrName); err == nil {
			// GetWorkflowDraft reports a missing row as an error rather than a
			// nil draft, so a lookup failure here is indistinguishable from
			// "no such id" — either way the model needs the same advice.
			draft, err := repo.GetWorkflowDraft(ctx, idOrName)
			if err == nil && draft != nil {
				return draft, nil
			}
			return nil, workflowNotFoundError(idOrName)
		}

		userID, ok := auth.GetUserIDFromContext(ctx)
		if !ok || userID == "" {
			return nil, fmt.Errorf("unable to determine user identity")
		}

		if draft, err := repo.GetWorkflowDraftBySlug(ctx, userID, idOrName); err == nil && draft != nil {
			return draft, nil
		}
		if draft, err := repo.GetWorkflowDraftByName(ctx, userID, idOrName); err == nil && draft != nil {
			return draft, nil
		}
		return nil, workflowNotFoundError(idOrName)
	}

	if ctx == nil || ctx.ChatID == "" {
		return nil, workflowNotFoundError("")
	}

	draft, err := repo.GetWorkflowDraftByChatID(ctx, ctx.ChatID)
	if err != nil || draft == nil {
		return nil, workflowNotFoundError("")
	}
	return draft, nil
}

// workflowNotFoundError is the one message an LLM sees when no workflow could
// be resolved. It names the two tools that make progress from here, because a
// bare "not found" leaves the model retrying the same call with the same
// argument.
func workflowNotFoundError(idOrName string) error {
	if idOrName == "" {
		return fmt.Errorf(
			"no workflow is associated with this chat yet.\n\n" +
				"Call `create_workflow` to start a new one, or `list_workflows` to find an existing one " +
				"and pass its UUID, slug, or name as the `id` parameter.")
	}
	return fmt.Errorf(
		"workflow not found: %q (tried UUID, slug, and name).\n\n"+
			"Call `list_workflows` to see the workflows that exist and their exact names, "+
			"or `create_workflow` to start a new one. Omit `id` entirely to use the workflow this chat is editing.",
		idOrName)
}

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

// Every workflow and scenario tool that targets an existing workflow declares a
// REQUIRED `id` parameter (struct tags must be literals, so the text is repeated):
//
//	ID string `json:"id" jsonschema:"required,description=Workflow UUID, slug, or name (from create_workflow or list_workflows)."`
//
// resolveWorkflowDraft finds the workflow draft a tool call is about.
//
// The id is explicit on purpose. Nothing binds a chat to a workflow: no hidden
// "the workflow this chat is editing", no prompt injection. Any chat can work on
// any workflow the user owns, and a workflow editor's chat is an ordinary chat.
//
// Resolution order:
//  1. idOrName is a UUID          -> by primary key
//  2. idOrName is anything else   -> by slug, then by name, scoped to the user
//  3. idOrName is empty           -> error telling the model to pass an id
func resolveWorkflowDraft(ctx *rctx.ToolContext, repo db.Repository, idOrName string) (*db.WorkflowDraft, error) {
	if repo == nil {
		return nil, fmt.Errorf("this tool requires a database connection and is not available in daemon-only mode")
	}

	idOrName = strings.TrimSpace(idOrName)

	if idOrName != "" {
		userID, ok := auth.GetUserIDFromContext(ctx)
		if !ok || userID == "" {
			return nil, fmt.Errorf("unable to determine user identity")
		}

		if _, err := uuid.Parse(idOrName); err == nil {
			// GetWorkflowDraft reports a missing row as an error rather than a
			// nil draft, so a lookup failure here is indistinguishable from
			// "no such id" — either way the model needs the same advice. Another
			// user's draft is reported identically: a UUID must not confirm
			// that a stranger's workflow exists.
			draft, err := repo.GetWorkflowDraft(ctx, idOrName)
			if err == nil && draft != nil && draft.UserID == userID {
				return draft, nil
			}
			return nil, workflowNotFoundError(idOrName)
		}

		if draft, err := repo.GetWorkflowDraftBySlug(ctx, userID, idOrName); err == nil && draft != nil {
			return draft, nil
		}
		if draft, err := repo.GetWorkflowDraftByName(ctx, userID, idOrName); err == nil && draft != nil {
			return draft, nil
		}
		return nil, workflowNotFoundError(idOrName)
	}

	return nil, workflowNotFoundError("")
}

// workflowNotFoundError is the one message an LLM sees when no workflow could
// be resolved. It names the two tools that make progress from here, because a
// bare "not found" leaves the model retrying the same call with the same
// argument.
func workflowNotFoundError(idOrName string) error {
	if idOrName == "" {
		return fmt.Errorf(
			"`id` is required: pass the workflow's UUID, slug, or name.\n\n" +
				"Use the `id` returned by `create_workflow` for a workflow you just created, " +
				"or call `list_workflows` to find an existing one.")
	}
	return fmt.Errorf(
		"workflow not found: %q (tried UUID, slug, and name).\n\n"+
			"Call `list_workflows` to see the workflows that exist and their exact names, "+
			"or `create_workflow` to start a new one.",
		idOrName)
}

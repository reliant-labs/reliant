// Copyright (c) 2025 Reliant Labs

// Session-shape helpers for the start path: which worktree a chat belongs to,
// which directory it runs in, and the model-selector normalization that happens
// at the input boundary. Moved verbatim from ChatService; see workflows.go for
// why they live here now.
package launch

import (
	"context"
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// worktreeLookupLimit bounds the main-worktree scan in ResolveChatWorktreeID.
// A project has a handful of worktrees, not thousands, so this is a sanity
// ceiling rather than real pagination.
const worktreeLookupLimit = 1000

// ResolveChatWorktreeID resolves the worktree a chat belongs to, and is the
// single gate that keeps chat.worktree_id non-null.
//
// Every chat MUST name a resolvable worktree. The UI groups the chat list by
// worktree and drops chats whose worktree does not resolve, so a chat persisted
// with a null or dangling worktree_id runs to completion while staying
// invisible — the failure mode `reliant workflow run` hit, because the CLI has
// no worktree to name and sent none.
//
// Rather than teach every reader to tolerate the broken state, the write
// boundary refuses it:
//
//   - a supplied id must exist and belong to this project (a foreign worktree
//     would run the chat against another project's tree)
//   - an omitted id defaults to the project's main worktree, which is what
//     "run against the project itself" means
//   - a project with no main worktree is a failed precondition, never a null
//
// Callers pass the caller-supplied id (nil when absent) and get back the id to
// persist, or an error for the handler to map.
func (l *Launcher) ResolveChatWorktreeID(ctx context.Context, projectID string, requested *string) (*string, error) {
	if requested != nil && *requested != "" {
		worktree, err := l.repo.GetWorktree(ctx, *requested)
		if err != nil || worktree == nil {
			return nil, &NotFoundError{Reason: fmt.Sprintf("worktree %s not found", *requested)}
		}
		if worktree.ProjectID != projectID {
			return nil, &ValidationError{
				Kind:   ValidationInvalidArgument,
				Reason: fmt.Sprintf("worktree %s belongs to project %s, not %s", *requested, worktree.ProjectID, projectID),
			}
		}
		return requested, nil
	}

	// No worktree named: bind to the project's main checkout. The limit is
	// explicit because ListWorktrees defaults to 100 and orders by last_active,
	// which could page the main worktree out of a project with many branches —
	// and "main is missing" is reported below as a hard failure.
	worktrees, err := l.repo.ListWorktrees(ctx, db.WorktreeFilters{
		ProjectID: &projectID,
		Limit:     worktreeLookupLimit,
	})
	if err != nil {
		logging.Error("Failed to list worktrees while resolving chat worktree", "error", err, "projectID", projectID)
		return nil, &InternalError{Reason: "failed to resolve project worktree", Err: err}
	}
	for _, worktree := range worktrees {
		if worktree.IsMain {
			id := worktree.ID
			return &id, nil
		}
	}

	// ListWorktrees self-heals this for projects that predate the invariant, so
	// reaching here means the project is genuinely unusable for chats.
	return nil, &ValidationError{
		Kind:   ValidationFailedPrecondition,
		Reason: fmt.Sprintf("project %s has no main worktree; cannot create a chat without one", projectID),
	}
}

// GetEffectiveWorkingPath returns the working directory path for a chat.
// If the chat has a worktree, it returns the worktree path.
// Otherwise, it returns the project path.
// This ensures tools execute in the correct directory based on the chat's context.
func (l *Launcher) GetEffectiveWorkingPath(ctx context.Context, chat *db.Chat) string {
	// First try to get worktree path if the chat has one
	if chat.WorktreeID != nil && *chat.WorktreeID != "" {
		if worktree, err := l.repo.GetWorktree(ctx, *chat.WorktreeID); err == nil && worktree != nil {
			return worktree.Path
		} else {
			// A worktree-bound chat whose worktree can't be resolved must NOT
			// silently degrade to the project (main) checkout — that runs the
			// branch chat against the wrong tree and looks like it worked. Make
			// the failure visible; the caller still gets project path as a
			// last resort, but the log names the broken invariant.
			logging.Error("[getEffectiveWorkingPath] chat has worktree_id but worktree could not be resolved; falling back to project path",
				"chatID", chat.ID, "worktreeID", *chat.WorktreeID, "error", err)
		}
	}

	// Fall back to project path
	if chat.ProjectID != "" {
		if project, err := l.repo.GetProject(ctx, chat.ProjectID); err == nil && project != nil {
			return project.Path
		}
	}

	return ""
}

// NormalizeWorkflowSlug produces a URL-safe slug from a workflow name.
// This MUST stay in sync with generateWorkflowSlug in
// internal/workflow/runtime/activities/handlers/load_workflow.go.
func NormalizeWorkflowSlug(name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	slug = strings.ReplaceAll(slug, " ", "-")
	slug = strings.ReplaceAll(slug, "_", "-")
	return slug
}

func dbPresetToRuntimePreset(p *db.Preset) *preset.Preset {
	description := ""
	if p.Description != nil {
		description = *p.Description
	}

	result := &preset.Preset{
		Name:        p.Name,
		Description: description,
		Tag:         p.Tag,
		Params:      p.Params,
		Source:      "user",
	}
	// Normalize model params: convert any legacy string model values to {id: string} objects.
	preset.NormalizeModelParams(result)
	return result
}

// InjectSessionDaemonID adds the session's active daemon to workflow inputs.
// This is a runtime-injected input that flows through to daemon resolution.
//
// The preview URL is deliberately NOT injected here. A handoff/terminal node runs
// INSIDE the session's daemon container, which already knows its own preview URL
// (RELIANT_PREVIEW_URL_TEMPLATE env var; forge surfaces it directly for the
// forge-one-shot flow). The agent discovers it at runtime rather than having it
// threaded through the workflow input plane — that keeps preview delivery out of
// the CEL/input-schema layer entirely.
func InjectSessionDaemonID(inputs map[string]interface{}, chat *db.Chat) {
	if chat != nil && chat.ActiveDaemonID != nil && *chat.ActiveDaemonID != "" {
		inputs["session_daemon_id"] = *chat.ActiveDaemonID
	}
}

// NormalizeModelInputs walks all model-type inputs using the schema and converts
// any remaining string values to model selector objects. Legacy "model@provider"
// strings are normalized to {id, providers} at this boundary.
// This is the single boundary conversion point — everything downstream expects objects.
func NormalizeModelInputs(inputs map[string]interface{}, schemas map[string]*reliantv1.Input) {
	for name, schema := range schemas {
		if schema == nil {
			continue
		}

		switch model.GetInputType(schema) {
		case "model":
			if value, ok := inputs[name]; ok && value != nil {
				if s, ok := value.(string); ok {
					selector, normalized := NormalizeLegacyModelSelectorString(s)
					if normalized != nil {
						inputs[name] = normalized
						logging.Info("[normalizeModelInputs] Converted string model to object", "input", name, "model", selector, "providers", normalized["providers"])
					}
				}
			}
		case "group":
			groupInputs := model.GetGroupInputs(schema)
			if groupInputs != nil {
				if groupValue, ok := inputs[name].(map[string]interface{}); ok {
					NormalizeModelInputs(groupValue, groupInputs)
				}
			}
		}
	}
}

// NormalizeLegacyModelSelectorString converts a legacy "model@provider" string
// into the {id, providers} selector object everything downstream expects.
// Returns the model id and the selector, or ("", nil) for an empty input.
func NormalizeLegacyModelSelectorString(raw string) (string, map[string]interface{}) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}

	selector := map[string]interface{}{}
	modelID := raw

	if at := strings.LastIndex(raw, "@"); at > 0 && at < len(raw)-1 {
		provider := strings.TrimSpace(raw[at+1:])
		candidateID := strings.TrimSpace(raw[:at])
		if provider != "" && candidateID != "" {
			modelID = candidateID
			selector["providers"] = []interface{}{provider}
		}
	}

	selector["id"] = modelID
	return modelID, selector
}

// ExtractModelSelectors recursively extracts model selector values from workflow inputs
// using proto Input schemas. Returns a map of input path -> selector value.
func ExtractModelSelectors(inputs map[string]interface{}, schemas map[string]*reliantv1.Input, prefix string) map[string]interface{} {
	result := make(map[string]interface{})

	for name, schema := range schemas {
		if schema == nil {
			continue
		}

		path := name
		if prefix != "" {
			path = prefix + "." + name
		}

		switch model.GetInputType(schema) {
		case "model":
			if value, ok := inputs[name]; ok && value != nil {
				result[path] = value
			}
		case "group":
			groupInputs := model.GetGroupInputs(schema)
			if groupInputs != nil {
				if groupValue, ok := inputs[name].(map[string]interface{}); ok {
					nestedSelectors := ExtractModelSelectors(groupValue, groupInputs, path)
					for k, v := range nestedSelectors {
						result[k] = v
					}
				}
			}
		}
	}

	return result
}

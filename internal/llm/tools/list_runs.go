// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type ListRunsParams struct {
	ProjectID string `json:"project_id,omitempty" jsonschema:"description=Only runs in this project. Defaults to every project you own."`
	State     string `json:"state,omitempty" jsonschema:"enum=pending,enum=running,enum=paused,enum=completed,enum=failed,enum=cancelled,description=Only runs whose root workflow is in this state."`
	Limit     int    `json:"limit,omitempty" jsonschema:"description=Maximum runs to return, most recently active first (default 20, max 100)."`
}

const (
	ListRunsToolName    = "list_runs"
	listRunsDescription = `List the user's recent top-level runs (chats), most recently active first, with the state of each run's root workflow.

These are detached runs — the ones start_run creates and the ones the user starts themselves — not your own sub-agents (spawn_status lists those). Archived chats are not shown.

Each entry has the run id (use it with get_run, control_run and send_to_run), title, workflow, project and state: pending, running, paused, completed, failed or cancelled. The run you are executing in is marked.`

	listRunsDefaultLimit = 20
	listRunsMaxLimit     = 100
	// listRunsStateScan is how many chats are read when a state filter is
	// applied. State lives on the root workflow, not the chat row, so the
	// filter runs after the read; scanning a wider window keeps a filter on a
	// rare state from coming back empty only because the newest chats are
	// all another state.
	listRunsStateScan = 500
)

var validRunStates = map[string]bool{
	"pending": true, "running": true, "paused": true,
	"completed": true, "failed": true, "cancelled": true,
}

type listRunsTool struct {
	repo db.Repository
}

func NewListRunsTool(repo db.Repository) Tool {
	return NewToolWrapper[ListRunsParams, ToolResponse](&listRunsTool{repo: repo})
}

func (l *listRunsTool) Name() string        { return ListRunsToolName }
func (l *listRunsTool) Description() string { return listRunsDescription }

func (l *listRunsTool) RequiresPermission(params ListRunsParams) (bool, error) {
	return false, nil
}

// RunSummary is one run in list_runs' metadata.
type RunSummary struct {
	RunID        string `json:"run_id"`
	Title        string `json:"title"`
	Workflow     string `json:"workflow,omitempty"`
	ProjectID    string `json:"project_id"`
	State        string `json:"state"`
	LastActiveAt string `json:"last_active_at"`
	Current      bool   `json:"current,omitempty"`
}

func (l *listRunsTool) Execute(rctx *rctx.ToolContext, params ListRunsParams) (ToolResponse, error) {
	caller, errResp := resolveRunCaller(rctx, l.repo)
	if errResp != nil {
		return *errResp, nil
	}

	state := strings.ToLower(strings.TrimSpace(params.State))
	if state != "" && !validRunStates[state] {
		return NewTextErrorResponse(fmt.Sprintf("state %q is not one of pending, running, paused, completed, failed, cancelled", params.State)), nil
	}

	limit := params.Limit
	if limit <= 0 {
		limit = listRunsDefaultLimit
	}
	if limit > listRunsMaxLimit {
		limit = listRunsMaxLimit
	}

	filters := db.ChatFilters{UserID: caller.userID, ExcludeArchived: true, Limit: limit}
	if params.ProjectID != "" {
		projectID := params.ProjectID
		filters.ProjectID = &projectID
	}
	if state != "" {
		filters.Limit = listRunsStateScan
	}

	chats, err := l.repo.ListChats(rctx.Context, filters)
	if err != nil {
		return NewTextErrorResponse("Could not list runs: " + err.Error()), nil
	}

	summaries := make([]RunSummary, 0, len(chats))
	for _, chat := range chats {
		label := runStateLabel(chat)
		if state != "" && label != state {
			continue
		}
		summary := RunSummary{
			RunID:        chat.ID,
			Title:        chat.Title,
			ProjectID:    chat.ProjectID,
			State:        label,
			LastActiveAt: formatRunTime(chat.LastActive),
			Current:      chat.ID == rctx.ChatID,
		}
		if chat.WorkflowName != nil {
			summary.Workflow = *chat.WorkflowName
		}
		summaries = append(summaries, summary)
		if len(summaries) == limit {
			break
		}
	}

	if len(summaries) == 0 {
		return WithResponseMetadata(NewTextResponse("No matching runs."), map[string]any{"runs": summaries}), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d run(s), most recently active first:\n", len(summaries))
	for _, run := range summaries {
		title := run.Title
		if title == "" {
			title = "(untitled)"
		}
		marker := ""
		if run.Current {
			marker = "  <- this run (you)"
		}
		fmt.Fprintf(&sb, "- %s  [%s]  %s  workflow=%s  project=%s  active=%s%s\n",
			run.RunID, run.State, title, run.Workflow, run.ProjectID, run.LastActiveAt, marker)
	}
	return WithResponseMetadata(NewTextResponse(sb.String()), map[string]any{"runs": summaries}), nil
}

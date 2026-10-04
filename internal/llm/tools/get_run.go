// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/threads"
)

type GetRunParams struct {
	RunID string `json:"run_id" jsonschema:"required,description=The run id (chat id), from start_run or list_runs."`
}

const (
	GetRunToolName    = "get_run"
	getRunDescription = `Check on one top-level run: its state, title, workflow, when it was created and last active, and an excerpt of the last thing its agent said.

The state is the run's root workflow: pending, running, paused, completed, failed or cancelled. "completed" means the workflow reached its end, not that the work was right — read the excerpt.

Only runs the user owns can be inspected. To check on a sub-agent you spawned, use spawn_status.`

	// getRunExcerptLimit bounds the assistant excerpt in runes. It is a
	// glance, not a transcript: the human opens the chat for the rest.
	getRunExcerptLimit = 1500
)

type getRunTool struct {
	repo    db.Repository
	threads *threads.Service
}

func NewGetRunTool(repo db.Repository) Tool {
	t := &getRunTool{repo: repo}
	if repo != nil {
		t.threads = threads.NewService(repo)
	}
	return NewToolWrapper[GetRunParams, ToolResponse](t)
}

func (g *getRunTool) Name() string        { return GetRunToolName }
func (g *getRunTool) Description() string { return getRunDescription }

func (g *getRunTool) RequiresPermission(params GetRunParams) (bool, error) {
	return false, nil
}

// RunDetail is get_run's metadata.
type RunDetail struct {
	RunID        string `json:"run_id"`
	Title        string `json:"title"`
	Workflow     string `json:"workflow,omitempty"`
	ProjectID    string `json:"project_id"`
	State        string `json:"state"`
	CreatedAt    string `json:"created_at"`
	LastActiveAt string `json:"last_active_at"`
	LastMessage  string `json:"last_message,omitempty"`
}

func (g *getRunTool) Execute(rctx *rctx.ToolContext, params GetRunParams) (ToolResponse, error) {
	caller, errResp := resolveRunCaller(rctx, g.repo)
	if errResp != nil {
		return *errResp, nil
	}
	chat, errResp := resolveOwnedRun(rctx, g.repo, caller, params.RunID)
	if errResp != nil {
		return *errResp, nil
	}

	detail := RunDetail{
		RunID:        chat.ID,
		Title:        chat.Title,
		ProjectID:    chat.ProjectID,
		State:        runStateLabel(chat),
		CreatedAt:    formatRunTime(chat.CreatedAt),
		LastActiveAt: formatRunTime(chat.LastActive),
	}
	if chat.WorkflowName != nil {
		detail.Workflow = *chat.WorkflowName
	}

	// A run that has not started has no thread to read.
	if rootThread := chat.MainThreadID(); rootThread != "" {
		result, err := g.threads.LastAssistantMessage(rctx.Context, rootThread)
		if err != nil {
			return NewTextErrorResponse("Could not read the run's last message: " + err.Error()), nil
		}
		if result.Found {
			detail.LastMessage = excerpt(result.Content, getRunExcerptLimit)
		}
	}

	var sb strings.Builder
	title := detail.Title
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(&sb, "Run %s: %s\n", detail.RunID, title)
	fmt.Fprintf(&sb, "State: %s\nWorkflow: %s\nProject: %s\nCreated: %s\nLast active: %s\n",
		detail.State, detail.Workflow, detail.ProjectID, detail.CreatedAt, detail.LastActiveAt)
	if detail.LastMessage != "" {
		fmt.Fprintf(&sb, "\nLast assistant message:\n%s\n", detail.LastMessage)
	} else {
		sb.WriteString("\nNo assistant message yet.\n")
	}
	return WithResponseMetadata(NewTextResponse(sb.String()), detail), nil
}

// excerpt cuts s to at most limit runes, saying so when it cut.
func excerpt(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + fmt.Sprintf("… [truncated, %d more characters]", len(runes)-limit)
}

// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"strings"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/rundiagnostics"
	"github.com/reliant-labs/reliant/internal/threads"
)

type GetRunParams struct {
	RunID string `json:"run_id" jsonschema:"required,description=The run id (chat id), from start_run or list_runs."`
}

const (
	GetRunToolName    = "get_run"
	getRunDescription = `Check on one top-level run: its state, title, workflow, when it was created and last active, an excerpt of the last thing its agent said, and a nodes section with per-attempt status, timing and errors. Command nodes include exit codes and redacted stdout/stderr tails (up to 50 lines each). The newest 200 attempts are shown; older attempts are explicitly marked omitted.

The state is the run's root workflow: pending, running, paused, completed, failed or cancelled. "completed" means the workflow reached its end, not that the work was right — read the excerpt. A live run blocked outside itself also says what it is waiting for (usage limit with when it retries, machine, or input) and how many of its threads are working.

Only runs the user owns can be inspected. To check on a sub-agent you spawned, use spawn_status.`

	// getRunExcerptLimit bounds the assistant excerpt in runes. It is a
	// glance, not a transcript: the human opens the chat for the rest.
	getRunExcerptLimit = 1500
)

type getRunTool struct {
	repo    getRunStore
	threads *threads.Service
}

func NewGetRunTool(repo getRunStore) Tool {
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
	RunID     string `json:"run_id"`
	Title     string `json:"title"`
	Workflow  string `json:"workflow,omitempty"`
	ProjectID string `json:"project_id"`
	State     string `json:"state"`
	// Waiting is what a live run is blocked on outside itself ("waiting for
	// usage limit", "waiting for machine", "needs input"); empty otherwise.
	Waiting             string `json:"waiting,omitempty"`
	UsageLimitProvider  string `json:"usage_limit_provider,omitempty"`
	UsageLimitRetriesAt string `json:"usage_limit_retries_at,omitempty"`
	// WorkingThreads is how many of the run's threads (root and sub-agents)
	// are doing work right now.
	WorkingThreads int                    `json:"working_threads,omitempty"`
	CreatedAt      string                 `json:"created_at"`
	LastActiveAt   string                 `json:"last_active_at"`
	LastMessage    string                 `json:"last_message,omitempty"`
	Nodes          []*pb.RunNodeExecution `json:"nodes"`
	NodesTruncated bool                   `json:"nodes_truncated,omitempty"`
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
	if chat.Activity != nil {
		detail.Waiting = runWaitLabel(*chat.Activity)
	}
	if detail.Waiting == "waiting for usage limit" {
		detail.UsageLimitProvider = chat.UsageLimitProvider
		if chat.UsageLimitResumesAt != nil {
			detail.UsageLimitRetriesAt = formatRunTime(*chat.UsageLimitResumesAt)
		}
	}
	detail.WorkingThreads = chat.WorkingThreads

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

	detail.Nodes = []*pb.RunNodeExecution{}
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		rows, err := g.repo.ListRunNodeExecutions(rctx.Context, *chat.WorkflowID, rundiagnostics.RecordLimit+1)
		if err != nil {
			return NewTextErrorResponse("Could not read the run's nodes: " + err.Error()), nil
		}
		detail.NodesTruncated = len(rows) > rundiagnostics.RecordLimit
		if detail.NodesTruncated {
			rows = rows[:rundiagnostics.RecordLimit]
		}
		detail.Nodes = rundiagnostics.Nodes(rows)
	}

	var sb strings.Builder
	title := detail.Title
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(&sb, "Run %s: %s\n", detail.RunID, title)
	fmt.Fprintf(&sb, "State: %s\n", detail.State)
	if detail.Waiting != "" {
		fmt.Fprintf(&sb, "Waiting: %s", detail.Waiting)
		if detail.UsageLimitProvider != "" {
			fmt.Fprintf(&sb, " (%s)", detail.UsageLimitProvider)
		}
		if detail.UsageLimitRetriesAt != "" {
			fmt.Fprintf(&sb, ", retries by itself at %s", detail.UsageLimitRetriesAt)
		}
		sb.WriteString(". It is live: control_run cancel stops it and every sub-agent in it.\n")
	}
	if detail.WorkingThreads > 0 {
		fmt.Fprintf(&sb, "Working threads: %d\n", detail.WorkingThreads)
	}
	fmt.Fprintf(&sb, "Workflow: %s\nProject: %s\nCreated: %s\nLast active: %s\n",
		detail.Workflow, detail.ProjectID, detail.CreatedAt, detail.LastActiveAt)
	if detail.LastMessage != "" {
		fmt.Fprintf(&sb, "\nLast assistant message:\n%s\n", detail.LastMessage)
	} else {
		sb.WriteString("\nNo assistant message yet.\n")
	}
	writeRunNodes(&sb, detail.Nodes, detail.NodesTruncated)
	return WithResponseMetadata(NewTextResponse(sb.String()), detail), nil
}

func writeRunNodes(sb *strings.Builder, nodes []*pb.RunNodeExecution, truncated bool) {
	sb.WriteString("\nNodes (newest attempts first):\n")
	if len(nodes) == 0 {
		sb.WriteString("No recorded nodes yet.\n")
	}
	for _, n := range nodes {
		id := n.NodePath
		if id == "" {
			id = n.NodeId
		}
		fmt.Fprintf(sb, "- %s [%s] (workflow %s, attempt %d", id, n.Status, n.WorkflowId, n.Attempt)
		if n.LoopIteration != nil {
			fmt.Fprintf(sb, ", iteration %d", *n.LoopIteration)
		}
		if n.DurationMs != nil {
			fmt.Fprintf(sb, ", %d ms", *n.DurationMs)
		}
		if n.ExitCode != nil {
			fmt.Fprintf(sb, ", exit %d", *n.ExitCode)
		}
		sb.WriteString(")\n")
		if n.Error != "" {
			fmt.Fprintf(sb, "  Error: %s\n", n.Error)
		}
		if n.StdoutTail != "" {
			fmt.Fprintf(sb, "  stdout (tail):\n%s\n", n.StdoutTail)
		}
		if n.StderrTail != "" {
			fmt.Fprintf(sb, "  stderr (tail):\n%s\n", n.StderrTail)
		}
	}
	if truncated {
		sb.WriteString("Older node attempts omitted (response limit).\n")
	}
}

// excerpt cuts s to at most limit runes, saying so when it cut.
func excerpt(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + fmt.Sprintf("… [truncated, %d more characters]", len(runes)-limit)
}

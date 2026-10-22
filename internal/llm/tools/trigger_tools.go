// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/rctx"
)

// A workflow declares WHEN it runs in its `triggers:` block. Declaring fires
// nothing, and an agent cannot change that: enabling a workflow's triggers is
// the user's act, done in the app (Workflows -> the workflow -> Enable), which
// is where they choose the project, machine and connected accounts. An agent
// writes and tests the workflow; list_triggers shows what the user has enabled.

// TriggerLister lists the calling user's triggers. It is implemented over
// TriggerService by internal/triggertools — this package cannot import the
// gRPC services, which depend on it. There is deliberately no write method: no
// agent tool creates, enables or re-points an activation.
type TriggerLister interface {
	ListTriggers(ctx context.Context, userID string, f TriggerListFilter) ([]TriggerSummary, error)
}

// TriggerListFilter narrows list_triggers.
type TriggerListFilter struct {
	ProjectID string
	Workflow  string
}

// TriggerSummary is one trigger as the agent sees it.
type TriggerSummary struct {
	ID              string
	Name            string
	Workflow        string
	WorkflowTrigger string
	Kind            string
	Enabled         bool
	ProjectID       string
	DaemonID        string
	ConnectionID    string
	WebhookURL      string
	NextFireAt      string
	Health          string
	HealthDetail    string
	Source          string // a one-line description of what fires it
	Filter          string
}

// =============================================================================
// list_triggers
// =============================================================================

type ListTriggersParams struct {
	Workflow  string `json:"workflow,omitempty" jsonschema:"description=Only triggers that launch this workflow (a slug or builtin://<name>)."`
	ProjectID string `json:"project_id,omitempty" jsonschema:"description=Only triggers in this project."`
}

const (
	ListTriggersToolName        = "list_triggers"
	listTriggersToolDescription = `List the user's triggers: what starts runs without anyone typing — schedules, webhooks, integration events and workflow events — with each one's health.

Narrow by workflow to see a workflow's activations. Health is HEALTHY, DEGRADED, FAILING (its runs keep failing), UNKNOWN (has not fired) or BROKEN (it activates a declared trigger the workflow no longer has, or one changed in a way it cannot follow — the detail says which; fix the workflow, then the user re-enables it in the app).`
)

type listTriggersTool struct {
	repo   runCallerStore
	lister TriggerLister
}

func NewListTriggersTool(repo runCallerStore, lister TriggerLister) Tool {
	return NewToolWrapper[ListTriggersParams, ToolResponse](&listTriggersTool{lister: lister, repo: repo})
}

func (l *listTriggersTool) Name() string        { return ListTriggersToolName }
func (l *listTriggersTool) Description() string { return listTriggersToolDescription }

func (l *listTriggersTool) RequiresPermission(ListTriggersParams) (bool, error) { return false, nil }

func (l *listTriggersTool) Execute(rctx *rctx.ToolContext, params ListTriggersParams) (ToolResponse, error) {
	caller, errResp := resolveRunCaller(rctx, l.repo)
	if errResp != nil {
		return *errResp, nil
	}
	if l.lister == nil {
		return NewTextErrorResponse("Listing triggers is not available in this runtime."), nil
	}
	list, err := l.lister.ListTriggers(rctx.Context, caller.userID, TriggerListFilter{
		ProjectID: strings.TrimSpace(params.ProjectID), Workflow: strings.TrimSpace(params.Workflow),
	})
	if err != nil {
		return NewTextErrorResponse("Could not list triggers: " + err.Error()), nil
	}
	if len(list) == 0 {
		if params.Workflow != "" {
			return NewTextResponse(fmt.Sprintf("Nothing runs %s on its own. If it declares triggers, the user turns them on in the app: Workflows -> the workflow -> Enable.", params.Workflow)), nil
		}
		return NewTextResponse("The user has no triggers."), nil
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	var b strings.Builder
	fmt.Fprintf(&b, "%d trigger(s):\n", len(list))
	for _, t := range list {
		enabled := "enabled"
		if !t.Enabled {
			enabled = "paused"
		}
		fmt.Fprintf(&b, "\n- %s (id %s) — %s, %s, health %s\n", t.Name, t.ID, t.Kind, enabled, t.Health)
		if t.WorkflowTrigger != "" {
			fmt.Fprintf(&b, "  activates %q declared by %s\n", t.WorkflowTrigger, t.Workflow)
		} else {
			fmt.Fprintf(&b, "  launches %s (source written on the trigger)\n", t.Workflow)
		}
		fmt.Fprintf(&b, "  fires on: %s\n", t.Source)
		if t.Filter != "" {
			fmt.Fprintf(&b, "  filter: %s\n", t.Filter)
		}
		if t.NextFireAt != "" {
			fmt.Fprintf(&b, "  next fire: %s\n", t.NextFireAt)
		}
		if t.WebhookURL != "" {
			fmt.Fprintf(&b, "  webhook: %s\n", t.WebhookURL)
		}
		if t.HealthDetail != "" {
			fmt.Fprintf(&b, "  %s: %s\n", strings.ToLower(t.Health), t.HealthDetail)
		}
	}
	return WithResponseMetadata(NewTextResponse(strings.TrimRight(b.String(), "\n")), list), nil
}

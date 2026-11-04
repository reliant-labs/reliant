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
// nothing. Activation management tools require per-call interactive approval;
// unattended runs cannot create standing authority. list_triggers reads the
// resulting activations; fire_trigger runs one without changing configuration.

// TriggerLister lists the calling user's triggers. It is implemented over
// TriggerService by internal/triggertools — this package cannot import the
// gRPC services, which depend on it. Writes use the separate approval-backed
// ActivationManager interface.
type TriggerLister interface {
	ListTriggers(ctx context.Context, userID string, f TriggerListFilter) ([]TriggerSummary, error)
}

// TriggerFirer runs an existing activation once as its owner. It cannot create,
// enable or change a trigger.
type TriggerFirer interface {
	FireTrigger(ctx context.Context, userID, triggerID string) (string, error)
}

// TriggerEventLister reads firing history without exposing event payloads.
type TriggerEventLister interface {
	ListTriggerEvents(ctx context.Context, userID, triggerID string, limit int, pageToken string) (TriggerEventPage, error)
}

type TriggerEventSummary struct {
	FireWorkflowID string `json:"fire_workflow_id,omitempty"`
	ID             string `json:"id"`
	OccurredAt     string `json:"occurred_at"`
	Outcome        string `json:"outcome"`
	Detail         string `json:"detail,omitempty"`
	ChatID         string `json:"chat_id,omitempty"`
	RunState       string `json:"run_state,omitempty"`
	// Definition is which workflow definition the firing resolved.
	Definition *TriggerEventDefinition `json:"definition,omitempty"`
}

// TriggerEventDefinition is the workflow definition one firing resolved: the
// file and its version, the machine and git commit it was synced from, and
// why it could not run when the firing was refused.
type TriggerEventDefinition struct {
	Summary     string `json:"summary"`
	Workflow    string `json:"workflow"`
	Source      string `json:"source,omitempty"`
	Path        string `json:"path,omitempty"`
	Machine     string `json:"machine,omitempty"`
	DaemonID    string `json:"daemon_id,omitempty"`
	Repo        string `json:"repo,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Commit      string `json:"commit,omitempty"`
	Dirty       bool   `json:"dirty,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
	Error       string `json:"error,omitempty"`
	CatalogPin  string `json:"catalog_pin,omitempty"`
}

type TriggerEventPage struct {
	Events        []TriggerEventSummary `json:"events"`
	NextPageToken string                `json:"next_page_token,omitempty"`
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
	DaemonName      string
	MachineState    string
	NoMachine       bool
	ConnectionID    string
	WebhookURL      string
	NextFireAt      string
	Health          string
	HealthDetail    string
	Source          string // a one-line description of what fires it
	Filter          string
	// Definition is where its runs read the workflow from ("project workflow
	// at control-plane/.reliant/workflows/release.yaml on machine cicd"), and
	// DefinitionWarnings what about that the owner should act on.
	Definition         string
	DefinitionWarnings []string
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
	listTriggersToolDescription = `List the user's triggers: what starts runs without anyone typing — schedules, webhooks, integration events and workflow events — with each one's machine (name, id and current registry state) and health. Failed launched runs include the failing node's error.

Narrow by workflow to see a workflow's activations. Health is HEALTHY, DEGRADED, FAILING (its runs keep failing), UNKNOWN (has not fired) or BROKEN (it activates a declared trigger the workflow no longer has, or one changed in a way it cannot follow — the detail says which; every firing is refused and recorded as failed until the workflow is fixed, and pause_trigger turns it off). A DEGRADED detail that ends "N later run(s) succeeded" is a past failure the trigger has recovered from.`
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
		if t.NoMachine {
			b.WriteString("  machine: none (no-machine activation)\n")
		} else {
			state := t.MachineState
			if state == "" {
				state = "unknown"
			}
			fmt.Fprintf(&b, "  machine: %s (id %s), state %s\n", t.DaemonName, t.DaemonID, state)
		}
		fmt.Fprintf(&b, "  fires on: %s\n", t.Source)
		if t.Definition != "" {
			fmt.Fprintf(&b, "  definition: %s\n", t.Definition)
		}
		for _, w := range t.DefinitionWarnings {
			fmt.Fprintf(&b, "  warning: %s\n", w)
		}
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
		if t.Health == "BROKEN" && t.Enabled {
			// Enabled reads as live; a broken activation launches nothing.
			b.WriteString("  every firing is refused and recorded as failed (nothing launches) until the workflow declares this trigger again; pause_trigger stops the attempts\n")
		}
	}
	return WithResponseMetadata(NewTextResponse(strings.TrimRight(b.String(), "\n")), list), nil
}

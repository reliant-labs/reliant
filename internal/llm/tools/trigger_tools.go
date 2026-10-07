// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// A workflow declares WHEN it runs in its `triggers:` block. Declaring fires
// nothing: a user ACTIVATES a declared trigger, choosing as whom (always the
// calling user), in which project, on which daemon and through which
// connection. activate_trigger is that act for the agent; list_triggers shows
// what is active.

// TriggerActivator activates and lists the calling user's triggers. It is
// implemented over TriggerService by internal/triggertools — this package
// cannot import the gRPC services, which depend on it.
type TriggerActivator interface {
	ActivateTrigger(ctx context.Context, req ActivateTriggerRequest) (ActivatedTrigger, error)
	ListTriggers(ctx context.Context, userID string, f TriggerListFilter) ([]TriggerSummary, error)
}

// ActivateTriggerRequest is one activation. The owner is the calling chat's
// user, never a parameter.
type ActivateTriggerRequest struct {
	OwnerUserID     string
	Workflow        string
	WorkflowTrigger string
	Name            string
	ProjectID       string
	WorktreeID      string
	DaemonID        string
	// NoMachine is inherited from a calling chat that has no machine, exactly
	// as start_run does: such a chat has no daemon to pin, so its automations
	// run server-only (CreateTrigger refuses one whose workflow needs a
	// machine, so this never weakens the check).
	NoMachine       bool
	ConnectionID    string
	Message         string
	Params          map[string]any
	Presets         map[string]string
	Disabled        bool
	NotifyOnSuccess bool
}

// ActivatedTrigger is what an activation produced.
type ActivatedTrigger struct {
	Summary TriggerSummary
	// WebhookToken is set for a webhook trigger, and only here: it is shown
	// once and stored hashed.
	WebhookToken string
	// WebhookURLWithToken is the URL a sender that cannot set headers posts
	// to (it embeds the token).
	WebhookURLWithToken string
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
// activate_trigger
// =============================================================================

type ActivateTriggerParams struct {
	Workflow     string            `json:"workflow" jsonschema:"required,description=The workflow that declares the trigger: a slug from list_workflows or builtin://<name>."`
	Trigger      string            `json:"trigger" jsonschema:"required,description=The declared trigger's name (triggers[].name in the workflow's YAML)."`
	Message      string            `json:"message,omitempty" jsonschema:"description=The prompt each run it starts begins with. Say what to do with the event; the event itself is attached to the run as trigger.payload. Optional when the declared trigger has a prompt: (it is used instead); set it to override that prompt."`
	Name         string            `json:"name,omitempty" jsonschema:"description=A name for this activation, unique per project. Defaults to '<workflow> / <trigger>'."`
	ProjectID    string            `json:"project_id,omitempty" jsonschema:"description=Project the runs execute in. Defaults to the project of the run you are executing in."`
	DaemonID     string            `json:"daemon_id,omitempty" jsonschema:"description=Daemon every run executes its tools on. Defaults to the daemon of the run you are executing in."`
	ConnectionID string            `json:"connection_id,omitempty" jsonschema:"description=Integration triggers only: which of the user's connections to listen through. Defaults to the user's default connection for that integration."`
	WorktreeID   string            `json:"worktree_id,omitempty" jsonschema:"description=Worktree the runs execute in. Defaults to the project's main worktree."`
	Params       map[string]any    `json:"params,omitempty" jsonschema:"description=Workflow inputs set on every run. May not set an input the declaration already maps from the event."`
	Presets      map[string]string `json:"presets,omitempty" jsonschema:"description=Preset selections, keyed by preset slot."`
	Disabled     bool              `json:"disabled,omitempty" jsonschema:"description=Create it paused. Default: enabled (it fires as soon as it is created)."`
	Notify       bool              `json:"notify_on_complete,omitempty" jsonschema:"description=Notify the user when a run it starts completes. Failures always notify."`
}

// ActivateTriggerResponseMetadata is the machine-readable half of the result.
type ActivateTriggerResponseMetadata struct {
	TriggerID    string `json:"trigger_id"`
	Kind         string `json:"kind"`
	WebhookURL   string `json:"webhook_url,omitempty"`
	WebhookToken string `json:"webhook_token,omitempty"`
}

const (
	ActivateTriggerToolName        = "activate_trigger"
	activateTriggerToolDescription = `Activate a trigger a workflow declares, for the user you are working for, so the workflow starts running on it.

A workflow's YAML says WHEN it runs, in its triggers: block (a schedule, a webhook, an integration event, or another workflow's run finishing/failing/blocking). Declaring one fires nothing. Activating it says AS WHOM and WHERE: the user, a project, the daemon whose tools the runs use, and for an integration trigger the connection it listens through.

The declaration stays the source of truth: its source, filter and inputs are re-read from the workflow every time it fires, so editing the workflow's triggers: block changes this activation too. If the declaration is removed or renamed, the activation reports BROKEN health (see list_triggers) and fires nothing until it is restored.

Runs it starts are unattended: nobody answers questions or approvals.

RETURNS the trigger id; for a webhook trigger, its URL and token (the token is shown ONCE — give it to the user now).

Before activating, check the workflow validates (get_workflow) and, for an integration trigger, that the user has a connection for it.`
)

type activateTriggerTool struct {
	repo      db.Repository
	activator TriggerActivator
}

func NewActivateTriggerTool(repo db.Repository, activator TriggerActivator) Tool {
	return NewToolWrapper[ActivateTriggerParams, ToolResponse](&activateTriggerTool{repo: repo, activator: activator})
}

func (a *activateTriggerTool) Name() string        { return ActivateTriggerToolName }
func (a *activateTriggerTool) Description() string { return activateTriggerToolDescription }

func (a *activateTriggerTool) RequiresPermission(ActivateTriggerParams) (bool, error) {
	return false, nil
}

func (a *activateTriggerTool) Execute(rctx *rctx.ToolContext, params ActivateTriggerParams) (ToolResponse, error) {
	caller, errResp := resolveRunCaller(rctx, a.repo)
	if errResp != nil {
		return *errResp, nil
	}
	if a.activator == nil {
		return NewTextErrorResponse("Activating triggers is not available in this runtime."), nil
	}
	workflow := strings.TrimSpace(params.Workflow)
	declared := strings.TrimSpace(params.Trigger)
	if workflow == "" || declared == "" {
		return NewTextErrorResponse("workflow and trigger are required: name the workflow and the trigger it declares (triggers[].name)."), nil
	}
	req := ActivateTriggerRequest{
		OwnerUserID:     caller.userID,
		Workflow:        workflow,
		WorkflowTrigger: declared,
		Name:            strings.TrimSpace(params.Name),
		ProjectID:       params.ProjectID,
		WorktreeID:      params.WorktreeID,
		DaemonID:        params.DaemonID,
		ConnectionID:    params.ConnectionID,
		Message:         params.Message,
		Params:          params.Params,
		Presets:         params.Presets,
		Disabled:        params.Disabled,
		NotifyOnSuccess: params.Notify,
	}
	if req.Name == "" {
		req.Name = workflow + " / " + declared
	}
	if req.ProjectID == "" {
		req.ProjectID = caller.chat.ProjectID
	}
	if req.DaemonID == "" {
		if caller.chat.NoMachine {
			req.NoMachine = true
		} else {
			req.DaemonID = callerDaemonID(caller.chat)
		}
	}

	activated, err := a.activator.ActivateTrigger(rctx.Context, req)
	if err != nil {
		return NewTextErrorResponse("Could not activate the trigger: " + err.Error()), nil
	}
	return activatedTriggerResponse(activated), nil
}

func activatedTriggerResponse(a ActivatedTrigger) ToolResponse {
	s := a.Summary
	var b strings.Builder
	state := "enabled — it fires from now on"
	if !s.Enabled {
		state = "created paused — it fires nothing until enabled"
	}
	fmt.Fprintf(&b, "Activated %q of workflow %s as trigger %s (%s trigger, %s).\n", s.WorkflowTrigger, s.Workflow, s.ID, s.Kind, state)
	fmt.Fprintf(&b, "Fires on: %s\n", s.Source)
	if s.Filter != "" {
		fmt.Fprintf(&b, "Filter: %s\n", s.Filter)
	}
	if s.NextFireAt != "" {
		fmt.Fprintf(&b, "Next fire: %s\n", s.NextFireAt)
	}
	if s.ConnectionID != "" {
		fmt.Fprintf(&b, "Listens through connection %s.\n", s.ConnectionID)
	}
	meta := ActivateTriggerResponseMetadata{TriggerID: s.ID, Kind: s.Kind, WebhookURL: s.WebhookURL, WebhookToken: a.WebhookToken}
	if a.WebhookToken != "" {
		b.WriteString("\nWebhook (the token is shown ONCE; give it to the user now):\n")
		if strings.HasPrefix(s.WebhookURL, "/") {
			fmt.Fprintf(&b, "- path: %s (this server does not know its public URL; prefix it with the reliant API's address)\n", s.WebhookURL)
		} else {
			fmt.Fprintf(&b, "- URL: %s  — POST with header Authorization: Bearer <token>\n", s.WebhookURL)
		}
		fmt.Fprintf(&b, "- token: %s\n", a.WebhookToken)
		if a.WebhookURLWithToken != "" {
			fmt.Fprintf(&b, "- for senders that cannot set headers (Zapier): %s\n", a.WebhookURLWithToken)
		}
	}
	fmt.Fprintf(&b, "\nCheck on it with list_triggers(workflow=%q).", s.Workflow)
	return WithResponseMetadata(NewTextResponse(b.String()), meta)
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

Narrow by workflow to see a workflow's activations. Health is HEALTHY, DEGRADED, FAILING (its runs keep failing), UNKNOWN (has not fired) or BROKEN (it activates a declared trigger the workflow no longer has, or one changed in a way it cannot follow — the detail says which; fix the workflow or activate it again).`
)

type listTriggersTool struct {
	repo      db.Repository
	activator TriggerActivator
}

func NewListTriggersTool(repo db.Repository, activator TriggerActivator) Tool {
	return NewToolWrapper[ListTriggersParams, ToolResponse](&listTriggersTool{activator: activator, repo: repo})
}

func (l *listTriggersTool) Name() string        { return ListTriggersToolName }
func (l *listTriggersTool) Description() string { return listTriggersToolDescription }

func (l *listTriggersTool) RequiresPermission(ListTriggersParams) (bool, error) { return false, nil }

func (l *listTriggersTool) Execute(rctx *rctx.ToolContext, params ListTriggersParams) (ToolResponse, error) {
	caller, errResp := resolveRunCaller(rctx, l.repo)
	if errResp != nil {
		return *errResp, nil
	}
	if l.activator == nil {
		return NewTextErrorResponse("Listing triggers is not available in this runtime."), nil
	}
	list, err := l.activator.ListTriggers(rctx.Context, caller.userID, TriggerListFilter{
		ProjectID: strings.TrimSpace(params.ProjectID), Workflow: strings.TrimSpace(params.Workflow),
	})
	if err != nil {
		return NewTextErrorResponse("Could not list triggers: " + err.Error()), nil
	}
	if len(list) == 0 {
		if params.Workflow != "" {
			return NewTextResponse(fmt.Sprintf("No triggers launch %s. activate_trigger activates one it declares.", params.Workflow)), nil
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

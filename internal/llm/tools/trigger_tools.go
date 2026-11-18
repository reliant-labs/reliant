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
	Health    string `json:"health,omitempty" jsonschema:"enum=UNHEALTHY,enum=BROKEN,enum=FAILING,enum=DEGRADED,enum=NEEDS_SETUP,enum=UNKNOWN,enum=HEALTHY,description=Only triggers in this health. UNHEALTHY is BROKEN or FAILING or DEGRADED or NEEDS_SETUP."`
	Limit     int    `json:"limit,omitempty" jsonschema:"description=Page the listing, this many triggers per page (at most 200). Without limit or page_token, list_triggers returns every trigger."`
	PageToken string `json:"page_token,omitempty" jsonschema:"description=The next_page_token a previous list_triggers returned, to read the next page. Pass the same workflow, project_id, health and limit."`
}

const (
	// listTriggersPage is a paged list's page size when the caller gives
	// page_token but no limit, and the size past which an unpaged list says
	// it can be paged; listTriggersMax bounds one page.
	listTriggersPage = 20
	listTriggersMax  = 200
)

// triggerHealthOrder lists unhealthy triggers first: the ones an agent asked
// "what is wrong" should see without paging. Names not here (a status added
// later) sort with UNKNOWN.
var triggerHealthOrder = map[string]int{"BROKEN": 0, "FAILING": 1, "NEEDS_SETUP": 2, "DEGRADED": 3, "UNKNOWN": 4, "HEALTHY": 5}

func triggerHealthRank(health string) int {
	if rank, ok := triggerHealthOrder[health]; ok {
		return rank
	}
	return triggerHealthOrder["UNKNOWN"]
}

// matchesTriggerHealth reports whether a trigger in health passes the
// health filter want ("" passes everything).
func matchesTriggerHealth(want, health string) bool {
	switch want {
	case "":
		return true
	case "UNHEALTHY":
		return triggerHealthRank(health) < triggerHealthOrder["UNKNOWN"]
	default:
		return health == want
	}
}

const (
	ListTriggersToolName        = "list_triggers"
	listTriggersToolDescription = `List the user's triggers: what starts runs without anyone typing — schedules, webhooks, integration events and workflow events — with each one's machine (name, id and current registry state) and health. Failed launched runs include the failing node's error.

Narrow by workflow to see a workflow's activations, or by health (UNHEALTHY for everything that needs attention). Unhealthy triggers are listed first. Every trigger is listed unless you page: pass limit (triggers per page), and a page that has more ends with a next_page_token to pass back as page_token. Health is HEALTHY, DEGRADED, FAILING (its runs keep failing), UNKNOWN (has not fired) or BROKEN (it activates a declared trigger the workflow no longer has, or one changed in a way it cannot follow — the detail says which; every firing is refused and recorded as failed until the workflow is fixed, and pause_trigger turns it off). A DEGRADED detail that ends "N later run(s) succeeded" is a past failure the trigger has recovered from.`
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
	health := strings.ToUpper(strings.TrimSpace(params.Health))
	if _, known := triggerHealthOrder[health]; health != "" && health != "UNHEALTHY" && !known {
		return NewTextErrorResponse(fmt.Sprintf("Unknown health %q: use UNHEALTHY, BROKEN, FAILING, DEGRADED, NEEDS_SETUP, UNKNOWN or HEALTHY.", params.Health)), nil
	}
	// Paging is opt-in: a call without limit or page_token lists every
	// trigger, as it did before paging existed, so no caller is silently cut
	// short.
	paged := params.Limit != 0 || strings.TrimSpace(params.PageToken) != ""
	limit := params.Limit
	switch {
	case limit < 0:
		return NewTextErrorResponse("limit must not be negative"), nil
	case limit == 0:
		limit = listTriggersPage
	case limit > listTriggersMax:
		limit = listTriggersMax
	}
	offset, err := decodeOffsetToken(params.PageToken)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	all, err := l.lister.ListTriggers(rctx.Context, caller.userID, TriggerListFilter{
		ProjectID: strings.TrimSpace(params.ProjectID), Workflow: strings.TrimSpace(params.Workflow),
	})
	if err != nil {
		return NewTextErrorResponse("Could not list triggers: " + err.Error()), nil
	}
	if len(all) == 0 {
		if params.Workflow != "" {
			return NewTextResponse(fmt.Sprintf("Nothing runs %s on its own. If it declares triggers, the user turns them on in the app: Workflows -> the workflow -> Enable.", params.Workflow)), nil
		}
		return NewTextResponse("The user has no triggers."), nil
	}
	counts := map[string]int{}
	list := make([]TriggerSummary, 0, len(all))
	for _, t := range all {
		counts[t.Health]++
		if matchesTriggerHealth(health, t.Health) {
			list = append(list, t)
		}
	}
	if len(list) == 0 {
		return NewTextResponse(fmt.Sprintf("None of the %d trigger(s) is %s (%s).", len(all), health, triggerHealthCounts(counts))), nil
	}
	// Unhealthy first, then by name; the id makes the order total, so a
	// page_token names the same place on every call.
	sort.SliceStable(list, func(i, j int) bool {
		if ri, rj := triggerHealthRank(list[i].Health), triggerHealthRank(list[j].Health); ri != rj {
			return ri < rj
		}
		if list[i].Name != list[j].Name {
			return list[i].Name < list[j].Name
		}
		return list[i].ID < list[j].ID
	})
	total := len(list)
	if offset >= total {
		return NewTextErrorResponse(fmt.Sprintf("page_token is past the end: %d trigger(s) match; list again without page_token.", total)), nil
	}
	page := list
	next := ""
	if paged {
		page = list[offset:min(offset+limit, total)]
		if offset+len(page) < total {
			next = encodeOffsetToken(offset + len(page))
		}
	}
	var b strings.Builder
	if offset == 0 && next == "" {
		fmt.Fprintf(&b, "%d trigger(s)", total)
	} else {
		fmt.Fprintf(&b, "%d trigger(s), showing %d-%d", total, offset+1, offset+len(page))
	}
	if health != "" {
		fmt.Fprintf(&b, " %s of %d", health, len(all))
	}
	fmt.Fprintf(&b, " (%s):\n", triggerHealthCounts(counts))
	for _, t := range page {
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
	if next != "" {
		fmt.Fprintf(&b, "\nnext_page_token: %s\n%d more: call list_triggers again with page_token %q (same workflow, project_id, health and limit).\n", next, total-offset-len(page), next)
	} else if !paged && total > listTriggersPage {
		fmt.Fprintf(&b, "\nAll %d triggers are listed. To read fewer at a time, pass limit (then page_token), or narrow with health (UNHEALTHY) or workflow.\n", total)
	}
	return WithResponseMetadata(NewTextResponse(strings.TrimRight(b.String(), "\n")), page), nil
}

// triggerHealthCounts is "2 BROKEN, 5 HEALTHY": every health present, in
// listing order.
func triggerHealthCounts(counts map[string]int) string {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if ri, rj := triggerHealthRank(names[i]), triggerHealthRank(names[j]); ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = fmt.Sprintf("%d %s", counts[name], name)
	}
	return strings.Join(parts, ", ")
}

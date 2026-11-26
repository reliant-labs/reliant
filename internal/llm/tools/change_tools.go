// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/ctxkeys"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// list_changes, get_change and manage_change run Forge Changes — remote
// config, gradual rollouts and A/B tests — through the control plane's
// ChangesService, as the user: the same API, rules and audit trail as the
// console's Experiments page (forge docs/proposals/changes.md).
//
// Agents may draft, preview, read and stop. What widens who gets a change —
// a launch, a ramp UP, a roll-out — raises an approval card in an
// interactive chat and runs only once the user approves exactly that action,
// change, revision and percentage. The control plane enforces a person's
// approval for production on its own as well: a token acting for the user
// (what this tool holds) is refused there with reason approval_required,
// which the tool turns into "ask the user to approve it in the Experiments
// page, or for the emailed code". Reducing exposure — stop, end_run, a ramp
// down — never waits for anyone.
//
// An unattended run is handed manage_change only when its step names it
// (unattended.go), and may then only reduce exposure, draft and preview.

const (
	ToolListChanges  = "list_changes"
	ToolGetChange    = "get_change"
	ToolManageChange = "manage_change"
)

// ChangeAudience is who is eligible: the evaluation context's Attribute must
// be one of In. Nil is everyone.
type ChangeAudience struct {
	Attribute string   `json:"attribute"`
	In        []string `json:"in"`
}

// ChangeSplitArm is one variant's share of an experiment, in basis points.
type ChangeSplitArm struct {
	Variant  string `json:"variant"`
	WeightBP int32  `json:"weight_bp"`
}

// ChangeMetric is an experiment's success metric.
type ChangeMetric struct {
	Event           string `json:"event"`
	Prop            string `json:"prop,omitempty"`
	PropValue       string `json:"prop_value,omitempty"`
	WindowHours     int32  `json:"window_hours,omitempty"`
	MinSamplePerArm int64  `json:"min_sample_per_arm,omitempty"`
	ControlVariant  string `json:"control_variant,omitempty"`
}

// ChangePreviewEntry is one previewing unit as stored: hashed, never raw.
type ChangePreviewEntry struct {
	UnitHash string `json:"unit_hash"`
	Variant  string `json:"variant"`
	Label    string `json:"label,omitempty"`
}

// ChangePreviewInput is one previewing unit as a caller names it. The raw
// unit id is hashed by the control plane and never stored.
type ChangePreviewInput struct {
	UnitID  string `json:"unit_id"`
	Variant string `json:"variant"`
	Label   string `json:"label,omitempty"`
}

// ChangeVariant is one declared variant and its JSON value.
type ChangeVariant struct {
	Name      string `json:"name"`
	ValueJSON string `json:"value_json"`
}

// ChangeDeclaration is what the environment's current release declares for
// a change's key (the project's changes.yaml).
type ChangeDeclaration struct {
	Type           string          `json:"type"`
	DefaultVariant string          `json:"default_variant"`
	Variants       []ChangeVariant `json:"variants"`
	Description    string          `json:"description,omitempty"`
}

// ChangeRunSummary is a change's open run: one randomization epoch.
type ChangeRunSummary struct {
	ID             string    `json:"id"`
	Kind           string    `json:"kind"`
	StartedAt      time.Time `json:"started_at"`
	StartedBy      string    `json:"started_by,omitempty"`
	ApprovedBy     string    `json:"approved_by,omitempty"`
	ApprovalMethod string    `json:"approval_method,omitempty"`
}

// ChangeSummary is one change in one environment, as the agent sees it.
type ChangeSummary struct {
	ID              string `json:"id"`
	Key             string `json:"key"`
	EnvironmentID   string `json:"environment_id"`
	EnvironmentName string `json:"environment_name,omitempty"`
	Description     string `json:"description,omitempty"`
	// Mode is off, preview, everyone, rollout or experiment.
	Mode string `json:"mode"`
	// Unit is user, account or visitor.
	Unit             string               `json:"unit"`
	Audience         *ChangeAudience      `json:"audience,omitempty"`
	PercentBP        int32                `json:"percent_bp"`
	Split            []ChangeSplitArm     `json:"split,omitempty"`
	EveryoneVariant  string               `json:"everyone_variant,omitempty"`
	RolloutVariant   string               `json:"rollout_variant,omitempty"`
	Preview          []ChangePreviewEntry `json:"preview,omitempty"`
	Metric           *ChangeMetric        `json:"metric,omitempty"`
	Run              *ChangeRunSummary    `json:"run,omitempty"`
	Revision         int64                `json:"revision"`
	Declaration      *ChangeDeclaration   `json:"declaration,omitempty"`
	ApprovalRequired bool                 `json:"approval_required"`
	UpdatedAt        time.Time            `json:"updated_at,omitempty"`
}

// ChangeEvent is one entry of a change's audit trail.
type ChangeEvent struct {
	Revision       int64     `json:"revision"`
	ActorKind      string    `json:"actor_kind"`
	ActorID        string    `json:"actor_id"`
	Action         string    `json:"action"`
	DiffJSON       string    `json:"diff_json,omitempty"`
	RunID          string    `json:"run_id,omitempty"`
	ApprovalMethod string    `json:"approval_method,omitempty"`
	ApprovedBy     string    `json:"approved_by,omitempty"`
	At             time.Time `json:"at"`
}

// ChangeFilter narrows list_changes. Empty fields fall back to the calling
// chat's project: its org and its forge project name.
type ChangeFilter struct {
	OrgID       string
	Project     string
	Environment string
}

// ChangeCreate drafts a new change: Environment is a name of the project's
// environments, or an environment id.
type ChangeCreate struct {
	OrgID       string
	Project     string
	Environment string
	Key         string
	Description string
	Unit        string
}

// ChangeOp is one mutation of an existing change. Action is update_draft,
// set_preview, launch, ramp, roll_out, stop or end_run; the fields each
// action reads mirror the ChangesService request of that name.
type ChangeOp struct {
	Action   string
	ChangeID string
	// ExpectedRevision is the revision the caller read. 0 sends none, which
	// only stop accepts.
	ExpectedRevision int64
	// update_draft: nil leaves a field as it is.
	Description   *string
	Unit          string
	Audience      *ChangeAudience
	ClearAudience bool
	Split         []ChangeSplitArm
	Metric        *ChangeMetric
	ClearMetric   bool
	// set_preview.
	Preview  []ChangePreviewInput
	Activate bool
	// launch, ramp, roll_out.
	PercentBP  int32
	Variant    string
	Everyone   bool
	StepUpCode string
}

// ChangeApprovalCode is where RequestChangeApproval sent the one-time code.
type ChangeApprovalCode struct {
	SentTo    string
	ExpiresAt time.Time
}

// ChangeAPIError is a refusal from ChangesService: its Connect code
// ("failed_precondition") and the stable reason its proto names
// ("change_revision_conflict", "approval_required", "change_not_declared",
// "variant_not_declared", "approval_unavailable", "change_exists").
type ChangeAPIError struct {
	Code    string
	Reason  string
	Message string
}

func (e *ChangeAPIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Reason != "" {
		return e.Code + ": " + e.Reason
	}
	return e.Code
}

// ErrorClass implements svcerr.Classed, so the log and span layers grade a
// ChangesService refusal by who must act on it, as the control plane did:
// without it every refusal (a change id that does not exist, an approval the
// environment requires) was an unrecognised error, a server fault at ERROR.
// permission_denied, unauthenticated and invalid_argument are user errors only
// with a reason; without one they refuse our request or our bearer, which is
// ours to fix. Any other code has no opinion, which svcerr reads as a server
// fault.
func (e *ChangeAPIError) ErrorClass() svcerr.Class {
	switch e.Code {
	case "canceled":
		return svcerr.ClassCanceled
	case "failed_precondition", "not_found", "already_exists", "out_of_range", "resource_exhausted":
		return svcerr.ClassUser
	case "permission_denied", "unauthenticated", "invalid_argument":
		if e.Reason != "" {
			return svcerr.ClassUser
		}
	}
	return svcerr.ClassNone
}

// ChangeController reads and acts on Forge Changes as the user. internal/app
// implements it over the control plane, which this package may not import.
type ChangeController interface {
	// ListChanges lists the changes f selects; projectID is the calling
	// chat's project, which supplies the org and forge project f omits.
	ListChanges(ctx context.Context, userID, projectID string, f ChangeFilter) ([]ChangeSummary, error)
	// GetChange is one change and its most recent audit events, newest first.
	GetChange(ctx context.Context, userID, changeID string, events int) (ChangeSummary, []ChangeEvent, error)
	CreateChange(ctx context.Context, userID, projectID string, in ChangeCreate) (ChangeSummary, error)
	ApplyChange(ctx context.Context, userID string, op ChangeOp) (ChangeSummary, error)
	RequestChangeApproval(ctx context.Context, userID, changeID string) (ChangeApprovalCode, error)
	// GetChangeReadout is one experiment run's results; runID "" lets the
	// control plane pick (the open experiment run, else the latest).
	GetChangeReadout(ctx context.Context, userID, changeID, runID string) (ChangeReadout, error)
}

const changesUnavailable = "Forge Changes is not available in this runtime (it needs the hosted control plane)."

// =============================================================================
// list_changes
// =============================================================================

type ListChangesParams struct {
	Project     string `json:"project,omitempty" jsonschema:"description=The forge project (forge.yaml name). Default: the calling chat's project."`
	Environment string `json:"environment,omitempty" jsonschema:"description=Only this environment (a name such as prod or an environment id). Default: every environment."`
	OrgID       string `json:"org_id,omitempty" jsonschema:"description=The org to list. Default: the calling chat's project's org."`
}

const listChangesDescription = `List the Forge Changes of a project: remote config, gradual rollouts and A/B tests, the same list as the console's Experiments page. Each change shows its key, id, environment, mode (off, preview, everyone, rollout, experiment), the share of its audience that gets it, how long its current run has been open, whether the environment's current release declares it, and its revision. Use get_change for one change's variants, audience, metric and audit trail, and manage_change to draft, preview, launch, ramp, roll out or stop one.`

type listChangesTool struct {
	repo       runCallerStore
	controller ChangeController
}

func NewListChangesTool(repo runCallerStore, controller ChangeController) Tool {
	return NewToolWrapper[ListChangesParams, ToolResponse](&listChangesTool{repo: repo, controller: controller})
}

func (l *listChangesTool) Name() string        { return ToolListChanges }
func (l *listChangesTool) Description() string { return listChangesDescription }
func (l *listChangesTool) RequiresPermission(ListChangesParams) (bool, error) {
	return false, nil
}

func (l *listChangesTool) Execute(ctx *rctx.ToolContext, p ListChangesParams) (ToolResponse, error) {
	caller, failure := resolveRunCaller(ctx, l.repo)
	if failure != nil {
		return *failure, nil
	}
	if l.controller == nil {
		return NewTextErrorResponse(changesUnavailable), nil
	}
	list, err := l.controller.ListChanges(ctx.Context, caller.userID, caller.chat.ProjectID, ChangeFilter{
		OrgID: strings.TrimSpace(p.OrgID), Project: strings.TrimSpace(p.Project), Environment: strings.TrimSpace(p.Environment),
	})
	if err != nil {
		return NewTextErrorResponse("Could not list changes: " + describeChangeError(err, "")), nil
	}
	if len(list) == 0 {
		return NewTextResponse("No changes yet. manage_change action=draft creates one."), nil
	}
	sort.SliceStable(list, func(i, j int) bool {
		if a, b := envLabel(list[i]), envLabel(list[j]); a != b {
			return a < b
		}
		return list[i].Key < list[j].Key
	})
	now := time.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "%d change(s):\n", len(list))
	for _, c := range list {
		fmt.Fprintf(&b, "- %s (id %s) in %s: %s", c.Key, c.ID, envLabel(c), describeDelivery(c))
		if c.Run != nil && !c.Run.StartedAt.IsZero() {
			fmt.Fprintf(&b, "; run %s open %s", c.Run.ID, formatAge(now.Sub(c.Run.StartedAt)))
		}
		if c.Declaration == nil {
			b.WriteString("; not declared by the current release")
		}
		fmt.Fprintf(&b, "; revision %d\n", c.Revision)
	}
	return WithResponseMetadata(NewTextResponse(strings.TrimRight(b.String(), "\n")), list), nil
}

// =============================================================================
// get_change
// =============================================================================

type GetChangeParams struct {
	ChangeID string `json:"change_id" jsonschema:"required,description=A change id from list_changes. Never guess one."`
}

const getChangeDescription = `Show one Forge Change in full: its declared variants (from the environment's current release), mode, who gets it (unit, audience, percentage, split), the preview list, the success metric, the open run, whether the environment needs a person's approval to launch or ramp up, its revision, and its most recent audit events (who did what, and how it was approved). For an experiment's results use change_results.`

type getChangeTool struct {
	repo       runCallerStore
	controller ChangeController
}

func NewGetChangeTool(repo runCallerStore, controller ChangeController) Tool {
	return NewToolWrapper[GetChangeParams, ToolResponse](&getChangeTool{repo: repo, controller: controller})
}

func (g *getChangeTool) Name() string        { return ToolGetChange }
func (g *getChangeTool) Description() string { return getChangeDescription }
func (g *getChangeTool) RequiresPermission(GetChangeParams) (bool, error) {
	return false, nil
}

func (g *getChangeTool) Execute(ctx *rctx.ToolContext, p GetChangeParams) (ToolResponse, error) {
	caller, failure := resolveRunCaller(ctx, g.repo)
	if failure != nil {
		return *failure, nil
	}
	if g.controller == nil {
		return NewTextErrorResponse(changesUnavailable), nil
	}
	id := strings.TrimSpace(p.ChangeID)
	if id == "" {
		return NewTextErrorResponse("change_id is required; list_changes shows them."), nil
	}
	c, events, err := g.controller.GetChange(ctx.Context, caller.userID, id, 10)
	if err != nil {
		return NewTextErrorResponse("Could not read change " + id + ": " + describeChangeError(err, "")), nil
	}
	var b strings.Builder
	writeChangeDetail(&b, c, time.Now())
	if len(events) > 0 {
		b.WriteString("Recent audit events (newest first):\n")
		for _, e := range events {
			fmt.Fprintf(&b, "- r%d %s by %s %s", e.Revision, e.Action, e.ActorKind, e.ActorID)
			if e.ApprovalMethod != "" && e.ApprovalMethod != "not_required" {
				fmt.Fprintf(&b, ", approved by %s (%s)", orNone(e.ApprovedBy), e.ApprovalMethod)
			}
			if !e.At.IsZero() {
				fmt.Fprintf(&b, ", %s", e.At.UTC().Format(time.RFC3339))
			}
			b.WriteString("\n")
		}
	}
	return WithResponseMetadata(NewTextResponse(strings.TrimRight(b.String(), "\n")), c), nil
}

// =============================================================================
// manage_change
// =============================================================================

// ChangeAudienceParam is who is eligible, as the model names it.
type ChangeAudienceParam struct {
	Attribute string   `json:"attribute" jsonschema:"required,description=An evaluation-context attribute the app provides (e.g. plan) or targetingKey."`
	In        []string `json:"in" jsonschema:"required,description=The attribute values that are eligible."`
}

// ChangeSplitParam is one variant's share of an experiment, in percent.
type ChangeSplitParam struct {
	Variant       string  `json:"variant" jsonschema:"required,description=A declared variant name."`
	WeightPercent float64 `json:"weight_percent" jsonschema:"required,description=Its share of the enrolled units in percent. The arms must add up to 100."`
}

// ChangeMetricParam is an experiment's success metric, as the model names it.
type ChangeMetricParam struct {
	Event           string `json:"event" jsonschema:"required,description=The product event that counts as success (the name the app tracks without the product. prefix)."`
	Prop            string `json:"prop,omitempty" jsonschema:"description=Count only events whose property prop equals prop_value."`
	PropValue       string `json:"prop_value,omitempty"`
	WindowHours     int32  `json:"window_hours,omitempty" jsonschema:"description=Hours after assignment the event counts. Default 168 (a week); at most 2160."`
	MinSamplePerArm int64  `json:"min_sample_per_arm,omitempty" jsonschema:"description=Units per arm before the readout may decide. Default: the readout's."`
	ControlVariant  string `json:"control_variant,omitempty" jsonschema:"description=The arm the others are compared against; must be in the split."`
}

// ChangePreviewParam is one teammate who previews a variant.
type ChangePreviewParam struct {
	UnitID  string `json:"unit_id" jsonschema:"required,description=The teammate's unit id (their user id for unit user). Hashed by the control plane and never stored."`
	Variant string `json:"variant" jsonschema:"required,description=The declared variant they see."`
	Label   string `json:"label,omitempty" jsonschema:"description=A display label such as their email. Never evaluated."`
}

type ManageChangeParams struct {
	Action string `json:"action" jsonschema:"required,enum=draft,enum=preview,enum=launch,enum=ramp,enum=roll_out,enum=stop,enum=end_run,enum=request_approval,description=What to do; see the tool description."`
	// change_id is every action's but a draft that creates a change.
	ChangeID string `json:"change_id,omitempty" jsonschema:"description=The change from list_changes. Required for every action except a draft that creates a new change."`
	// A draft that creates a change.
	Key         string `json:"key,omitempty" jsonschema:"description=draft (new change) only: the change's key as changes.yaml declares it: lowercase letters digits . _ - starting with a letter."`
	Environment string `json:"environment,omitempty" jsonschema:"description=draft (new change) only: the environment (a name such as prod or an environment id)."`
	Project     string `json:"project,omitempty" jsonschema:"description=draft (new change) only: the forge project. Default: the calling chat's project."`
	// draft.
	Description   string               `json:"description,omitempty" jsonschema:"description=draft only: what the change does."`
	Unit          string               `json:"unit,omitempty" jsonschema:"enum=user,enum=account,enum=visitor,description=draft only: what stays together. user (default) keeps one signed-in user's outcome; account keeps an org's; visitor keeps an anonymous visitor's."`
	Audience      *ChangeAudienceParam `json:"audience,omitempty" jsonschema:"description=draft only: limit who is eligible to one attribute's values."`
	ClearAudience bool                 `json:"clear_audience,omitempty" jsonschema:"description=draft only: make everyone eligible again."`
	Split         []ChangeSplitParam   `json:"split,omitempty" jsonschema:"description=draft only: how an experiment splits the enrolled units across variants."`
	Metric        *ChangeMetricParam   `json:"metric,omitempty" jsonschema:"description=draft only: the experiment's one success metric."`
	ClearMetric   bool                 `json:"clear_metric,omitempty" jsonschema:"description=draft only: remove the success metric."`
	// preview.
	Preview  []ChangePreviewParam `json:"preview,omitempty" jsonschema:"description=preview only: the whole new preview list (empty clears it)."`
	Activate bool                 `json:"activate,omitempty" jsonschema:"description=preview only: move a change that is off to preview so the listed teammates see it now."`
	// launch, ramp, roll_out.
	Percent  *float64 `json:"percent,omitempty" jsonschema:"description=launch/ramp/roll_out: the share of the audience in percent (0-100; 0.5 is half a percent)."`
	Variant  string   `json:"variant,omitempty" jsonschema:"description=roll_out only: the declared variant to give."`
	Everyone bool     `json:"everyone,omitempty" jsonschema:"description=roll_out only: give the variant to everyone (remote config); percent is then ignored."`
	// Approval and concurrency.
	StepUpCode       string `json:"step_up_code,omitempty" jsonschema:"description=launch/ramp/roll_out in a protected environment: the one-time code request_approval emailed to the user and they read to you."`
	ExpectedRevision int64  `json:"expected_revision,omitempty" jsonschema:"description=The revision you last read (list_changes/get_change). The call is refused if the change has moved since. Default: the current revision."`
}

const manageChangeDescription = `Draft, preview, launch, ramp, roll out or stop one Forge Change (remote config, gradual rollout or A/B test), exactly as the console's Experiments page does, as the user, with the same audit trail. The release declares what a change can be (changes.yaml: key, variants, default); this decides who gets it.

- draft: without change_id, create a change (key + environment; it starts off). With change_id, edit what the next launch freezes: description, unit, audience (or clear_audience), split, metric (or clear_metric). A running experiment keeps its frozen copy until the next launch. No card.
- preview: replace the preview list (teammates by unit id, each with a variant; ids are hashed server-side and never measured). activate moves an off change to preview. No card.
- launch: start an experiment (a new run) at percent of the audience with the draft's split and metric. Launching while a run is open ends that run. Needs the user's approval card.
- ramp: move a running experiment or rollout to percent. Raising it only adds people; it needs an approval card. Lowering it needs none.
- roll_out: give variant to everyone (everyone=true) or to percent of the audience. Needs an approval card.
- stop: the kill switch. Everyone gets the default, previews included, and the open run ends. Never needs approval.
- end_run: conclude an experiment or rollout without shipping a variant (back to preview). Never needs approval.
- request_approval: email the user a one-time code for one launch, ramp up or roll-out in a protected environment.

Production needs a person: in a protected environment (prod) the control plane refuses a launch, ramp up or roll-out from an agent with approval_required even after the card. Then ask the user to approve it in the Experiments page of the Forge console, or run request_approval and retry with the code they read to you as step_up_code. Launching or rolling out a variant the environment's current release does not declare is refused until a release that declares it is deployed. Unattended runs may only draft, preview, stop, end_run and ramp down.`

type changeTool struct {
	Tool
	repo       runCallerStore
	controller ChangeController
}

// Preserve the approval hook when a workflow/global setting binds parameters.
func (t *changeTool) Bindings() Bindings { return t.Tool.(BindableTool).Bindings() }
func (t *changeTool) WithBindings(bindings Bindings) (Tool, error) {
	bound, err := t.Tool.(BindableTool).WithBindings(bindings)
	if err != nil {
		return t, err
	}
	clone := *t
	clone.Tool = bound
	return &clone, nil
}

type changeTypedTool struct{ outer *changeTool }

func (t *changeTypedTool) Name() string        { return ToolManageChange }
func (t *changeTypedTool) Description() string { return manageChangeDescription }
func (t *changeTypedTool) RequiresPermission(ManageChangeParams) (bool, error) {
	return true, nil
}

func NewManageChangeTool(repo runCallerStore, controller ChangeController) Tool {
	t := &changeTool{repo: repo, controller: controller}
	t.Tool = NewToolWrapper[ManageChangeParams, ToolResponse](&changeTypedTool{outer: t})
	return t
}

// changeActions are manage_change's actions; changeWidening those that can
// give a change to more people, and so need the user's approval card.
var changeActions = []string{"draft", "preview", "launch", "ramp", "roll_out", "stop", "end_run", "request_approval"}

// normalizedChange is a validated manage_change call.
type normalizedChange struct {
	action    string
	changeID  string
	percentBP int32
	hasPct    bool
	variant   string
	everyone  bool
}

// normalize validates p: each action's required fields, and no field that
// belongs to another action, so a misplaced argument is never ignored.
func (p ManageChangeParams) normalize() (normalizedChange, error) {
	n := normalizedChange{
		action:   strings.ToLower(strings.TrimSpace(p.Action)),
		changeID: strings.TrimSpace(p.ChangeID),
		variant:  strings.TrimSpace(p.Variant),
		everyone: p.Everyone,
	}
	known := false
	for _, a := range changeActions {
		known = known || a == n.action
	}
	if !known {
		return n, fmt.Errorf("action %q is not one of %s", p.Action, strings.Join(changeActions, ", "))
	}
	if p.Percent != nil {
		pct := *p.Percent
		if math.IsNaN(pct) || pct < 0 || pct > 100 {
			return n, fmt.Errorf("percent must be between 0 and 100, not %v", pct)
		}
		n.percentBP, n.hasPct = int32(math.Round(pct*100)), true
	}
	drafting := p.Description != "" || p.Unit != "" || p.Audience != nil || p.ClearAudience || len(p.Split) > 0 || p.Metric != nil || p.ClearMetric
	creating := p.Key != "" || p.Environment != "" || p.Project != ""
	previewing := len(p.Preview) > 0 || p.Activate
	misplaced := func(what string) error { return fmt.Errorf("%s does not apply to %s", what, n.action) }
	if n.action != "draft" {
		switch {
		case drafting:
			return n, misplaced("description, unit, audience, split and metric (draft fields)")
		case creating:
			return n, misplaced("key, environment and project (fields of a draft that creates a change)")
		}
	}
	if n.action != "preview" && previewing {
		return n, misplaced("preview and activate")
	}
	if n.action != "roll_out" && (n.variant != "" || n.everyone) {
		return n, misplaced("variant and everyone (roll_out fields)")
	}
	widening := n.action == "launch" || n.action == "ramp" || n.action == "roll_out"
	if !widening && (n.hasPct || p.StepUpCode != "") {
		return n, misplaced("percent and step_up_code")
	}
	if n.changeID == "" && (n.action != "draft" || !creating) {
		return n, fmt.Errorf("%s needs change_id (list_changes shows them)", n.action)
	}
	switch n.action {
	case "draft":
		if n.changeID != "" && creating {
			return n, errors.New("key, environment and project create a new change; omit them to edit change_id")
		}
		if n.changeID == "" && (strings.TrimSpace(p.Key) == "" || strings.TrimSpace(p.Environment) == "") {
			return n, errors.New("a draft that creates a change needs key and environment")
		}
		if p.Audience != nil && p.ClearAudience {
			return n, errors.New("audience and clear_audience contradict each other")
		}
		if p.Metric != nil && p.ClearMetric {
			return n, errors.New("metric and clear_metric contradict each other")
		}
		if _, err := splitBP(p.Split); err != nil {
			return n, err
		}
	case "launch", "ramp":
		if !n.hasPct {
			return n, fmt.Errorf("%s needs percent", n.action)
		}
	case "roll_out":
		if n.variant == "" {
			return n, errors.New("roll_out needs variant")
		}
		if !n.everyone && !n.hasPct {
			return n, errors.New("roll_out needs everyone=true or percent")
		}
	}
	return n, nil
}

// splitBP converts a split in percent to basis points; the arms must add up
// to 100%.
func splitBP(in []ChangeSplitParam) ([]ChangeSplitArm, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]ChangeSplitArm, 0, len(in))
	var total int32
	for _, arm := range in {
		v := strings.TrimSpace(arm.Variant)
		if v == "" {
			return nil, errors.New("every split arm needs a variant")
		}
		if arm.WeightPercent < 0 || arm.WeightPercent > 100 {
			return nil, fmt.Errorf("split weight for %s must be between 0 and 100", v)
		}
		bp := int32(math.Round(arm.WeightPercent * 100))
		total += bp
		out = append(out, ChangeSplitArm{Variant: v, WeightBP: bp})
	}
	if total != 10000 {
		return nil, fmt.Errorf("split weights add up to %s%%, not 100%%", formatBP(total))
	}
	return out, nil
}

// needsCard reports whether the action widens who gets the change: a
// launch, a roll-out, or a ramp above the current percentage.
func (n normalizedChange) needsCard(current ChangeSummary) bool {
	switch n.action {
	case "launch", "roll_out":
		return true
	case "ramp":
		return n.percentBP > current.PercentBP
	}
	return false
}

// changeApprovalHash binds an approval to exactly one action on one change
// at one revision, with its percentage and variant: the card the user
// approved is the call that runs, and a change that moved since is not.
func changeApprovalHash(action, changeID string, revision int64, percentBP int32, variant string, everyone bool) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		action, strings.TrimSpace(changeID), strconv.FormatInt(revision, 10), strconv.Itoa(int(percentBP)), variant, strconv.FormatBool(everyone),
	}, "\x00")))
	return "change:" + hex.EncodeToString(sum[:])
}

func (n normalizedChange) hash(current ChangeSummary) string {
	pct := n.percentBP
	if n.action == "roll_out" && n.everyone {
		pct = 0
	}
	return changeApprovalHash(n.action, current.ID, current.Revision, pct, n.variant, n.everyone)
}

// cardTitle says what the user approves, in their words.
func (n normalizedChange) cardTitle(c ChangeSummary) string {
	where := envLabel(c)
	unit := orDefault(c.Unit, "user")
	prod := ""
	if c.ApprovalRequired {
		prod = " This is a protected environment: the control plane also needs your approval in the Experiments page or an emailed code."
	}
	switch n.action {
	case "launch":
		run := "A new run starts with fresh randomization."
		if c.Run != nil {
			run = "The open run " + c.Run.ID + " ends and a new one starts with fresh randomization."
		}
		return fmt.Sprintf("Launch experiment %s in %s to %s of %ss? %s%s", c.Key, where, formatBP(n.percentBP)+"%", unit, run, prod)
	case "ramp":
		return fmt.Sprintf("Ramp %s in %s from %s to %s of %ss? Everyone already enrolled stays enrolled.%s", c.Key, where, formatBP(c.PercentBP)+"%", formatBP(n.percentBP)+"%", unit, prod)
	default: // roll_out
		if n.everyone {
			return fmt.Sprintf("Roll out %s of %s to everyone in %s?%s", n.variant, c.Key, where, prod)
		}
		return fmt.Sprintf("Roll out %s of %s to %s of %ss in %s?%s", n.variant, c.Key, formatBP(n.percentBP)+"%", unit, where, prod)
	}
}

// decode parses a call's input as the tool would, bindings applied.
func (t *changeTool) decode(call ToolCall) (ManageChangeParams, error) {
	input := normalizeModelInput(call.Input, t.ParamSchema())
	if bindings := t.Bindings(); len(bindings) > 0 {
		var err error
		if input, err = ApplyBindings(ToolManageChange, input, bindings); err != nil {
			return ManageChangeParams{}, RefuseActivation("tool bindings could not be applied", err)
		}
	}
	var p ManageChangeParams
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return ManageChangeParams{}, RefuseActivation("tool input is not a valid manage_change call", err)
	}
	return p, nil
}

// unattendedAllowed is nil for what an unattended run may do: draft,
// preview, reduce exposure. It reads the change only for a ramp.
func (t *changeTool) unattendedAllowed(ctx context.Context, userID string, n normalizedChange) error {
	switch n.action {
	case "draft", "preview", "stop", "end_run":
		return nil
	case "ramp":
		c, _, err := t.controller.GetChange(ctx, userID, n.changeID, 0)
		if err != nil {
			return fmt.Errorf("could not read change %s: %s", n.changeID, describeChangeError(err, ""))
		}
		if n.needsCard(c) {
			return errors.New("unattended runs may only ramp a change down; ask the user in an interactive chat to ramp it up")
		}
		return nil
	}
	return fmt.Errorf("unattended runs cannot %s a change; ask the user in an interactive chat", strings.ReplaceAll(n.action, "_", " "))
}

// PrepareAutomation implements AutomationApprovalTool. It reports whether
// the call needs a card: launch, roll_out and a ramp up do when attended;
// nothing else ever does. An unattended run may only draft, preview and
// reduce exposure, and is refused the rest.
func (t *changeTool) PrepareAutomation(ctx *rctx.ToolContext, call ToolCall, unattended bool) (ActivationPreview, bool, error) {
	caller, failure := resolveRunCaller(ctx, t.repo)
	if failure != nil {
		return ActivationPreview{}, false, RefuseActivation("the calling chat could not be resolved", fmt.Errorf("%s", failure.Content))
	}
	if t.controller == nil {
		return ActivationPreview{}, false, RefuseActivation("Forge Changes is unavailable on this server", errors.New("no change controller"))
	}
	p, err := t.decode(call)
	if err != nil {
		return ActivationPreview{}, false, err
	}
	n, err := p.normalize()
	if err != nil {
		return ActivationPreview{}, false, RefuseActivation(err.Error(), err)
	}
	if unattended {
		if err := t.unattendedAllowed(ctx.Context, caller.userID, n); err != nil {
			return ActivationPreview{}, false, RefuseActivation(err.Error(), err)
		}
		return ActivationPreview{}, false, nil
	}
	if n.action != "launch" && n.action != "ramp" && n.action != "roll_out" {
		return ActivationPreview{}, false, nil
	}
	c, _, err := t.controller.GetChange(ctx.Context, caller.userID, n.changeID, 0)
	if err != nil {
		reason := "could not read change " + n.changeID + ": " + describeChangeError(err, "")
		return ActivationPreview{}, false, RefuseActivation(reason, err)
	}
	if p.ExpectedRevision != 0 && p.ExpectedRevision != c.Revision {
		err := fmt.Errorf("change %s moved since revision %d (now %d); read it again with get_change", c.Key, p.ExpectedRevision, c.Revision)
		return ActivationPreview{}, false, RefuseActivation(err.Error(), err)
	}
	if !n.needsCard(c) {
		return ActivationPreview{}, false, nil
	}
	return ActivationPreview{Title: n.cardTitle(c), Hash: n.hash(c)}, true, nil
}

func (t *changeTypedTool) Execute(ctx *rctx.ToolContext, p ManageChangeParams) (ToolResponse, error) {
	o := t.outer
	caller, failure := resolveRunCaller(ctx, o.repo)
	if failure != nil {
		return *failure, nil
	}
	if o.controller == nil {
		return NewTextErrorResponse(changesUnavailable), nil
	}
	n, err := p.normalize()
	if err != nil {
		return NewTextErrorResponse(err.Error() + ". Nothing changed."), nil
	}
	exec := automationpolicy.From(ctx.Context)
	if exec.Unattended {
		if err := o.unattendedAllowed(ctx.Context, caller.userID, n); err != nil {
			return NewTextErrorResponse(err.Error() + ". Nothing changed."), nil
		}
	}
	switch n.action {
	case "draft":
		return o.draft(ctx, caller, p, n), nil
	case "request_approval":
		code, err := o.controller.RequestChangeApproval(ctx.Context, caller.userID, n.changeID)
		if err != nil {
			return NewTextErrorResponse("Could not send an approval code: " + describeChangeError(err, "") + ". Nothing changed."), nil
		}
		msg := "A one-time approval code was emailed"
		if code.SentTo != "" {
			msg += " to " + code.SentTo
		}
		if !code.ExpiresAt.IsZero() {
			msg += "; it works until " + code.ExpiresAt.UTC().Format(time.RFC3339)
		}
		return NewTextResponse(msg + ". Ask the user to read it to you, then retry the launch, ramp or roll-out with step_up_code."), nil
	case "stop":
		// The kill switch never loses a race: no revision unless the model named one.
		c, err := o.controller.ApplyChange(ctx.Context, caller.userID, ChangeOp{Action: "stop", ChangeID: n.changeID, ExpectedRevision: p.ExpectedRevision})
		if err != nil {
			return NewTextErrorResponse("Could not stop change " + n.changeID + ": " + describeChangeError(err, "stop") + "."), nil
		}
		return NewTextResponse(fmt.Sprintf("Stopped %s in %s: everyone gets the default now, previews included (revision %d).", c.Key, envLabel(c), c.Revision)), nil
	}

	current, _, err := o.controller.GetChange(ctx.Context, caller.userID, n.changeID, 0)
	if err != nil {
		return NewTextErrorResponse("Could not read change " + n.changeID + ": " + describeChangeError(err, "") + ". Nothing changed."), nil
	}
	if p.ExpectedRevision != 0 && p.ExpectedRevision != current.Revision {
		return NewTextErrorResponse(fmt.Sprintf("Change %s moved since revision %d (it is at %d now). Read it again with get_change and retry. Nothing changed.", current.Key, p.ExpectedRevision, current.Revision)), nil
	}
	if n.needsCard(current) {
		callID := ""
		if call, ok := ctx.Value(ctxkeys.ToolCallContextKey).(*ctxkeys.ToolCallContext); ok && call != nil {
			callID = call.CurrentToolCallID
		}
		if exec.Approvals[callID] != n.hash(current) {
			return NewTextErrorResponse(fmt.Sprintf("Change %s (%s) requires the user's approval card in an interactive chat, for exactly this change at its current revision. It was not run.", strings.ReplaceAll(n.action, "_", " "), current.Key)), nil
		}
	}
	op := ChangeOp{Action: n.action, ChangeID: n.changeID, ExpectedRevision: current.Revision, StepUpCode: strings.TrimSpace(p.StepUpCode)}
	switch n.action {
	case "preview":
		op.Action, op.Activate = "set_preview", p.Activate
		for _, e := range p.Preview {
			op.Preview = append(op.Preview, ChangePreviewInput{UnitID: strings.TrimSpace(e.UnitID), Variant: strings.TrimSpace(e.Variant), Label: strings.TrimSpace(e.Label)})
		}
	case "launch", "ramp":
		op.PercentBP = n.percentBP
	case "roll_out":
		op.Variant, op.Everyone, op.PercentBP = n.variant, n.everyone, n.percentBP
		if n.everyone {
			op.PercentBP = 0
		}
	}
	c, err := o.controller.ApplyChange(ctx.Context, caller.userID, op)
	if err != nil {
		return NewTextErrorResponse("Could not " + strings.ReplaceAll(n.action, "_", " ") + " change " + current.Key + ": " + describeChangeError(err, n.action) + ". Nothing changed."), nil
	}
	return NewTextResponse(fmt.Sprintf("%s: %s in %s is now %s (revision %d).", changeDone(n.action), c.Key, envLabel(c), describeDelivery(c), c.Revision)), nil
}

// draft creates a change, or edits the draft of one; a create that also
// names draft fields applies them as a second, revision-checked write.
func (t *changeTool) draft(ctx *rctx.ToolContext, caller *runCaller, p ManageChangeParams, n normalizedChange) ToolResponse {
	split, _ := splitBP(p.Split) // validated by normalize
	op := ChangeOp{Action: "update_draft", ChangeID: n.changeID, ExpectedRevision: p.ExpectedRevision,
		Unit: strings.ToLower(strings.TrimSpace(p.Unit)), ClearAudience: p.ClearAudience, Split: split, ClearMetric: p.ClearMetric}
	if p.Audience != nil {
		op.Audience = &ChangeAudience{Attribute: strings.TrimSpace(p.Audience.Attribute), In: p.Audience.In}
	}
	if p.Metric != nil {
		m := ChangeMetric(*p.Metric)
		op.Metric = &m
	}
	created := ""
	if n.changeID == "" {
		c, err := t.controller.CreateChange(ctx.Context, caller.userID, caller.chat.ProjectID, ChangeCreate{
			Project: strings.TrimSpace(p.Project), Environment: strings.TrimSpace(p.Environment),
			Key: strings.TrimSpace(p.Key), Description: strings.TrimSpace(p.Description), Unit: op.Unit,
		})
		if err != nil {
			return NewTextErrorResponse("Could not create change " + strings.TrimSpace(p.Key) + ": " + describeChangeError(err, "draft") + ". Nothing changed.")
		}
		created = fmt.Sprintf("Created %s (id %s) in %s, off.", c.Key, c.ID, envLabel(c))
		if c.Declaration == nil {
			created += " The environment's current release does not declare it yet: it can be previewed, but not launched until a release whose changes.yaml declares it is deployed."
		}
		if op.Audience == nil && !op.ClearAudience && op.Split == nil && op.Metric == nil && !op.ClearMetric {
			return WithResponseMetadata(NewTextResponse(created+fmt.Sprintf(" Revision %d.", c.Revision)), c)
		}
		op.ChangeID, op.ExpectedRevision, op.Unit = c.ID, c.Revision, ""
	} else {
		if d := strings.TrimSpace(p.Description); d != "" {
			op.Description = &d
		}
		if op.ExpectedRevision == 0 {
			current, _, err := t.controller.GetChange(ctx.Context, caller.userID, n.changeID, 0)
			if err != nil {
				return NewTextErrorResponse("Could not read change " + n.changeID + ": " + describeChangeError(err, "") + ". Nothing changed.")
			}
			op.ExpectedRevision = current.Revision
		}
	}
	c, err := t.controller.ApplyChange(ctx.Context, caller.userID, op)
	if err != nil {
		prefix := ""
		if created != "" {
			prefix = created + " But its draft could not be saved: "
		} else {
			prefix = "Could not save the draft of " + n.changeID + ": "
		}
		return NewTextErrorResponse(prefix + describeChangeError(err, "draft") + ".")
	}
	var b strings.Builder
	if created != "" {
		b.WriteString(created + " ")
	}
	fmt.Fprintf(&b, "Draft saved (revision %d); the next launch uses it.\n", c.Revision)
	writeChangeDetail(&b, c, time.Now())
	return WithResponseMetadata(NewTextResponse(strings.TrimRight(b.String(), "\n")), c)
}

func changeDone(action string) string {
	switch action {
	case "preview":
		return "Preview list saved"
	case "launch":
		return "Launched"
	case "ramp":
		return "Ramped"
	case "roll_out":
		return "Rolled out"
	case "end_run":
		return "Run ended"
	}
	return "Done"
}

// describeChangeError turns a ChangesService refusal into what the model
// should do next. action is the mutation that was refused, "" for a read.
func describeChangeError(err error, action string) string {
	var apiErr *ChangeAPIError
	if !errors.As(err, &apiErr) {
		return err.Error()
	}
	switch apiErr.Reason {
	case "approval_required":
		return "this is a protected environment and it needs a person's approval to " + strings.ReplaceAll(orDefault(action, "do that"), "_", " ") +
			". Ask the user to approve it in the Experiments page of the Forge console, or run manage_change action=request_approval and retry with the code they read to you as step_up_code"
	case "approval_unavailable":
		return "there is no person to send an approval code to for this credential; the user has to approve it in the Experiments page of the Forge console"
	case "change_revision_conflict":
		return "the change moved since it was read. Read it again with get_change and retry against its new revision"
	case "change_not_declared":
		return "the environment's current release does not declare this change. Add it to the project's changes.yaml and deploy a release first (drafts, previews and stop still work)"
	case "variant_not_declared":
		return "the environment's current release does not declare that variant. Use a variant get_change lists, or deploy a release that declares it"
	case "change_exists":
		return "a change with that key already exists in that environment; list_changes shows its id"
	case "no_experiment_run":
		return "this change has never run an experiment, so there are no results yet; manage_change action=launch starts one"
	case "run_not_experiment":
		return "that run was a rollout, not an experiment, so it has no results to compare"
	case "metric_missing":
		return "the run was launched without a success metric, so there is nothing to measure. Set one with manage_change action=draft (metric) and relaunch"
	case "readout_plan_invalid":
		return "the run's split has no control and treatment to compare. Fix the split with manage_change action=draft and relaunch"
	case "scope_missing":
		return "the user's credential lacks the scope to read results (obs:read)"
	}
	switch apiErr.Code {
	case "unimplemented":
		if action == "results" {
			return "the control plane does not serve experiment results yet (the readout service is not deployed)"
		}
		return "this control plane does not run Forge Changes yet"
	case "unavailable":
		if action == "results" {
			return "results need observability, which is not set up for this org yet, or it did not answer; try again later"
		}
	case "not_found":
		return "no such change or environment that the user can see"
	case "permission_denied":
		return "the user's credential may not do that here (" + apiErr.Error() + ")"
	case "unauthenticated":
		return "the user's sign-in was refused; they can send a message in this chat to refresh it"
	}
	return apiErr.Error()
}

// writeChangeDetail writes one change in full.
func writeChangeDetail(b *strings.Builder, c ChangeSummary, now time.Time) {
	fmt.Fprintf(b, "%s (id %s) in %s, revision %d\n", c.Key, c.ID, envLabel(c), c.Revision)
	if c.Description != "" {
		fmt.Fprintf(b, "  %s\n", c.Description)
	}
	fmt.Fprintf(b, "Now: %s\n", describeDelivery(c))
	if d := c.Declaration; d != nil {
		names := make([]string, 0, len(d.Variants))
		for _, v := range d.Variants {
			names = append(names, v.Name+"="+v.ValueJSON)
		}
		fmt.Fprintf(b, "Declared (%s): default %s; variants %s\n", orDefault(d.Type, "json"), orNone(d.DefaultVariant), orNone(strings.Join(names, ", ")))
	} else {
		b.WriteString("Declared: no. The environment's current release does not declare this key; it can be drafted, previewed and stopped, not launched.\n")
	}
	audience := "everyone"
	if c.Audience != nil {
		audience = fmt.Sprintf("%s in [%s]", c.Audience.Attribute, strings.Join(c.Audience.In, ", "))
	}
	fmt.Fprintf(b, "Draft (what the next launch freezes): unit %s; eligible: %s", orDefault(c.Unit, "user"), audience)
	if len(c.Split) > 0 {
		arms := make([]string, 0, len(c.Split))
		for _, arm := range c.Split {
			arms = append(arms, arm.Variant+" "+formatBP(arm.WeightBP)+"%")
		}
		fmt.Fprintf(b, "; split %s", strings.Join(arms, " / "))
	}
	b.WriteString("\n")
	if m := c.Metric; m != nil {
		fmt.Fprintf(b, "Success metric: %s", m.Event)
		if m.Prop != "" {
			fmt.Fprintf(b, " where %s=%s", m.Prop, m.PropValue)
		}
		fmt.Fprintf(b, " within %dh", max(m.WindowHours, 0))
		if m.ControlVariant != "" {
			fmt.Fprintf(b, ", against %s", m.ControlVariant)
		}
		b.WriteString("\n")
	}
	if len(c.Preview) > 0 {
		labels := make([]string, 0, len(c.Preview))
		for _, e := range c.Preview {
			labels = append(labels, orDefault(e.Label, e.UnitHash)+" → "+e.Variant)
		}
		fmt.Fprintf(b, "Preview: %s\n", strings.Join(labels, ", "))
	}
	if r := c.Run; r != nil {
		fmt.Fprintf(b, "Open run %s (%s)", r.ID, r.Kind)
		if !r.StartedAt.IsZero() {
			fmt.Fprintf(b, ", open %s", formatAge(now.Sub(r.StartedAt)))
		}
		if r.ApprovalMethod != "" && r.ApprovalMethod != "not_required" {
			fmt.Fprintf(b, ", approved by %s (%s)", orNone(r.ApprovedBy), r.ApprovalMethod)
		}
		b.WriteString("\n")
	}
	if c.ApprovalRequired {
		b.WriteString("Protected environment: a launch, ramp up or roll-out needs the user's approval in the Experiments page or an emailed code.\n")
	}
}

// describeDelivery is who gets the change right now, in a phrase.
func describeDelivery(c ChangeSummary) string {
	unit := orDefault(c.Unit, "user") + "s"
	switch c.Mode {
	case "off":
		return "off (everyone gets the default)"
	case "preview":
		return fmt.Sprintf("preview only (%d teammate(s))", len(c.Preview))
	case "everyone":
		return "everyone gets " + orNone(c.EveryoneVariant)
	case "rollout":
		return fmt.Sprintf("rolling out %s to %s%% of %s", orNone(c.RolloutVariant), formatBP(c.PercentBP), unit)
	case "experiment":
		return fmt.Sprintf("experiment on %s%% of %s", formatBP(c.PercentBP), unit)
	}
	return orDefault(c.Mode, "unknown")
}

func envLabel(c ChangeSummary) string {
	return orDefault(c.EnvironmentName, c.EnvironmentID)
}

// formatBP renders basis points as a percentage: 2500 → "25", 50 → "0.5".
func formatBP(bp int32) string {
	return strconv.FormatFloat(float64(bp)/100, 'f', -1, 64)
}

func formatAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(int(d.Minutes()), 0))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

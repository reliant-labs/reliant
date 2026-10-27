// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
)

// draft_automation proposes an automation for the user to review. It is the
// agent's half of "describe it" (ui-review report 03, §6.7 option B): the
// agent turns intent into fields, and a person confirms where it runs and as
// whom. It stores nothing and creates nothing — the user's Create in the
// automation dialog does. Separate activation tools require interactive owner
// approval; drafting never grants or substitutes for that approval.
//
// The same schema and the same normalizer back TriggerService.DraftAutomation
// (the "Describe it" box): its one-shot model call is pinned to this tool, so
// a draft from chat and a draft from the box are the same shape, checked by
// the same rules, and opened in the same dialog.

// DraftAutomationParams is what the agent proposes.
type DraftAutomationParams struct {
	When        DraftAutomationWhen `json:"when" jsonschema:"required,description=What starts each run."`
	Prompt      string              `json:"prompt" jsonschema:"required,description=The message each run starts from. Nobody is watching when it runs\\, so say everything the agent needs: what to look at\\, what to do\\, and what done looks like."`
	Summary     string              `json:"summary" jsonschema:"required,description=One line the user reads: when it runs and what it does\\, e.g. 'Every weekday at 9:00\\, summarise yesterday's failed CI runs'."`
	Name        string              `json:"name,omitempty" jsonschema:"description=A short name (at most 6 words). Leave empty to let the app name it from when and what."`
	Workflow    string              `json:"workflow,omitempty" jsonschema:"description=The workflow each run executes: a slug from list_workflows\\, or builtin://<name>. Leave empty for the user's default agent\\, which is right unless a workflow clearly fits."`
	Params      map[string]any      `json:"params,omitempty" jsonschema:"description=Inputs for the chosen workflow\\, by input name. Only inputs the workflow declares."`
	Assumptions []string            `json:"assumptions,omitempty" jsonschema:"description=What you assumed that the user did not say\\, one short sentence each\\, e.g. 'Assumed 9:00 in your time zone'. At most 5."`
}

// DraftAutomationWhen is the trigger source, flattened so a model fills one
// object rather than choosing a oneof arm.
type DraftAutomationWhen struct {
	Kind        string            `json:"kind" jsonschema:"required,enum=schedule,enum=webhook,enum=integration,enum=workflow_event,description=schedule: on a cron or interval. webhook: when something POSTs to the automation's own URL. integration: on a provider event (GitHub\\, Slack\\, Gmail\\, Twilio). workflow_event: when a run of one of the user's workflows finishes\\, fails or is blocked."`
	Cron        []string          `json:"cron,omitempty" jsonschema:"description=schedule: 5-field cron expressions (minute hour day-of-month month day-of-week)\\, e.g. '0 9 * * 1-5' for weekdays at 9:00. One is almost always enough."`
	Interval    string            `json:"interval,omitempty" jsonschema:"description=schedule: instead of cron\\, every Go duration\\, e.g. '30m' or '2h'. At least 1m."`
	Timezone    string            `json:"timezone,omitempty" jsonschema:"description=schedule: IANA zone the cron times are in\\, e.g. 'America/New_York'. Leave empty for the user's own zone."`
	Integration string            `json:"integration,omitempty" jsonschema:"description=integration: the integration id\\, e.g. 'github'."`
	Events      []string          `json:"events,omitempty" jsonschema:"description=integration: event types as the integration names them\\, e.g. 'pull_request.opened' or 'issues.*'. search_integrations lists a provider's trigger events."`
	Match       map[string]string `json:"match,omitempty" jsonschema:"description=integration: event attributes that must equal these values\\, e.g. {\"repository\": \"acme/app\"}."`
	Workflows   []string          `json:"workflows,omitempty" jsonschema:"description=workflow_event: the workflows whose runs it watches (slugs or builtin://<name>). Empty: any of the user's workflows."`
	Outcomes    []string          `json:"outcomes,omitempty" jsonschema:"description=workflow_event: finished\\, failed and/or blocked. Empty: all three."`
	Filter      string            `json:"filter,omitempty" jsonschema:"description=Not for schedules. Optional CEL bool over the trigger root that an event must satisfy\\, e.g. 'trigger.payload.data.pull_request.draft == false'."`
}

// DraftOptions is what the caller knows that the draft does not say.
type DraftOptions struct {
	// Timezone is the user's IANA zone, used when a schedule names none.
	// Empty leaves the draft's zone empty for the dialog to fill.
	Timezone string
	// Workflow, when set, is the workflow the automation must run: it
	// replaces whatever the draft proposed.
	Workflow string
}

// DraftAutomationMetadata is draft_automation's result metadata: the draft,
// as protojson AutomationDraft, which the chat renders as a "Review & create"
// card that opens the automation dialog prefilled.
type DraftAutomationMetadata struct {
	Draft json.RawMessage `json:"draft"`
}

const (
	maxDraftNameRunes   = 80
	maxDraftAssumptions = 5
)

// BuildAutomationDraft validates a proposed automation and returns it as an
// AutomationDraft. Errors are written for the model that proposed it to fix.
//
// Only the WHEN and the WHAT are ever set. Where it runs (project, machine,
// workspace), through which connection, and as whom are the person's choices
// in the dialog, so the definition never carries them.
func BuildAutomationDraft(p DraftAutomationParams, opts DraftOptions) (*reliantv1.AutomationDraft, error) {
	prompt := strings.TrimSpace(p.Prompt)
	if prompt == "" {
		return nil, errors.New("prompt is required: write the message each run starts from")
	}
	summary := strings.TrimSpace(p.Summary)
	if summary == "" {
		return nil, errors.New("summary is required: one line saying when it runs and what it does")
	}

	def := &reliantv1.TriggerDefinition{
		Name:     truncateDraftRunes(strings.Join(strings.Fields(p.Name), " "), maxDraftNameRunes),
		Workflow: strings.TrimSpace(p.Workflow),
		Message:  prompt,
	}
	if w := strings.TrimSpace(opts.Workflow); w != "" {
		def.Workflow = w
	}

	if err := setDraftSource(def, p.When, opts); err != nil {
		return nil, err
	}
	if _, err := triggerspec.FromDefinition(def); err != nil {
		return nil, fmt.Errorf("when: %w", err)
	}
	if filter := strings.TrimSpace(p.When.Filter); filter != "" {
		if def.GetSchedule() != nil {
			return nil, errors.New("when.filter: a schedule has no event to filter; leave it empty")
		}
		if _, err := triggerspec.CompileFilter(filter); err != nil {
			return nil, fmt.Errorf("when.filter: %w", err)
		}
		def.Filter = filter
	}

	if len(p.Params) > 0 {
		def.Params = make(map[string]*structpb.Value, len(p.Params))
		for name, raw := range p.Params {
			value, err := structpb.NewValue(raw)
			if err != nil {
				return nil, fmt.Errorf("params.%s: %w", name, err)
			}
			def.Params[name] = value
		}
	}

	draft := &reliantv1.AutomationDraft{Definition: def, Summary: summary}
	for _, a := range p.Assumptions {
		if a = strings.TrimSpace(a); a != "" && len(draft.Assumptions) < maxDraftAssumptions {
			draft.Assumptions = append(draft.Assumptions, a)
		}
	}
	return draft, nil
}

func setDraftSource(def *reliantv1.TriggerDefinition, w DraftAutomationWhen, opts DraftOptions) error {
	switch strings.TrimSpace(w.Kind) {
	case "schedule":
		var cron []string
		for _, c := range w.Cron {
			if c = strings.Join(strings.Fields(c), " "); c != "" {
				cron = append(cron, c)
			}
		}
		interval := strings.TrimSpace(w.Interval)
		if len(cron) == 0 && interval == "" {
			return errors.New("when: a schedule needs cron (e.g. '0 9 * * 1-5') or interval (e.g. '30m')")
		}
		// Empty stays empty when the caller does not know the user's zone
		// (a chat): the dialog reads it as the browser's zone, which is what
		// "at 9" meant to the person who said it.
		tz := strings.TrimSpace(w.Timezone)
		if tz == "" {
			tz = strings.TrimSpace(opts.Timezone)
		}
		src := &reliantv1.ScheduleSource{Cron: cron, Timezone: tz, Overlap: reliantv1.TriggerOverlapPolicy_TRIGGER_OVERLAP_POLICY_SKIP}
		if interval != "" {
			src.Interval = &interval
		}
		def.Source = &reliantv1.TriggerDefinition_Schedule{Schedule: src}
	case "webhook":
		def.Source = &reliantv1.TriggerDefinition_Webhook{Webhook: &reliantv1.WebhookSource{}}
	case "integration":
		integration := strings.TrimSpace(w.Integration)
		if integration == "" {
			return errors.New("when.integration is required for an integration event, e.g. 'github'")
		}
		def.Source = &reliantv1.TriggerDefinition_Integration{Integration: &reliantv1.IntegrationSource{
			Integration: integration,
			Events:      trimmedNonEmpty(w.Events),
			Match:       w.Match,
		}}
	case "workflow_event":
		def.Source = &reliantv1.TriggerDefinition_WorkflowEvent{WorkflowEvent: &reliantv1.WorkflowEventSource{
			Workflows: trimmedNonEmpty(w.Workflows),
			Outcomes:  trimmedNonEmpty(w.Outcomes),
		}}
	default:
		return fmt.Errorf("when.kind must be schedule, webhook, integration or workflow_event, got %q", w.Kind)
	}
	return nil
}

func trimmedNonEmpty(values []string) []string {
	var out []string
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func truncateDraftRunes(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

// MarshalAutomationDraft is the draft as the web client decodes it.
func MarshalAutomationDraft(draft *reliantv1.AutomationDraft) (json.RawMessage, error) {
	return protojson.Marshal(draft)
}

const draftAutomationDescription = `Draft an automation for the user to review: a schedule, webhook, integration event (a GitHub pull request, a Slack message, an email) or workflow outcome that starts a run by itself.

Use it when the user asks for something to happen on its own — "every weekday at 9, summarise failed CI", "when a PR is opened, review it", "when my deploy workflow fails, open an issue". Fill in when it runs and the prompt each run starts from; leave the workflow empty to use their default agent unless one clearly fits (list_workflows).

It creates NOTHING. The user sees the draft as a card with "Review & create", which opens the automation dialog prefilled: there they choose the project, the machine it runs on and the account it writes as, and press Create. Never tell the user the automation is running or turned on; say it is drafted and ready for them to review. This tool never turns it on. Separate activation tools require interactive owner approval; a draft is not that approval.`

type draftAutomationTool struct{}

// NewDraftAutomationTool returns draft_automation. It needs no store: a draft
// is validated, not saved.
func NewDraftAutomationTool() Tool {
	return NewToolWrapper[DraftAutomationParams, ToolResponse](&draftAutomationTool{})
}

func (t *draftAutomationTool) Name() string        { return ToolDraftAutomation }
func (t *draftAutomationTool) Description() string { return draftAutomationDescription }

func (t *draftAutomationTool) RequiresPermission(DraftAutomationParams) (bool, error) {
	return false, nil
}

func (t *draftAutomationTool) IsReadOnly() bool { return true }

func (t *draftAutomationTool) Execute(_ *rctx.ToolContext, params DraftAutomationParams) (ToolResponse, error) {
	draft, err := BuildAutomationDraft(params, DraftOptions{})
	if err != nil {
		return NewTextErrorResponse("The draft is not valid yet: " + err.Error() + ". Fix it and call draft_automation again."), nil
	}
	raw, err := MarshalAutomationDraft(draft)
	if err != nil {
		return NewTextErrorResponse("Could not encode the draft: " + err.Error()), nil
	}
	text := fmt.Sprintf("Drafted: %s. The user now sees it as a card with \"Review & create\"; nothing is created or turned on until they choose where it runs and press Create. Tell them it is ready to review, not that it is running.", draft.GetSummary())
	return WithResponseMetadata(NewTextResponse(text), DraftAutomationMetadata{Draft: raw}), nil
}

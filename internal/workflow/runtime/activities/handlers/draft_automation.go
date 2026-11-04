// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/store"
)

// DraftAutomationInput is what TriggerService.DraftAutomation asks for: a
// person's description of an automation, with what the dialog knows.
type DraftAutomationInput struct {
	UserID      string `json:"user_id" reliant:"-"`
	Description string `json:"description"`
	// Workflow, when set, is the workflow the automation must run.
	Workflow string `json:"workflow,omitempty"`
	// Timezone is the person's IANA zone, for "at 9".
	Timezone string `json:"timezone,omitempty"`
}

// DraftAutomationOutput carries the draft as protojson AutomationDraft — a
// string, so the Temporal payload converter never re-encodes a proto with
// encoding/json rules.
type DraftAutomationOutput struct {
	Draft string `json:"draft"`
}

// DraftAutomationActivity drafts an automation from a description with one
// model request pinned to the draft_automation tool (ui-review report 03,
// §6.7). The tool's schema is the structured output and its normalizer is
// the validation, so the "Describe it" box and the chat tool produce the
// same draft. Nothing is stored.
type DraftAutomationActivity struct {
	driverResolver drivers.DriverResolver
	// routing picks which account of the resolved provider drafts, and
	// benches or retires one the provider refuses; nil routes nothing.
	routing *AccountRouting
	// usage records the draft's model request in the usage ledger; nil
	// records nothing.
	usage LLMUsageRecorder
}

// WithUsageRecorder wires the usage ledger into drafting.
func (a *DraftAutomationActivity) WithUsageRecorder(r LLMUsageRecorder) *DraftAutomationActivity {
	a.usage = r
	return a
}

// WithAccountRouting wires multi-account routing into drafting, as titling
// has it: without it the draft always goes to the provider's default account,
// even while that account is limited, and a refusal records nothing.
func (a *DraftAutomationActivity) WithAccountRouting(r *AccountRouting) *DraftAutomationActivity {
	a.routing = r
	return a
}

// NewDraftAutomationActivity creates a DraftAutomationActivity. A nil
// resolver selects the model from the user's providers.
func NewDraftAutomationActivity(resolver drivers.DriverResolver) *DraftAutomationActivity {
	return &DraftAutomationActivity{driverResolver: resolver}
}

// Name returns the activity name for registration.
func (a *DraftAutomationActivity) Name() string { return "DraftAutomation" }

// Execute drafts the automation.
func (a *DraftAutomationActivity) Execute(ctx context.Context, input DraftAutomationInput) (DraftAutomationOutput, error) {
	description := strings.TrimSpace(input.Description)
	if description == "" {
		return DraftAutomationOutput{}, fmt.Errorf("description is empty")
	}
	// The user's own providers, for API key loading.
	ctx = context.WithValue(ctx, auth.UserIDContextKey, input.UserID)

	temperature := 0.2
	maxTokens := int64(2048)
	spec := llmCallSpec{
		UserID:      input.UserID,
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
		// A single request with one tool: a tool call is the only thing the
		// model can emit, so the answer is always the draft's shape.
		ForceToolChoice: tools.ToolDraftAutomation,
	}
	if a.driverResolver == nil {
		// Writing a cron line and a self-contained prompt is a small job:
		// the user's fast text model, and moderate only when they have none.
		spec.Selector = titleModelSelector()
		spec.FallbackSelector = titleModelFallbackSelector()
	}
	// No ThreadID: a draft belongs to no thread, so it reads and writes no
	// sticky account assignment. A refused account is benched below, so the
	// workflow's retry re-resolves to another one.
	spec.Routing = a.routing
	resolved, err := resolveLLMCall(ctx, a.driverResolver, spec)
	if err != nil {
		return DraftAutomationOutput{}, err
	}

	draftTool := tools.NewDraftAutomationTool()
	prompt := []string{draftAutomationSystemPrompt(input)}
	messages := []message.Message{{
		Role: "user",
		Parts: []message.ContentPart{message.TextContent{Text: fmt.Sprintf(
			"<automation_description>\n%s\n</automation_description>\n\n"+
				"Do not carry out the request above. Call %s once with the automation it describes.",
			description, tools.ToolDraftAutomation)}},
	}}

	// Stream and accumulate: not every provider has a non-streaming path
	// (see generateTitle).
	// A draft belongs to no chat, so its ledger row has none.
	meter := newUsageMeter(a.usage, spec.UserID, "", store.LLMPurposeDraftAutomation)
	response, err := meter.streamAndAccumulate(a.routing.observe(ctx, spec.UserID, resolved.Account), resolved, prompt, messages, []tools.Tool{draftTool})
	if err != nil {
		return DraftAutomationOutput{}, a.routing.providerFailure(ctx, fmt.Errorf("draft automation: %w", err), usageLimitContext{
			UserID: spec.UserID, Driver: models.DriverID(resolved.ProviderDriver),
			ModelID: definitionID(resolved.Definition), Account: resolved.Account,
		})
	}
	params, err := draftParamsFromToolCalls(response.ToolCalls)
	if err != nil {
		return DraftAutomationOutput{}, err
	}
	draft, err := tools.BuildAutomationDraft(params, tools.DraftOptions{Timezone: input.Timezone, Workflow: input.Workflow})
	if err != nil {
		// Retried by the workflow: a second sample usually fixes a bad cron.
		return DraftAutomationOutput{}, fmt.Errorf("the drafted automation is not valid: %w", err)
	}
	raw, err := tools.MarshalAutomationDraft(draft)
	if err != nil {
		return DraftAutomationOutput{}, fmt.Errorf("encode draft: %w", err)
	}
	return DraftAutomationOutput{Draft: string(raw)}, nil
}

func draftAutomationSystemPrompt(input DraftAutomationInput) string {
	var b strings.Builder
	b.WriteString("You turn a person's description of an automation into a draft they will review in a form before anything is created. ")
	b.WriteString("Call " + tools.ToolDraftAutomation + " exactly once.\n\n")
	b.WriteString("- when: a schedule for times (\"every weekday at 9\" is cron '0 9 * * 1-5'; \"every 30 minutes\" is interval '30m'); integration for a provider event (GitHub pull requests and issues, Slack messages, Gmail email, Twilio SMS), with the event types as the provider names them ('pull_request.opened', 'issues.opened'); webhook when another system will call it; workflow_event when it follows another workflow's run finishing, failing or being blocked.\n")
	b.WriteString("- prompt: the message every run starts from. Nobody is watching when it runs, so make it complete and specific; keep the person's intent and do not invent requirements.\n")
	b.WriteString("- summary: one line saying when and what, in plain words.\n")
	b.WriteString("- Leave name empty unless the person named it. Leave workflow empty (the person's default agent).\n")
	b.WriteString("- assumptions: list anything you had to assume (a time, a repository, an event type).\n")
	if tz := strings.TrimSpace(input.Timezone); tz != "" {
		fmt.Fprintf(&b, "\nThe person's time zone is %s; leave when.timezone empty to use it.\n", tz)
	}
	if w := strings.TrimSpace(input.Workflow); w != "" {
		fmt.Fprintf(&b, "\nThe automation runs the workflow %q; describe in the prompt what each run should do with it.\n", w)
	}
	return b.String()
}

func draftParamsFromToolCalls(toolCalls []message.ToolCall) (tools.DraftAutomationParams, error) {
	for _, call := range toolCalls {
		// Claude Code presents tools under an mcp__ prefix (see titleFromToolCalls).
		if call.Name != tools.ToolDraftAutomation && !strings.HasSuffix(call.Name, "__"+tools.ToolDraftAutomation) {
			continue
		}
		var params tools.DraftAutomationParams
		if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
			return params, fmt.Errorf("parse %s arguments: %w", tools.ToolDraftAutomation, err)
		}
		return params, nil
	}
	return tools.DraftAutomationParams{}, fmt.Errorf("model did not call %s", tools.ToolDraftAutomation)
}

package celeval

import (
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
)

// TriggerRun is what one declared trigger does with one sample event: whether
// its filter passes it, what its inputs mapping yields, and the prompt it
// renders. Each part runs through the functions the runtime fires a trigger
// with (triggerspec.CompileFilter, EvaluateInputs, RenderPrompt), so a sample
// that evaluates here evaluates for a real event of the same shape.
type TriggerRun struct {
	Trigger string `json:"trigger"`
	Kind    string `json:"kind"`
	// Source says where trigger.payload came from.
	Source string `json:"payload_source"`
	// Matched is whether the filter passes the sample; nil when it errored.
	Matched     *bool          `json:"matched,omitempty"`
	FilterError string         `json:"filter_error,omitempty"`
	Inputs      map[string]any `json:"inputs,omitempty"`
	InputErrors []string       `json:"input_errors,omitempty"`
	Prompt      string         `json:"prompt,omitempty"`
	PromptError string         `json:"prompt_error,omitempty"`
	// DedupeKey is the rendered dedupe_key (webhook triggers that declare one),
	// the text deliveries are grouped by; empty when none is declared.
	DedupeKey   string `json:"dedupe_key,omitempty"`
	DedupeError string `json:"dedupe_error,omitempty"`
	// Findings are reads the integration's declared payload rules out.
	Findings []string `json:"findings,omitempty"`
}

// Errors lists what failed, one message each; empty when the trigger ran clean
// on the sample. A filter that simply did not match is not an error.
func (r TriggerRun) Errors() []string {
	var out []string
	prefix := fmt.Sprintf("trigger %q: ", r.Trigger)
	if r.FilterError != "" {
		out = append(out, prefix+"filter: "+r.FilterError)
	}
	for _, e := range r.InputErrors {
		out = append(out, prefix+e)
	}
	if r.PromptError != "" {
		out = append(out, prefix+"prompt: "+r.PromptError)
	}
	if r.DedupeError != "" {
		out = append(out, prefix+"dedupe_key: "+r.DedupeError)
	}
	for _, f := range r.Findings {
		out = append(out, prefix+f)
	}
	return out
}

// DryRunTrigger runs a declared trigger against a sample payload. For an
// integration trigger the payload is merged over the sample generated from the
// declared payload of the event (payload["event"], else the trigger's first
// exact event), so a partial sample is enough.
//
// activation says who the sample activation belongs to and what it sets; nil
// is an activation with no params and no GitHub connection.
//
// workflowInputs are the inputs of the workflow that declares the trigger, and
// the sample's Params what the activation sets for them: together they make
// trigger.activation.params, as an activation of it would have. Params nobody sets
// read as the workflow's defaults.
func DryRunTrigger(wt *reliantv1.WorkflowTrigger, workflowInputs map[string]*reliantv1.Input, payload, sender map[string]any, activation *ActivationSample) TriggerRun {
	run := TriggerRun{Trigger: wt.GetName()}
	req := Request{TriggerPayload: payload, TriggerSender: sender}
	switch {
	case wt.GetIntegration() != nil:
		run.Kind = "integration"
		src := wt.GetIntegration()
		req.Integration = strings.TrimSpace(src.GetIntegration())
		req.Event = sampleEvent(src, payload)
		if req.Event == "" {
			req.Integration = "" // no exact event to generate from: use the payload as given
		}
		req.TriggerKind = "integration"
	case wt.GetWebhook() != nil:
		run.Kind = "webhook"
		req.TriggerKind = "webhook"
	case wt.GetSchedule() != nil:
		run.Kind = "schedule"
		req.TriggerKind = "schedule"
	case wt.GetWorkflowEvent() != nil:
		run.Kind = "workflow_event"
		req.TriggerKind = "workflow_event"
	}

	trigger, source, err := buildTrigger(req)
	if err != nil {
		run.FilterError = err.Error()
		return run
	}
	run.Source = source
	if trigger == nil {
		trigger = map[string]any{}
	}
	scope := triggerspec.Scope{
		Trigger: trigger,
		Activation: triggerspec.ActivationRoot("sample-activation",
			triggerspec.Owner{UserID: "sample-owner", GitHubID: activation.ownerGitHubID()},
			runtime.ActivationParams(workflowInputs, wt.GetInputs(), normalizeNumbers(activation.params()))),
	}

	if filter := strings.TrimSpace(wt.GetFilter()); filter != "" {
		compiled, cerr := triggerspec.CompileFilter(filter)
		if cerr != nil {
			run.FilterError = cerr.Error()
		} else if hit, merr := compiled.MatchScope(scope); merr != nil {
			run.FilterError = merr.Error()
		} else {
			run.Matched = &hit
		}
		if req.Integration != "" {
			run.Findings = append(run.Findings, shapeFindings(Request{Expression: filter, Integration: req.Integration, Event: req.Event}, KindExpression)...)
		}
	} else {
		matched := true
		run.Matched = &matched
	}

	if len(wt.GetInputs()) > 0 {
		inputs, ierr := triggerspec.EvaluateInputs(wt.GetInputs(), scope)
		if ierr != nil {
			run.InputErrors = append(run.InputErrors, ierr.Error())
		} else {
			run.Inputs = inputs
			// What a firing of this event would be refused for: the values
			// against the workflow's inputs, by the launch's own rule.
			for _, problem := range validation.ValidateTriggerInputValues(workflowInputs, inputs, nil) {
				run.InputErrors = append(run.InputErrors, "inputs: "+problem)
			}
		}
	}
	if key := wt.GetDedupeKey(); key != "" {
		if cerr := triggerspec.CompileDedupe(key, wt.GetDedupeWindow()); cerr != nil {
			run.DedupeError = cerr.Error()
		} else if rendered, rerr := triggerspec.RenderDedupeKey(key, scope); rerr != nil {
			run.DedupeError = rerr.Error()
		} else {
			run.DedupeKey = rendered
		}
	}
	if prompt := wt.GetPrompt(); prompt != "" {
		rendered, perr := triggerspec.RenderPrompt(prompt, scope)
		if perr != nil {
			run.PromptError = perr.Error()
		} else {
			run.Prompt = rendered
		}
	}
	return run
}

// sampleEvent picks the integration event to generate a sample from: the one
// the payload names, else the trigger's first exact (non-wildcard) event.
func sampleEvent(src *reliantv1.IntegrationSource, payload map[string]any) string {
	if ev, ok := payload["event"].(string); ok && ev != "" {
		return ev
	}
	for _, ev := range src.GetEvents() {
		ev = strings.TrimSpace(ev)
		if ev != "" && !strings.Contains(ev, "*") {
			return ev
		}
	}
	return ""
}

// ActivationSample is the activation a dry run stands in for.
type ActivationSample struct {
	// Params are what it sets for the workflow's inputs.
	Params map[string]any
	// OwnerGitHubID is trigger.activation.owner.github_id.
	OwnerGitHubID string
}

func (a *ActivationSample) params() map[string]any {
	if a == nil {
		return nil
	}
	return a.Params
}

func (a *ActivationSample) ownerGitHubID() string {
	if a == nil {
		return ""
	}
	return a.OwnerGitHubID
}

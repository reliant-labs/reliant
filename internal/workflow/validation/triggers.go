// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// Layer 7: the workflow's `triggers:` block — WHEN it should run.
//
// Every rule a stored activation of a trigger would face is checked here,
// with the same code (triggerspec): a declaration this layer accepts is one
// TriggerService will activate. Activation-only facts — which connection,
// which daemon, whether this deployment receives the integration's events —
// belong to whoever activates it and are checked then, not here.

// triggerNamePattern is a trigger's name: what an activation refers to. A
// lowercase identifier — the same alphabet as a node id plus hyphens, so the
// `name: issue_opened` the integration schema tool suggests is valid.
var triggerNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]{0,62}[a-z0-9])?$`)

// IntegrationIndex says which integrations exist and what their declared
// triggers deliver: the provider event types (TriggerSpec.events) and the
// routing attributes (TriggerSpec.attributes) those events carry. ok=false is
// an integration not in the catalog; an integration that declares no
// triggers returns no types and is not narrowed.
type IntegrationIndex interface {
	TriggerTypes(integration string) (eventTypes, attributes []string, ok bool)
}

// catalogIntegrations is the IntegrationIndex over the embedded catalog.
type catalogIntegrations struct{}

func (catalogIntegrations) TriggerTypes(integration string) ([]string, []string, bool) {
	c, err := catalog.Builtin()
	if err != nil {
		return nil, nil, false
	}
	found := false
	var events, attrs []string
	for _, m := range c.Manifests() {
		if m.GetId() != integration {
			continue
		}
		found = true
		for _, ts := range m.GetTriggers() {
			events = append(events, ts.GetEvents()...)
			for _, a := range ts.GetAttributes() {
				attrs = append(attrs, a.GetName())
			}
		}
	}
	return events, attrs, found
}

func validateTriggers(wf *reliantv1.Workflow, opts *ValidationOptions, integrations IntegrationIndex, result *Result) {
	triggers := wf.GetTriggers()
	if len(triggers) == 0 {
		return
	}
	declaredInputs := wf.GetInputs()
	seen := make(map[string]int, len(triggers))

	for i, wt := range triggers {
		name := strings.TrimSpace(wt.GetName())
		path := []string{wf.GetName(), fmt.Sprintf("triggers[%d](%s)", i, name)}
		if name == "" {
			path[1] = fmt.Sprintf("triggers[%d]", i)
		}

		switch {
		case name == "":
			result.AddErrorWithSuggestion(CategoryTrigger, path, "name", "required",
				"name the trigger; activations refer to it by name")
		case !triggerNamePattern.MatchString(name):
			result.AddErrorWithSuggestion(CategoryTrigger, path, "name",
				fmt.Sprintf("%q is not a slug", name),
				"use lowercase letters, digits and hyphens, e.g. new-issue")
		default:
			if first, dup := seen[name]; dup {
				result.AddError(CategoryTrigger, path, "name",
					fmt.Sprintf("%q is used more than once (first at triggers[%d]); names must be unique within the workflow", name, first))
			} else {
				seen[name] = i
			}
		}

		src, err := triggerspec.FromWorkflowTrigger(wt)
		if err != nil {
			addTriggerSourceError(result, path, wt, err)
		}

		if filter := strings.TrimSpace(wt.GetFilter()); filter != "" {
			if wt.GetSchedule() != nil {
				result.AddErrorWithSuggestion(CategoryTrigger, path, "filter",
					"a schedule fires on time, not on an event, so there is nothing to filter",
					"remove the filter, or branch on the schedule inside the workflow")
			} else if _, err := triggerspec.CompileFilter(filter); err != nil {
				result.AddErrorWithSuggestion(CategoryTrigger, path, "filter", strings.TrimPrefix(err.Error(), "filter: "),
					"a filter is a raw CEL bool over `trigger` (e.g. trigger.payload.data.action == 'opened'); guard optional fields with has()")
			}
		}

		if src != nil && wt.GetIntegration() != nil {
			validateTriggerIntegration(wt.GetIntegration(), path, integrations, result)
		}
		if ev := wt.GetWorkflowEvent(); ev != nil && opts != nil && opts.WorkflowLoader != nil {
			validateWorkflowEventRefs(ev, path, opts.WorkflowLoader, result)
		}
		validateTriggerInputs(wt.GetInputs(), declaredInputs, path, result)
		warnUnmappedRequiredInputs(wt.GetInputs(), declaredInputs, path, result)
		if err := triggerspec.CompilePrompt(wt.GetPrompt()); err != nil {
			result.AddErrorWithSuggestion(CategoryTrigger, path, "prompt", strings.TrimPrefix(err.Error(), "prompt: "),
				"a prompt reads only `trigger` (e.g. Triage #{{ trigger.payload.data.issue.number }}); the run's inputs and nodes do not exist yet")
		}
	}
}

// warnUnmappedRequiredInputs points out a required input that only an event
// or a param can supply and that this trigger does not map: every activation
// would have to set it in params, or every firing fails input validation.
// Only plain value inputs are considered; a model, message, preset, group or
// tools input is normally supplied by presets or the launch itself.
func warnUnmappedRequiredInputs(mapped map[string]string, declared map[string]*reliantv1.Input, path []string, result *Result) {
	var missing []string
	for name, input := range declared {
		if _, ok := mapped[name]; ok || !model.IsInputRequired(input) {
			continue
		}
		switch input.GetConfig().(type) {
		case *reliantv1.Input_StringInput, *reliantv1.Input_IntegerInput, *reliantv1.Input_NumberInput,
			*reliantv1.Input_BooleanInput, *reliantv1.Input_EnumInput, *reliantv1.Input_ArrayInput,
			*reliantv1.Input_ObjectInput:
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return
	}
	sort.Strings(missing)
	result.AddWarning(CategoryTrigger, path, "inputs", fmt.Sprintf(
		"required input(s) %s are not mapped from the event, so every activation must set them in params or each firing fails; map them here, or give them a default",
		strings.Join(missing, ", ")))
}

// addTriggerSourceError reports a source the shared rules rejected, under
// the field of the arm it is about.
func addTriggerSourceError(result *Result, path []string, wt *reliantv1.WorkflowTrigger, err error) {
	field := "source"
	if src := wt.ProtoReflect().WhichOneof(wt.ProtoReflect().Descriptor().Oneofs().ByName("source")); src != nil {
		field = string(src.Name())
	}
	var cfgErr *triggerspec.ConfigError
	msg := err.Error()
	if ok := asConfigError(err, &cfgErr); ok {
		sub := strings.TrimPrefix(cfgErr.Field, field+".")
		if sub != "" && sub != field && sub != "source" {
			field += "." + sub
		}
		msg = cfgErr.Reason
	}
	if wt.GetSource() == nil {
		result.AddErrorWithSuggestion(CategoryTrigger, path, "source", "a source is required",
			"give it exactly one of schedule, webhook, integration or workflow_event")
		return
	}
	result.AddError(CategoryTrigger, path, field, msg)
}

func asConfigError(err error, target **triggerspec.ConfigError) bool {
	ce, ok := err.(*triggerspec.ConfigError)
	if ok {
		*target = ce
	}
	return ok
}

func validateTriggerIntegration(src *reliantv1.IntegrationSource, path []string, integrations IntegrationIndex, result *Result) {
	id := strings.TrimSpace(src.GetIntegration())
	types, attrs, ok := integrations.TriggerTypes(id)
	if !ok {
		// A warning, not an error: which integrations can DELIVER events is
		// a fact about the deployment (its registered webhook providers and
		// pollers), which activation checks; the catalog only says which are
		// documented. And a declaration is not part of what a run executes,
		// so it must not make the workflow unrunnable from a chat.
		result.Add(&Error{
			Severity: SeverityWarning, Category: CategoryTrigger, Path: path, Field: "integration.integration",
			Message:    fmt.Sprintf("%q is not in the integration catalog, so its events cannot be checked; activating this trigger will fail unless this server receives %s events", id, id),
			Suggestion: "search_integrations(kind: trigger) lists what exists",
		})
		return
	}
	if len(types) == 0 {
		return
	}
	var unknown []string
	for _, ev := range src.GetEvents() {
		if !eventMatchesAny(strings.TrimSpace(ev), types) {
			unknown = append(unknown, ev)
		}
	}
	if len(unknown) > 0 {
		result.AddErrorWithSuggestion(CategoryTrigger, path, "integration.events",
			fmt.Sprintf("%s does not deliver %s; it delivers %s",
				id, strings.Join(unknown, ", "), strings.Join(uniqueSorted(types), ", ")),
			"get_integration_schema on the trigger shows its events")
	}
	// match compares event attributes by equality; a key no event of this
	// integration carries can never be equal, so the trigger would never fire.
	declared := make(map[string]bool, len(attrs))
	for _, a := range attrs {
		declared[a] = true
	}
	keys := make([]string, 0, len(src.GetMatch()))
	for key := range src.GetMatch() {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !declared[key] {
			known := "none"
			if len(attrs) > 0 {
				known = strings.Join(uniqueSorted(attrs), ", ")
			}
			result.AddError(CategoryTrigger, path, "integration.match."+key,
				fmt.Sprintf("%s events carry no %q attribute, so this trigger would never fire (attributes: %s)", id, key, known))
		}
	}
}

func uniqueSorted(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// eventMatchesAny reports whether a trigger's event pattern names at least
// one type the integration delivers: "*" matches all, "issues.*" a prefix.
func eventMatchesAny(pattern string, types []string) bool {
	if pattern == "*" {
		return true
	}
	for _, t := range types {
		if pattern == t {
			return true
		}
		if strings.HasSuffix(pattern, ".*") && strings.HasPrefix(t, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
	return false
}

// validateWorkflowEventRefs warns about a referenced workflow that does not
// resolve. A warning, not an error: the user may be writing the referenced
// workflow next, and the trigger simply never matches until it exists.
func validateWorkflowEventRefs(src *reliantv1.WorkflowEventSource, path []string, loader WorkflowLoader, result *Result) {
	for _, ref := range src.GetWorkflows() {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		wf, err := loader(ref)
		if err == nil && wf != nil {
			continue
		}
		msg := fmt.Sprintf("workflow %q does not resolve yet; this trigger matches nothing from it until it exists", ref)
		if err != nil {
			msg = fmt.Sprintf("workflow %q does not resolve (%v); this trigger matches nothing from it until it does", ref, err)
		}
		result.AddWarning(CategoryTrigger, path, "workflow_event.workflows", msg)
	}
}

// validateTriggerInputs checks the inputs mapping: every key is a declared
// workflow input, and every value compiles as a template over `trigger`.
func validateTriggerInputs(inputs map[string]string, declared map[string]*reliantv1.Input, path []string, result *Result) {
	if len(inputs) == 0 {
		return
	}
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := declared[name]; !ok {
			result.AddErrorWithSuggestion(CategoryTrigger, path, "inputs."+name,
				fmt.Sprintf("%q is not a declared input of this workflow", name),
				declaredInputsHint(declared))
			continue
		}
		if triggerspec.LooksLikeBareExpression(inputs[name]) {
			result.AddWarning(CategoryTrigger, path, "inputs."+name,
				fmt.Sprintf("%q is passed as literal text; wrap it as {{ %s }} to read the event", inputs[name], strings.TrimSpace(inputs[name])))
		}
	}
	for _, e := range triggerspec.CompileInputs(inputs) {
		if _, ok := declared[e.Input]; !ok {
			continue // already reported
		}
		result.AddErrorWithSuggestion(CategoryTrigger, path, "inputs."+e.Input, e.Reason,
			"an input mapping reads only `trigger` (e.g. {{ trigger.payload.data.issue.number }}); the run's inputs and nodes do not exist yet")
	}
}

func declaredInputsHint(declared map[string]*reliantv1.Input) string {
	if len(declared) == 0 {
		return "declare it under the workflow's inputs: first"
	}
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)
	return "declared inputs: " + strings.Join(names, ", ")
}

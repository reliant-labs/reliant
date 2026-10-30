// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
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
//
// PayloadSchemas is the JSON Schema of trigger.payload for one event type
// (manifest.TriggerPayloadSchema), one per declared trigger that delivers
// it; none when nothing declares it.
//
// EventAttributes are the routing attributes one event type carries: name ->
// the qualified value type (`github.repository`) the attribute is declared
// with, "" for a plain string. An attribute a trigger declares for the event
// is present; one it does not declare is absent.
type IntegrationIndex interface {
	TriggerTypes(integration string) (eventTypes, attributes []string, ok bool)
	PayloadSchemas(integration, eventType string) []map[string]any
	EventAttributes(integration, eventType string) map[string]string
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

func (catalogIntegrations) PayloadSchemas(integration, eventType string) []map[string]any {
	c, err := catalog.Builtin()
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, m := range c.Manifests() {
		if m.GetId() != integration {
			continue
		}
		for _, ts := range m.GetTriggers() {
			for _, ev := range ts.GetEvents() {
				if ev == eventType {
					out = append(out, manifest.TriggerPayloadSchema(m, ts))
					break
				}
			}
		}
	}
	return out
}

func (catalogIntegrations) EventAttributes(integration, eventType string) map[string]string {
	c, err := catalog.Builtin()
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, m := range c.Manifests() {
		if m.GetId() != integration {
			continue
		}
		for _, ts := range m.GetTriggers() {
			delivers := false
			for _, ev := range ts.GetEvents() {
				if ev == eventType {
					delivers = true
					break
				}
			}
			if !delivers {
				continue
			}
			for _, a := range ts.GetAttributes() {
				out[a.GetName()] = qualifyTypeRef(integration, a.GetType())
			}
		}
	}
	return out
}

// qualifyTypeRef writes a manifest's type reference as `<integration>.<id>`:
// an attribute names a type of its own manifest by its bare id.
func qualifyTypeRef(integration, ref string) string {
	if ref == "" || strings.Contains(ref, ".") {
		return ref
	}
	return integration + "." + ref
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

		// The declared types of `trigger`, `trigger.activation` included, for this
		// trigger: the payload its source's events declare (dynamic when
		// there is none), and the workflow inputs an activation sets.
		var payloads []map[string]any
		declaredBy := ""
		if src != nil && wt.GetIntegration() != nil {
			validateTriggerIntegration(wt, declaredInputs, path, integrations, result)
			warnLegacyScopeFilter(wt, declaredInputs, path, integrations, result)
			payloads, declaredBy = integrationPayloads(wt.GetIntegration(), integrations)
		}
		shape := newPayloadShape(triggerspec.ParamsSchema(triggerspec.ParamsInputs(declaredInputs, wt.GetInputs()), opts.valueTypes()), payloads, declaredBy)

		if filter := strings.TrimSpace(wt.GetFilter()); filter != "" {
			if wt.GetSchedule() != nil {
				result.AddErrorWithSuggestion(CategoryTrigger, path, "filter",
					"a schedule fires on time, not on an event, so there is nothing to filter",
					"remove the filter, or branch on the schedule inside the workflow")
			} else if _, err := triggerspec.CompileFilter(filter); err != nil {
				result.AddErrorWithSuggestion(CategoryTrigger, path, "filter", strings.TrimPrefix(err.Error(), "filter: "),
					"a filter is a raw CEL bool over `trigger` (e.g. trigger.payload.data.action == 'opened' or trigger.payload.attributes.repository in trigger.activation.params.repos); guard optional fields with has()")
			} else if shape != nil {
				shape.report(result, path, "filter", "", shape.CheckFilter(filter))
			}
		}

		validateTriggerOriginFilter(wt, src, path, shape, result)

		if ev := wt.GetWorkflowEvent(); ev != nil && opts != nil && opts.WorkflowLoader != nil {
			validateWorkflowEventRefs(ev, path, opts.WorkflowLoader, result)
		}
		validateTriggerDedupe(wt, path, result)
		validateTriggerInputs(wt.GetInputs(), declaredInputs, path, shape, opts.valueTypes(), result)
		warnUnmappedRequiredInputs(wt.GetInputs(), scopeInputNames(wt), declaredInputs, path, result)
		if err := triggerspec.CompilePrompt(wt.GetPrompt()); err != nil {
			result.AddErrorWithSuggestion(CategoryTrigger, path, "prompt", strings.TrimPrefix(err.Error(), "prompt: "),
				"a prompt reads only `trigger` (e.g. Triage #{{ trigger.payload.data.issue.number }}); the run's inputs and nodes do not exist yet")
		} else if shape != nil {
			// A prompt that does not scan was reported by CompilePrompt above.
			exprs, _ := triggerspec.InputExpressions(wt.GetPrompt())
			for _, expr := range exprs {
				shape.report(result, path, "prompt", expr, shape.CheckExpr(expr))
			}
		}
	}
}

// payloadShape is a trigger's typed `trigger` root, with
// what the payload was declared by for the findings it produces.
type payloadShape struct {
	*triggerspec.Shape
	// declaredBy names where the payload type comes from, e.g. "checked
	// against the payload github declares for issues.opened"; empty when the
	// payload is dynamic.
	declaredBy string
}

// newPayloadShape types the roots. params is trigger.activation.params' schema and
// payloads the schemas of trigger.payload (none leaves it dynamic). nil when
// the environment cannot be built.
func newPayloadShape(params map[string]any, payloads []map[string]any, declaredBy string) *payloadShape {
	shape, err := triggerspec.NewShape(params, payloads...)
	if err != nil {
		// The environment is built from the catalog and the workflow's own
		// input declarations, not from anything an author wrote here: a
		// failure is a bug, and the untyped checks still ran.
		return nil
	}
	return &payloadShape{Shape: shape, declaredBy: declaredBy}
}

// integrationPayloads are the payload schemas an integration source's events
// declare: the union over the events it listens to, and the patterns as
// written that named a declared payload. none when no event declares one.
func integrationPayloads(src *reliantv1.IntegrationSource, integrations IntegrationIndex) ([]map[string]any, string) {
	id := strings.TrimSpace(src.GetIntegration())
	types, _, ok := integrations.TriggerTypes(id)
	if !ok || len(types) == 0 {
		return nil, ""
	}
	var schemas []map[string]any
	var typed []string // the patterns as written that named a declared payload
	seen := map[string]bool{}
	for _, pattern := range src.GetEvents() {
		pattern = strings.TrimSpace(pattern)
		matched := false
		for _, t := range uniqueSorted(types) {
			if !eventMatches(pattern, t) {
				continue
			}
			if !seen[t] {
				seen[t] = true
				schemas = append(schemas, integrations.PayloadSchemas(id, t)...)
			}
			matched = true
		}
		if matched {
			typed = append(typed, pattern)
		}
	}
	if len(schemas) == 0 {
		return nil, ""
	}
	return schemas, fmt.Sprintf("checked against the payload %s declares for %s", id, strings.Join(typed, ", "))
}

// activationParamsNote says where trigger.activation.params' fields come from, for a
// finding about one.
const activationParamsNote = "trigger.activation.params holds the workflow's declared inputs that this trigger does not map from the event"

// report adds a type error for each finding under field. expr, when set, is
// the template expression the findings are about.
func (s *payloadShape) report(result *Result, path []string, field, expr string, errs []*triggerspec.TypeError) {
	for _, e := range errs {
		msg := e.Reason
		switch {
		case strings.Contains(e.Reason, "trigger.activation."):
			msg += " (" + activationParamsNote + ")"
		case s.declaredBy != "":
			msg += " (" + s.declaredBy + ")"
		}
		if expr != "" {
			msg = fmt.Sprintf("{{ %s }}: %s", expr, msg)
		}
		if e.Suggestion != "" {
			result.AddErrorWithSuggestion(CategoryTrigger, path, field, msg, e.Suggestion)
		} else {
			result.AddError(CategoryTrigger, path, field, msg)
		}
	}
}

// warnUnmappedRequiredInputs points out a required input that only an event
// or a param can supply and that this trigger does not map: every activation
// would have to set it in params, or every firing fails input validation.
// An input that scopes this trigger (match_input) is not flagged: choosing it
// is what turning the automation on asks for. Only plain value inputs are considered; a model, message, preset, group or
// tools input is normally supplied by presets or the launch itself. An
// integration input is a plain value, so it is considered.
func warnUnmappedRequiredInputs(mapped map[string]string, scoping map[string]bool, declared map[string]*reliantv1.Input, path []string, result *Result) {
	var missing []string
	for name, input := range declared {
		if _, ok := mapped[name]; ok || scoping[name] || !model.IsInputRequired(input) {
			continue
		}
		if isSuppliedByParams(input) {
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

// scopeInputNames are the workflow inputs a trigger's typed scope reads.
func scopeInputNames(wt *reliantv1.WorkflowTrigger) map[string]bool {
	out := map[string]bool{}
	for _, input := range wt.GetIntegration().GetMatchInput() {
		out[strings.TrimSpace(input)] = true
	}
	return out
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

func validateTriggerIntegration(wt *reliantv1.WorkflowTrigger, wfInputs map[string]*reliantv1.Input, path []string, integrations IntegrationIndex, result *Result) {
	src := wt.GetIntegration()
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
		if len(src.GetMatchInput()) > 0 {
			result.AddWarning(CategoryTrigger, path, "integration.match",
				fmt.Sprintf("%q is not in the integration catalog, so the types of its attributes cannot be checked against the inputs that scope this trigger", id))
		}
		return
	}
	if len(types) == 0 {
		validateTriggerScope(wt, wfInputs, path, id, nil, integrations, result)
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
	var delivered []string
	for _, ev := range src.GetEvents() {
		ev = strings.TrimSpace(ev)
		for _, t := range uniqueSorted(types) {
			if eventMatches(ev, t) {
				delivered = append(delivered, t)
			}
		}
	}
	validateTriggerScope(wt, wfInputs, path, id, uniqueSorted(delivered), integrations, result)
}

// validateTriggerScope checks `match: {attribute: {input: name}}` (match_input).
// A scope narrows by an attribute's declared type, so for every event the
// trigger listens to:
//
//   - the attribute must be one that event carries, with a value type;
//   - the input must be an integration input of that same type, single or
//     multi; and it must not be one the trigger sets from the event, because
//     then the event would scope itself;
//   - the input should be required: an unset scope matches nothing.
//
// events are the concrete event types the source's patterns name; none when
// the integration declares none.
func validateTriggerScope(wt *reliantv1.WorkflowTrigger, declared map[string]*reliantv1.Input, path []string, integration string, events []string, integrations IntegrationIndex, result *Result) {
	scope := wt.GetIntegration().GetMatchInput()
	attrs := make([]string, 0, len(scope))
	for attr := range scope {
		attrs = append(attrs, attr)
	}
	sort.Strings(attrs)
	for _, attr := range attrs {
		field := "integration.match." + attr
		name := scope[attr]

		input, ok := declared[name]
		switch {
		case !ok:
			result.AddErrorWithSuggestion(CategoryTrigger, path, field,
				fmt.Sprintf("input %q is not a declared input of this workflow", name), declaredInputsHint(declared))
			continue
		case !model.IsIntegrationInput(input):
			result.AddErrorWithSuggestion(CategoryTrigger, path, field,
				fmt.Sprintf("input %q is a %s, and a scope needs an input typed by the integration (for example type: github.repository, multi: true)", name, input.GetType()),
				"get_integration_schema shows the types an integration defines")
			continue
		}
		if _, mapped := wt.GetInputs()[name]; mapped {
			result.AddErrorWithSuggestion(CategoryTrigger, path, field,
				fmt.Sprintf("input %q is set from the event by this trigger's inputs, so it cannot scope that same event", name),
				"scope by a different input, or stop mapping "+name+" from the event")
			continue
		}

		attrTypes := scopeAttributeTypes(integration, attr, events, integrations)
		for _, ev := range events {
			t, carried := attrTypes[ev]
			switch {
			case !carried:
				result.AddError(CategoryTrigger, path, field,
					fmt.Sprintf("%s event %s carries no %q attribute, so it cannot be scoped by it", integration, ev, attr))
			case t == "":
				result.AddErrorWithSuggestion(CategoryTrigger, path, field,
					fmt.Sprintf("attribute %q of %s has no value type, so %q cannot be checked against it", attr, ev, name),
					"declare a type on the attribute in the integration's manifest; until then compare it in the filter")
			case t != input.GetType():
				result.AddErrorWithSuggestion(CategoryTrigger, path, field,
					fmt.Sprintf("input %q is a %s, but attribute %q of %s is a %s", name, input.GetType(), attr, ev, t),
					fmt.Sprintf("declare %s as type: %s", name, t))
			default:
				continue
			}
			break // one finding per entry: the same cause repeats for every event
		}
		if len(events) == 0 && len(scope) > 0 {
			result.AddWarning(CategoryTrigger, path, field,
				fmt.Sprintf("%s declares no events, so the type of attribute %q cannot be checked against input %q", integration, attr, name))
		}
		if !model.IsInputRequired(input) {
			result.AddWarning(CategoryTrigger, path, field,
				fmt.Sprintf("input %q is optional, and while it is unset this trigger matches nothing; mark it required: true (and drop its default) so it must be chosen when the automation is turned on", name))
		}
	}
}

// scopeAttributeTypes is the declared type of attribute for each event, for
// the events that carry it.
func scopeAttributeTypes(integration, attr string, events []string, integrations IntegrationIndex) map[string]string {
	out := make(map[string]string, len(events))
	for _, ev := range events {
		if types := integrations.EventAttributes(integration, ev); types != nil {
			if t, ok := types[attr]; ok {
				out[ev] = t
			}
		}
	}
	return out
}

// legacyScopeFilter finds a filter that scopes by hand: an attribute of the
// event tested for membership in an activation param, the pattern a typed
// scope replaces.
var legacyScopeFilter = regexp.MustCompile(
	`trigger\.payload\.attributes(?:\.([A-Za-z_][A-Za-z0-9_]*)|\[\s*['"]([A-Za-z_][A-Za-z0-9_]*)['"]\s*\])\s+in\s+trigger\.activation\.params\.([A-Za-z_][A-Za-z0-9_]*)`)

// warnLegacyScopeFilter points a filter that tests an event attribute for
// membership in a param at the typed scope. Such a filter works, but an event
// outside the list still costs a skipped row per activation, and an empty
// list silently matches nothing.
func warnLegacyScopeFilter(wt *reliantv1.WorkflowTrigger, declared map[string]*reliantv1.Input, path []string, integrations IntegrationIndex, result *Result) {
	filter := strings.TrimSpace(wt.GetFilter())
	if filter == "" {
		return
	}
	src := wt.GetIntegration()
	id := strings.TrimSpace(src.GetIntegration())
	for _, m := range legacyScopeFilter.FindAllStringSubmatch(filter, -1) {
		attr, param := m[1], m[3]
		if attr == "" {
			attr = m[2]
		}
		if _, ok := declared[param]; !ok || src.GetMatchInput()[attr] == param {
			continue
		}
		typed := "a typed input"
		for _, ev := range src.GetEvents() {
			if t := integrations.EventAttributes(id, strings.TrimSpace(ev))[attr]; t != "" {
				typed = "type: " + t
				break
			}
		}
		result.AddWarning(CategoryTrigger, path, "filter", fmt.Sprintf(
			"the filter compares trigger.payload.attributes.%s with the input %s; declare %s as %s and scope with `match: {%s: {input: %s}}` so events outside it are dropped before they are recorded, and an unset list is caught when the automation is turned on",
			attr, param, param, typed, attr, param))
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
// one type the integration delivers.
func eventMatchesAny(pattern string, types []string) bool {
	for _, t := range types {
		if eventMatches(pattern, t) {
			return true
		}
	}
	return false
}

// eventMatches reports whether an event pattern names event type t: "*"
// matches all, "issues.*" a prefix.
func eventMatches(pattern, t string) bool {
	return pattern == "*" || pattern == t ||
		(strings.HasSuffix(pattern, ".*") && strings.HasPrefix(t, strings.TrimSuffix(pattern, "*")))
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
// workflow input, every value compiles as a template over `trigger` — against
// the payload's declared shape, when there is one — and every value whose
// type is known before an event arrives fits its input by the rule the launch
// applies (validateTriggerInputType).
func validateTriggerInputs(inputs map[string]string, declared map[string]*reliantv1.Input, path []string, shape *payloadShape, vt ValueTypes, result *Result) {
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
	failed := map[string]bool{}
	for _, e := range triggerspec.CompileInputs(inputs) {
		failed[e.Input] = true
		if _, ok := declared[e.Input]; !ok {
			continue // already reported
		}
		result.AddErrorWithSuggestion(CategoryTrigger, path, "inputs."+e.Input, e.Reason,
			"an input mapping reads only `trigger` (e.g. {{ trigger.payload.data.issue.number }}); the run's inputs and nodes do not exist yet")
	}
	for _, name := range names {
		if _, ok := declared[name]; !ok || failed[name] {
			continue
		}
		// A value that does not scan was reported by CompileInputs above.
		exprs, _ := triggerspec.InputExpressions(inputs[name])
		if shape != nil {
			typeErrs := false
			for _, expr := range exprs {
				errs := shape.CheckExpr(expr)
				typeErrs = typeErrs || len(errs) > 0
				shape.report(result, path, "inputs."+name, expr, errs)
			}
			if typeErrs {
				continue
			}
		}
		if msg := validateTriggerInputType(name, inputs[name], exprs, declared[name], shape, vt); msg != "" {
			result.AddError(CategoryTrigger, path, "inputs."+name, msg)
		}
	}
}

// validateTriggerInputType checks one inputs value against its declared input
// by the rule every launch applies (InputValue, validateProtoInputType),
// wherever the value's type is known before an event arrives:
//
//   - a literal ("main", 'true') is that text, so it is checked whole, type
//     and constraints, exactly as a firing would check it. A bare YAML
//     boolean or number is not a literal: the parser keeps its type as a
//     {{ }} expression.
//   - a pure expression ({{ expr }}) yields expr's type, when the checker
//     knows it;
//   - text with expressions in it yields a string.
//
// Only plain value inputs are checked: a model, message, preset, group or
// tools input is shaped by the launch before it is validated. It returns the
// problem, or "" when there is none or the type is not known until the event.
func validateTriggerInputType(name, value string, exprs []string, input *reliantv1.Input, shape *payloadShape, vt ValueTypes) string {
	if input == nil || !isSuppliedByParams(input) {
		return ""
	}
	trimmed := strings.TrimSpace(value)
	if len(exprs) == 0 {
		// A literal is passed to the run as its trimmed text.
		if err := validateProtoInputType(name, trimmed, input, vt); err != nil {
			return err.Error()
		}
		return ""
	}
	yields, sample := "", any(nil)
	mixed := !(len(exprs) == 1 && strings.HasPrefix(trimmed, "{{") && strings.HasSuffix(trimmed, "}}") &&
		strings.TrimSpace(trimmed[2:len(trimmed)-2]) == strings.TrimSpace(exprs[0]))
	if mixed {
		yields, sample = "string", ""
	} else if shape != nil {
		switch yields = shape.ExprType(exprs[0]); yields {
		case "bool":
			sample = false
		case "int":
			sample = int64(0)
		case "double":
			// A double may be whole at run time (JSON numbers are doubles),
			// which an integer input accepts: only its non-number uses are
			// known wrong now.
			sample = float64(0)
		case "string":
			sample = ""
		}
	}
	if sample == nil {
		return ""
	}
	switch model.GetInputType(input) {
	case "boolean", "integer", "number", "string", "message", "enum":
	default:
		return "" // a list, object or integration value: checked when it fires
	}
	if _, ok := InputValue(sample, input); ok {
		return ""
	}
	if mixed {
		return fmt.Sprintf("input '%s' is %s, and %q interpolates into text, so it yields a string; write one expression alone, {{ ... }}, yielding %s",
			name, model.GetInputType(input), value, withArticle(model.GetInputType(input)))
	}
	msg := fmt.Sprintf("input '%s' is %s, and %q yields %s, which is never read as %s",
		name, model.GetInputType(input), value, withArticle(celTypeNoun(yields)), withArticle(model.GetInputType(input)))
	if _, ok := InputValue("", input); ok {
		msg += fmt.Sprintf("; convert it, {{ string(%s) }}", strings.TrimSpace(exprs[0]))
	}
	return msg
}

// withArticle is noun with its indefinite article: "an integer", "a string".
func withArticle(noun string) string {
	if noun != "" && strings.ContainsRune("aeiou", rune(noun[0])) {
		return "an " + noun
	}
	return "a " + noun
}

// celTypeNoun is how a CEL scalar type is named to an author.
func celTypeNoun(t string) string {
	switch t {
	case "bool":
		return "boolean"
	case "int":
		return "integer"
	case "double":
		return "number"
	}
	return t
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

// validateTriggerDedupe checks dedupe_key and dedupe_window: webhook triggers
// only (every other source has its own event id), the key a template over
// `trigger` that compiles, the window a duration within bounds.
func validateTriggerDedupe(wt *reliantv1.WorkflowTrigger, path []string, result *Result) {
	key, window := strings.TrimSpace(wt.GetDedupeKey()), strings.TrimSpace(wt.GetDedupeWindow())
	if key == "" && window == "" {
		return
	}
	if wt.GetWebhook() == nil {
		result.AddErrorWithSuggestion(CategoryTrigger, path, "dedupe_key",
			"dedupe_key applies to webhook triggers only",
			"integration, schedule and workflow_event triggers dedupe by their own event id")
		return
	}
	if err := triggerspec.CompileDedupe(key, window); err != nil {
		result.AddErrorWithSuggestion(CategoryTrigger, path, "dedupe_key", strings.TrimPrefix(err.Error(), "dedupe: "),
			"a dedupe_key is a template over `trigger`, e.g. {{ trigger.payload.body.groupKey }}:{{ trigger.payload.body.status }}; dedupe_window is a duration such as 4h (default 1h, at most 24h)")
	}
}

// validateTriggerOriginFilter compiles origin_filter. It is the same language
// and root as `filter`, so it is checked the same way, including against the
// payload's declared shape when the source has one.
func validateTriggerOriginFilter(wt *reliantv1.WorkflowTrigger, src *triggerspec.Source, path []string, shape *payloadShape, result *Result) {
	expr := strings.TrimSpace(wt.GetOriginFilter())
	if expr == "" {
		return
	}
	if src != nil && !src.Kind.IsEventDriven() {
		result.AddErrorWithSuggestion(CategoryTrigger, path, "origin_filter",
			"a schedule fires on time, not on an event, so there is no origin to filter on",
			"remove the origin_filter")
		return
	}
	if _, err := triggerspec.CompileOriginFilter(expr); err != nil {
		result.AddErrorWithSuggestion(CategoryTrigger, path, "origin_filter", strings.TrimPrefix(err.Error(), "filter: origin_filter: "),
			"an origin_filter is a raw CEL bool over `trigger` that decides who may fire this trigger by who caused the event, e.g. !trigger.origin.same_workflow || trigger.origin.trigger_name == 'responder-push'; write true to receive everything")
		return
	}
	if shape != nil {
		shape.report(result, path, "origin_filter", "", shape.CheckFilter(expr))
	}
}

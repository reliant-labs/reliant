package trigger

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	runtime "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// A declared trigger's `inputs:` mapping fills workflow inputs from each event
// (repo: '{{ trigger.payload.attributes.repository }}'), and that mapping wins
// over the activation's params. Approving such an activation therefore cannot
// resolve those inputs to one value: every event brings its own. Refusing
// would make every event-driven workflow that pins its tools to the event's
// repository unapprovable, which is exactly the shape that keeps an agent off
// other repositories.
//
// What the approval can do is bound them. An input mapped from an event
// attribute that the trigger's own match restricts — `match_input:
// {repository: repos}` (one of the activation's repos) or `match: {repository:
// acme/app}` — can only ever take one of those values, because the dispatcher
// delivers no other event. So the snapshot resolves the graph once per
// possible value and discloses the union. An input mapped from anything else
// is unbounded: it takes the opaque eventSupplied value, which a target field
// discloses as dynamic, and which fails the approval closed wherever it
// would choose a privilege (a child workflow or preset, a tool, spawn or
// permission declaration) — no default stands in for what an event decides.

// maxEventContexts bounds the product of the bounded inputs' value sets.
const maxEventContexts = maxApprovalDependencies

// eventSupplied is an unbounded event input's value during approval, and a
// child's input whose argument reads what only a run has (maskTaintedArgs). A
// target field resolved from it is not a scope identifier, so discloseScope
// withholds it as dynamic. It also marks the input as tainted: see
// taintedInputs.
const eventSupplied = "\u27e8event-supplied\u27e9"

// An unbounded event input must not choose a privilege: a child workflow or
// preset, a tool, a spawn or a permission. The check is by REFERENCE, not by
// value: an expression can compare or transform an input away
// ("{{ inputs.mode == 'admin' ? ['shell'] : ['view'] }}"), so whether its
// resolved value still carries the sentinel proves nothing. Taint follows
// arguments into child workflows: an argument leaf whose expression reads a
// tainted input reaches the child as the sentinel (maskTaintedArgs), and a
// target or binding expression that reads one is disclosed as dynamic.
//
// A value only a run has is tainted the same way, for the same reason: a
// node's outputs (nodes.*), the event (trigger.*), a loop's iteration and
// outputs (iter.*, outputs.*), a node's own output (output.*), the failure an
// on_failure handler runs for,
// and the run's own workflow context (workflow.id, .path, .branch, …; not
// workflow.inputs or .name, see approvalWorkflowFields). None of them exists
// before a run, and the approval's evaluator fills each with an empty
// default, so an expression such as `nodes.?lane.?tools.orValue(['view'])`
// would otherwise resolve to its fallback and be approved narrower than the
// run. An agent's model or prompt passed from an earlier node's output is the
// ordinary shape of that (pr-review); it reaches the child as the sentinel,
// so the child can still be approved, and wherever it would choose a
// privilege the approval refuses, as it does for an event value.

var inputsReference = regexp.MustCompile(`\binputs\b\s*(?:\.\s*([A-Za-z_][A-Za-z0-9_]*)|\[\s*['"]([^'"]+)['"]\s*\])?`)

// runtimeNamespaces are the CEL namespaces a run fills and an approval cannot.
var runtimeNamespaces = []wfcel.CELNamespace{wfcel.CELNodes, wfcel.CELTrigger, wfcel.CELIter, wfcel.CELOutput, wfcel.CELOutputs, wfcel.CELFailure}

// referencesRuntime reports whether a {{ }} template reads a value only a run
// has (see above). It answers by parsing, so a namespace mentioned only inside
// a string literal is no reference; a template that does not parse could read
// anything, so it counts as one.
func referencesRuntime(text string) bool {
	reads, err := runtimeReference(text)
	return reads || err != nil
}

// runtimeReference is referencesRuntime, with a parse failure as an error.
func runtimeReference(text string) (bool, error) {
	if !strings.Contains(text, "{{") {
		return false, nil
	}
	exprs, err := wfcel.TemplateExpressions(text, false)
	if err != nil || len(exprs) == 0 {
		return false, err
	}
	for _, ns := range runtimeNamespaces {
		refs, err := wfcel.ReferencesOf(exprs, ns)
		if err != nil || refs.Referenced {
			return refs.Referenced, err
		}
	}
	refs, err := wfcel.ReferencesOf(exprs, wfcel.CELWorkflow)
	if err != nil || refs.Whole {
		return refs.Whole, err
	}
	for _, field := range refs.Fields {
		if !approvalWorkflowFields[field] {
			return true, nil
		}
	}
	return false, nil
}

// approvalWorkflowFields are the workflow namespace's fields an approval
// knows: the inputs it resolves, and the name of the definition it is
// reading (each is evaluated under its own ref, so `spawn(workflow.name, …)`
// names the very definition the closure loads). The rest (id, chat, path,
// branch, mode, …) describe one run.
var approvalWorkflowFields = map[string]bool{"inputs": true, "name": true}

// taintedInputs is the set of a context's inputs an event supplies unbounded.
func taintedInputs(values map[string]any) map[string]bool {
	out := map[string]bool{}
	for name, v := range values {
		raw, err := json.Marshal(v)
		if err == nil && strings.Contains(string(raw), eventSupplied) {
			out[name] = true
		}
	}
	return out
}

// referencesTainted reports whether text, an expression, reads a tainted
// input or a value only a run has (referencesRuntime), or carries the
// sentinel itself (an expression already resolved from one). A reference that
// names no input (bare `inputs`, or a computed `inputs[k]`) could read any of
// them.
func referencesTainted(text string, tainted map[string]bool) bool {
	if strings.Contains(text, eventSupplied) {
		return true
	}
	if referencesRuntime(text) {
		return true
	}
	if len(tainted) == 0 {
		return false
	}
	for _, m := range inputsReference.FindAllStringSubmatch(text, -1) {
		name := m[1]
		if name == "" {
			name = m[2]
		}
		if name == "" || tainted[name] {
			return true
		}
	}
	return false
}

// referencesTaintedIn is referencesTainted over a proto message's expressions.
func referencesTaintedIn(m proto.Message, tainted map[string]bool) bool {
	return taintedField(m, tainted) != ""
}

// taintedField names the first field of m (by its JSON path, in sorted key
// order) whose expression referencesTainted, or returns "" when none does.
// Each string is judged on its own, since a template is parsed and the
// message's JSON escaping is not CEL. A message that cannot be read is
// tainted throughout.
func taintedField(m proto.Message, tainted map[string]bool) string {
	// Proto names are the YAML's: preloaded_tools, not preloadedTools.
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	var value any
	if err == nil {
		err = json.Unmarshal(raw, &value)
	}
	if err != nil {
		return "(unreadable)"
	}
	return taintedLeaf(value, "", tainted)
}

func taintedLeaf(v any, path string, tainted map[string]bool) string {
	switch value := v.(type) {
	case string:
		if referencesTainted(value, tainted) {
			if path == "" {
				return "(value)"
			}
			return path
		}
	case []any:
		for i, item := range value {
			if p := taintedLeaf(item, fmt.Sprintf("%s[%d]", path, i), tainted); p != "" {
				return p
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(value))
		for k := range value {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			// A CelString/CelStringList's literal|expr wrapper is not a field
			// its author wrote: `ref`, not `ref.expr`.
			child := path
			if k != "literal" && k != "expr" {
				child = strings.TrimPrefix(path+"."+k, ".")
			}
			if referencesTainted(k, tainted) {
				return child
			}
			if p := taintedLeaf(value[k], child, tainted); p != "" {
				return p
			}
		}
	}
	return ""
}

var eventAttributeTemplate = regexp.MustCompile(`^\{\{\s*trigger\.payload\.attributes\.([A-Za-z0-9_]+)\s*\}\}$`)

// eventInputContexts returns the root workflow's input contexts — one per
// combination of the values the event-supplied inputs can take — and the
// card lines that say where each event-supplied input comes from.
func eventInputContexts(wt *pb.WorkflowTrigger, wf *pb.Workflow, effective map[string]any) ([]map[string]any, []string, error) {
	if wt == nil || len(wt.GetInputs()) == 0 {
		return []map[string]any{effective}, nil, nil
	}
	integration := wt.GetIntegration()
	names := make([]string, 0, len(wt.GetInputs()))
	for name := range wt.GetInputs() {
		names = append(names, name)
	}
	sort.Strings(names)

	// The event's value replaces the activation's: start from the workflow's
	// defaults for these inputs, not from params the event will overwrite.
	base := map[string]any{}
	for k, v := range effective {
		if _, mapped := wt.GetInputs()[k]; !mapped {
			base[k] = v
		}
	}
	base = runtime.ApplyDefaults(base, wf.GetInputs())

	type bounded struct {
		name   string
		values []any
	}
	var axes []bounded
	var lines []string
	for _, name := range names {
		attr := ""
		if m := eventAttributeTemplate.FindStringSubmatch(strings.TrimSpace(wt.GetInputs()[name])); m != nil {
			attr = m[1]
		}
		var values []any
		var bound string
		if attr != "" && integration != nil {
			if fixed, ok := integration.GetMatch()[attr]; ok {
				values, bound = []any{fixed}, fmt.Sprintf("the trigger matches only %s=%s", attr, discloseScope(fixed))
			} else if input, ok := integration.GetMatchInput()[attr]; ok {
				values = listValues(effective[input])
				bound = fmt.Sprintf("the trigger matches only events whose %s is one of input %s = %s", attr, input, discloseScope(values))
				if len(values) == 0 {
					// An unset match input matches nothing: no event, no run.
					bound = fmt.Sprintf("input %s is empty, so the trigger matches no event", input)
				}
			}
		}
		if bound == "" {
			lines = append(lines, fmt.Sprintf("Event-supplied input %s: set by each event; unbounded by this approval (targets derived from it are dynamic; it may not choose a workflow, preset or tool)", name))
			base[name] = eventSupplied
			continue
		}
		lines = append(lines, fmt.Sprintf("Event-supplied input %s: set by each event; bounded: %s. Privileges below are resolved for every one of those values", name, bound))
		if len(values) > 0 {
			axes = append(axes, bounded{name: name, values: values})
		}
	}

	contexts := []map[string]any{base}
	for _, axis := range axes {
		// Refuse before allocating: the product of a few long lists is large.
		if len(axis.values) > maxEventContexts/len(contexts) {
			return nil, nil, refusedPrecondition("Event-supplied input combinations exceed the activation approval limit")
		}
		next := make([]map[string]any, 0, len(contexts)*len(axis.values))
		for _, ctx := range contexts {
			for _, value := range axis.values {
				c := make(map[string]any, len(ctx)+1)
				for k, v := range ctx {
					c[k] = v
				}
				c[axis.name] = value
				next = append(next, c)
			}
		}
		contexts = next
	}
	return contexts, lines, nil
}

// listValues reads an input value as the set it allows: a list's elements,
// or a single scalar.
func listValues(v any) []any {
	switch value := v.(type) {
	case nil:
		return nil
	case []any:
		return value
	case []string:
		out := make([]any, len(value))
		for i, s := range value {
			out[i] = s
		}
		return out
	case string:
		if value == "" {
			return nil
		}
		return []any{value}
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return nil
		}
		var list []any
		if json.Unmarshal(raw, &list) == nil {
			return list
		}
		return []any{value}
	}
}

// maskTaintedArgs replaces every argument leaf whose expression reads a
// tainted input or a run-time value with the sentinel, so the child sees that
// input as event-supplied however the parent transformed it.
func maskTaintedArgs(args map[string]*structpb.Value, tainted map[string]bool) map[string]*structpb.Value {
	if len(args) == 0 {
		return args
	}
	out := make(map[string]*structpb.Value, len(args))
	for name, v := range args {
		out[name] = maskTainted(v, tainted)
	}
	return out
}

func maskTainted(v *structpb.Value, tainted map[string]bool) *structpb.Value {
	switch k := v.GetKind().(type) {
	case *structpb.Value_StringValue:
		// One that does not parse is left for the resolver to refuse: an
		// argument nobody can read is not approved as unbounded.
		if _, err := runtimeReference(k.StringValue); err != nil {
			return v
		}
		if strings.Contains(k.StringValue, "{{") && referencesTainted(k.StringValue, tainted) {
			return structpb.NewStringValue(eventSupplied)
		}
	case *structpb.Value_ListValue:
		items := make([]*structpb.Value, len(k.ListValue.GetValues()))
		for i, item := range k.ListValue.GetValues() {
			items[i] = maskTainted(item, tainted)
		}
		return structpb.NewListValue(&structpb.ListValue{Values: items})
	case *structpb.Value_StructValue:
		fields := make(map[string]*structpb.Value, len(k.StructValue.GetFields()))
		for name, item := range k.StructValue.GetFields() {
			fields[name] = maskTainted(item, tainted)
		}
		return structpb.NewStructValue(&structpb.Struct{Fields: fields})
	}
	return v
}

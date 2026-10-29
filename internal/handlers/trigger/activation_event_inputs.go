package trigger

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	runtime "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"google.golang.org/protobuf/proto"
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

// eventSupplied is an unbounded event input's value during approval. It is
// not a scope identifier (discloseScope withholds it as dynamic) and no real
// value contains it, so a privilege resolved from it is recognisable.
const eventSupplied = "\u27e8event-supplied\u27e9"

// dependsOnEvent reports whether a resolved privilege-bearing message took
// any part of its value from an unbounded event input.
func dependsOnEvent(m proto.Message) bool {
	return strings.Contains(canonicalProto(m), eventSupplied)
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
		if len(next) > maxEventContexts {
			return nil, nil, svcerr.FailedPrecondition("Event-supplied input combinations exceed the activation approval limit")
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

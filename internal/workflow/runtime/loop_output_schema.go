// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"regexp"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// Typed zero values for declared outputs that reference a node field the node
// never produced.
//
// Node outputs are already typed: StepExecutor.normalizeOutput backfills every
// field of an activity's registered output type to its zero value, so
// `size(nodes.call_llm.tool_calls)` is safe once call_llm has run. Declared
// `outputs:` blocks had no such typing. An output referencing a node that did
// not run this iteration failed the whole evaluation — EvaluateWorkflowOutputs
// returns on the first error, and a loop turns that into a fatal iteration
// error — which is why the builtin workflows hand-write
// `has(a) && has(a.b) ? a.b : default` around nearly every loop output.
//
// The substitution here closes that gap from the other side: the expression is
// evaluated first, exactly as before, and a typed zero is only considered once
// evaluation has already failed. Anything that evaluates today reaches CEL with
// a byte-identical activation and returns its existing value, so the guarded
// form keeps working unchanged and only the unguarded form changes behaviour.
//
// WHY NOT PRE-SEED `nodes.*` WITH TYPED DEFAULTS BEFORE EVALUATING:
// because the workflows use `has(nodes.X)` — with no second component — to ask
// "did node X run this iteration", which is a different question from "does
// field b exist on node a". Seeding the namespace answers the first question
// wrong for every such guard. See internal/workflow/builtin/migrate.yaml:92,
// where `has(nodes.final_summary) ? nodes.final_summary.message : ...` would
// take the wrong branch on every run. Evaluating first and falling back only on
// failure leaves has() untouched, and leaves the CEL activation unwrapped so
// `x != null` keeps reporting null correctly.

// bareNodePath matches an expression that is nothing but a node field
// reference: `nodes.<id>.<field>` with at least one field and optional further
// nesting. Anything with an operator, index, call or ternary in it fails to
// match and is never a substitution candidate, which is what keeps type errors
// (`inputs.max_turns - 1` against a string) raising as before.
var bareNodePath = regexp.MustCompile(`^nodes\.([A-Za-z_][A-Za-z0-9_]*)((?:\.[A-Za-z_][A-Za-z0-9_]*)+)$`)

// parseBareNodePath reports whether a declared output expression is a bare node
// field reference, returning the node id and the field path beneath it.
// The expression may be wrapped in a single {{...}} template, which is how
// workflow YAML always writes it.
func parseBareNodePath(expr string) (nodeID string, fieldPath []string, ok bool) {
	trimmed := strings.TrimSpace(expr)
	if strings.HasPrefix(trimmed, "{{") && strings.HasSuffix(trimmed, "}}") && len(trimmed) > 4 {
		trimmed = strings.TrimSpace(trimmed[2 : len(trimmed)-2])
	}

	match := bareNodePath.FindStringSubmatch(trimmed)
	if match == nil {
		return "", nil, false
	}

	fields := strings.Split(strings.TrimPrefix(match[2], "."), ".")
	return match[1], fields, true
}

// findDeclaredNode returns the node with this id from the workflow's own node
// list. The lookup is deliberately restricted to nodes the graph declares: a
// misspelled reference (`nodes.typo_name.field`) finds nothing and keeps
// raising, so this never converts an authoring mistake into a silent zero.
func findDeclaredNode(wf *reliantv1.Workflow, nodeID string) *reliantv1.Node {
	if wf == nil {
		return nil
	}
	for _, node := range wf.GetNodes() {
		if node.GetId() == nodeID {
			return node
		}
	}
	return nil
}

// nodeOutputPathAbsent reports whether a node field path is genuinely missing
// from the evaluated node outputs.
//
// "Absent" means a map lookup ran off the end of what exists. A path whose
// container is present but is NOT a map is a type confusion rather than an
// absence — `nodes.call_llm.tool_calls` where call_llm evaluated to a string
// also fails with "no such key", and substituting a zero there would hide a
// real shape mismatch. That case reports false and the original error stands.
func nodeOutputPathAbsent(nodeOutputs map[string]interface{}, nodeID string, fieldPath []string) bool {
	current, exists := nodeOutputs[nodeID]
	if !exists {
		return true
	}

	for _, field := range fieldPath {
		container, isMap := current.(map[string]interface{})
		if !isMap {
			// Present but not a traversable map: not an absence.
			return false
		}
		next, exists := container[field]
		if !exists {
			return true
		}
		current = next
	}

	// Every component resolved, so the path exists. Evaluation failed for some
	// other reason and the caller must surface it.
	return false
}

// substitutableZero reports whether a schema zero value may stand in for an
// absent field.
//
// ONLY CONTAINERS. A slice or map zero is unambiguous: an empty list really is
// "no tool calls", and there is no second reading to confuse it with.
//
// Scalars are deliberately excluded even though the schema has a zero for them,
// because a scalar zero is SEMANTICALLY LOADED and silently wrong in a way an
// empty container never is. The load-bearing case is exit codes:
// get-it-right.yaml declares `lint_exit: "{{nodes.lint.exit_code}}"` as a bare
// path, and its own comment at that node spells out the hazard — "an
// unconfigured lane reports exit 0 and 'did not run' is indistinguishable from
// 'ran and passed'". Substituting 0 for a lane whose node produced nothing
// would report a gate as GREEN over a lane that never ran. The same shape
// applies to a substituted `false` for gate_failed, or "" for a verdict.
//
// So the rule an author has to remember is one sentence: an absent CONTAINER
// reads as empty, anything else raises. That is narrower than "typed zeros
// everywhere", and the narrowness is the point — it keeps every case where
// substitution is provably safe and drops every case where it is a judgement
// call about what a zero means in that workflow.
//
// (Pointer and interface fields never reach here anyway: schema's getZeroValue
// returns nil for them, so `response`, `response_data`, `message` and
// `thinking` stay nil — "no structured response" is not "an empty one".)
func substitutableZero(v interface{}) bool {
	switch v.(type) {
	case []interface{}:
		return true
	case map[string]interface{}:
		return true
	default:
		return false
	}
}

// nodeFieldZeroValue returns the typed zero value for a field path on a node's
// registered activity output type, and whether one is available.
//
// Only container zeros are offered — see substitutableZero. A scalar or nil
// default is reported as unavailable, which leaves the original error in place.
func nodeFieldZeroValue(node *reliantv1.Node, fieldPath []string) (interface{}, bool) {
	activityName := NodeActivityName(node)
	if activityName == "" {
		return nil, false
	}

	defaults := schema.GetOutputDefaults(activityName)
	if defaults == nil {
		return nil, false
	}

	var current interface{} = defaults
	for _, field := range fieldPath {
		container, isMap := current.(map[string]interface{})
		if !isMap {
			return nil, false
		}
		next, exists := container[field]
		if !exists {
			return nil, false
		}
		current = next
	}

	if !substitutableZero(current) {
		return nil, false
	}
	return current, true
}

// substituteTypedZero decides whether a failed output expression should resolve
// to a typed zero instead of propagating its error. Every condition must hold:
// the expression is a bare node field reference, the node is declared in this
// graph, the path really is absent from the node outputs, and the node's
// activity schema supplies a non-nil zero for that field.
func substituteTypedZero(
	expr string,
	nodeOutputs map[string]interface{},
	wf *reliantv1.Workflow,
) (value interface{}, nodeID string, ok bool) {
	nodeID, fieldPath, isBarePath := parseBareNodePath(expr)
	if !isBarePath {
		return nil, "", false
	}

	node := findDeclaredNode(wf, nodeID)
	if node == nil {
		return nil, "", false
	}

	if !nodeOutputPathAbsent(nodeOutputs, nodeID, fieldPath) {
		return nil, "", false
	}

	zero, available := nodeFieldZeroValue(node, fieldPath)
	if !available {
		return nil, "", false
	}

	return zero, nodeID, true
}

// outputSubstitutionLogger is the only logging this needs. Declared at the
// consumer, like joinLogger, so a one-method test logger satisfies it without
// implementing the whole temporal log.Logger surface.
type outputSubstitutionLogger interface {
	Warn(msg string, keyvals ...interface{})
}

// logTypedZeroSubstitution records a substitution at Warn. A substitution means
// a workflow referenced a path its node did not produce, which is usually
// schema or authoring drift worth seeing even though it is no longer fatal.
func logTypedZeroSubstitution(logger outputSubstitutionLogger, outputName, expr, nodeID string, value interface{}) {
	if logger == nil {
		return
	}
	logger.Warn("[WorkflowOutputs] Declared output referenced an absent node field; substituted typed zero",
		"output", outputName,
		"expr", expr,
		"nodeID", nodeID,
		"substituted", value,
	)
}

// nodeTypeToActivityNameOverrides lists node types whose activity name the
// snake_case -> PascalCase rule cannot derive. `run` is structural but
// dispatches ExecuteRunStep.
var nodeTypeToActivityNameOverrides = map[string]string{
	model.NodeTypeRun: "ExecuteRunStep",
}

// NodeActivityName is the registered activity a node dispatches (and whose
// output schema describes the node's output), or "" for a structural node that
// dispatches none. The scenario runner uses it to register exactly the
// activities a graph can reach, so it can never drift from the dispatcher.
func NodeActivityName(node *reliantv1.Node) string {
	if node == nil {
		return ""
	}
	// Check overrides first — handles structural nodes like "run" that have
	// registered activity types with output schemas.
	if override, ok := nodeTypeToActivityNameOverrides[node.GetType()]; ok {
		return override
	}
	if !isActivityType(node.GetType()) {
		return ""
	}
	return nodeTypeToActivityName(node.GetType())
}

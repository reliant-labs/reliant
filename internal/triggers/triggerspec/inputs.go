// Copyright (c) 2025 Reliant Labs
package triggerspec

import (
	"fmt"
	"sort"
	"strings"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
)

// A declared trigger's `inputs` maps the event that fired it onto the
// workflow's inputs: input name -> a template over the `trigger` root, e.g.
//
//	issue_number: "{{ trigger.payload.data.issue.number }}"
//
// It uses the template syntax every other workflow field uses, so a value is
// one of:
//   - a literal ("main"), passed through as-is;
//   - a pure expression ("{{ trigger.payload.ref }}"), which yields the
//     expression's native type (a number stays a number);
//   - a mixed string ("PR #{{ trigger.payload.data.number }}"), which yields
//     the interpolated string.
//
// Like a filter, it sees only `trigger`: it is evaluated when the event
// launches a run, before that run has inputs, nodes or a workflow context.

// InputsError reports an inputs mapping that cannot run.
type InputsError struct {
	Input  string
	Reason string
}

func (e *InputsError) Error() string { return fmt.Sprintf("inputs.%s: %s", e.Input, e.Reason) }

// InputExpressions returns the CEL expressions inside one inputs value.
func InputExpressions(value string) []string { return wfcel.TemplateExpressions(value, false) }

// CompileInputs checks that every value's expressions compile over the
// `trigger` root. It reports the first problem per input, in input order.
func CompileInputs(inputs map[string]string) []*InputsError {
	if len(inputs) == 0 {
		return nil
	}
	env, err := triggerEnv()
	if err != nil {
		return []*InputsError{{Reason: "build environment: " + err.Error()}}
	}
	var errs []*InputsError
	for _, name := range sortedKeys(inputs) {
		for _, expr := range InputExpressions(inputs[name]) {
			if _, issues := env.Compile(expr); issues != nil && issues.Err() != nil {
				errs = append(errs, &InputsError{Input: name, Reason: fmt.Sprintf("{{ %s }}: %v", expr, issues.Err())})
				break
			}
		}
	}
	return errs
}

// LooksLikeBareExpression reports a literal value that was almost certainly
// meant as an expression: it names the trigger root but has no {{ }}, so it
// would be passed to the workflow as the literal text "trigger.payload.x".
func LooksLikeBareExpression(value string) bool {
	v := strings.TrimSpace(value)
	return len(InputExpressions(v)) == 0 && strings.HasPrefix(v, string(wfcel.CELTrigger)+".")
}

// EvaluateInputs resolves an inputs mapping against a `trigger` root
// (runtime.TriggerInfo.CELValue's shape). An evaluation error — a key the
// payload lacks — fails the whole mapping: launching with an input silently
// missing is worse than recording why it could not launch.
func EvaluateInputs(inputs map[string]string, root map[string]any) (map[string]any, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	ctx := &triggerEvalContext{root: root}
	out := make(map[string]any, len(inputs))
	for _, name := range sortedKeys(inputs) {
		value, err := wfcel.EvaluateTemplate(inputs[name], ctx)
		if err != nil {
			return nil, &InputsError{Input: name, Reason: err.Error() + "; guard optional fields with has() or a ternary"}
		}
		out[name] = value
	}
	return out, nil
}

// triggerEvalContext is the CEL evaluation context with only the `trigger`
// namespace.
type triggerEvalContext struct{ root map[string]any }

func (c *triggerEvalContext) Activation() map[string]any {
	return wfcel.EnsureNamespaceDefaults(map[string]any{string(wfcel.CELTrigger): c.root}, c.Namespaces())
}

func (c *triggerEvalContext) Namespaces() []wfcel.CELNamespace {
	return []wfcel.CELNamespace{wfcel.CELTrigger}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

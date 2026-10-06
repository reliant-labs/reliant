// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"strings"

	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// requireRunCommandValues fails a `run` node whose command interpolates a value
// reference that resolved to nothing.
//
// A shell command is the one template position where an empty value is not a
// cosmetic bug: it shifts meaning. `rsync --delete "{{a}}/" "{{b}}/"` with b
// empty is a sync onto "/", which is what builtin parallel-compete ran on
// 2026-10-06. So in a run command every interpolated plain reference —
// `{{workflow.path}}`, `{{inputs.dir}}`, `{{nodes.x._results[k].path}}` — must
// render non-empty, and the node fails before anything is dispatched to the
// daemon if one does not.
//
// The rule is deliberately limited to plain references (identifier, field and
// index chains). An expression that computes its value — a ternary, has(), a
// function call — is the author stating what empty means, e.g.
// `{{has(inputs.flags) ? inputs.flags : ""}}`, and is left alone.
func requireRunCommandValues(node *reliantv1.Node, builder *CELContextBuilder) error {
	run := node.GetRun()
	if run == nil {
		return nil
	}
	raw := model.CelStringRaw(run.GetCommand())
	matches := extractTemplateExpressions(raw)
	if len(matches) == 0 {
		return nil
	}
	env, activation, err := builder.env()
	if err != nil {
		return err
	}
	for _, match := range matches {
		expr := strings.TrimSpace(match.expr)
		checked, issues := env.Compile(expr)
		if issues != nil && issues.Err() != nil {
			// Resolution already ran and would have reported this.
			return fmt.Errorf("CEL compilation error: %w", issues.Err())
		}
		if !isPlainValueReference(checked.NativeRep().Expr()) {
			continue
		}
		value, err := builder.evalSingleCEL(expr, env, activation)
		if err != nil {
			return fmt.Errorf("run node %q: {{%s}}: %w", node.GetId(), expr, err)
		}
		if value == nil || strings.TrimSpace(valueToInterpolatedString(value)) == "" {
			return fmt.Errorf("run node %q: {{%s}} rendered empty, so its command was not run. "+
				"An empty value in a shell command changes what the command does (an empty "+
				"destination path becomes \"/\"). If the value is genuinely optional, say what "+
				"empty means in the expression, e.g. {{has(x) && x != '' ? x : '…'}}",
				node.GetId(), expr)
		}
	}
	return nil
}

// isPlainValueReference reports whether e only looks a value up: an identifier,
// or a field or index access on one. Index ARGUMENTS may be anything — in
// `_results[string(nodes.review.response.winner)]` the lookup is still plain.
func isPlainValueReference(e celast.Expr) bool {
	switch e.Kind() {
	case celast.IdentKind:
		return true
	case celast.SelectKind:
		sel := e.AsSelect()
		return !sel.IsTestOnly() && isPlainValueReference(sel.Operand())
	case celast.CallKind:
		call := e.AsCall()
		if call.FunctionName() == operators.Index && len(call.Args()) == 2 {
			return isPlainValueReference(call.Args()[0])
		}
	}
	return false
}

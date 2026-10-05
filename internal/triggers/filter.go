// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/cel-go/cel"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// A trigger's filter is a CEL bool over the `trigger` root — the SAME root,
// built by the same code (runtime.TriggerInfo.CELValue), that every node of
// the launched run sees. So `trigger.payload.data.issue.number` means one
// thing whether it is written in a filter or in a node's template: an author
// can move a condition from one to the other without translating it.
//
// The expression is a raw CEL expression, not a {{ }} template: a filter is
// all expression, and braces would only add a second way to spell it.

// FilterError reports a filter that cannot run. Handlers map it to
// InvalidArgument.
type FilterError struct {
	Expr   string
	Reason string
}

func (e *FilterError) Error() string { return "filter: " + e.Reason }

// FilterInput is the event a filter is evaluated against: the fields of the
// `trigger` root that exist before a run does.
type FilterInput struct {
	Kind       string
	TriggerID  string
	EventID    string
	OccurredAt time.Time
	Payload    map[string]any
}

// Filter is a compiled filter. The zero value (from an empty expression)
// matches everything.
type Filter struct {
	expr    string
	program cel.Program
}

// CompileFilter validates expr and prepares it for evaluation. An empty or
// blank expression is the match-everything filter.
func CompileFilter(expr string) (*Filter, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return &Filter{}, nil
	}
	if strings.Contains(expr, "{{") {
		return nil, &FilterError{Expr: expr, Reason: "write the CEL expression without {{ }}: a filter is an expression, not a template"}
	}
	env, err := wfcel.NewEnv(wfcel.CELEnvConfig{
		// Only `trigger`: at filter time there is no run, so inputs, nodes
		// and workflow would be empty, and accepting them would let an
		// expression compile that can only ever see blanks.
		Namespaces:             []wfcel.CELNamespace{wfcel.CELTrigger},
		IncludeStdLib:          true,
		IncludeCustomFunctions: true,
	})
	if err != nil {
		return nil, fmt.Errorf("build filter environment: %w", err)
	}
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, &FilterError{Expr: expr, Reason: issues.Err().Error()}
	}
	// The trigger root is dynamic, so a field read type-checks as dyn; only
	// an expression whose result is statically not a bool can be rejected
	// here. Anything else is checked at evaluation.
	if out := ast.OutputType(); out != cel.BoolType && out != cel.DynType {
		return nil, &FilterError{Expr: expr, Reason: fmt.Sprintf("must evaluate to a bool, not %s", out)}
	}
	program, err := env.Program(ast)
	if err != nil {
		return nil, &FilterError{Expr: expr, Reason: err.Error()}
	}
	return &Filter{expr: expr, program: program}, nil
}

// Expr is the filter's source.
func (f *Filter) Expr() string { return f.expr }

// Match evaluates the filter. An evaluation error — a key the payload lacks,
// a non-bool result — is returned, not folded into false: "this event is not
// for me" and "my filter no longer fits the payload" must be told apart on
// the recorded firing.
func (f *Filter) Match(in FilterInput) (bool, error) {
	if f == nil || f.program == nil {
		return true, nil
	}
	info := &v2.TriggerInfo{
		Kind:      in.Kind,
		TriggerID: in.TriggerID,
		EventID:   in.EventID,
		Payload:   in.Payload,
	}
	if !in.OccurredAt.IsZero() {
		info.OccurredAt = in.OccurredAt.UTC().Format(time.RFC3339)
	}
	return f.MatchRoot(info.CELValue())
}

// MatchRoot evaluates the filter against an already-built `trigger` root
// (runtime.TriggerInfo.CELValue's shape), for a caller that has built the root
// the launched run will see.
func (f *Filter) MatchRoot(root map[string]any) (bool, error) {
	if f == nil || f.program == nil {
		return true, nil
	}
	out, _, err := f.program.Eval(map[string]any{string(wfcel.CELTrigger): root})
	if err != nil {
		return false, &FilterError{Expr: f.expr, Reason: err.Error()}
	}
	hit, ok := out.Value().(bool)
	if !ok {
		return false, &FilterError{Expr: f.expr, Reason: fmt.Sprintf("evaluated to %T, not a bool", out.Value())}
	}
	return hit, nil
}

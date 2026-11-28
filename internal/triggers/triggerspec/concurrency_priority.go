// Copyright (c) 2025 Reliant Labs
package triggerspec

import (
	"fmt"
	"math"
	"strings"

	"cel.dev/cel-go/cel"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
)

// A workflow's `concurrency: {priority: ...}` orders its waiting firings:
// higher first, then by arrival. Like a filter it is a raw CEL expression
// over the `trigger` root, evaluated once per firing when it starts waiting:
//
//	concurrency: {max_running: 2, priority: "'main-red' in trigger.payload.labels ? 10 : 0"}

// ConcurrencyPriority is a compiled concurrency.priority. The zero value
// (from an empty expression) gives every firing priority 0.
type ConcurrencyPriority struct {
	expr    string
	program cel.Program
}

// CompileConcurrencyPriority validates expr and prepares it for evaluation.
func CompileConcurrencyPriority(expr string) (*ConcurrencyPriority, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return &ConcurrencyPriority{}, nil
	}
	if strings.Contains(expr, "{{") {
		return nil, fmt.Errorf("write the CEL expression without {{ }}: priority is an expression, not a template")
	}
	env, err := triggerEnv()
	if err != nil {
		return nil, fmt.Errorf("build environment: %w", err)
	}
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}
	// A payload read is dyn; only a statically non-numeric result is refused
	// here, the rest when a firing evaluates it.
	switch out := ast.OutputType(); out {
	case cel.IntType, cel.UintType, cel.DoubleType, cel.DynType:
	default:
		return nil, fmt.Errorf("must evaluate to an integer, not %s", out)
	}
	program, err := env.Program(ast, wfcel.ProgramOptions()...)
	if err != nil {
		return nil, err
	}
	return &ConcurrencyPriority{expr: expr, program: program}, nil
}

// Evaluate returns a firing's priority. A JSON number in the payload is a
// double in CEL, so a whole double is accepted; a fraction, an out-of-range
// value or any other type is an error the caller reports.
func (p *ConcurrencyPriority) Evaluate(scope Scope) (int64, error) {
	if p == nil || p.program == nil {
		return 0, nil
	}
	out, _, err := p.program.Eval(scope.Vars())
	if err != nil {
		return 0, fmt.Errorf("concurrency priority: %w", err)
	}
	switch v := out.Value().(type) {
	case int64:
		return v, nil
	case uint64:
		if v > math.MaxInt64 {
			return 0, fmt.Errorf("concurrency priority %d is out of range", v)
		}
		return int64(v), nil
	case float64:
		if v != math.Trunc(v) || v < math.MinInt64 || v >= math.MaxInt64 {
			return 0, fmt.Errorf("concurrency priority %v is not an integer", v)
		}
		return int64(v), nil
	default:
		return 0, fmt.Errorf("concurrency priority evaluated to %T, not an integer", out.Value())
	}
}

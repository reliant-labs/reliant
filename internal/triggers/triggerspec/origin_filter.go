// Copyright (c) 2025 Reliant Labs
package triggerspec

import (
	"errors"
	"strings"
)

// DefaultOriginFilter is the origin_filter a trigger has when it declares none:
// a workflow ignores its own echoes, the events a run of it caused by writing to
// the thing the trigger watches. Only a confident attribution counts (exact or
// intent): an `app` or `claimed` origin says Reliant did it but not which run, so
// it is not known to be this workflow's own.
//
// It is a deliberately ordinary expression, written out, so the behaviour a
// workflow gets by default is visible in the same language it overrides it in.
const DefaultOriginFilter = "!(trigger.origin.same_workflow && trigger.origin.confidence in ['exact', 'intent'])"

// OriginFilter is a compiled origin_filter: a CEL bool over `trigger` (its
// `origin` in particular), evaluated before the trigger's filter. It is the same
// language and the same root as a filter, so everything a filter can read, it
// can too, and an author moves a condition between the two without translating.
type OriginFilter struct {
	f *Filter
}

// CompileOriginFilter validates expr. Empty or blank is DefaultOriginFilter.
func CompileOriginFilter(expr string) (*OriginFilter, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		expr = DefaultOriginFilter
	}
	f, err := CompileFilter(expr)
	if err != nil {
		var fe *FilterError
		if errors.As(err, &fe) {
			return nil, &FilterError{Expr: expr, Reason: "origin_filter: " + fe.Reason}
		}
		return nil, err
	}
	return &OriginFilter{f: f}, nil
}

// Expr is the expression in force: the declared one, or the default.
func (o *OriginFilter) Expr() string {
	if o == nil || o.f == nil {
		return DefaultOriginFilter
	}
	return o.f.Expr()
}

// Allow evaluates the filter against a `trigger` root (runtime.TriggerInfo's
// CELValue): true means the event is wanted. An evaluation error (a non-bool
// result, a read of something absent) is returned, not folded into either
// answer: the owner's expression is wrong for this event, which is something to
// see rather than to guess about.
func (o *OriginFilter) Allow(root map[string]any) (bool, error) {
	if o == nil || o.f == nil {
		return true, nil
	}
	return o.f.MatchRoot(root)
}

// Copyright (c) 2025 Reliant Labs
package workflowevent

import (
	"fmt"
	"strings"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
)

// FilterFunc evaluates a trigger's CEL filter against the `trigger` root the
// launched run would see, returning whether the event passes. An empty filter
// must pass. It is the seam for the generic trigger filter, so every trigger
// kind evaluates `filter` the same way.
type FilterFunc func(filter string, trigger map[string]any) (bool, error)

// EvaluateFilter is the default FilterFunc: a CEL bool over the `trigger`
// namespace, with the same environment (stdlib, custom functions) a workflow
// node's expressions get.
func EvaluateFilter(filter string, trigger map[string]any) (bool, error) {
	if strings.TrimSpace(filter) == "" {
		return true, nil
	}
	ok, err := wfcel.EvaluateBool(filter, triggerContext{trigger: trigger})
	if err != nil {
		return false, fmt.Errorf("filter %q: %w", filter, err)
	}
	return ok, nil
}

// triggerContext is a CEL evaluation context exposing only `trigger`.
type triggerContext struct{ trigger map[string]any }

func (c triggerContext) Activation() map[string]interface{} {
	return map[string]interface{}{string(wfcel.CELTrigger): c.trigger}
}

func (c triggerContext) Namespaces() []wfcel.CELNamespace {
	return []wfcel.CELNamespace{wfcel.CELTrigger}
}

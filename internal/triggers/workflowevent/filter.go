// Copyright (c) 2025 Reliant Labs
package workflowevent

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
)

// FilterFunc evaluates a trigger's CEL filter against the `trigger` root the
// launched run would see and the activation it fires for, returning whether
// the event passes. An empty filter must pass. It is the seam for the generic trigger filter, so every trigger
// kind evaluates `filter` the same way.
type FilterFunc func(filter string, scope triggerspec.Scope) (bool, error)

// EvaluateFilter is the default FilterFunc. It is the shared trigger filter
// (triggers.CompileFilter): a CEL bool over the `trigger` namespace with the
// environment every workflow expression gets, so a workflow-event trigger's
// filter means exactly what a webhook or integration trigger's does.
func EvaluateFilter(filter string, scope triggerspec.Scope) (bool, error) {
	if strings.TrimSpace(filter) == "" {
		return true, nil
	}
	compiled, err := triggers.CompileFilter(filter)
	if err != nil {
		return false, err
	}
	ok, err := compiled.MatchScope(scope)
	if err != nil {
		return false, fmt.Errorf("filter %q: %w", filter, err)
	}
	return ok, nil
}

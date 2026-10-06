// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// A trigger's filter is a CEL bool over the `trigger` root — the SAME root,
// built by the same code (runtime.TriggerInfo.CELValue), that every node of
// the launched run sees. Compilation lives in triggerspec, so a filter that
// workflow validation accepts is one this package can run.

// FilterError reports a filter that cannot run. Handlers map it to
// InvalidArgument.
type FilterError = triggerspec.FilterError

// FilterInput is the event a filter is evaluated against: the fields of the
// `trigger` root that exist before a run does.
type FilterInput struct {
	Kind       string
	TriggerID  string
	EventID    string
	OccurredAt time.Time
	Payload    map[string]any
	// Sender is trigger.sender: who the source says sent the event.
	Sender *core.TriggerSender
}

// Root is the `trigger` CEL value for this event: what a filter and an
// inputs mapping read, and what the launched run sees.
func (in FilterInput) Root() map[string]any {
	info := &v2.TriggerInfo{
		Kind:      in.Kind,
		TriggerID: in.TriggerID,
		EventID:   in.EventID,
		Payload:   in.Payload,
		Sender:    in.Sender,
	}
	if !in.OccurredAt.IsZero() {
		info.OccurredAt = in.OccurredAt.UTC().Format(time.RFC3339)
	}
	return info.CELValue()
}

// Filter is a compiled filter. The zero value (from an empty expression)
// matches everything.
type Filter struct{ *triggerspec.Filter }

// CompileFilter validates expr and prepares it for evaluation. An empty or
// blank expression is the match-everything filter.
func CompileFilter(expr string) (*Filter, error) {
	f, err := triggerspec.CompileFilter(expr)
	if err != nil {
		return nil, err
	}
	return &Filter{f}, nil
}

// Match evaluates the filter. An evaluation error — a key the payload lacks,
// a non-bool result — is returned, not folded into false: "this event is not
// for me" and "my filter no longer fits the payload" must be told apart on
// the recorded firing.
func (f *Filter) Match(in FilterInput) (bool, error) {
	if f == nil {
		return true, nil
	}
	return f.MatchRoot(in.Root())
}

// MatchRoot evaluates the filter against an already-built `trigger` root, for
// a caller that has built the root the launched run will see.
func (f *Filter) MatchRoot(root map[string]any) (bool, error) {
	if f == nil {
		return true, nil
	}
	return f.Filter.MatchRoot(root)
}

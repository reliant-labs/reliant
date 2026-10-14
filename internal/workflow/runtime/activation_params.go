// Copyright (c) 2025 Reliant Labs
package runtime

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
)

// ActivationParams is `activation.params`: what an activation's params will
// be when its run starts, as far as the trigger can know. The workflow's input
// defaults are applied, every value is coerced to its input's declared type,
// and an input the trigger maps from the event (mapped) is left out, because
// the event sets it. An input with neither a param nor a default reads as its
// type's zero ([] for a list), so a filter on an unset list matches nothing
// rather than failing.
func ActivationParams(declared map[string]*reliantv1.Input, mapped map[string]string, stored map[string]any) map[string]any {
	set := make(map[string]any, len(stored))
	for name, value := range stored {
		if _, fromEvent := mapped[name]; !fromEvent {
			set[name] = value
		}
	}
	return ApplyDefaultsForRuntime(set, triggerspec.ParamsInputs(declared, mapped))
}

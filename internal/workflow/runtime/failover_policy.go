// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// failoverPolicy is what a run with these inputs allows when a provider's
// accounts are all limited: stay put and wait (the zero value), or continue on
// a named model on another provider until the first one resets.
//
// Cross-provider fallback is OPT-IN: it is on only when the run's
// ProviderPolicy says on_limit "fallback" with a model (the chat's choice, the
// Settings default, a preset's or a spawn call's `providers:`, or inherited).
func failoverPolicy(inputs map[string]interface{}) types.FailoverPolicy {
	policy := ProviderPolicy(inputs)
	out := types.FailoverPolicy{PreferredAccount: policy.PreferredAccount}
	if policy.OnLimit == types.OnLimitFallback {
		out.FallbackModel = policy.FallbackModel
	}
	return out
}

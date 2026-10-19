// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// failoverPolicy is what a run with these inputs allows when a provider's
// accounts are all limited: stay put and wait (the zero value), or continue on
// a named model on another provider until the first one resets.
//
// Cross-provider fallback is OPT-IN. Until the per-chat provider policy
// (ProviderPolicy, DESIGN §6.3) lands, nothing opts in, so this is the zero
// value; that package replaces this one body and no caller changes.
func failoverPolicy(inputs map[string]interface{}) types.FailoverPolicy {
	_ = inputs
	return types.FailoverPolicy{}
}

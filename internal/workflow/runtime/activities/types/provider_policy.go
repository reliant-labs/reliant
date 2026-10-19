// Copyright (c) 2025 Reliant Labs
package types

// Values of RunProviderPolicy.OnLimit.
const (
	OnLimitWait     = "wait"
	OnLimitFallback = "fallback"
)

// RunProviderPolicy is what a run does when its provider is rate-limited, and
// which of a provider's accounts it prefers. It is a fact about the RUN, carried
// down the run tree in the runtime-injected input "__provider_policy" (see
// runtime.ProviderPolicy), so a sub-agent behaves as its chat chose.
//
// The zero value is the default: wait for the limit to reset. Cross-provider
// fallback is opt-in.
type RunProviderPolicy struct {
	// OnLimit is "wait" or "fallback"; empty is "wait".
	OnLimit string `json:"on_limit,omitempty"`
	// FallbackModel is "<model>@<driver>", the model to continue on while the
	// run's own provider is limited. Set only with OnLimit "fallback".
	FallbackModel string `json:"fallback_model,omitempty"`
	// PreferredAccount maps a driver id to the account (id, email or label)
	// the run prefers on it.
	PreferredAccount map[string]string `json:"preferred_account,omitempty"`
}

// IsZero reports whether the policy says nothing: no on-limit choice and no
// preferred account.
func (p RunProviderPolicy) IsZero() bool {
	return p.OnLimit == "" && len(p.PreferredAccount) == 0
}

// Value is the policy as a run input: plain maps and slices only, so it
// survives a structpb round trip (a spawn node's args) and compares by JSON
// the same before and after Temporal stores it.
func (p RunProviderPolicy) Value() map[string]interface{} {
	out := map[string]interface{}{}
	if p.OnLimit != "" {
		out["on_limit"] = p.OnLimit
	}
	if p.FallbackModel != "" {
		out["fallback_model"] = p.FallbackModel
	}
	if len(p.PreferredAccount) > 0 {
		accounts := make(map[string]interface{}, len(p.PreferredAccount))
		for driver, account := range p.PreferredAccount {
			accounts[driver] = account
		}
		out["preferred_account"] = accounts
	}
	return out
}

// RunProviderPolicyFromValue reads a policy from a run input value: a
// RunProviderPolicy, or the map it becomes once the input has been through JSON.
// Anything else, and any on_limit it does not know, is the default. A fallback
// with no model is a wait: there is nowhere to fall back to.
func RunProviderPolicyFromValue(value interface{}) RunProviderPolicy {
	var p RunProviderPolicy
	switch v := value.(type) {
	case RunProviderPolicy:
		p = v
	case *RunProviderPolicy:
		if v != nil {
			p = *v
		}
	case map[string]interface{}:
		p.OnLimit, _ = v["on_limit"].(string)
		p.FallbackModel, _ = v["fallback_model"].(string)
		switch accounts := v["preferred_account"].(type) {
		case map[string]interface{}:
			for driver, account := range accounts {
				if s, ok := account.(string); ok && s != "" {
					p = p.withAccount(driver, s)
				}
			}
		case map[string]string:
			for driver, account := range accounts {
				if account != "" {
					p = p.withAccount(driver, account)
				}
			}
		}
	default:
		return RunProviderPolicy{}
	}
	switch p.OnLimit {
	case OnLimitWait:
		p.FallbackModel = ""
	case OnLimitFallback:
		if p.FallbackModel == "" {
			p.OnLimit = OnLimitWait
		}
	default:
		p.OnLimit, p.FallbackModel = "", ""
	}
	return p
}

func (p RunProviderPolicy) withAccount(driver, account string) RunProviderPolicy {
	accounts := make(map[string]string, len(p.PreferredAccount)+1)
	for k, v := range p.PreferredAccount {
		accounts[k] = v
	}
	accounts[driver] = account
	p.PreferredAccount = accounts
	return p
}

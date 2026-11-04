// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"slices"
	"strings"

	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// The provider a person chose for a run applies to the whole run.
//
// Picking "gpt-5.6-sol@codex" in the composer says which model the agent runs
// on AND which provider serves the run. The model half reaches only the inputs
// it was set on; everything else in the run — a spawned sub-agent, whose preset
// names a tier ("fast", "moderate"), a sub-workflow or loop body with its own
// model input — resolves a tier against every connected provider, and the tier
// lists lead with Claude. So a chat on Codex spawned Claude sub-agents whenever
// Claude was connected: the harness, not the user, picked the provider.
//
// The preference is a fact about the RUN, like unattended (unattended.go), so
// the runtime carries it down the tree rather than asking each YAML to forward
// it. It never overrides a choice: a model that names its own provider keeps
// it, and the preference only fills in for a selector that names none (see
// handlers.inheritProviderPreference). Where the preferred provider serves no
// model of the tier, resolution falls back to the tier's own order.
//
// This is the single seam for run-scoped provider policy: every executor calls
// propagateProviderPreference, and everything call_llm reads comes from
// ProviderPreference.
const (
	// InputKeyProviderPreference is the runtime-injected input that carries an
	// ancestor's provider preference (an ordered []string of driver ids) to a
	// child. Never client-settable: it is in workflow.RuntimeInjectedInputs.
	InputKeyProviderPreference = "__provider_preference"

	// InputKeyProviderPolicy is the runtime-injected input that carries what the
	// run does when its provider is rate-limited (types.RunProviderPolicy) to
	// every child. Written by the launcher from the chat's policy and the
	// Settings default, and by a preset's or a spawn call's `providers:`. Never
	// client-settable: it is in workflow.RuntimeInjectedInputs.
	InputKeyProviderPolicy = "__provider_policy"

	// providerPreferenceModelInput is the input whose value is the agent's own
	// model: the composer's model pill and every builtin agent workflow use
	// this name.
	providerPreferenceModelInput = "model"
)

// ProviderPreference returns the providers a run with these inputs prefers for
// a model selector that names none: its own model's provider when that model
// names one ("<model>@<driver>", or providers: [...]), otherwise the
// preference it inherited from its parent. Nil when there is neither.
func ProviderPreference(inputs map[string]interface{}) []string {
	if inputs == nil {
		return nil
	}
	if own := explicitModelProviders(inputs[providerPreferenceModelInput]); len(own) > 0 {
		return own
	}
	return registryProviders(stringList(inputs[InputKeyProviderPreference]))
}

// propagateProviderPreference carries the parent's provider preference and
// provider policy onto a child input map. Call it LAST in child-input
// assembly, beside propagateUnattended: the child's own model, preset and
// spawn args are already in place.
//
// What the child set for itself wins, inherited fills the rest. That is the
// precedence spawn args > preset > inherited: a preset's `providers:` and a
// spawn call's `providers` are written into the child before this runs, and a
// child that pins its own provider (its model) hands that on when its own
// children are built.
func propagateProviderPreference(parent, child map[string]interface{}) {
	if child == nil {
		return
	}
	if driver, _ := parent[InputKeyProviderSwitch].(string); driver != "" {
		child[InputKeyProviderSwitch] = driver
	}
	if own := registryProviders(stringList(child[InputKeyProviderPreference])); len(own) > 0 {
		child[InputKeyProviderPreference] = own
	} else if preferred := ProviderPreference(parent); len(preferred) > 0 {
		child[InputKeyProviderPreference] = preferred
	} else {
		delete(child, InputKeyProviderPreference)
	}

	inherited := readProviderPolicy(parent)
	own := readProviderPolicy(child)
	merged := mergeProviderPolicy(inherited, own)
	if merged.IsZero() {
		delete(child, InputKeyProviderPolicy)
		return
	}
	child[InputKeyProviderPolicy] = merged.Value()
}

// ProviderPolicy is what a run with these inputs does when its provider is
// rate-limited: the policy the launcher resolved for the chat, a preset's or a
// spawn call's `providers:` for this run alone, or what it inherited from its
// parent — they all arrive in the one input, already merged by
// propagateProviderPreference.
//
// A fallback on the provider the run is already on is ignored (falling back to
// the limited provider is waiting), so the policy is read with the run's own
// primary provider (ProviderPreference(inputs)[0]) in mind.
func ProviderPolicy(inputs map[string]interface{}) types.RunProviderPolicy {
	policy := readProviderPolicy(inputs)
	if policy.OnLimit != types.OnLimitFallback {
		return policy
	}
	if primary := ProviderPreference(inputs); len(primary) > 0 && FallbackDriver(policy.FallbackModel) == primary[0] {
		policy.OnLimit, policy.FallbackModel = types.OnLimitWait, ""
	}
	return policy
}

// FallbackDriver is the driver of a "<model>@<driver>" fallback model.
func FallbackDriver(fallbackModel string) string {
	if driver := pinnedDriver(fallbackModel); len(driver) > 0 {
		return driver[0]
	}
	return ""
}

func readProviderPolicy(inputs map[string]interface{}) types.RunProviderPolicy {
	if inputs == nil {
		return types.RunProviderPolicy{}
	}
	return types.RunProviderPolicyFromValue(inputs[InputKeyProviderPolicy])
}

// mergeProviderPolicy lays own over inherited: own's on-limit choice (with its
// fallback model) when it made one, own's preferred account per driver.
func mergeProviderPolicy(inherited, own types.RunProviderPolicy) types.RunProviderPolicy {
	out := inherited
	if own.OnLimit != "" {
		out.OnLimit, out.FallbackModel = own.OnLimit, own.FallbackModel
	}
	if len(own.PreferredAccount) > 0 {
		accounts := make(map[string]string, len(inherited.PreferredAccount)+len(own.PreferredAccount))
		for driver, account := range inherited.PreferredAccount {
			accounts[driver] = account
		}
		for driver, account := range own.PreferredAccount {
			accounts[driver] = account
		}
		out.PreferredAccount = accounts
	}
	return out
}

// explicitModelProviders returns the providers a model input value names: the
// driver of a pinned id, else its providers list.
func explicitModelProviders(value interface{}) []string {
	switch v := value.(type) {
	case string:
		return registryProviders(pinnedDriver(v))
	case map[string]interface{}:
		id, _ := v["id"].(string)
		if driver := pinnedDriver(id); len(driver) > 0 {
			return registryProviders(driver)
		}
		return registryProviders(stringList(v["providers"]))
	default:
		return nil
	}
}

// pinnedDriver is the driver a "<model>@<driver>" id pins, as a one-element
// list, or nil for an id that pins none.
func pinnedDriver(id string) []string {
	idx := strings.LastIndex(id, "@")
	if idx <= 0 || idx == len(id)-1 {
		return nil
	}
	return []string{id[idx+1:]}
}

// registryProviders keeps the providers a registry tier can be resolved on.
// A local model is served by one machine's daemon ("<name>@local",
// "local:<daemonID>", "endpoint:<id>") and never by a tier: handed to a child's
// tier selector it would turn that selector into a local-model lookup
// (local.IsSelector) instead of a preference, so it is not carried.
func registryProviders(providers []string) []string {
	out := make([]string, 0, len(providers))
	for _, p := range providers {
		p = strings.TrimSpace(p)
		if p == "" || p == "local" || strings.Contains(p, ":") || slices.Contains(out, p) {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

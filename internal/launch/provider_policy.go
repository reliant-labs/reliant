// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// ProviderPolicyDefaultsKey is the user setting holding the default on-limit
// behaviour per provider, a JSON object keyed by driver id:
//
//	{"claude": {"on_limit": "fallback", "fallback_model": "gpt-5.6-sol@codex"},
//	 "codex":  {"on_limit": "wait"}}
//
// A provider with no entry waits for its limit to reset. Cross-provider
// fallback is opt-in.
const ProviderPolicyDefaultsKey = "provider.policy.defaults"

// ProviderPolicyDefaults is the Settings default, by driver id.
type ProviderPolicyDefaults map[string]chatselection.ProviderPolicy

// ParseProviderPolicyDefaults reads the setting's value. A value it cannot read
// is no defaults, never an error: the default is to wait.
func ParseProviderPolicyDefaults(value string) ProviderPolicyDefaults {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var out ProviderPolicyDefaults
	if err := json.Unmarshal([]byte(value), &out); err != nil {
		logging.Warn("Ignoring an unreadable provider policy default", "key", ProviderPolicyDefaultsKey, "error", err)
		return nil
	}
	return out
}

// EffectiveProviderPolicy is the policy a chat's run starts with: the chat's own
// choice when it made one, else what the run's preset declared (already in
// inputs), else the Settings default for the run's primary provider. A policy
// that says nothing is the default, to wait.
//
// Account preferences only come from a preset's `providers:`; the chat and the
// Settings default choose what to do on a limit, not which account to use.
func EffectiveProviderPolicy(
	inputs map[string]interface{},
	chat chatselection.ProviderPolicy,
	defaults ProviderPolicyDefaults,
	primaryDriver string,
) types.RunProviderPolicy {
	policy := v2.ProviderPolicy(inputs)
	switch {
	case chat.OnLimit != chatselection.OnLimitInherit:
		policy.OnLimit, policy.FallbackModel = string(chat.OnLimit), chat.FallbackModel
	case policy.OnLimit == "" && primaryDriver != "":
		if def, ok := defaults[primaryDriver]; ok {
			policy.OnLimit, policy.FallbackModel = string(def.OnLimit), def.FallbackModel
		}
	}
	policy = types.RunProviderPolicyFromValue(policy)
	if policy.OnLimit == types.OnLimitFallback && primaryDriver != "" && v2.FallbackDriver(policy.FallbackModel) == primaryDriver {
		policy.OnLimit, policy.FallbackModel = types.OnLimitWait, ""
	}
	return policy
}

// injectProviderPolicy writes the chat's effective provider policy into a run's
// inputs as the runtime-injected __provider_policy, which the runtime hands to
// every sub-agent, loop body and sub-workflow. Inputs that resolve to no policy
// carry no key, so a run that never opted in is byte-for-byte what it was.
//
// A read that fails leaves the policy at its default (wait): a settings hiccup
// must not turn into a cross-provider spend.
func (l *Launcher) injectProviderPolicy(ctx context.Context, userID, chatID string, inputs map[string]interface{}) {
	if inputs == nil {
		return
	}
	var chat chatselection.ProviderPolicy
	if chatID != "" {
		sel, err := l.repo.GetChatSelection(ctx, chatID)
		if err != nil {
			logging.Warn("Could not read the chat's provider policy; using the default", "chatID", chatID, "error", err)
		} else if sel != nil {
			chat = sel.Policy
		}
	}
	var defaults ProviderPolicyDefaults
	if setting, err := l.repo.GetSetting(ctx, userID, nil, ProviderPolicyDefaultsKey); err == nil && setting != nil {
		defaults = ParseProviderPolicyDefaults(setting.Value)
	}

	policy := EffectiveProviderPolicy(inputs, chat, defaults, l.primaryDriver(ctx, userID, inputs))
	if policy.IsZero() {
		delete(inputs, v2.InputKeyProviderPolicy)
		return
	}
	inputs[v2.InputKeyProviderPolicy] = policy.Value()
}

// ResolveProviderPolicy is the policy a run holding inputs should have now, for
// a chat: its presets' `providers:`, then the chat's own choice, then the
// Settings default for the run's primary provider. The inputs carry the policy
// the run was last given, which the chat or the Settings default may have
// changed since, so it is not used as a layer; the run's model and provider
// preference are.
func (l *Launcher) ResolveProviderPolicy(ctx context.Context, userID string, chat *db.Chat, inputs map[string]interface{}) types.RunProviderPolicy {
	base := make(map[string]interface{}, len(inputs))
	for key, value := range inputs {
		base[key] = value
	}
	delete(base, v2.InputKeyProviderPolicy)
	if chat != nil && len(chat.SelectedPresets) > 0 {
		fromPresets := l.applyPresets(ctx, userID, chat.ProjectID, map[string]interface{}{}, chat.SelectedPresets)
		if value, ok := fromPresets[v2.InputKeyProviderPolicy]; ok {
			base[v2.InputKeyProviderPolicy] = value
		}
	}
	chatID := ""
	if chat != nil {
		chatID = chat.ID
	}
	l.injectProviderPolicy(ctx, userID, chatID, base)
	return v2.ProviderPolicy(base)
}

// RefreshProviderPolicy sets inputs' policy to ResolveProviderPolicy, in place.
// For the inputs of a stopped run that a new run is built from.
func (l *Launcher) RefreshProviderPolicy(ctx context.Context, userID string, chat *db.Chat, inputs map[string]interface{}) {
	if inputs == nil || chat == nil {
		return
	}
	policy := l.ResolveProviderPolicy(ctx, userID, chat, inputs)
	if policy.IsZero() {
		delete(inputs, v2.InputKeyProviderPolicy)
		return
	}
	inputs[v2.InputKeyProviderPolicy] = policy.Value()
}

// primaryDriver is the provider a run starts on: the one its inputs prefer or
// pin, else the one its model input resolves to for this user's providers. ""
// when it cannot be told, in which case no Settings default applies.
func (l *Launcher) primaryDriver(ctx context.Context, userID string, inputs map[string]interface{}) string {
	if preferred := v2.ProviderPreference(inputs); len(preferred) > 0 {
		return preferred[0]
	}
	selector, ok := inputs["model"]
	if !ok || selector == nil {
		return ""
	}
	available, err := drivers.LookupAvailableDrivers(ctx, userID)
	if err != nil {
		return ""
	}
	return resolvedDriver(selector, available)
}

// resolvedDriver is the driver a model selector value resolves to over the
// providers available, "" when it resolves to none.
func resolvedDriver(selector interface{}, available models.AvailableDrivers) string {
	var ms models.ModelSelector
	data, err := json.Marshal(selector)
	if err != nil || json.Unmarshal(data, &ms) != nil {
		return ""
	}
	var providers []string
	for id, cfg := range available.Drivers {
		if cfg.IsConfigured() {
			providers = append(providers, string(id))
		}
	}
	resolved, err := models.MustGetRegistry().WithAvailability(available.Availability).Resolve(ms, providers)
	if err != nil || resolved == nil {
		return ""
	}
	return resolved.Provider.Driver
}

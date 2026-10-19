// Copyright (c) 2025 Reliant Labs
package preset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"gopkg.in/yaml.v3"
)

// The runtime-injected inputs a `providers:` block becomes. They are the same
// names as runtime.InputKeyProviderPreference / InputKeyProviderPolicy and
// workflow.RuntimeInjectedInputs (pinned equal by a test in the runtime
// package): this package cannot import either.
const (
	InputKeyProviderPreference = "__provider_preference"
	InputKeyProviderPolicy     = "__provider_policy"
)

// IsProviderInput reports whether key is one of the runtime inputs a
// `providers:` block writes. Those belong at the TOP of a run's inputs, never
// inside a preset group's map.
func IsProviderInput(key string) bool {
	return key == InputKeyProviderPreference || key == InputKeyProviderPolicy
}

// ProviderRouting is the declarative provider policy a preset or a spawn call
// carries: which providers (and accounts) a run prefers, and what it does when
// they are rate-limited. No expressions: a checked-in preset says
// `claude:work@acme.com` and means it.
//
//	providers:
//	  prefer: [codex, "claude:work@acme.com"]   # ordered; "driver" or "driver:account"
//	  on_limit: fallback                         # wait (the default) | fallback
//	  fallback: claude-5.5-opus@claude           # "<model>@<driver>"; needs on_limit: fallback
type ProviderRouting struct {
	Prefer  []string `yaml:"prefer,omitempty" json:"prefer,omitempty"`
	OnLimit string   `yaml:"on_limit,omitempty" json:"on_limit,omitempty"`
	// Fallback is the model to continue on, "<model>@<driver>".
	Fallback string `yaml:"fallback,omitempty" json:"fallback,omitempty"`
}

// ParseProviderRouting reads the `providers` argument of a spawn call (any JSON
// value) and validates it. Unknown keys are an error: a typo must not silently
// turn a fallback off.
func ParseProviderRouting(raw interface{}) (*ProviderRouting, error) {
	if raw == nil {
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("providers: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r ProviderRouting
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("providers: %w (allowed: prefer, on_limit, fallback)", err)
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

// UnmarshalYAML rejects unknown keys, which yaml.Unmarshal would drop.
func (r *ProviderRouting) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("providers must be a mapping with prefer, on_limit and fallback")
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		switch key := node.Content[i].Value; key {
		case "prefer", "on_limit", "fallback":
		default:
			return fmt.Errorf("unknown key %q in providers (allowed: prefer, on_limit, fallback)", key)
		}
	}
	type plain ProviderRouting
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	*r = ProviderRouting(p)
	return nil
}

// Validate checks every reference the way validateModelParam checks a model.
func (r *ProviderRouting) Validate() error {
	if r == nil {
		return nil
	}
	for _, entry := range r.Prefer {
		ref, err := models.ParseProviderRef(entry)
		if err != nil {
			return fmt.Errorf("providers.prefer: %w", err)
		}
		if ref.Driver == "local" {
			return fmt.Errorf("providers.prefer: %q is a local model; pick it as the model, not as a provider", entry)
		}
	}
	switch r.OnLimit {
	case "", "wait":
		if r.Fallback != "" {
			return fmt.Errorf("providers.fallback is only meaningful with on_limit: fallback")
		}
	case "fallback":
		if r.Fallback == "" {
			return fmt.Errorf("providers.on_limit: fallback needs providers.fallback (\"<model>@<driver>\")")
		}
		if err := validateFallbackModel(r.Fallback); err != nil {
			return err
		}
	default:
		return fmt.Errorf("providers.on_limit %q is not wait or fallback", r.OnLimit)
	}
	return nil
}

func validateFallbackModel(id string) error {
	idx := strings.LastIndex(id, "@")
	if idx <= 0 || idx == len(id)-1 {
		return fmt.Errorf("providers.fallback %q must be \"<model>@<driver>\"", id)
	}
	model, driver := id[:idx], id[idx+1:]
	if _, ok := models.ProviderPriority[driver]; !ok || driver == "local" {
		return fmt.Errorf("providers.fallback %q names an unknown provider %q", id, driver)
	}
	registry := models.MustGetRegistry()
	if _, found := registry.GetDefinition(model); !found && !registry.IsRetired(model) {
		return fmt.Errorf("providers.fallback: invalid model %q - see docs/models.md for available models", model)
	}
	return nil
}

// Inputs is the routing as runtime-injected inputs: the ordered drivers of
// `prefer` as __provider_preference, and the policy (on-limit choice, fallback
// model, and the account each `driver:account` entry names) as
// __provider_policy. Plain maps and []interface{} so the result survives a
// structpb round trip. Nil when the routing says nothing.
func (r *ProviderRouting) Inputs() map[string]interface{} {
	if r == nil {
		return nil
	}
	out := map[string]interface{}{}
	var drivers []interface{}
	accounts := map[string]interface{}{}
	seen := map[string]bool{}
	for _, entry := range r.Prefer {
		ref, err := models.ParseProviderRef(entry)
		if err != nil {
			continue
		}
		driver := string(ref.Driver)
		if !seen[driver] {
			seen[driver] = true
			drivers = append(drivers, driver)
		}
		if ref.Instance != "" {
			if _, set := accounts[driver]; !set {
				accounts[driver] = ref.Instance
			}
		}
	}
	if len(drivers) > 0 {
		out[InputKeyProviderPreference] = drivers
	}
	policy := map[string]interface{}{}
	if r.OnLimit != "" {
		policy["on_limit"] = r.OnLimit
	}
	if r.OnLimit == "fallback" && r.Fallback != "" {
		policy["fallback_model"] = r.Fallback
	}
	if len(accounts) > 0 {
		policy["preferred_account"] = accounts
	}
	if len(policy) > 0 {
		out[InputKeyProviderPolicy] = policy
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// InputParams is the preset's params plus the runtime inputs its `providers:`
// block writes: what a preset contributes to a run's inputs.
func (p *Preset) InputParams() map[string]interface{} {
	extra := p.Providers.Inputs()
	if len(extra) == 0 {
		return p.Params
	}
	out := make(map[string]interface{}, len(p.Params)+len(extra))
	for k, v := range p.Params {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

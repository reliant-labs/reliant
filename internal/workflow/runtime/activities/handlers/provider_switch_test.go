// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// A chat-wide provider switch (llmCallSpec.ChatProvider) keeps the root's exact
// model and moves every descendant's semantic tags onto the selected provider.
// The registry's Providers preference falls through to ANY configured provider,
// so these tests connect the OLD provider as well and prove it is never chosen:
// a descendant either lands on the selected provider or is visibly held.

// switchResolver connects every named provider for one user and returns a
// resolver over the real registry and real provider-account config.
func switchResolver(t *testing.T, connected ...string) func(llmCallSpec) (*resolvedLLMCall, error) {
	t.Helper()
	ctx := context.Background()
	userID := "user-" + uuid.NewString()
	provisionProviderKeys(t, ctx, userID, connected)
	original := drivers.GetDriver
	drivers.GetDriver = captureDriverOptionsResolver(&capturedDriverOptions{})
	t.Cleanup(func() { drivers.GetDriver = original })
	return func(spec llmCallSpec) (*resolvedLLMCall, error) {
		spec.UserID, spec.SessionID = userID, "s"
		return resolveLLMCall(ctx, nil, spec)
	}
}

// firstOn is the model the registry lists first under tag that driver serves,
// i.e. what a tag resolves to when driver is the only eligible provider.
func firstOn(t *testing.T, tag, driver string) string {
	t.Helper()
	reg := models.MustGetRegistry()
	for _, entry := range reg.TagEntries(tag) {
		def, ok := reg.GetDefinition(entry.Model)
		require.True(t, ok, entry.Model)
		if def.Capabilities.CanOutput(models.ModalityText) && defServedBy(def, driver) {
			return def.ID + "@" + driver
		}
	}
	t.Fatalf("registry lists no text model under tag %q served by %q", tag, driver)
	return ""
}

func defServedBy(def *models.ModelDefinition, driver string) bool {
	return slices.ContainsFunc(def.Providers, func(p models.ProviderMapping) bool { return p.Driver == driver })
}

// tagServedBy reports whether any model listed under tag is served by driver.
func tagServedBy(tag, driver string) bool {
	reg := models.MustGetRegistry()
	for _, entry := range reg.TagEntries(tag) {
		if def, ok := reg.GetDefinition(entry.Model); ok && defServedBy(def, driver) {
			return true
		}
	}
	return false
}

// switchDrivers are the providers every resolver in this file connects.
var switchDrivers = []string{"anthropic", "codex", "openai", "gemini"}

// disabledProviderClient is a connected provider whose account reports every
// model as unavailable (an authoritative, empty catalog). The real registry and
// real tags stay in play; only this provider stops serving anything.
type disabledProviderClient struct{ staticCatalogClient }

func (disabledProviderClient) ReportAvailability(context.Context) (registry.ProviderAvailability, error) {
	return registry.ProviderAvailability{Authoritative: true, UnlistedReason: "disabled for this account (test)"}, nil
}

// switchResolverWithDisabled is switchResolver where `disabled` is connected
// but serves no model. Every tag therefore cannot map onto it, while every
// other connected provider still serves the tag: the case where a fallthrough
// would silently succeed.
func switchResolverWithDisabled(t *testing.T, disabled string, connected ...string) func(llmCallSpec) (*resolvedLLMCall, error) {
	t.Helper()
	resolve := switchResolver(t, connected...)
	previous := drivers.AccountAvailabilityClient
	drivers.AccountAvailabilityClient = func(ctx context.Context, id models.DriverID, cfg models.DriverConfig) (registry.Client, error) {
		if string(id) == disabled {
			return disabledProviderClient{staticCatalogClient{name: string(id)}}, nil
		}
		return previous(ctx, id, cfg)
	}
	t.Cleanup(func() { drivers.AccountAvailabilityClient = previous })
	return resolve
}

// requireOn is the strict final guard: whatever resolved, it resolved on the
// selected provider and nowhere else.
func requireOn(t *testing.T, res *resolvedLLMCall, err error, selected string) {
	t.Helper()
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, selected, res.ProviderDriver, "resolved to %s, an unselected provider", res.ModelID)
	assert.True(t, strings.HasSuffix(res.ModelID, "@"+selected), "model id %q is not on %s", res.ModelID, selected)
}

func requireProviderSwitchHold(t *testing.T, res *resolvedLLMCall, err error) {
	t.Helper()
	require.Error(t, err)
	require.Nil(t, res, "a held call must not resolve to any model")
	var app *temporal.ApplicationError
	require.True(t, errors.As(err, &app), "hold must be a typed temporal application error, got %T: %v", err, err)
	assert.Equal(t, "ProviderSwitchHold", app.Type())
	assert.True(t, app.NonRetryable(), "a hold must not be retried inside the activity")
}

func TestProviderSwitch_RootKeepsExactModel(t *testing.T) {
	resolve := switchResolver(t, switchDrivers...)

	for _, tc := range []struct{ id, selected string }{
		{"gpt-6.1-sol@codex", "codex"},
		{"gpt-6.1-sol@openai", "openai"},
		{"claude-4.5-haiku@anthropic", "anthropic"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			res, err := resolve(llmCallSpec{Selector: models.ModelSelector{ID: tc.id}, ChatProvider: tc.selected})
			requireOn(t, res, err, tc.selected)
			assert.Equal(t, tc.id, res.ModelID, "the root's exact model is the user's choice and is never rewritten by a switch")
		})
	}
}

// The root is the user's explicit picker selection. When it names a model the
// selected provider cannot run it is rejected visibly, never substituted.
func TestProviderSwitch_RootUnavailableExactSelectionIsRejectedNotSubstituted(t *testing.T) {
	resolve := switchResolver(t, switchDrivers...)

	for _, tc := range []struct{ name, id, selected string }{
		{"pinned to an unselected provider", "gpt-6.1-sol@openai", "codex"},
		{"model the selected provider does not serve", "claude-4.5-haiku@anthropic", "codex"},
		{"unpinned model the selected provider does not serve", "claude-4.5-haiku", "codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := resolve(llmCallSpec{Selector: models.ModelSelector{ID: tc.id}, ChatProvider: tc.selected})
			if err == nil {
				t.Fatalf("root %q resolved to %s instead of being rejected", tc.id, res.ModelID)
			}
			requireProviderSwitchHold(t, res, err)
		})
	}

	t.Run("root ignores preset tags: no tag-based substitution", func(t *testing.T) {
		res, err := resolve(llmCallSpec{
			Selector:        models.ModelSelector{ID: "claude-4.5-haiku@anthropic"},
			PresetModelTags: []string{models.TagFast},
			ChatProvider:    "codex",
		})
		if err == nil {
			t.Fatalf("root resolved to %s via preset tags", res.ModelID)
		}
		requireProviderSwitchHold(t, res, err)
	})
}

func TestProviderSwitch_DescendantTagsResolveOnSelectedProvider(t *testing.T) {
	resolve := switchResolver(t, switchDrivers...)

	for _, selected := range []string{"codex", "anthropic"} {
		for _, tag := range []string{models.TagFlagship, models.TagFast} {
			t.Run(tag+" on "+selected, func(t *testing.T) {
				want := firstOn(t, tag, selected)
				res, err := resolve(llmCallSpec{
					// Stale concrete model from the previous provider's resolution.
					Selector:     models.ModelSelector{Tags: []string{tag}},
					ChatProvider: selected,
					Descendant:   true,
				})
				require.NoError(t, err)
				assert.Equal(t, selected, res.ProviderDriver)
				assert.Equal(t, want, res.ModelID)
			})
		}
	}
}

func TestProviderSwitch_DescendantExplicitTagsBeatPresetTags(t *testing.T) {
	resolve := switchResolver(t, switchDrivers...)

	want := firstOn(t, models.TagFast, "codex")
	require.NotEqual(t, firstOn(t, models.TagFlagship, "codex"), want,
		"fixture needs fast and flagship to differ on codex for this test to discriminate")

	t.Run("selector tags", func(t *testing.T) {
		res, err := resolve(llmCallSpec{
			Selector:        models.ModelSelector{Tags: []string{models.TagFast}},
			PresetModelTags: []string{models.TagFlagship},
			ChatProvider:    "codex",
			Descendant:      true,
		})
		require.NoError(t, err)
		assert.Equal(t, want, res.ModelID)
	})

	t.Run("selector tags beat a stamped id too", func(t *testing.T) {
		res, err := resolve(llmCallSpec{
			Selector:        models.ModelSelector{ID: "claude-4.5-haiku@anthropic", Tags: []string{models.TagFast}},
			PresetModelTags: []string{models.TagFlagship},
			ChatProvider:    "codex",
			Descendant:      true,
		})
		require.NoError(t, err)
		assert.Equal(t, want, res.ModelID)
	})
}

func TestProviderSwitch_DescendantRecoversPresetTagsFromStaleConcreteModel(t *testing.T) {
	resolve := switchResolver(t, switchDrivers...)

	for _, tag := range []string{models.TagFlagship, models.TagFast} {
		t.Run(tag, func(t *testing.T) {
			res, err := resolve(llmCallSpec{
				// The old provider's resolution was stamped as an exact id; the preset's
				// original tag is the only memory of what the thread asked for.
				Selector:        models.ModelSelector{ID: "claude-4.5-haiku@anthropic"},
				PresetModelTags: []string{tag},
				ChatProvider:    "codex",
				Descendant:      true,
			})
			require.NoError(t, err)
			assert.Equal(t, "codex", res.ProviderDriver)
			assert.Equal(t, firstOn(t, tag, "codex"), res.ModelID)
		})
	}

	t.Run("an unpinned stale id recovers too", func(t *testing.T) {
		res, err := resolve(llmCallSpec{
			Selector:        models.ModelSelector{ID: "claude-4.5-haiku"},
			PresetModelTags: []string{models.TagFlagship},
			ChatProvider:    "codex",
			Descendant:      true,
		})
		require.NoError(t, err)
		assert.Equal(t, firstOn(t, models.TagFlagship, "codex"), res.ModelID)
	})
}

// Anthropic leads the fast and flagship tiers and stays connected throughout:
// any fallthrough lands there.
func TestProviderSwitch_OldProviderStillAvailableIsNeverChosen(t *testing.T) {
	resolve := switchResolver(t, switchDrivers...)

	t.Run("control: without a switch the tier resolves elsewhere, so the cases below discriminate", func(t *testing.T) {
		res, err := resolve(llmCallSpec{Selector: models.ModelSelector{Tags: []string{models.TagFast}}, Descendant: true})
		require.NoError(t, err)
		assert.NotEqual(t, "codex", res.ProviderDriver)
	})

	t.Run("a selector that lists the old provider is still constrained", func(t *testing.T) {
		res, err := resolve(llmCallSpec{
			Selector:     models.ModelSelector{Tags: []string{models.TagFast}, Providers: []string{"anthropic"}},
			ChatProvider: "codex",
			Descendant:   true,
		})
		require.NoError(t, err)
		assert.Equal(t, "codex", res.ProviderDriver)
	})

	t.Run("run provider preference naming the old provider is overridden", func(t *testing.T) {
		res, err := resolve(llmCallSpec{
			Selector:     models.ModelSelector{Tags: []string{models.TagFast}},
			RunProviders: []string{"anthropic"},
			ChatProvider: "codex",
			Descendant:   true,
		})
		require.NoError(t, err)
		assert.Equal(t, "codex", res.ProviderDriver)
	})

	t.Run("a tag preference pinned to the old provider's model is skipped", func(t *testing.T) {
		reader := fakeSettingsReader{rows: map[string]string{
			"model.tag_config.fast": `{"model_id":"claude-4.5-haiku"}`,
		}}
		res, err := resolve(llmCallSpec{
			Selector:       models.ModelSelector{Tags: []string{models.TagFast}},
			TagPrefsReader: reader,
			ChatProvider:   "codex",
			Descendant:     true,
		})
		require.NoError(t, err)
		assert.Equal(t, "codex", res.ProviderDriver)
		assert.Equal(t, firstOn(t, models.TagFast, "codex"), res.ModelID)
	})

}

func TestProviderSwitch_FallbackSelectorCannotReachOldProvider(t *testing.T) {
	// codex is connected but serves nothing; anthropic and openai serve every tier.
	resolve := switchResolverWithDisabled(t, "codex", switchDrivers...)

	t.Run("control: without a switch the same call lands on another provider", func(t *testing.T) {
		res, err := resolve(llmCallSpec{
			Selector:     models.ModelSelector{Tags: []string{models.TagFast}},
			RunProviders: []string{"codex"},
			Descendant:   true,
		})
		require.NoError(t, err, "the old provider must be able to serve this tier for the cases below to discriminate")
		assert.NotEqual(t, "codex", res.ProviderDriver)
	})

	for name, fallback := range map[string]models.ModelSelector{
		"tag fallback":                   {Tags: []string{models.TagFast}},
		"tag fallback listing old":       {Tags: []string{models.TagFast}, Providers: []string{"anthropic"}},
		"exact fallback on old provider": {ID: "claude-4.5-haiku@anthropic"},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := resolve(llmCallSpec{
				Selector:         models.ModelSelector{Tags: []string{models.TagFlagship}},
				FallbackSelector: fallback,
				ChatProvider:     "codex",
				Descendant:       true,
			})
			if err == nil {
				t.Fatalf("resolved to %s via fallback instead of holding", res.ModelID)
			}
			requireProviderSwitchHold(t, res, err)
		})
	}
}

func TestProviderSwitch_UnsupportedTagOnSelectedProviderHolds(t *testing.T) {
	resolve := switchResolverWithDisabled(t, "codex", switchDrivers...)

	for _, tag := range []string{models.TagFlagship, models.TagFast, models.TagModerate} {
		t.Run(tag, func(t *testing.T) {
			require.True(t, tagServedBy(tag, "anthropic"), "the old provider serves %s in the registry", tag)

			res, err := resolve(llmCallSpec{
				Selector:     models.ModelSelector{Tags: []string{tag}},
				ChatProvider: "codex",
				Descendant:   true,
			})
			if err == nil {
				t.Fatalf("tag %q resolved to %s although codex serves nothing; other providers do (silent fallthrough)", tag, res.ModelID)
			}
			requireProviderSwitchHold(t, res, err)
			assert.Contains(t, err.Error(), "codex", "the hold names the selected provider")
			assert.Contains(t, err.Error(), tag, "the hold names the tag that cannot be mapped")
		})
	}

	t.Run("recovered preset tags hold too", func(t *testing.T) {
		res, err := resolve(llmCallSpec{
			Selector:        models.ModelSelector{ID: "claude-4.5-haiku@anthropic"},
			PresetModelTags: []string{models.TagFast},
			ChatProvider:    "codex",
			Descendant:      true,
		})
		if err == nil {
			t.Fatalf("resolved to %s instead of holding", res.ModelID)
		}
		requireProviderSwitchHold(t, res, err)
	})

	t.Run("a tag preference cannot rescue it elsewhere", func(t *testing.T) {
		reader := fakeSettingsReader{rows: map[string]string{"model.tag_config.fast": `{"model_id":"claude-4.5-haiku"}`}}
		res, err := resolve(llmCallSpec{
			Selector:       models.ModelSelector{Tags: []string{models.TagFast}},
			TagPrefsReader: reader,
			ChatProvider:   "codex",
			Descendant:     true,
		})
		if err == nil {
			t.Fatalf("resolved to %s via a tag preference instead of holding", res.ModelID)
		}
		requireProviderSwitchHold(t, res, err)
	})

	t.Run("the disabled root exact model is rejected, not substituted", func(t *testing.T) {
		res, err := resolve(llmCallSpec{Selector: models.ModelSelector{ID: "gpt-6.1-sol@codex"}, ChatProvider: "codex"})
		if err == nil {
			t.Fatalf("root resolved to %s instead of being rejected", res.ModelID)
		}
		requireProviderSwitchHold(t, res, err)
	})
}

func TestProviderSwitch_UnknownTagHoldsInsteadOfGuessing(t *testing.T) {
	resolve := switchResolver(t, switchDrivers...)

	res, err := resolve(llmCallSpec{
		Selector:     models.ModelSelector{Tags: []string{"no-such-tier"}},
		ChatProvider: "codex",
		Descendant:   true,
	})
	if err == nil {
		t.Fatalf("unknown tag resolved to %s instead of holding", res.ModelID)
	}
	requireProviderSwitchHold(t, res, err)
	assert.Contains(t, err.Error(), "no-such-tier")
}

func TestProviderSwitch_SelectedProviderNotConnectedHolds(t *testing.T) {
	resolve := switchResolver(t, "anthropic", "codex")

	res, err := resolve(llmCallSpec{
		Selector:     models.ModelSelector{Tags: []string{models.TagFast}},
		ChatProvider: "gemini",
		Descendant:   true,
	})
	if err == nil {
		t.Fatalf("resolved to %s although the selected provider is not connected", res.ModelID)
	}
	requireProviderSwitchHold(t, res, err)
}

func TestProviderSwitch_ExactOnlyDescendantOnOldDriver(t *testing.T) {
	resolve := switchResolver(t, switchDrivers...)

	t.Run("same model served by the selected driver keeps its id and drops @old", func(t *testing.T) {
		require.True(t, defServedBy(mustDef(t, "gpt-6.1-sol"), "openai"))
		require.True(t, defServedBy(mustDef(t, "gpt-6.1-sol"), "codex"))
		res, err := resolve(llmCallSpec{
			Selector:     models.ModelSelector{ID: "gpt-6.1-sol@openai"},
			ChatProvider: "codex",
			Descendant:   true,
		})
		requireOn(t, res, err, "codex")
		assert.Equal(t, "gpt-6.1-sol@codex", res.ModelID)
	})

	t.Run("same model, unpinned id", func(t *testing.T) {
		res, err := resolve(llmCallSpec{
			Selector:     models.ModelSelector{ID: "gpt-6.1-sol"},
			ChatProvider: "codex",
			Descendant:   true,
		})
		requireOn(t, res, err, "codex")
		assert.Equal(t, "gpt-6.1-sol@codex", res.ModelID)
	})

	t.Run("model the selected driver does not serve holds", func(t *testing.T) {
		require.False(t, defServedBy(mustDef(t, "claude-4.5-haiku"), "codex"))
		res, err := resolve(llmCallSpec{
			Selector:     models.ModelSelector{ID: "claude-4.5-haiku@anthropic"},
			ChatProvider: "codex",
			Descendant:   true,
		})
		if err == nil {
			t.Fatalf("exact-only descendant resolved to %s instead of holding", res.ModelID)
		}
		requireProviderSwitchHold(t, res, err)
	})

	t.Run("unpinned model the selected driver does not serve holds", func(t *testing.T) {
		res, err := resolve(llmCallSpec{
			Selector:     models.ModelSelector{ID: "claude-4.5-haiku"},
			ChatProvider: "codex",
			Descendant:   true,
		})
		if err == nil {
			t.Fatalf("exact-only descendant resolved to %s instead of holding", res.ModelID)
		}
		requireProviderSwitchHold(t, res, err)
	})

	t.Run("a tag preference cannot rescue or redirect an exact-only descendant", func(t *testing.T) {
		reader := fakeSettingsReader{rows: map[string]string{"model.tag_config.fast": `{"model_id":"gpt-6-luna"}`}}
		res, err := resolve(llmCallSpec{
			Selector:       models.ModelSelector{ID: "claude-4.5-haiku@anthropic"},
			TagPrefsReader: reader,
			ChatProvider:   "codex",
			Descendant:     true,
		})
		if err == nil {
			t.Fatalf("exact-only descendant resolved to %s via a tag preference", res.ModelID)
		}
		requireProviderSwitchHold(t, res, err)
	})
}

// User per-tag model preferences, pinned ids and preset tags must all end on the
// selected provider: no path may resolve elsewhere, including a preference
// whose own provider pin names a connected, unselected provider.
func TestProviderSwitch_StrictGuardAcrossPrefsPinsAndPresetTags(t *testing.T) {
	resolve := switchResolver(t, switchDrivers...)
	fast := models.ModelSelector{Tags: []string{models.TagFast}}

	prefs := map[string]string{
		"claude model":                      `{"model_id":"claude-4.5-haiku"}`,
		"claude model pinned":               `{"model_id":"claude-4.5-haiku","providers":["anthropic"]}`,
		"codex-served model pinned to old":  `{"model_id":"gpt-6.1-sol","providers":["openai"]}`,
		"codex-served model, old @ in id":   `{"model_id":"gpt-6.1-sol@openai"}`,
		"unknown model":                     `{"model_id":"no-such-model"}`,
		"model served by selected provider": `{"model_id":"gpt-6-luna"}`,
	}
	for name, raw := range prefs {
		t.Run("tag pref: "+name, func(t *testing.T) {
			reader := fakeSettingsReader{rows: map[string]string{"model.tag_config.fast": raw}}
			for _, spec := range []llmCallSpec{
				{Selector: fast},
				{Selector: models.ModelSelector{ID: "claude-4.5-haiku@anthropic"}, PresetModelTags: []string{models.TagFast}},
				{Selector: models.ModelSelector{ID: "gpt-6.1-sol@openai"}, PresetModelTags: []string{models.TagFast}},
				{Selector: models.ModelSelector{Tags: []string{models.TagFast}, Providers: []string{"anthropic", "openai"}}},
			} {
				spec.TagPrefsReader, spec.ChatProvider, spec.Descendant = reader, "codex", true
				res, err := resolve(spec)
				requireOn(t, res, err, "codex")
			}
		})
	}

	t.Run("a preference the selected provider serves still applies to a tag descendant", func(t *testing.T) {
		reader := fakeSettingsReader{rows: map[string]string{"model.tag_config.fast": `{"model_id":"gpt-6-luna"}`}}
		res, err := resolve(llmCallSpec{Selector: fast, TagPrefsReader: reader, ChatProvider: "codex", Descendant: true})
		requireOn(t, res, err, "codex")
		assert.Equal(t, "gpt-6-luna@codex", res.ModelID)
	})

	t.Run("preset tags recovered from a pinned id resolve strictly", func(t *testing.T) {
		for _, tag := range []string{models.TagFlagship, models.TagFast, models.TagModerate} {
			for _, selected := range []string{"codex", "anthropic", "gemini"} {
				if !tagServedBy(tag, selected) {
					continue
				}
				res, err := resolve(llmCallSpec{
					Selector:        models.ModelSelector{ID: "gpt-6.1-sol@openai"},
					PresetModelTags: []string{tag},
					ChatProvider:    selected,
					Descendant:      true,
				})
				requireOn(t, res, err, selected)
			}
		}
	})
}

// Without a switch nothing changes: a descendant pinned to another provider
// keeps it. Guards against the constraint leaking into ordinary calls.
func TestProviderSwitch_NoChatProviderLeavesDescendantsAlone(t *testing.T) {
	resolve := switchResolver(t, switchDrivers...)

	res, err := resolve(llmCallSpec{
		Selector:   models.ModelSelector{ID: "claude-4.5-haiku@anthropic"},
		Descendant: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-4.5-haiku@anthropic", res.ModelID)
}

func mustDef(t *testing.T, id string) *models.ModelDefinition {
	t.Helper()
	def, ok := models.MustGetRegistry().GetDefinition(id)
	require.True(t, ok, id)
	return def
}

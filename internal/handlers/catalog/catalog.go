// Copyright (c) 2025 Reliant Labs
package catalog

import (
	"context"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/modelprefs"
)

// localModelInfos lists one entry per (daemon, endpoint, chat-capable model),
// including offline daemons' models (online=false) so the picker can show them
// disabled. A directory failure omits local models rather than failing the
// whole catalog.
func localModelInfos(found []local.Model) []*reliantv1.ModelInfo {
	infos := make([]*reliantv1.ModelInfo, 0, len(found))
	for _, m := range found {
		def := m.Definition
		defaultCompaction, maxCompaction := compactionThresholds(&def, string(local.Family))
		infos = append(infos, &reliantv1.ModelInfo{
			Id:                         m.CatalogID(),
			Name:                       def.Name,
			Provider:                   getDriverDisplayName(string(local.Family)),
			DriverId:                   string(local.Family),
			Capabilities:               capabilitiesToStrings(def.Capabilities),
			ContextWindow:              int64(def.Capabilities.MaxContextWindow),
			DefaultMaxTokens:           int64(def.Capabilities.MaxOutputTokens),
			CanReason:                  def.Capabilities.CanReason,
			SupportsAttachments:        def.Capabilities.SupportsAttachments,
			SupportsTools:              def.Capabilities.SupportsTools,
			SupportedThinkingLevels:    models.SupportedThinkingLevels(def.Capabilities),
			SupportsTemperature:        true,
			Local:                      localModelSource(m),
			DefaultCompactionThreshold: defaultCompaction,
			MaxCompactionThreshold:     maxCompaction,
		})
	}
	return infos
}

// compactionThresholds returns where an unpinned chat on def served by driver
// compacts, and the largest pin that is honored there — the same derivation
// call_llm uses (models.ProviderPromptCeiling).
func compactionThresholds(def *models.ModelDefinition, driver string) (defaultThreshold, maxThreshold int64) {
	ceiling := models.ProviderPromptCeiling(def, driver)
	return int64(models.CompactionThresholdForProvider(def, driver)),
		int64(models.CompactionThresholdCeiling(def, ceiling))
}

// localModelSource locates a model for the picker. A configured endpoint is
// grouped under its own name and carries no daemon id: a "local:<daemonID>"
// pin means "a model that daemon DETECTED", which a configured endpoint is not,
// even when that daemon relays it.
func localModelSource(m local.Model) *reliantv1.LocalModelSource {
	if m.Custom != nil {
		return &reliantv1.LocalModelSource{
			MachineName:  m.Custom.Name,
			EndpointId:   m.Custom.ID,
			EndpointKind: m.EndpointKind,
			Online:       m.Online,
		}
	}
	return &reliantv1.LocalModelSource{
		DaemonId:     m.DaemonID,
		MachineName:  m.Machine,
		EndpointId:   m.EndpointID,
		EndpointKind: m.EndpointKind,
		Online:       m.Online,
	}
}

// pickerScope is the scope a model picker lists for: the org the request
// names, once the caller is verified a live member of it (orgs.Named: never
// the client's word alone, and a machine credential's own org only), so the
// picker shows what a chat of that org may run on: its org accounts and
// seats, the caller's own accounts as its policy admits them, and nothing its
// policy refuses (control-plane docs/design/org-policies.md §4.1 point 3,
// org-provider-accounts.md §4.1). A request that names no org, from every
// client before orgs, lists the caller's own accounts.
func (s *Service) pickerScope(ctx context.Context, userID, requested string) (scope drivers.Scope, err error) {
	if s.deps.Orgs == nil {
		return drivers.PersonalScope(userID), nil
	}
	orgID, err := s.deps.Orgs.Named(ctx, userID, requested)
	if err != nil {
		return scope, svcerr.Wrap(err)
	}
	if orgID == "" {
		return drivers.PersonalScope(userID), nil
	}
	return drivers.Scope{UserID: userID, OrgID: orgID, Attended: true}, nil
}

func (s *Service) listLocalModels(ctx context.Context, userID string) []local.Model {
	found, err := local.ListModels(ctx, s.deps.LocalModels, userID)
	if err != nil {
		logging.WarnContext(ctx, "[ListModels] Could not list local models", "userID", userID, "error", err)
		return nil
	}
	return found
}

// localTierThinking is the effort a pinned local model runs at: only an
// explicit level it accepts, since local models have no tier default.
func localTierThinking(def models.ModelDefinition, level string) string {
	if level == "" {
		return ""
	}
	return models.ReconcileThinkingLevel(models.ResolveThinkingCapability(def.Capabilities), level)
}

// tierResolutions resolves every declared tag against the given providers,
// yielding the model and effort a `{tags: [tag]}` selector runs at — INCLUDING
// the user's default provider and tag preference (model_id when still
// available, thinking_level), exactly as resolveLLMCall applies them, so the
// UI never shows a tier the server will not run. Tags that do not resolve to a text model
// (unconfigured providers, image-gen) are skipped.
func tierResolutions(registry *models.ModelRegistry, providers []string, prefs map[string]modelprefs.TagPrefs, defaultProvider modelprefs.ProviderPreference, localModels []local.Model) []*reliantv1.TierResolution {
	tags := registry.ListAllTags()
	tiers := make([]*reliantv1.TierResolution, 0, len(tags))
	for _, tag := range tags {
		selector := defaultProvider.Apply(models.ModelSelector{
			Tags:                  []string{tag},
			RequireOutputModality: models.ModalityText,
		})
		tagPrefs := prefs[tag]
		if pinned, err := local.Resolve(localModels, tagPrefs.LocalSelector(), ""); tagPrefs.IsLocalModel() && err == nil {
			tiers = append(tiers, &reliantv1.TierResolution{
				Tag:           tag,
				ModelId:       pinned.CatalogID(),
				ThinkingLevel: localTierThinking(pinned.Definition, tagPrefs.ValidThinkingLevel()),
			})
			continue
		}
		resolved := tagPrefs.PreferredModel(registry, selector, providers)
		if resolved == nil {
			var err error
			resolved, err = registry.Resolve(selector, providers)
			if err != nil {
				continue
			}
		}
		if !resolved.Definition.Capabilities.CanOutput(models.ModalityText) {
			continue
		}
		thinking := resolved.ThinkingLevel
		if level := tagPrefs.ValidThinkingLevel(); level != "" {
			thinking = models.ReconcileThinkingLevel(models.ResolveThinkingCapability(resolved.Definition.Capabilities), level)
		}
		tiers = append(tiers, &reliantv1.TierResolution{
			Tag:           tag,
			ModelId:       resolved.Definition.ID + "@" + resolved.Provider.Driver,
			ThinkingLevel: thinking,
		})
	}
	return tiers
}

// capabilitiesToStrings converts model capabilities to a list of strings for display
func capabilitiesToStrings(caps models.ModelCapabilities) []string {
	var result []string
	if caps.SupportsTools {
		result = append(result, "tools")
	}
	if caps.SupportsCaching {
		result = append(result, "caching")
	}
	if caps.SupportsAttachments {
		result = append(result, "attachments")
	}
	if caps.CanReason {
		result = append(result, "reasoning")
	}
	// Output modalities beyond text. Text is every model's default and would be
	// noise on every row, so only the generative extras are surfaced — this is
	// what lets a picker distinguish an image generator from a vision model,
	// which "attachments" alone cannot express.
	for _, m := range caps.EffectiveOutputModalities() {
		if m != models.ModalityText {
			result = append(result, "generates_"+string(m))
		}
	}
	return result
}

// getDriverDisplayName returns a human-readable name for a driver
func getDriverDisplayName(driverID string) string {
	displayNames := map[string]string{
		"anthropic":  "Anthropic API",
		"claude":     "Claude subscription",
		"openai":     "OpenAI",
		"codex":      "Codex",
		"gemini":     "Google AI",
		"reliant":    "Reliant",
		"openrouter": "OpenRouter",
		"local":      "Local",
	}
	if name, ok := displayNames[driverID]; ok {
		return name
	}
	return driverID
}

// extractBaseModelID extracts the model ID without the driver suffix
// e.g., "claude-4.5-sonnet@openrouter" -> "claude-4.5-sonnet"
func extractBaseModelID(id string) string {
	if idx := strings.Index(id, "@"); idx != -1 {
		return id[:idx]
	}
	return id
}

// toolBuilder is what ListTools and ListNodes build tools through to read
// their descriptions. A description is static text, so a bare factory (a
// service built without Deps.Tools) is enough to read it.
func (s *Service) toolBuilder() ToolBuilder {
	if s.deps.Tools != nil {
		return s.deps.Tools
	}
	return tools.NewToolsFactory(nil)
}

// firstSentence is the text up to the first sentence end (or line break), for
// a one-line picker description of a long agent-facing tool description.
func firstSentence(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexAny(text, "\n"); i >= 0 {
		text = text[:i]
	}
	if i := strings.Index(text, ". "); i >= 0 {
		return text[:i+1]
	}
	return text
}

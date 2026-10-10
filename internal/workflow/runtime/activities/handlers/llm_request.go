// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/modelprefs"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// This file is the single request-construction path for LLM calls made by
// workflow activities. CallLLM (the normal agent turn) and Compact (context
// summarization) both build their request envelope here, so provider
// constraints — thinking configuration, temperature gating, tool-less history
// sanitization, deterministic driver/provider selection — are enforced in
// exactly one place instead of being re-derived per activity.

// llmCallSpec describes an LLM call to resolve through the standard path.
type llmCallSpec struct {
	UserID    string
	SessionID string

	// Selector picks the model via the registry. Ignored when an injected
	// resolver (tests) supplies its own driver/model, mirroring CallLLM.
	Selector models.ModelSelector

	// FallbackSelector, when set, is tried only if Selector cannot resolve at
	// all against the user's providers. This is a STRICT ladder, unlike a
	// multi-tag Selector: tag scoring SUMS the weights of every selector tag a
	// model is listed under, so [fast, moderate] prefers a model carrying both
	// over one carrying only fast — which is the opposite of "fast, or moderate
	// if there is no fast model". Use this when the second tier is a genuine
	// fallback rather than a secondary preference.
	FallbackSelector models.ModelSelector

	// Explicit per-call overrides. Zero values mean "use the resolved model's
	// default" — exactly like an unset workflow arg on a normal request.
	Temperature   *float64
	ThinkingLevel string
	MaxTokens     *int64
	WorkingDir    string

	// ForceToolChoice pins the request to a single named tool, making a tool
	// call the only thing the model can emit.
	//
	// Safe only for a ONE-SHOT request, never an agent loop: the pin applies to
	// every request in a turn, so a model given real tools alongside it could
	// never call them.
	ForceToolChoice string

	// TagPrefsReader, when set, layers the user's Settings → Model
	// preferences (model.tag_config.<tag>) under Selector/Temperature/
	// ThinkingLevel: node arg > model value > tag pref > tier/model default.
	// Only the chat-turn path sets it. Compaction and title generation pick
	// their own internal selectors ("cheap"/"fast" housekeeping tiers) and
	// must not inherit a user's per-tier temperature, thinking effort or
	// compaction threshold, nor be redirected to an expensive model_id the
	// user chose for conversation. Ignored when a resolver is injected.
	TagPrefsReader modelprefs.SettingsReader

	// Local, when set, lets a selector name a model served by one of the
	// user's daemons ("<name>@local", provider "local:<daemonID>"). Local
	// models never come from the registry: they are synthesized per request
	// from the daemons' published inventories.
	Local *LocalModelSpec
}

// LocalModelSpec is what resolving a local model needs beyond the selector.
// Exported so the dev probe can supply an in-process relay.
type LocalModelSpec struct {
	Directory local.Directory
	Transport local.TransportFactory
	// PreferDaemonID is the chat's worktree daemon, tried first when the
	// selector does not pin a daemon.
	PreferDaemonID string
	// Custom reaches user-configured endpoints (model_endpoints); nil leaves
	// them unavailable on this worker.
	Custom *local.CustomRoutes
}

// resolvedLLMCall bundles the driver plus the effective settings the shared
// request path computed for it.
type resolvedLLMCall struct {
	Driver llm.Driver

	// Model is the resolved model (probed from the driver when a resolver is
	// injected).
	Model models.Model

	// ModelID is the model ID handed to driver resolution, including the
	// @driver suffix when resolved via the registry (e.g.
	// "claude-4.6-sonnet@anthropic").
	ModelID string

	// Definition is the registry definition backing the model; nil when an
	// injected resolver supplied the model.
	Definition *models.ModelDefinition

	// ProviderDriver is the driver of the provider the registry selected to
	// serve this call (e.g. "codex", "anthropic"). Empty when an injected
	// resolver supplied the model. Context management derives the model's REAL
	// window from this provider, since a provider can serve a smaller window
	// than the model-wide capability (see models.EffectiveContextWindow).
	ProviderDriver string

	// ThinkingLevel is the effective, capability-reconciled thinking level.
	// Empty means the model cannot reason and the driver omits thinking.
	ThinkingLevel string

	// Temperature is the effective temperature (explicit override or model
	// default), nil when neither is set.
	Temperature *float64

	// TagCompactionThreshold is the tag preference's compaction threshold, 0
	// when none applied. The caller uses it only when neither the node arg
	// nor the model value set one.
	TagCompactionThreshold int32
}

// promptCeiling is the largest prompt this call may send: the context budget
// compaction, the pin cap, the trim backstop and the stored-token-count sanity
// check all derive from (models.PromptCeiling). def is nil when an injected
// resolver supplied model.
func promptCeiling(def *models.ModelDefinition, providerDriver string, model models.Model) int64 {
	if def != nil {
		return int64(models.ProviderPromptCeiling(def, providerDriver))
	}
	return int64(models.PromptCeiling(int(model.ContextWindow), int(model.DefaultMaxTokens)))
}

// resolveLLMCall resolves a spec into a ready-to-use driver the same way for
// every activity:
//   - registry resolution over the user's configured providers (the provider
//     is pinned via the id@driver preference, so selection is deterministic)
//   - model-default temperature and thinking level layered under explicit
//     overrides, then reconciled against the model's actual capabilities
//   - standard driver options (session, working directory, max tokens,
//     reasoning effort)
//
// When resolver is non-nil (injected, e.g. in tests) the registry is skipped
// and the model is probed from the resolver, matching CallLLM's behavior.
func resolveLLMCall(ctx context.Context, resolver drivers.DriverResolver, spec llmCallSpec) (*resolvedLLMCall, error) {
	var legacyModel models.Model
	var definition *models.ModelDefinition
	var modelIDForDriver string
	var providerDriver string
	var localTransport http.RoundTripper
	effectiveTemperature := spec.Temperature
	effectiveThinkingLevel := spec.ThinkingLevel
	var tagCompactionThreshold int32

	if resolver != nil {
		// Probe the injected resolver for its model. Explicit spec values are
		// used directly (no model defaults available), but are still
		// reconciled against the probed model's capabilities so non-reasoning
		// drivers don't receive unsupported reasoning options.
		probeDriver, probeErr := resolver(ctx, spec.UserID, nil)
		if probeErr != nil {
			return nil, fmt.Errorf("injected driver resolver failed: %w", probeErr)
		}
		legacyModel = probeDriver.Model()
		modelIDForDriver = string(legacyModel.ID)
		effectiveThinkingLevel = models.ReconcileThinkingLevel(
			models.ResolveThinkingCapability(models.ModelCapabilities{CanReason: legacyModel.CanReason}),
			effectiveThinkingLevel,
		)
	} else if local.IsSelector(spec.Selector) {
		var err error
		legacyModel, definition, modelIDForDriver, effectiveThinkingLevel, localTransport, err = resolveLocalModel(ctx, spec)
		if err != nil {
			return nil, err
		}
		providerDriver = string(local.Family)
	} else if pinned, pinnedSpec, ok := resolvePinnedLocalPref(ctx, spec); ok {
		legacyModel, definition, modelIDForDriver, effectiveThinkingLevel, localTransport = pinned.legacy, pinned.definition, pinned.modelID, pinned.thinkingLevel, pinned.transport
		providerDriver = string(local.Family)
		if effectiveTemperature == nil {
			effectiveTemperature = pinnedSpec.Temperature
		}
		if pinnedSpec.TagCompactionThreshold > 0 {
			tagCompactionThreshold = pinnedSpec.TagCompactionThreshold
		}
	} else {
		// Resolve model using the registry against the providers the user has
		// configured.
		availableDrivers := drivers.GetAvailableDrivers(ctx, spec.UserID)
		availableProviders := configuredProviderIDs(availableDrivers)

		registry := models.MustGetRegistry().WithAvailability(availableDrivers.Availability)
		resolve := registry.Resolve
		if len(spec.FallbackSelector.Tags) > 0 || spec.FallbackSelector.ID != "" {
			resolve = func(selector models.ModelSelector, providers []string) (*models.ResolvedModel, error) {
				return registry.ResolveWithFallback(selector, spec.FallbackSelector, providers)
			}
		}

		prefs := loadTagPrefs(ctx, spec, registry)
		var resolved *models.ResolvedModel
		if preferred := prefs.PreferredModel(registry, spec.Selector, availableProviders); preferred != nil {
			resolved = preferred
		} else {
			if prefs.ModelID != "" && !prefs.IsLocalModel() {
				logging.Warn("[LLMRequest] Ignoring tag preference model_id: not available for this user",
					"userID", spec.UserID, "tag", modelprefs.TagFor(spec.Selector), "modelID", prefs.ModelID)
			}
			var err error
			resolved, err = resolve(spec.Selector, availableProviders)
			if err != nil {
				return nil, resolutionError(err, availableDrivers)
			}
		}
		if effectiveTemperature == nil {
			effectiveTemperature = prefs.Temperature
		}
		if effectiveThinkingLevel == "" {
			effectiveThinkingLevel = prefs.ValidThinkingLevel()
		}
		if prefs.CompactionThreshold != nil && *prefs.CompactionThreshold > 0 {
			tagCompactionThreshold = *prefs.CompactionThreshold
		}

		resolvedDef := resolved.Definition
		definition = &resolvedDef

		// Apply model defaults for unset parameters.
		if effectiveTemperature == nil {
			effectiveTemperature = resolvedDef.DefaultTemperature
		}
		// Thinking level precedence, highest first:
		//   1. explicit node/preset/request thinking_level (spec.ThinkingLevel)
		//   2. the registry's answer: the winning tag entry's level (clamped to
		//      the model), or the model's capability default when chosen by id
		//      — see ModelRegistry.Resolve
		if effectiveThinkingLevel == "" {
			effectiveThinkingLevel = resolved.ThinkingLevel
		}
		if effectiveThinkingLevel != "" {
			tl := ThinkingLevel(effectiveThinkingLevel)
			if !tl.IsValid() {
				return nil, fmt.Errorf("invalid thinking_level: %s (must be one of: %s)", tl, strings.Join(models.KnownThinkingLevels, ", "))
			}
		}
		// Reconcile through the canonical model capability policy so stale
		// defaults on non-reasoning models disable thinking instead of
		// failing before a request can be made.
		effectiveThinkingLevel = models.ReconcileThinkingLevel(
			models.ResolveThinkingCapability(resolvedDef.Capabilities),
			effectiveThinkingLevel,
		)

		// Build the model ID string with driver suffix so driver resolution
		// uses the exact provider the registry picked (deterministic).
		modelIDForDriver = resolvedDef.ID
		providerDriver = resolved.Provider.Driver
		if resolved.Provider.Driver != "" {
			modelIDForDriver = resolvedDef.ID + "@" + resolved.Provider.Driver
		}

		// The model as the chosen provider serves it, so driver.Model() reports
		// that provider's window and max output, not the model-wide figures.
		legacyModel = resolvedDef.ToModel()
		legacyModel.ContextWindow = int64(models.EffectiveContextWindow(&resolvedDef, providerDriver))
		legacyModel.DefaultMaxTokens = int64(models.EffectiveMaxOutputTokens(&resolvedDef, providerDriver))
	}

	// Build preferences for driver selection.
	preferences := models.Preferences{
		{
			ModelID:     models.ModelID(modelIDForDriver),
			Temperature: effectiveTemperature,
		},
	}

	// Standard driver options shared by every request.
	driverOpts := []llm.DriverOption{
		llm.WithModel(legacyModel),
		llm.WithSessionID(spec.SessionID),
	}
	if spec.WorkingDir != "" {
		driverOpts = append(driverOpts, llm.WithWorkingDirectory(spec.WorkingDir))
	}
	if spec.MaxTokens != nil {
		driverOpts = append(driverOpts, llm.WithMaxTokens(*spec.MaxTokens))
	}
	if effectiveThinkingLevel != "" {
		driverOpts = append(driverOpts, llm.WithReasoningEffort(effectiveThinkingLevel))
	}
	if spec.ForceToolChoice != "" {
		driverOpts = append(driverOpts, llm.WithForceToolChoice(spec.ForceToolChoice))
	}

	var driver llm.Driver
	if localTransport != nil {
		driverOpts = append(driverOpts,
			llm.WithBaseURL(local.PlaceholderBaseURL),
			llm.WithTransport(localTransport),
			llm.WithMaxTokens(int64(definition.Capabilities.MaxOutputTokens)))
		if effectiveTemperature != nil {
			driverOpts = append(driverOpts, llm.WithTemperature(*effectiveTemperature))
		}
		if spec.MaxTokens != nil {
			driverOpts = append(driverOpts, llm.WithMaxTokens(*spec.MaxTokens))
		}
		var err error
		if driver, err = drivers.NewLocalDriver(legacyModel, driverOpts...); err != nil {
			return nil, fmt.Errorf("failed to get LLM driver: %w", err)
		}
	} else {
		resolve := drivers.GetDriver
		if resolver != nil {
			resolve = resolver
		}
		var err error
		if driver, err = resolve(ctx, spec.UserID, preferences, driverOpts...); err != nil {
			return nil, fmt.Errorf("failed to get LLM driver: %w", err)
		}
	}

	return &resolvedLLMCall{
		Driver:         driver,
		Model:          legacyModel,
		ModelID:        modelIDForDriver,
		Definition:     definition,
		ProviderDriver: providerDriver,
		ThinkingLevel:  effectiveThinkingLevel,
		Temperature:    effectiveTemperature,

		TagCompactionThreshold: tagCompactionThreshold,
	}, nil
}

type pinnedLocalPref struct {
	legacy        models.Model
	definition    *models.ModelDefinition
	modelID       string
	thinkingLevel string
	transport     http.RoundTripper
}

// pinnedLocalSpec carries the pref-derived values the local path needs.
type pinnedLocalSpec struct {
	Temperature            *float64
	TagCompactionThreshold int32
}

// resolvePinnedLocalPref applies a tag preference whose model_id is a local
// model. It reports ok=false (so the caller falls back to the tier) when
// there is no such preference or its machine cannot serve it right now.
func resolvePinnedLocalPref(ctx context.Context, spec llmCallSpec) (pinnedLocalPref, pinnedLocalSpec, bool) {
	prefs := loadTagPrefs(ctx, spec, nil)
	if !prefs.IsLocalModel() {
		return pinnedLocalPref{}, pinnedLocalSpec{}, false
	}
	localSpec := spec
	localSpec.Selector = prefs.LocalSelector()
	if localSpec.ThinkingLevel == "" {
		localSpec.ThinkingLevel = prefs.ValidThinkingLevel()
	}
	legacy, def, modelID, thinking, transport, err := resolveLocalModel(ctx, localSpec)
	if err != nil {
		logging.Warn("[LLMRequest] Ignoring tag preference model_id: local model unavailable; using the tier",
			"userID", spec.UserID, "tag", modelprefs.TagFor(spec.Selector), "modelID", prefs.ModelID, "error", err)
		return pinnedLocalPref{}, pinnedLocalSpec{}, false
	}
	out := pinnedLocalSpec{Temperature: prefs.Temperature}
	if prefs.CompactionThreshold != nil && *prefs.CompactionThreshold > 0 {
		out.TagCompactionThreshold = *prefs.CompactionThreshold
	}
	return pinnedLocalPref{legacy, def, modelID, thinking, transport}, out, true
}

// loadTagPrefs returns the user's preference for the selector's tier, or the
// zero value when there is none, the selector is id-based, no reader is
// wired, or the settings read fails (a preference must never block a call).
func loadTagPrefs(ctx context.Context, spec llmCallSpec, registry *models.ModelRegistry) modelprefs.TagPrefs {
	tag := modelprefs.TagFor(spec.Selector)
	if tag == "" || spec.TagPrefsReader == nil {
		return modelprefs.TagPrefs{}
	}
	all, err := modelprefs.LoadAll(ctx, spec.TagPrefsReader, spec.UserID)
	if err != nil {
		logging.Warn("[LLMRequest] Model tag preferences partially unreadable; affected tags use defaults",
			"userID", spec.UserID, "error", err)
	}
	return all[tag]
}

// resolveFailure is a model resolution that failed, worded for the user. Its
// cause stays in the chain, so a no-servable-provider failure remains
// errors.Is(drivererrors.ErrNoServableProvider) and the runtime fails the call
// once instead of retrying a resolution only the user can change.
type resolveFailure struct {
	msg   string
	cause error
}

func (e *resolveFailure) Error() string { return e.msg }
func (e *resolveFailure) Unwrap() error { return e.cause }

// resolutionError says why no provider can serve the request and what the user
// can do about it. A model pinned to one provider ("gpt-5.6-sol@codex", which
// is what picking a model in the composer stores) names that provider and why
// it cannot serve: not connected, or connected with a credential the provider
// rejected. Anything else names every rejected provider, since a rejected
// credential is the likeliest reason a configured user's tier came up empty.
func resolutionError(err error, available models.AvailableDrivers) error {
	var pinned *models.ProviderUnavailableError
	if errors.As(err, &pinned) {
		return &resolveFailure{msg: "failed to resolve model: " + pinned.Explain(available.Unavailable), cause: err}
	}
	msg := "failed to resolve model: " + err.Error()
	if len(available.Unavailable) > 0 {
		reasons := make([]string, 0, len(available.Unavailable))
		for _, reason := range available.Unavailable {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)
		msg += ". " + strings.Join(reasons, "; ") + ". Reconnect it in Settings → Providers, or connect another provider"
	} else {
		msg += ". Connect a provider that serves this model in Settings → Providers"
	}
	return &resolveFailure{msg: msg, cause: err}
}

// configuredProviderIDs returns the driver IDs the user has properly
// configured (API key drivers and local drivers with a BaseURL).
func configuredProviderIDs(availableDrivers models.AvailableDrivers) []string {
	providers := make([]string, 0, len(availableDrivers.Drivers))
	for driverID, driverConfig := range availableDrivers.Drivers {
		if driverConfig.IsConfigured() {
			providers = append(providers, string(driverID))
		}
	}
	return providers
}

// prepareHistoryForLLM applies the standard provider-safety transforms every
// outgoing request needs, in one place:
//
//  1. Trim history to fit the context window, accounting for system prompts
//     and tool definitions. The backstop threshold is derived from the model's
//     real context window (contextWindow) so it scales per-model and sits above
//     the compaction threshold; pass 0 when the window is unknown to fall back
//     to the fixed legacy threshold.
//  2. Tool-less requests: flatten tool_use/tool_result content blocks to plain
//     text. Providers (Anthropic directly and via LiteLLM, OpenAI, ...) reject
//     histories that carry tool-call blocks when the request has no tools
//     param, so any request sent without tools (the compaction summarization
//     call, a call_llm node with tools disabled) must not carry them.
//  3. Normalize internal roles (agent -> user) to API-compatible roles.
//
// TRIM BEFORE FLATTEN (load-bearing ordering): the trim's only mechanism for
// freeing real volume is trimLargeToolResults, which cuts ToolResult parts
// larger than 10k chars. Flattening first rewrites every ToolResult into a
// TextContent, so the trim would find nothing to cut, free nothing, and still
// report success — shipping an oversized request the provider rejects. Trimming
// first lets the backstop see the tool results it is designed to shrink; the
// flatten then runs on the already-trimmed history and still guarantees no
// tool-call blocks survive on a tool-less request.
func prepareHistoryForLLM(chatID string, history []message.Message, systemPrompts []string, availableTools []tools.Tool, contextWindow int64) []message.Message {
	if message.TrimMessagesToFitContextWindow(history, systemPrompts, wrapToolsForEstimation(availableTools), contextWindow) {
		logging.Debug("[LLMRequest] Trimmed history to fit context window",
			"chatID", chatID,
			"contextWindow", contextWindow)
	}
	if len(availableTools) == 0 {
		history = flattenToolContentToText(history)
	}
	return normalizeRolesForLLM(history)
}

// flattenToolContentToText rewrites a conversation into text-only messages
// suitable for a request that passes no tools.
//
// tool_use and tool_result content blocks are converted to human-readable text
// so the model still sees what tools were called and what they returned,
// without carrying tool-call content blocks that providers reject when no
// `tools=` param is set. Assistant tool calls collapse into the assistant
// turn; tool-result messages become user turns (the only non-system role that
// reliably accepts free text across providers).
func flattenToolContentToText(messages []message.Message) []message.Message {
	flattened := make([]message.Message, 0, len(messages))

	for _, msg := range messages {
		var sb strings.Builder
		for _, tc := range msg.TextContents() {
			if tc.Text != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n\n")
				}
				sb.WriteString(tc.Text)
			}
		}

		for _, call := range msg.ToolCalls() {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			fmt.Fprintf(&sb, "[Called tool %s with input: %s]", call.Name, call.Input)
		}

		role := msg.Role
		for _, result := range msg.ToolResults() {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			status := "result"
			if result.IsError {
				status = "error"
			}
			fmt.Fprintf(&sb, "[Tool %s %s: %s]", result.Name, status, result.Content)
			// Tool messages have no valid free-text role of their own; fold into the user turn.
			role = message.User
		}

		text := sb.String()
		if strings.TrimSpace(text) == "" {
			// Nothing summarizable (e.g. a bare finish marker); drop it.
			continue
		}

		flattened = append(flattened, message.Message{
			Role:  role,
			Parts: []message.ContentPart{message.TextContent{Text: text}},
		})
	}

	return flattened
}

// resolveLocalModel resolves a "<name>@local" selector against the inventories
// of the user's daemons. Failures are *local.UnavailableError, worded for the
// user; they are not API-key problems and are never reported as one.
func resolveLocalModel(ctx context.Context, spec llmCallSpec) (legacy models.Model, def *models.ModelDefinition, modelID, thinkingLevel string, transport http.RoundTripper, err error) {
	if spec.Local == nil || spec.Local.Directory == nil {
		return legacy, nil, "", "", nil, &local.UnavailableError{
			Model:  strings.TrimSuffix(spec.Selector.ID, "@"+local.IDSuffix),
			Reason: "local models are not reachable from this server",
		}
	}
	all, err := local.ListModels(ctx, spec.Local.Directory, spec.UserID)
	if err != nil {
		return legacy, nil, "", "", nil, fmt.Errorf("failed to list local models: %w", err)
	}
	picked, err := local.Resolve(all, spec.Selector, spec.Local.PreferDaemonID)
	if err != nil {
		return legacy, nil, "", "", nil, err
	}

	definition := picked.Definition
	// A local model thinks only when asked and only at a level its server
	// takes; no tag or capability default applies, since an unprompted level
	// can 400 on a model that offers none.
	if spec.ThinkingLevel != "" {
		thinkingLevel = models.ReconcileThinkingLevel(models.ResolveThinkingCapability(definition.Capabilities), spec.ThinkingLevel)
	}
	transport, err = local.TransportFor(ctx, spec.UserID, picked, spec.Local.Transport, spec.Local.Custom)
	if err != nil {
		return legacy, nil, "", "", nil, err
	}
	return definition.ToModel(), &definition, picked.CatalogID(), thinkingLevel, transport, nil
}

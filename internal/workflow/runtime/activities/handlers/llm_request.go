// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
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

	// Explicit per-call overrides. Zero values mean "use the resolved model's
	// default" — exactly like an unset workflow arg on a normal request.
	Temperature   *float64
	ThinkingLevel string
	MaxTokens     *int64
	WorkingDir    string
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
	effectiveTemperature := spec.Temperature
	effectiveThinkingLevel := spec.ThinkingLevel

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
	} else {
		// Resolve model using the registry against the providers the user has
		// configured.
		availableProviders := configuredProviderIDs(drivers.GetAvailableDrivers(ctx, spec.UserID))

		registry := models.MustGetRegistry()
		resolved, err := registry.Resolve(spec.Selector, availableProviders)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve model: %w. Please check your API key configuration in Settings", err)
		}

		resolvedDef := resolved.Definition
		definition = &resolvedDef

		// Apply model defaults for unset parameters.
		if effectiveTemperature == nil {
			effectiveTemperature = resolvedDef.DefaultTemperature
		}
		if effectiveThinkingLevel == "" {
			effectiveThinkingLevel = resolvedDef.DefaultThinkingLevel
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

		legacyModel = resolvedDef.ToModel()
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

	resolve := drivers.GetDriver
	if resolver != nil {
		resolve = resolver
	}
	driver, err := resolve(ctx, spec.UserID, preferences, driverOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to get LLM driver: %w", err)
	}

	return &resolvedLLMCall{
		Driver:         driver,
		Model:          legacyModel,
		ModelID:        modelIDForDriver,
		Definition:     definition,
		ProviderDriver: providerDriver,
		ThinkingLevel:  effectiveThinkingLevel,
		Temperature:    effectiveTemperature,
	}, nil
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
		logging.Info("[LLMRequest] Trimmed history to fit context window",
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

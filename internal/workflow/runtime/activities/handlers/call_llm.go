// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"

	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	cfgpkg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/skills/suggest"

	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"go.temporal.io/sdk/activity"
	"google.golang.org/protobuf/types/known/structpb"
)

// ============================================================================
// TYPES - Input/output types defined in types.go
// ============================================================================

// streamProcessingState holds mutable state during stream event processing
type streamProcessingState struct {
	blockStates       *BlockStreamState
	textParts         []string
	thinkingParts     []string // Extended thinking content parts
	thinkingSignature string   // Thinking signature for multi-turn preservation
	toolCalls         []message.ToolCall
	tokenCount        int    // Total tokens (prompt + response + context)
	workingDir        string // Working directory for trimming bash commands

	upstreamRequestID  string // Provider response header x-oai-request-id (if available)
	upstreamProxymanID string // Provider response header x-proxyman-id (if available)
}

// ============================================================================
// ACTIVITY IMPLEMENTATION
// ============================================================================

// CallLLMActivity implements the call_llm activity.
// This activity streams LLM response by content blocks and is separate from message saving
type CallLLMActivity struct {
	repo           db.Repository
	hub            streaming.StreamingHub
	toolsFactory   *tools.ToolsFactory
	configProvider cfgpkg.ConfigProvider
	driverResolver drivers.DriverResolver
	mcpBinder      toolexec.MCPContextBinder
}

// NewCallLLMActivity creates a new CallLLMActivity.
// The optional variadic arguments accept a cfgpkg.ConfigProvider and/or a drivers.DriverResolver.
func NewCallLLMActivity(repo db.Repository, hub streaming.StreamingHub, toolsFactory *tools.ToolsFactory, cfgProvider cfgpkg.ConfigProvider, resolver drivers.DriverResolver, mcpBinder toolexec.MCPContextBinder) *CallLLMActivity {
	return &CallLLMActivity{
		repo:           repo,
		hub:            hub,
		toolsFactory:   toolsFactory,
		configProvider: cfgProvider,
		driverResolver: resolver,
		mcpBinder:      mcpBinder,
	}
}

// resolveDriver returns the injected resolver or falls back to the default.
func (a *CallLLMActivity) resolveDriver(ctx context.Context, userID string, prefs models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) {
	if a.driverResolver != nil {
		return a.driverResolver(ctx, userID, prefs, opts...)
	}
	return drivers.GetDriver(ctx, userID, prefs, opts...)
}

// Name returns the activity name for registration
func (a *CallLLMActivity) Name() string {
	return "CallLLM"
}

// DisplayName returns human-readable name for UI
func (a *CallLLMActivity) DisplayName() string {
	return "Call LLM"
}

// Description returns what the activity does
func (a *CallLLMActivity) Description() string {
	return "Send a prompt to a language model and get a response with optional tool calls"
}

// Category returns the activity category for UI grouping
func (a *CallLLMActivity) Category() schema.ActivityCategory {
	return schema.CategoryMessageProcessing
}

// Execute is the Temporal-compatible entry point that takes ActivityInput.
func (a *CallLLMActivity) Execute(ctx context.Context, input ActivityInput) (*reliantv1.CallLLMOutput, error) {
	args := model.GetCallLLMArgs(input.Node)
	if args == nil {
		return nil, fmt.Errorf("expected call_llm node, got %s", model.NodeType(input.Node))
	}
	return a.executeCore(ctx, input.Runtime, args)
}

// executeCore contains PURE BUSINESS LOGIC only
func (a *CallLLMActivity) executeCore(ctx context.Context, rtx RuntimeContext, args *reliantv1.CallLLMArgs) (*reliantv1.CallLLMOutput, error) {
	logger := activity.GetLogger(ctx)

	// Thread path must be provided - no more "0" default
	thread := rtx.Thread
	if thread == "" {
		return nil, fmt.Errorf("thread is required")
	}

	// Validate thinking level if provided
	if model.CelStringIsSet(args.GetThinkingLevel()) && !model.CelStringIsExpr(args.GetThinkingLevel()) {
		tl := ThinkingLevel(model.CelStringValue(args.GetThinkingLevel()))
		if !tl.IsValid() {
			return nil, fmt.Errorf("invalid thinking_level: %s (must be one of: low, medium, high, xhigh)", tl)
		}
	}

	// Load chat configuration
	chat, err := a.repo.GetChat(ctx, rtx.ChatID)
	if err != nil {
		return nil, fmt.Errorf("failed to get chat: %w", err)
	}

	// Add userID to context for API key loading
	ctx = context.WithValue(ctx, auth.UserIDContextKey, chat.UserID)

	// Load conversation history
	history, err := a.loadConversationHistory(ctx, rtx.ChatID, thread, rtx.ContextSequence)
	if err != nil {
		return nil, fmt.Errorf("failed to load conversation history: %w", err)
	}

	if len(history) == 0 && len(args.GetMessages()) == 0 {
		return nil, fmt.Errorf("cannot call LLM with empty message history (chat=%s, thread=%s)", rtx.ChatID, thread)
	}

	// Defense-in-depth: warn if the conversation doesn't end with a user/tool message.
	// This helps diagnose thread routing mismatches where SendMessage saves to a
	// different thread than CallLLM reads from.
	if len(history) > 0 {
		if lastMsg := history[len(history)-1]; lastMsg.Role != message.User && lastMsg.Role != message.Tool && lastMsg.Role != message.Agent {
			logger.Warn("[CallLLM] Conversation does not end with user/tool message - possible thread mismatch",
				"chatID", rtx.ChatID,
				"thread", thread,
				"lastRole", lastMsg.Role,
				"lastMsgID", lastMsg.ID,
				"historyLen", len(history),
			)
		}
	}

	// Stream LLM response and collect data in memory
	output, err := a.streamLLMResponse(ctx, chat, thread, history, rtx, args)
	if err != nil {
		return nil, fmt.Errorf("failed to stream LLM response: %w", err)
	}

	logger.Info("[CallLLM] Completed",
		"chatID", rtx.ChatID,
		"toolCalls", len(output.ToolCalls),
		"tokenCount", output.TokenCount,
		"x_oai_request_id", strings.TrimSpace(output.GetUpstreamRequestId()),
		"x_proxyman_id", strings.TrimSpace(output.GetUpstreamProxymanId()))

	return output, nil
}

// loadConversationHistory loads all messages for the conversation.
// For branched chats, this automatically includes inherited parent messages.
//
// This delegates to LoadMessagesForLLM which handles:
//   - Fork chain traversal (inherits messages from parent threads)
//   - Compaction boundary detection
//   - DB-level orphan repair (Layer 2)
//   - In-memory orphan repair (Layer 3)
func (a *CallLLMActivity) loadConversationHistory(ctx context.Context, chatID string, thread string, explicitContextSeq *int) ([]message.Message, error) {
	return LoadMessagesForLLM(ctx, a.repo, chatID, thread, explicitContextSeq)
}

func (a *CallLLMActivity) getProjectConfig(ctx context.Context, project *db.Project) (*cfgpkg.Config, error) {
	if project == nil {
		return nil, fmt.Errorf("project is nil")
	}

	if a.configProvider == nil {
		return nil, fmt.Errorf("config provider is required")
	}

	cfg, err := a.configProvider.GetProjectConfig(ctx, cfgpkg.ProjectRef{
		ProjectID: project.ID,
	})
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func (a *CallLLMActivity) mcpRuntimeFromContext(ctx context.Context) tools.MCPRuntime {
	if a.mcpBinder == nil {
		return nil
	}
	toolCtx := rctx.NewToolContext(ctx, "", "", nil, nil)
	bound := a.mcpBinder.Bind(toolCtx)
	if bound == nil {
		return nil
	}
	return bound.MCP
}

// streamLLMResponse streams an LLM response to content_block_chunks (UI-only) and collects data in memory
func (a *CallLLMActivity) streamLLMResponse(ctx context.Context, chat *db.Chat, thread string, history []message.Message, rtx RuntimeContext, args *reliantv1.CallLLMArgs) (*reliantv1.CallLLMOutput, error) {
	// Extract options from runtime context
	// Check if this workflow was spawned by the spawn tool (prevents recursive spawn)
	spawnedBySpawnTool := rtx.SpawnedBy == "spawn_tool"

	// Tools enabled by default, disabled if explicitly set to false
	toolsEnabled := !model.CelBoolIsSet(args.GetTools()) || model.CelBoolValue(args.GetTools())

	// Resolve permission level (defaults to orchestrator for backward compatibility)
	permission := tools.PermissionOrchestrator
	if model.CelStringIsSet(args.GetPermission()) {
		permission = model.CelStringValue(args.GetPermission())
	}
	if chat != nil {
		tools.GetLoadedToolsStore().SetPermission(chat.ID, permission)
	}

	// Model must be provided via workflow inputs
	if !model.CelModelSelectorIsSet(args.GetModel()) {
		return nil, fmt.Errorf("model is required - must be provided via workflow inputs")
	}

	// Resolve model for the LLM call.
	// When a custom driver resolver is injected (e.g., in tests), skip normal
	// model resolution since the injected resolver provides its own driver/model.
	var legacyModel models.Model
	var modelIDWithDriver string

	if a.driverResolver != nil {
		// Probe the injected resolver for its model
		probeDriver, probeErr := a.driverResolver(ctx, chat.UserID, nil)
		if probeErr != nil {
			return nil, fmt.Errorf("injected driver resolver failed: %w", probeErr)
		}
		legacyModel = probeDriver.Model()
		modelIDWithDriver = string(legacyModel.ID)
		activity.GetLogger(ctx).Info("[CallLLM] Using injected driver resolver",
			"modelID", legacyModel.ID)
	} else {
		// Get available drivers to check which providers the user has configured
		availableDrivers := drivers.GetAvailableDrivers(ctx, chat.UserID)

		// Build availableProviders slice from configured drivers
		availableProviders := make([]string, 0, len(availableDrivers.Drivers))
		for driverID, driverConfig := range availableDrivers.Drivers {
			// Use IsConfigured() which handles both API key drivers and local drivers (BaseURL)
			if driverConfig.IsConfigured() {
				availableProviders = append(availableProviders, string(driverID))
			}
		}

		// Convert proto model selector to models.ModelSelector
		protoModel := model.CelModelSelectorValue(args.GetModel())
		modelSelector := models.ModelSelector{}
		if protoModel != nil {
			modelSelector.ID = protoModel.GetId()
			modelSelector.Tags = protoModel.GetTags()
			modelSelector.Providers = protoModel.GetProviders()
		}

		// Resolve model using the new registry
		registry := models.MustGetRegistry()
		resolved, err := registry.Resolve(modelSelector, availableProviders)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve model: %w. Please check your API key configuration in Settings", err)
		}

		activity.GetLogger(ctx).Info("[CallLLM] Resolved model",
			"selector", modelSelector,
			"modelID", resolved.Definition.ID,
			"provider", resolved.Provider.Driver)

		if model.CelStringIsSet(args.GetThinkingLevel()) && !model.CelStringIsExpr(args.GetThinkingLevel()) {
			thinkingLevel := model.CelStringValue(args.GetThinkingLevel())
			if !models.SupportsThinkingLevelForModelDriver(
				resolved.Definition.Capabilities.CanReason,
				resolved.Definition.ID,
				resolved.Provider.Driver,
				thinkingLevel,
			) {
				supported := models.SupportedThinkingLevelsForModelDriver(
					resolved.Definition.Capabilities.CanReason,
					resolved.Definition.ID,
					resolved.Provider.Driver,
				)
				return nil, fmt.Errorf(
					"thinking_level '%s' is not supported for model '%s' on driver '%s' (supported: %s)",
					thinkingLevel,
					resolved.Definition.ID,
					resolved.Provider.Driver,
					strings.Join(supported, ", "),
				)
			}
		}

		// Build the model ID string with driver suffix for the driver system
		modelIDWithDriver = resolved.Definition.ID
		if resolved.Provider.Driver != "" {
			modelIDWithDriver = resolved.Definition.ID + "@" + resolved.Provider.Driver
		}

		// Convert the resolved model definition to a legacy Model for the driver system
		legacyModel = resolved.Definition.ToModel()
	}

	// Build preferences for driver selection
	preferences := models.Preferences{
		{
			ModelID:     models.ModelID(modelIDWithDriver),
			Temperature: celDoubleValuePtr(args.GetTemperature()),
		},
	}

	// Load project and project config via provider abstraction
	project, err := a.repo.GetProject(ctx, chat.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to load project: %w", err)
	}
	projectPath := project.Path

	projectCfg, err := a.getProjectConfig(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("failed to load project config: %w", err)
	}
	if err := models.InitGlobalRegistryWithDiscovery(projectCfg.Models, local.DiscoverModels); err != nil {
		return nil, fmt.Errorf("failed to initialize model registry from project config: %w", err)
	}
	if projectCfg.Models != nil && projectCfg.Models.Providers.Local != nil {
		local.SetLocalConfig(projectCfg.Models.Providers.Local)
		if err := local.DiscoverAndRegisterModels(projectCfg.Models.Providers.Local.BaseURL); err != nil {
			activity.GetLogger(ctx).Warn("failed to register local models with driver", "error", err)
		}
	}

	// Load worktree path if applicable
	var worktreePath string
	if chat.WorktreeID != nil {
		worktree, err := a.repo.GetWorktree(ctx, *chat.WorktreeID)
		if err != nil {
			return nil, fmt.Errorf("failed to load worktree %s for chat %s: %w", *chat.WorktreeID, chat.ID, err)
		}
		worktreePath = worktree.Path
	}

	// Determine working directory (prefer worktree path over project path)
	workingDir := projectPath
	if worktreePath != "" {
		workingDir = worktreePath
	}

	// Create driver with the resolved model
	driverOpts := []llm.DriverOption{
		llm.WithModel(legacyModel),
		llm.WithSessionID(chat.ID),
		llm.WithWorkingDirectory(workingDir),
	}
	if model.CelIntIsSet(args.GetMaxTokens()) {
		driverOpts = append(driverOpts, llm.WithMaxTokens(model.CelIntValue(args.GetMaxTokens())))
	}
	if model.CelStringIsSet(args.GetThinkingLevel()) && !model.CelStringIsExpr(args.GetThinkingLevel()) {
		// Validation already done in Execute(), just pass it to the driver
		driverOpts = append(driverOpts, llm.WithReasoningEffort(model.CelStringValue(args.GetThinkingLevel())))
	}

	driver, err := a.resolveDriver(ctx, chat.UserID, preferences, driverOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to get LLM driver: %w", err)
	}

	// Get available tools (filtered by planning mode and tool_filter if provided)
	var availableTools []tools.Tool
	var spawnPresets []string // Track spawn presets for tool call validation
	if !toolsEnabled {
		activity.GetLogger(ctx).Info("[CallLLM] Tools disabled")
		availableTools = []tools.Tool{}
	} else {
		toolsResult := a.getAvailableToolsWithSpawn(ctx, chat, workingDir, projectCfg, model.CelStringListValue(args.GetToolFilter()), thread)
		availableTools = toolsResult.Tools

		// Emit warning to chat if MCP servers failed to load
		if len(toolsResult.FailedMCPServers) > 0 {
			a.writeStreamingDelta(ctx, chat.ID, "mcp_warning", map[string]interface{}{
				"message":        fmt.Sprintf("Some MCP servers failed to start: %v. Tools from these servers will be unavailable.", toolsResult.FailedMCPServers),
				"failed_servers": toolsResult.FailedMCPServers,
				"thread":         thread,
			})
		}

		// Add spawn tools from filter configs (spawn:workflow(presets) syntax)
		// Workflows spawned via spawn tool should NOT have access to spawn tool to prevent infinite recursion
		if !spawnedBySpawnTool {
			for _, spawnConfig := range toolsResult.SpawnConfigs {
				spawnTool := a.getSpawnToolFromFilterConfig(ctx, chat.ProjectID, spawnConfig)
				if spawnTool != nil {
					availableTools = append(availableTools, spawnTool)
					// Collect spawn presets for tool call validation
					spawnPresets = append(spawnPresets, spawnConfig.Presets...)
					activity.GetLogger(ctx).Info("[CallLLM] Added spawn tool from filter",
						"workflow", spawnConfig.Workflow,
						"presets", spawnConfig.Presets)
				}
			}
		} else {
			activity.GetLogger(ctx).Info("[CallLLM] Skipping spawn tool for spawn-spawned workflow", "thread", thread)
		}

	}

	// Add custom response tool defined in the workflow
	// This is outside the tools check because response tools are for structured output,
	// not general tool calling. tools: false should disable MCP/regular tools, not response tools.
	if rt := args.GetResponseTool(); rt != nil {
		// Convert proto V2ResponseTool to tools.ResponseToolDefinition
		var schema map[string]interface{}
		if rt.GetSchema() != nil {
			schema = rt.GetSchema().AsMap()
		}
		rtName := model.CelStringValue(rt.GetName())
		rtDesc := model.CelStringValue(rt.GetDescription())
		responseTool := tools.NewResponseTool(tools.ResponseToolDefinition{
			Name:        rtName,
			Description: rtDesc,
			Schema:      schema,
		})
		availableTools = append(availableTools, responseTool)
		activity.GetLogger(ctx).Debug("[CallLLM] Added response tool", "name", rtName)
	}

	// Generate system prompts
	// Always include base prompts for the driver (claude-code requires specific prompts for sk-ant-oat keys)

	systemPrompts := a.getSystemPrompts(
		chat,
		projectPath,
		worktreePath,
		projectCfg,
		celStringValuePtr(args.GetSystemPrompt()),
	)

	// Announce deferred tools (available via load_tool but not yet loaded)
	if toolsEnabled && len(availableTools) > 0 {
		currentToolNames := make([]string, len(availableTools))
		for i, t := range availableTools {
			currentToolNames[i] = t.Name()
		}
		announcement := tools.FormatDeferredToolsAnnouncement(chat.ID, permission, currentToolNames)
		if announcement != "" {
			systemPrompts = append(systemPrompts, announcement)
		}
	}

	// On Temporal retry (attempt > 1), inject a hidden system reminder so the LLM
	// knows the previous attempt failed and can try a different approach.
	attempt := int32(1)
	func() {
		defer func() { _ = recover() }() // safe outside activity context (tests)
		attempt = activity.GetInfo(ctx).Attempt
	}()
	if attempt > 1 {
		systemPrompts = injectRetryHint(systemPrompts, attempt)
		activity.GetLogger(ctx).Warn("[CallLLM] Injecting retry hint into system prompt",
			"chatID", chat.ID,
			"attempt", attempt)
	}

	// Initialize in-memory state for collecting response data
	streamState := &streamProcessingState{
		blockStates:       NewBlockStreamState(),
		textParts:         []string{},
		thinkingParts:     []string{},
		thinkingSignature: "",
		toolCalls:         []message.ToolCall{},
		tokenCount:        0,
		workingDir:        workingDir,
	}

	// Setup cancellation context
	// Temporal's signal-based activity cancellation (workflow.WithCancel + CancelFunc)
	// propagates cancellation through the context when the workflow is paused/cancelled.
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()

	// Append injected messages to history (for ad-hoc LLM calls like filtering)
	if len(args.GetMessages()) > 0 {
		for _, msg := range args.GetMessages() {
			injectedMsg := message.Message{
				Role: message.MessageRole(msg.GetRole()),
			}
			if msg.GetContent() != "" {
				injectedMsg.Parts = []message.ContentPart{message.TextContent{Text: msg.GetContent()}}
			}
			if msg.GetToolResult() != nil {
				tr := msg.GetToolResult()
				injectedMsg.Parts = append(injectedMsg.Parts, message.ToolResult{
					ToolCallID: tr.GetToolCallId(),
					Name:       tr.GetName(),
					Content:    tr.GetContent(),
					IsError:    tr.GetIsError(),
				})
			}
			// ToolCalls for assistant messages (e.g., showing what tools were requested)
			for _, tc := range msg.GetToolCalls() {
				injectedMsg.Parts = append(injectedMsg.Parts, message.ToolCall{
					ID:    tc.GetId(),
					Name:  tc.GetName(),
					Input: tc.GetInput(),
				})
			}
			history = append(history, injectedMsg)
		}
		activity.GetLogger(ctx).Info("[CallLLM] Appended injected messages",
			"count", len(args.GetMessages()))
	}

	// Trim history if it would exceed context window limits
	// This prevents API errors from context overflow by accounting for:
	// - Message content tokens
	// - System prompt tokens
	// - Tool definition tokens (name, description, parameter schema)
	if err := validateToolNamesForLLMRequest(availableTools); err != nil {
		activity.GetLogger(ctx).Error("[CallLLM] Tool name invariant violation before LLM request",
			"chatID", chat.ID,
			"thread", thread,
			"error", err,
			"toolNames", summarizeToolNamesForLogging(availableTools))
		return nil, fmt.Errorf("tool name invariant violation: %w", err)
	}

	toolDefs := wrapToolsForEstimation(availableTools)
	if message.TrimMessagesToFitContextWithFullEstimate(history, systemPrompts, toolDefs) {
		activity.GetLogger(ctx).Info("[CallLLM] Trimmed history to fit context window",
			"chatID", chat.ID)
	}

	// Normalize message roles before sending to LLM driver
	// Warning, Info, and Agent roles are converted to User for API compatibility
	history = normalizeRolesForLLM(history)

	// Inject skill suggestions into the latest user message based on token matching.
	// Skills come from the synced project config — no filesystem access.
	if projectCfg != nil && len(projectCfg.Skills) > 0 {
		if latestUserText := getLatestUserMessageText(history); latestUserText != "" {
			suggestions := suggest.Suggest(projectCfg.Skills, latestUserText, 5)
			if len(suggestions) > 0 {
				reminder := buildSkillSuggestionReminder(suggestions)
				injectReminderIntoLastUserMessage(history, reminder)
				activity.GetLogger(ctx).Debug("[CallLLM] Injected skill suggestions",
					"chatID", chat.ID,
					"count", len(suggestions))
			}
		}
	}

	toolNameDebug := summarizeToolNamesForLogging(availableTools)
	activity.GetLogger(ctx).Info("[CallLLM] Starting stream",
		"chatID", chat.ID,
		"thread", thread,
		"contextSequence", rtx.ContextSequence,
		"historyMessages", len(history),
		"systemPrompts", len(systemPrompts),
		"tools", len(availableTools),
		"toolNames", toolNameDebug,
	)

	// Defense-in-depth: verify conversation ends with user/tool message.
	// This catches thread routing mismatches before they hit the API.
	if len(history) > 0 {
		lastMsg := history[len(history)-1]
		if lastMsg.Role == message.Assistant {
			return nil, fmt.Errorf(
				"conversation history ends with assistant message after all transformations (chat=%s, thread=%s, last_msg_id=%s, msg_count=%d) — this usually means the user message was saved to a different thread than CallLLM reads from",
				chat.ID, thread, lastMsg.ID, len(history),
			)
		}
	}

	// Stream response with cancellation support
	llmCallStart := time.Now()
	eventChan := driver.StreamResponse(streamCtx, systemPrompts, history, availableTools)
	var streamErr error

streamLoop:
	for {
		select {
		case <-streamCtx.Done():
			// Context cancelled (either Temporal or application-level)
			activity.GetLogger(ctx).Info("[CallLLM] Streaming cancelled",
				"chatID", chat.ID,
				"reason", streamCtx.Err())

			// Notify UI that streaming was cancelled so it can update the UI accordingly
			// Use the original ctx (not streamCtx) since streamCtx is already cancelled
			a.writeStreamingDelta(ctx, chat.ID, "stream_cancelled", map[string]interface{}{
				"reason": "user_cancelled",
				"thread": thread,
			})

			streamErr = errors.New("streaming cancelled by user")
			break streamLoop

		case event, ok := <-eventChan:
			if !ok {
				// Channel closed, stream complete
				break streamLoop
			}

			// CRITICAL: Check for cancellation before processing each event
			// This is necessary because Go's select is non-deterministic, and when
			// events are continuously available, the Done() case may never be selected.
			// This check ensures we respond to cancellation within ~100ms even during
			// continuous streaming.
			select {
			case <-streamCtx.Done():
				activity.GetLogger(ctx).Info("[CallLLM] Streaming cancelled during event processing",
					"chatID", chat.ID,
					"reason", streamCtx.Err())
				a.writeStreamingDelta(ctx, chat.ID, "stream_cancelled", map[string]interface{}{
					"reason": "user_cancelled",
					"thread": thread,
				})
				streamErr = errors.New("streaming cancelled by user")
				break streamLoop
			default:
				// Not cancelled, continue processing
			}

			// Record heartbeat during streaming
			func() {
				defer func() {
					// Recover from panic if not in activity context
					_ = recover()
				}()
				activity.RecordHeartbeat(ctx, "streaming")
			}()

			// Process event through handler methods
			if err := a.processStreamEvent(ctx, chat.ID, thread, event, streamState); err != nil {
				streamErr = err
				break streamLoop
			}
		}
	}

	// Track LLM call completion for analytics
	llmLatencyMs := time.Since(llmCallStart).Milliseconds()
	a.trackLLMCallCompleted(ctx, chat, driver, llmLatencyMs, streamState.tokenCount, streamErr)

	if streamState.upstreamRequestID != "" || streamState.upstreamProxymanID != "" {
		activity.GetLogger(ctx).Info("[CallLLM] Upstream correlation",
			"chatID", chat.ID,
			"thread", thread,
			"x_oai_request_id", streamState.upstreamRequestID,
			"x_proxyman_id", streamState.upstreamProxymanID)
	}

	// Handle stream error
	if streamErr != nil {
		return nil, streamErr
	}

	// Combine all text parts
	responseText := strings.Join(streamState.textParts, "")

	// Combine all thinking parts
	thinkingText := strings.Join(streamState.thinkingParts, "")

	toolCalls := streamState.toolCalls

	// Attach available tools/presets to each tool call for validation in ExecuteTools.
	// This prevents the LLM from hallucinating tools/presets that weren't in its allowed set.
	if len(availableTools) > 0 {
		// Extract tool names from available tools
		availableToolNames := make([]string, len(availableTools))
		for i, t := range availableTools {
			availableToolNames[i] = t.Name()
		}

		// Attach to each tool call
		for i := range toolCalls {
			toolCalls[i].AvailableTools = availableToolNames
			// Only attach spawn presets to spawn tool calls
			if toolCalls[i].Name == "spawn" && len(spawnPresets) > 0 {
				toolCalls[i].AvailablePresets = spawnPresets
			}
		}
	}

	output := &reliantv1.CallLLMOutput{
		ResponseText:       responseText,
		ToolCalls:          messageToolCallsToProto(toolCalls),
		TokenCount:         int32(streamState.tokenCount),
		UpstreamRequestId:  streamState.upstreamRequestID,
		UpstreamProxymanId: streamState.upstreamProxymanID,
		Thinking: &reliantv1.ThinkingOutput{
			Content:   thinkingText,
			Signature: streamState.thinkingSignature,
		},
		Message: &reliantv1.MessageOutput{
			Role: "assistant",
			Text: responseText,
		},
	}

	// When a response_tool is configured, the LLM returns structured data as a
	// tool call rather than text. Extract the response tool's input into
	// ResponseData (structured) and ResponseText (raw JSON string) so consumers
	// can use whichever form they need.
	if rt := args.GetResponseTool(); rt != nil {
		rtName := model.CelStringValue(rt.GetName())
		for _, tc := range toolCalls {
			if tc.Name == rtName {
				output.ResponseText = tc.Input
				output.Message.Text = tc.Input
				var parsed map[string]interface{}
				if err := json.Unmarshal([]byte(tc.Input), &parsed); err == nil {
					if s, err := structpb.NewStruct(parsed); err == nil {
						output.ResponseData = s
					}
				}
				break
			}
		}
	}

	return output, nil
}

// toolsWithSpawnResult holds the result of getAvailableToolsWithSpawn
type toolsWithSpawnResult struct {
	Tools            []tools.Tool
	SpawnConfigs     []tools.SpawnFilterConfig
	FailedMCPServers []string // Names of MCP servers that failed to load
}

func scopedToolsFactoryForProject(baseFactory *tools.ToolsFactory, scopePath string) *tools.ToolsFactory {
	if baseFactory == nil {
		return nil
	}
	return baseFactory.WithMCPProjectPath(scopePath)
}

func summarizeToolNamesForLogging(availableTools []tools.Tool) []string {
	summaries := make([]string, 0, len(availableTools))
	for index, availableTool := range availableTools {
		if availableTool == nil {
			summaries = append(summaries, fmt.Sprintf("[%d] <nil>", index))
			continue
		}
		name := availableTool.Name()
		summary := fmt.Sprintf("[%d] %s (len=%d)", index, name, len(name))
		if strings.Contains(name, "::") || strings.Contains(name, "/") || len(name) > 64 {
			summary += " [suspicious]"
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func validateToolNamesForLLMRequest(availableTools []tools.Tool) error {
	for index, availableTool := range availableTools {
		if availableTool == nil {
			continue
		}
		toolName := availableTool.Name()
		if strings.Contains(toolName, "::") || strings.Contains(toolName, "/") {
			return fmt.Errorf("invalid tool name at index %d: %q contains forbidden scoped delimiters", index, toolName)
		}
		if len(toolName) > 64 {
			return fmt.Errorf("invalid tool name at index %d: %q exceeds max length 64", index, toolName)
		}
	}
	return nil
}

// getAvailableToolsWithSpawn returns available tools and spawn configurations from the filter.
// Spawn configs are extracted from spawn:workflow(presets) syntax in the filter.
// Dynamically loaded tools (via load_tool) are automatically included.
func (a *CallLLMActivity) getAvailableToolsWithSpawn(ctx context.Context, chat *db.Chat, scopePath string, projectCfg *cfgpkg.Config, toolFilter []string, _ string) toolsWithSpawnResult {
	if a.toolsFactory == nil {
		return toolsWithSpawnResult{}
	}

	projectScopedToolsFactory := scopedToolsFactoryForProject(a.toolsFactory, scopePath)
	if projectScopedToolsFactory == nil {
		return toolsWithSpawnResult{}
	}

	// Inject skills loaded via the project config so the skill tool never
	// touches the filesystem on the server side.
	if projectCfg != nil && len(projectCfg.Skills) > 0 {
		projectScopedToolsFactory = projectScopedToolsFactory.WithSkills(projectCfg.Skills)
	}

	logInfo := func(msg string, keyvals ...interface{}) {
		if !activity.IsActivity(ctx) {
			return
		}
		activity.GetLogger(ctx).Info(msg, keyvals...)
	}
	logWarn := func(msg string, keyvals ...interface{}) {
		if !activity.IsActivity(ctx) {
			return
		}
		activity.GetLogger(ctx).Warn(msg, keyvals...)
	}
	logDebug := func(msg string, keyvals ...interface{}) {
		if !activity.IsActivity(ctx) {
			return
		}
		activity.GetLogger(ctx).Debug(msg, keyvals...)
	}

	// Ensure MCP servers for this project are loaded before getting MCP tools.
	// Execution-time MCP binding for actual tool runs happens at the executor boundary.
	var failedMCPServers []string
	toolRuntime := a.mcpRuntimeFromContext(ctx)
	if toolRuntime != nil {
		result := toolRuntime.EnsureProjectServersLoaded(ctx, scopePath)
		if result.HasFailures() {
			failedMCPServers = result.FailedServers
			logWarn("Some MCP servers failed to load (tools from these servers will be unavailable)",
				"scopePath", scopePath,
				"failedServers", result.FailedServers)
		}
	}

	// Get MCP tool names for filter expansion
	mcpTools := projectScopedToolsFactory.GetMCPTools(toolRuntime)
	mcpToolNames := make([]string, len(mcpTools))
	for i, t := range mcpTools {
		mcpToolNames[i] = t.Name()
	}

	// Expand tool filter with spawn support
	filterResult := tools.ExpandToolFilterWithSpawn(toolFilter, mcpToolNames)

	// Include dynamically loaded tools (via load_tool)
	if chat != nil {
		loadedTools := tools.GetLoadedToolsStore().Get(chat.ID)
		if len(loadedTools) > 0 {
			filterResult.ToolNames = append(filterResult.ToolNames, loadedTools...)
			logDebug("[CallLLM] Including dynamically loaded tools",
				"chatID", chat.ID,
				"loadedTools", loadedTools)
		}
	}

	// Log expanded MCP tools specifically for debugging tag:mcp filtering
	var expandedMCPTools []string
	for _, name := range filterResult.ToolNames {
		if strings.HasPrefix(name, "mcp__") {
			expandedMCPTools = append(expandedMCPTools, name)
		}
	}
	logInfo("[CallLLM] Tool filter expansion",
		"input_filter", toolFilter,
		"expanded_total", len(filterResult.ToolNames),
		"expanded_mcp_count", len(expandedMCPTools),
		"expanded_mcp_tools", expandedMCPTools,
		"spawnConfigs", len(filterResult.SpawnConfigs),
		"available_mcp_tools", len(mcpToolNames))

	// Build filter set for O(1) lookup
	filterSet := make(map[string]bool, len(filterResult.ToolNames))
	for _, name := range filterResult.ToolNames {
		filterSet[name] = true
	}

	// Build tools list from registry
	registry := tools.GetToolRegistry()
	toolsList := make([]tools.Tool, 0, len(filterResult.ToolNames))

	for _, def := range registry {
		if filterSet[def.Name] {
			tool := def.Factory(projectScopedToolsFactory)
			if tool != nil {
				toolsList = append(toolsList, tool)
				delete(filterSet, def.Name) // Mark as found
			}
		}
	}

	// Add MCP tools that match the filter
	for _, mcpTool := range mcpTools {
		if filterSet[mcpTool.Name()] {
			toolsList = append(toolsList, mcpTool)
			delete(filterSet, mcpTool.Name()) // Mark as found
		}
	}

	// Warn about tools in filter that weren't found
	if len(filterSet) > 0 {
		unfound := make([]string, 0, len(filterSet))
		for name := range filterSet {
			unfound = append(unfound, name)
		}
		logWarn("[CallLLM] Tools in filter not found",
			"tools", unfound)
	}

	sort.Slice(toolsList, func(i, j int) bool {
		return toolsList[i].Name() < toolsList[j].Name()
	})

	logDebug("[CallLLM] Available tools",
		"count", len(toolsList),
		"hasCustomFilter", len(toolFilter) > 0)

	return toolsWithSpawnResult{
		Tools:            toolsList,
		SpawnConfigs:     filterResult.SpawnConfigs,
		FailedMCPServers: failedMCPServers,
	}
}

// getSpawnToolFromFilterConfig creates a spawn tool from a SpawnFilterConfig.
// Used when spawn configurations come from tool_filter syntax.
func (a *CallLLMActivity) getSpawnToolFromFilterConfig(ctx context.Context, projectID string, config tools.SpawnFilterConfig) tools.Tool {
	if len(config.Presets) == 0 {
		return nil
	}

	// Convert SpawnFilterConfig to SpawnConfig
	spawnConfig := &SpawnConfig{
		Workflow: config.Workflow,
		Presets:  config.Presets,
	}

	return a.getSpawnTool(ctx, projectID, spawnConfig)
}

// getSpawnTool returns a spawn tool for orchestration workflows.
// The spawn tool allows the LLM to delegate tasks to child workflows with specific presets.
// config contains the workflow to spawn and available presets.
func (a *CallLLMActivity) getSpawnTool(ctx context.Context, projectID string, config *SpawnConfig) tools.Tool {
	if config == nil || len(config.Presets) == 0 {
		return nil
	}

	// Load preset descriptions from stored config (synced by daemon) + builtins
	var storedPresets []cfgpkg.StoredPreset
	if projectID != "" && a.repo != nil {
		record, err := a.repo.GetProjectConfigRecord(ctx, projectID)
		if err == nil {
			storedPresets, _ = cfgpkg.ParseStoredPresets(record.ProjectPresetsJSON)
		}
	}

	var presetDescriptions []string
	for _, presetName := range config.Presets {
		var p *preset.Preset

		// Try stored project presets
		sp := cfgpkg.FindStoredPresetByName(storedPresets, presetName)
		if sp != nil {
			p, _ = preset.ParsePreset([]byte(sp.YAMLContent), presetName)
		}

		// Try builtin presets
		if p == nil {
			data, err := builtin.BuiltinPresetsFS.ReadFile("presets/" + presetName + ".yaml")
			if err == nil {
				p, _ = preset.ParsePreset(data, presetName)
			}
		}

		if p == nil {
			presetDescriptions = append(presetDescriptions, fmt.Sprintf("- %s", presetName))
		} else {
			desc := p.Description
			if desc == "" {
				desc = presetName
			}
			presetDescriptions = append(presetDescriptions, fmt.Sprintf("- %s: %s", presetName, desc))
		}
	}

	presetList := strings.Join(presetDescriptions, "\n")

	description := fmt.Sprintf(`Spawn a sub-workflow to delegate tasks. The spawned workflow runs in a separate thread and its result will be returned to you.

Available presets:
%s

Parameters:
- preset: REQUIRED - Name of the preset to use for the spawned workflow
- prompt: REQUIRED - Detailed task description for the spawned workflow
- title: Optional - Human-readable title for the spawned thread (defaults to preset name)
- agent_id: Optional - Agent ID to resume existing conversation`, presetList)

	return tools.NewSchemaOnlyTool(
		"spawn",
		description,
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"preset": map[string]interface{}{
					"type":        "string",
					"enum":        config.Presets,
					"description": "Name of the preset to use for the spawned workflow",
				},
				"prompt": map[string]interface{}{
					"type":        "string",
					"description": "Detailed task description for the spawned workflow",
				},
				"title": map[string]interface{}{
					"type":        "string",
					"description": "Optional - Human-readable title for the spawned thread (defaults to preset name)",
				},
				"agent_id": map[string]interface{}{
					"type":        "string",
					"description": "Optional - Agent ID to resume existing conversation",
				},
			},
			"required": []string{"preset", "prompt"},
		},
	)
}

// getSystemPrompts generates system prompts for the LLM
func (a *CallLLMActivity) getSystemPrompts(
	chat *db.Chat,
	projectPath string,
	worktreePath string,
	projectCfg *cfgpkg.Config,
	systemPrompt *string,
) []string {
	// Add project context
	workingDir := projectPath
	if worktreePath != "" {
		workingDir = worktreePath
	}
	var bb strings.Builder
	bb.WriteString("You are Reliant, a world class Software Engineer with advanced reasoning and capabilities. Note: it is very likely you are working in parallel with other agents, potentially in the same directory, or across multiple git worktrees. Please be careful of other's work.")

	storedMemories := formatStoredMemories(projectCfg)
	if storedMemories != "" {
		bb.WriteString(storedMemories)
	}

	if workingDir != "" {
		bb.WriteString("\n\nIMPORTANT: You are working in a git worktree at: ")
		bb.WriteString(workingDir)
	}

	// Append skill announcement if any skills are available. Skills are
	// synced from the daemon via project config — this is pure in-memory.
	if projectCfg != nil {
		if announcement := tools.SkillsAnnouncement(projectCfg.Skills); announcement != "" {
			bb.WriteString(announcement)
		}
	}

	prompts := []string{bb.String()}
	// Add custom system prompt if provided
	if systemPrompt != nil && *systemPrompt != "" {
		prompts = append(prompts, *systemPrompt)
	}

	return prompts
}

// ============================================================================
// TOOL ESTIMATION HELPER
// ============================================================================

// toolDefWrapper wraps a tools.Tool to implement message.ToolDefinition
type toolDefWrapper struct {
	tool tools.Tool
}

func (w toolDefWrapper) Name() string {
	return w.tool.Name()
}

func (w toolDefWrapper) Description() string {
	return w.tool.Description()
}

func (w toolDefWrapper) ParamSchemaJSON() []byte {
	schema := w.tool.ParamSchema()
	if schema == nil {
		return nil
	}
	// Marshal the schema to JSON for token estimation
	data, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	return data
}

// wrapToolsForEstimation converts tools.Tool slice to message.ToolDefinition slice
// for accurate token estimation
func wrapToolsForEstimation(toolsList []tools.Tool) []message.ToolDefinition {
	result := make([]message.ToolDefinition, len(toolsList))
	for i, t := range toolsList {
		result[i] = toolDefWrapper{tool: t}
	}
	return result
}

// ============================================================================
// BLOCK STREAM STATE HELPER
// ============================================================================

// BlockStreamState tracks the state of each content block as it streams
type BlockStreamState struct {
	position              int
	chunkIndex            int
	currentTextBlock      string
	currentToolBlock      string
	thinkingBlockPosition int // Track the position of the thinking block (-1 if none)
}

// NewBlockStreamState creates a new block stream state tracker
func NewBlockStreamState() *BlockStreamState {
	return &BlockStreamState{
		position:              0,
		chunkIndex:            0,
		thinkingBlockPosition: -1, // No thinking block yet
	}
}

// NextPosition returns the next block position and increments the counter
func (s *BlockStreamState) NextPosition() int {
	pos := s.position
	s.position++
	s.chunkIndex = 0 // Reset chunk index for new block
	return pos
}

// CurrentPosition returns the current block position without incrementing
func (s *BlockStreamState) CurrentPosition() int {
	return s.position
}

// NextChunkIndex returns the next chunk index and increments the counter
func (s *BlockStreamState) NextChunkIndex() int {
	idx := s.chunkIndex
	s.chunkIndex++
	return idx
}

// StartTextBlock starts tracking a new text block
func (s *BlockStreamState) StartTextBlock(blockID string) {
	s.currentTextBlock = blockID
	s.NextPosition()
}

// EndTextBlock ends tracking the current text block
func (s *BlockStreamState) EndTextBlock() {
	s.currentTextBlock = ""
}

// StartToolBlock starts tracking a new tool block
func (s *BlockStreamState) StartToolBlock(blockID string) {
	s.currentToolBlock = blockID
	s.NextPosition()
}

// EndToolBlock ends tracking the current tool block
func (s *BlockStreamState) EndToolBlock() {
	s.currentToolBlock = ""
}

// StartThinkingBlock starts tracking a thinking block and returns its position
func (s *BlockStreamState) StartThinkingBlock() int {
	if s.thinkingBlockPosition < 0 {
		s.thinkingBlockPosition = s.NextPosition()
	}
	return s.thinkingBlockPosition
}

// GetThinkingBlockPosition returns the thinking block position, or -1 if none
func (s *BlockStreamState) GetThinkingBlockPosition() int {
	return s.thinkingBlockPosition
}

// ============================================================================
// STREAM EVENT HANDLERS
// ============================================================================

// processStreamEvent dispatches stream events to their appropriate handlers
func (a *CallLLMActivity) processStreamEvent(ctx context.Context, chatID string, thread string, event llm.DriverEvent, state *streamProcessingState) error {
	switch event.Type {
	case llm.EventContentStart:
		a.handleContentStart(ctx, chatID, thread, event, state)
	case llm.EventContentDelta:
		a.handleContentDelta(ctx, chatID, thread, event, state)
	case llm.EventToolUseStart:
		a.handleToolUseStart(ctx, chatID, thread, event, state)
	case llm.EventToolUseDelta:
		// Currently unused - for streaming partial tool input if needed
	case llm.EventThinkingDelta:
		a.handleThinkingDelta(ctx, chatID, thread, event, state)
	case llm.EventToolUseStop:
		a.handleToolUseStop(ctx, chatID, thread, state)
	case llm.EventComplete:
		a.handleComplete(ctx, event, state)
	case llm.EventError:
		return a.handleError(event)
	}
	return nil
}

// handleContentStart handles the start of a new content block
func (a *CallLLMActivity) handleContentStart(ctx context.Context, chatID string, thread string, _ llm.DriverEvent, state *streamProcessingState) {
	// Start tracking a new text block
	state.blockStates.StartTextBlock("")
	state.textParts = append(state.textParts, "")

	// Dual-write: Write content_block_start delta to chat_updates
	a.writeStreamingDelta(ctx, chatID, "content_block_start", map[string]interface{}{
		"block_index": len(state.textParts) - 1,
		"block_type":  "text",
		"thread":      thread,
	})
}

// handleContentDelta handles content delta events (text streaming)
func (a *CallLLMActivity) handleContentDelta(ctx context.Context, chatID string, thread string, event llm.DriverEvent, state *streamProcessingState) {
	// Append to current text block
	currentBlockIndex := len(state.textParts) - 1
	if currentBlockIndex < 0 {
		// No current text block - start a new one
		state.blockStates.StartTextBlock("")
		state.textParts = append(state.textParts, "")
		currentBlockIndex = 0
	}

	// Append to in-memory text
	state.textParts[currentBlockIndex] += event.Content

	// Dual-write: Write content_block_delta to chat_updates
	a.writeStreamingDelta(ctx, chatID, "content_block_delta", map[string]interface{}{
		"block_index": currentBlockIndex,
		"delta":       event.Content,
		"thread":      thread,
	})
}

// handleThinkingDelta handles thinking delta events (extended thinking streaming)
func (a *CallLLMActivity) handleThinkingDelta(ctx context.Context, chatID string, thread string, event llm.DriverEvent, state *streamProcessingState) {
	if event.Thinking == "" {
		return
	}

	// Start a new thinking block if needed
	thinkingPos := state.blockStates.GetThinkingBlockPosition()
	if len(state.thinkingParts) == 0 {
		state.thinkingParts = append(state.thinkingParts, "")
		// Track thinking block position properly
		thinkingPos = state.blockStates.StartThinkingBlock()
		// Dual-write: Write thinking_block_start delta to chat_updates
		a.writeStreamingDelta(ctx, chatID, "thinking_block_start", map[string]interface{}{
			"block_index": thinkingPos,
			"block_type":  "thinking",
			"thread":      thread,
		})
	}

	// Append to thinking content
	state.thinkingParts[0] += event.Thinking

	// Dual-write: Write thinking_block_delta to chat_updates
	a.writeStreamingDelta(ctx, chatID, "thinking_block_delta", map[string]interface{}{
		"block_index": thinkingPos,
		"delta":       event.Thinking,
		"thread":      thread,
	})
}

// handleToolUseStart handles the start of a tool use block
func (a *CallLLMActivity) handleToolUseStart(ctx context.Context, chatID string, thread string, event llm.DriverEvent, state *streamProcessingState) {
	// End current text block
	state.blockStates.EndTextBlock()

	if event.ToolCall != nil {
		// NOTE: DO NOT capture tool calls here - Input is empty at this point!
		// Tool calls will be extracted from event.Response.ToolCalls in EventComplete
		state.blockStates.StartToolBlock("")

		// Dual-write: Write tool_use_start delta to chat_updates
		// NOTE: Input is not included because it's empty at this point
		// Status is "preparing" - LLM is still writing the tool request
		// Use CurrentPosition() - 1 since StartToolBlock already advanced the position
		a.writeStreamingDelta(ctx, chatID, "tool_use_start", map[string]interface{}{
			"block_index": state.blockStates.CurrentPosition() - 1,
			"thread":      thread,
			"tool_call": map[string]interface{}{
				"id":     event.ToolCall.ID,
				"name":   event.ToolCall.Name,
				"status": "preparing", // LLM is writing tool request
			},
		})
	}
}

// handleToolUseStop handles the end of a tool use block
func (a *CallLLMActivity) handleToolUseStop(ctx context.Context, chatID string, thread string, state *streamProcessingState) {
	// Get the current position before ending the block (this is the tool block's position)
	toolBlockPos := state.blockStates.CurrentPosition() - 1
	state.blockStates.EndToolBlock()

	// Dual-write: Write tool_use_stop delta to chat_updates
	// Status is now "requested" - LLM finished writing, ready for approval/execution
	// Use the position we captured before ending the block
	a.writeStreamingDelta(ctx, chatID, "tool_use_stop", map[string]interface{}{
		"block_index": toolBlockPos,
		"status":      "requested", // LLM finished writing tool request
		"thread":      thread,
	})
}

// handleComplete handles the completion event with token usage and final tool calls
func (a *CallLLMActivity) handleComplete(ctx context.Context, event llm.DriverEvent, state *streamProcessingState) {
	// Collect token usage (total tokens for compaction decisions)
	state.tokenCount = int(event.Response.Usage.TokenCount)

	// Extract thinking signature from the response (for multi-turn thinking preservation)
	if event.Response != nil && event.Response.ThinkingSignature != "" {
		state.thinkingSignature = event.Response.ThinkingSignature
	}

	if event.Response != nil {
		state.upstreamRequestID = strings.TrimSpace(event.Response.UpstreamRequestID)
		state.upstreamProxymanID = strings.TrimSpace(event.Response.UpstreamProxymanID)
	}

	// CRITICAL: Extract complete tool calls with full inputs from the final response
	// This is done here instead of EventToolUseStart because Input is empty at that point
	if event.Response != nil && len(event.Response.ToolCalls) > 0 {
		state.toolCalls = make([]message.ToolCall, len(event.Response.ToolCalls))
		for i, tc := range event.Response.ToolCalls {
			toolInput := tc.Input
			// Trim redundant cd prefix from Bash commands
			if tc.Name == "Bash" && state.workingDir != "" {
				toolInput = trimBashWorkspaceCD(toolInput, state.workingDir)
			}
			state.toolCalls[i] = message.ToolCall{
				ID:               tc.ID,
				Name:             tc.Name,
				Input:            toolInput,
				BlockIndex:       i,
				ThoughtSignature: tc.ThoughtSignature,
			}
		}
	}

	// Use authoritative response content when available.
	// During streaming, text is accumulated from EventContentDelta events which may contain
	// raw embedded function call patterns (e.g., "to=functions.view {json}") that the driver
	// strips in its authoritative done-event text. Override with the clean final content.
	if event.Response != nil && event.Response.Content != "" {
		state.textParts = []string{event.Response.Content}
	}
}

// trackLLMCallCompleted fires an analytics event after each LLM API call.
func (a *CallLLMActivity) trackLLMCallCompleted(ctx context.Context, chat *db.Chat, driver llm.Driver, latencyMs int64, tokenCount int, streamErr error) {
	model := driver.Model()
	metrics := analytics.LLMCallMetrics{
		Provider:    driver.Name(),
		Model:       string(model.ID),
		LatencyMs:   latencyMs,
		Success:     streamErr == nil,
		IsStreaming: true,
		ChatID:      chat.ID,
	}
	if streamErr != nil {
		metrics.ErrorType = classifyLLMError(streamErr)
	}
	// TokenCount is the total from the driver; we report it as inputTokens since
	// the driver aggregates input+output+cache into a single total.
	metrics.InputTokens = tokenCount
	if chat.WorkflowID != nil {
		metrics.WorkflowID = *chat.WorkflowID
	}

	analyticsClient := analytics.GetClientForUser(ctx, chat.UserID)
	analyticsClient.TrackLLMCallCompleted(metrics)
}

// classifyLLMError maps an LLM error to a category string for analytics.
func classifyLLMError(err error) string {
	if err == nil {
		return ""
	}
	errMsg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(errMsg, "rate limit") || strings.Contains(errMsg, "429"):
		return "rate_limit"
	case strings.Contains(errMsg, "context length") || strings.Contains(errMsg, "too many tokens") || strings.Contains(errMsg, "maximum context"):
		return "context_length"
	case strings.Contains(errMsg, "auth") || strings.Contains(errMsg, "401") || strings.Contains(errMsg, "403") || strings.Contains(errMsg, "invalid api key"):
		return "auth"
	case strings.Contains(errMsg, "timeout") || strings.Contains(errMsg, "deadline"):
		return "timeout"
	case strings.Contains(errMsg, "cancelled") || strings.Contains(errMsg, "canceled"):
		return "cancelled"
	case strings.Contains(errMsg, "connection") || strings.Contains(errMsg, "network") || strings.Contains(errMsg, "eof"):
		return "network"
	default:
		return "unknown"
	}
}

// trimBashWorkspaceCD removes redundant "cd <workspace> && " prefix from Bash tool input.
// This is done for cleaner display since the bash tool already runs in the workspace directory.
func trimBashWorkspaceCD(toolInput string, workspaceDir string) string {
	if workspaceDir == "" {
		return toolInput
	}

	var params map[string]interface{}
	if err := json.Unmarshal([]byte(toolInput), &params); err != nil {
		return toolInput
	}

	command, ok := params["command"].(string)
	if !ok {
		return toolInput
	}

	prefix := "cd " + workspaceDir + " && "
	if strings.HasPrefix(command, prefix) {
		params["command"] = strings.TrimPrefix(command, prefix)
		newInput, err := json.Marshal(params)
		if err != nil {
			return toolInput
		}
		return string(newInput)
	}

	return toolInput
}

// handleError handles error events from the stream
func (a *CallLLMActivity) handleError(event llm.DriverEvent) error {
	return fmt.Errorf("LLM streaming error: %w", event.Error)
}

// injectRetryHint appends a system-reminder to the prompts when a Temporal
// activity is being retried after a streaming failure. The hint is appended
// to the first prompt (not added as a separate prompt) so that prompt
// caching on the last 2 prompts is not disrupted.
func injectRetryHint(prompts []string, attempt int32) []string {
	hint := fmt.Sprintf(
		"\n\n<system-reminder>\n"+
			"This is retry attempt %d after a previous streaming failure (likely a truncated response from the LLM API). "+
			"The previous attempt's response was too long or got cut off mid-stream. Please try a different approach:\n"+
			"- Use fewer tool calls per response\n"+
			"- Keep individual tool call inputs shorter\n"+
			"- Break complex operations into multiple turns rather than one massive response\n"+
			"</system-reminder>", attempt)
	if len(prompts) > 0 {
		result := make([]string, len(prompts))
		copy(result, prompts)
		result[0] += hint
		return result
	}
	return []string{hint}
}

// getLatestUserMessageText extracts the text content from the last user message in history.
func getLatestUserMessageText(history []message.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != message.User {
			continue
		}
		for _, part := range history[i].Parts {
			if tc, ok := part.(message.TextContent); ok {
				return tc.Text
			}
		}
	}
	return ""
}

// buildSkillSuggestionReminder formats suggested skills as a system-reminder block.
func buildSkillSuggestionReminder(suggestions []suggest.Suggested) string {
	var sb strings.Builder
	sb.WriteString("\n\n<system-reminder>\nPotentially relevant skills (use the skill tool to load if needed):\n")
	for _, s := range suggestions {
		desc := s.Skill.Description
		if len(desc) > 80 {
			desc = desc[:77] + "..."
		}
		sb.WriteString(fmt.Sprintf("- %s: %s\n", s.Skill.Name, desc))
	}
	sb.WriteString("</system-reminder>")
	return sb.String()
}

// injectReminderIntoLastUserMessage appends a reminder string to the last user message's text content.
func injectReminderIntoLastUserMessage(history []message.Message, reminder string) {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != message.User {
			continue
		}
		for j, part := range history[i].Parts {
			if tc, ok := part.(message.TextContent); ok {
				history[i].Parts[j] = message.TextContent{Text: tc.Text + reminder}
				return
			}
		}
		// No text part found, append one
		history[i].Parts = append(history[i].Parts, message.TextContent{Text: reminder})
		return
	}
}

func formatStoredMemories(projectCfg *cfgpkg.Config) string {
	if projectCfg == nil {
		return ""
	}

	var memories []string
	if strings.TrimSpace(projectCfg.GlobalMemoryMD) != "" {
		memories = append(memories, "## Global Context\n\n"+projectCfg.GlobalMemoryMD)
	}
	if strings.TrimSpace(projectCfg.ProjectMemoryMD) != "" {
		memories = append(memories, "## Project Context\n\n"+projectCfg.ProjectMemoryMD)
	}

	if len(memories) == 0 {
		return ""
	}
	preamble := "\n\n# User defined rules, memories, and context\n\nYou must adhere to the user's defined rules and context below at all times while assisting them.\n\n"
	memories = append([]string{preamble}, memories...)
	return strings.Join(memories, "\n\n")
}

// writeStreamingDelta publishes a streaming delta event to the in-memory hub
// This replaces the previous DB-based streaming_delta writes for better performance
func (a *CallLLMActivity) writeStreamingDelta(ctx context.Context, chatID string, deltaType string, deltaData map[string]interface{}) {
	if a.hub == nil {
		return
	}
	// Check if context has been cancelled (via Temporal signal-based activity cancellation).
	// This prevents race conditions where streaming continues after cancellation status is emitted.
	// Allow "stream_cancelled" deltas through so the UI knows streaming stopped.
	if deltaType != "stream_cancelled" && ctx.Err() != nil {
		activity.GetLogger(ctx).Info("[STREAMING_DELTA] Dropping delta - context cancelled",
			"delta_type", deltaType,
			"chat_id", chatID)
		return
	}

	hub := a.hub

	// Build streaming delta from deltaData
	delta := streaming.StreamingDelta{
		DeltaType: streaming.DeltaType(deltaType),
	}

	// Extract known fields from deltaData
	if thread, ok := deltaData["thread"].(string); ok {
		delta.Thread = thread
	}
	if blockIndex, ok := deltaData["block_index"].(int); ok {
		delta.BlockIndex = blockIndex
	}
	if blockType, ok := deltaData["block_type"].(string); ok {
		delta.BlockType = blockType
	}
	if deltaStr, ok := deltaData["delta"].(string); ok {
		delta.Delta = deltaStr
	}
	if messageID, ok := deltaData["message_id"].(string); ok {
		delta.MessageID = messageID
	}
	if role, ok := deltaData["role"].(string); ok {
		delta.Role = role
	}
	if model, ok := deltaData["model"].(string); ok {
		delta.Model = model
	}
	if tokenCount, ok := deltaData["token_count"].(int); ok {
		delta.TokenCount = tokenCount
	}
	if thinkingSig, ok := deltaData["thinking_signature"].(string); ok {
		delta.ThinkingSignature = thinkingSig
	}

	// Handle tool_call data
	if toolCall, ok := deltaData["tool_call"].(map[string]interface{}); ok {
		tc := &streaming.StreamingToolCall{}
		if id, ok := toolCall["id"].(string); ok {
			tc.ID = id
		}
		if name, ok := toolCall["name"].(string); ok {
			tc.Name = name
		}
		if input, ok := toolCall["input"].(string); ok {
			tc.Input = input
		}
		delta.ToolCall = tc
	}

	// Publish to hub (non-blocking)
	hub.Publish(ctx, chatID, delta)
}

// ============================================================================
// MESSAGE HISTORY REPAIR (Layer 3 - In-Memory Fallback)
// ============================================================================
//
// This function provides in-memory fallback repair for orphaned tool calls.
// It is Layer 3 of the repair strategy - used when DB-level repairs aren't possible.
//
// See db_helpers.go LoadMessagesForLLM for the full layered repair strategy.

// repairMessageHistory validates message history and ensures tool_results appear
// IMMEDIATELY after their corresponding tool_calls. The Anthropic API requires this
// strict ordering - ALL tool_results for an assistant message's tool_calls must be
// in the message immediately following that assistant message.
//
// This is Layer 3 of the repair strategy (last resort fallback). It operates in-memory
// only and doesn't persist repairs. It handles edge cases that Layer 1 (CleanupActivity)
// and Layer 2 (repairOrphanedToolCalls) can't handle:
// - Inherited messages from parent chats (where we can't modify the parent)
// - Mid-conversation orphans (where ordinal insertion is complex)
// - Race conditions or other transient issues
//
// This handles:
// 1. Missing tool_results (creates synthetic ones)
// 2. Misplaced tool_results (moves them to correct position)
// 3. Partial tool_results (adds missing ones)
func repairMessageHistory(msgs []message.Message) []message.Message {
	if len(msgs) == 0 {
		return msgs
	}

	// Build a map of tool_call_id -> tool_result (from anywhere in messages)
	// We'll use these to find existing results that may be misplaced.
	// Also build a set of all tool_call IDs from assistant messages so we can
	// detect truly orphaned tool_results (ones whose assistant message is gone).
	existingResults := make(map[string]message.ToolResult)
	allToolCallIDs := make(map[string]bool)
	for _, msg := range msgs {
		if msg.Role == message.Tool {
			for _, result := range msg.ToolResults() {
				existingResults[result.ToolCallID] = result
			}
		}
		if msg.Role == message.Assistant {
			for _, tc := range msg.ToolCalls() {
				allToolCallIDs[tc.ID] = true
			}
		}
	}

	// Build result by processing each message and ensuring tool_results
	// immediately follow their assistant messages with ALL required results
	result := make([]message.Message, 0, len(msgs))
	usedToolResultIDs := make(map[string]bool) // Track which results we've placed

	for i, msg := range msgs {
		// Skip tool messages - we'll add tool results in the right position after assistant messages
		if msg.Role == message.Tool {
			// Only keep tool messages that have unused results AND have a
			// corresponding tool_call in some assistant message. Tool results
			// whose assistant message was lost (compaction, fork, etc.) are
			// truly orphaned and would cause API errors if emitted.
			var unusedResults []message.ContentPart
			for _, tr := range msg.ToolResults() {
				if !allToolCallIDs[tr.ToolCallID] {
					logging.Warn("[repairMessageHistory] Dropping orphaned tool_result with no matching tool_call in any assistant message",
						"tool_call_id", tr.ToolCallID,
						"tool_name", tr.Name,
					)
					continue
				}
				if !usedToolResultIDs[tr.ToolCallID] {
					unusedResults = append(unusedResults, tr)
					usedToolResultIDs[tr.ToolCallID] = true
				}
			}
			if len(unusedResults) > 0 {
				result = append(result, message.Message{
					ID:    msg.ID,
					Role:  message.Tool,
					Parts: unusedResults,
				})
			}
			continue
		}

		// Add the message
		result = append(result, msg)

		// If this is an assistant message with tool_calls, ensure ALL tool_results follow immediately
		if msg.Role == message.Assistant {
			toolCalls := msg.ToolCalls()
			if len(toolCalls) == 0 {
				continue
			}

			// Collect ALL tool results for this assistant message's tool calls
			var toolResults []message.ContentPart
			for _, tc := range toolCalls {
				if usedToolResultIDs[tc.ID] {
					continue // Already placed (shouldn't happen, but safety check)
				}

				if existing, ok := existingResults[tc.ID]; ok {
					// Use existing result
					toolResults = append(toolResults, existing)
				} else {
					// Create synthetic result
					toolResults = append(toolResults, message.ToolResult{
						ToolCallID: tc.ID,
						Name:       tc.Name,
						Content:    "Tool execution was cancelled before completion. The previous request was interrupted.",
						IsError:    true,
					})
				}
				usedToolResultIDs[tc.ID] = true
			}

			if len(toolResults) > 0 {
				result = append(result, message.Message{
					ID:    fmt.Sprintf("synthetic-repair-%d", i),
					Role:  message.Tool,
					Parts: toolResults,
				})
			}
		}
	}

	return result
}

// normalizeRolesForLLM converts internal message roles to LLM-compatible roles.
// Agent role is treated as User for LLM APIs since these are the only
// standard roles supported by most providers.
func normalizeRolesForLLM(history []message.Message) []message.Message {
	result := make([]message.Message, len(history))
	for i, msg := range history {
		result[i] = msg
		switch msg.Role {
		case message.Agent:
			result[i].Role = message.User
		}
	}
	return result
}

// celDoubleValuePtr returns a *float64 for the literal value of a CelDouble, or nil if not set/is an expr.
func celDoubleValuePtr(c *reliantv1.CelDouble) *float64 {
	if !model.CelDoubleIsSet(c) || model.CelDoubleIsExpr(c) {
		return nil
	}
	v := model.CelDoubleValue(c)
	return &v
}

// celStringValuePtr returns a *string for the literal value of a CelString, or nil if not set/is an expr.
func celStringValuePtr(c *reliantv1.CelString) *string {
	if !model.CelStringIsSet(c) || model.CelStringIsExpr(c) {
		return nil
	}
	v := model.CelStringValue(c)
	return &v
}

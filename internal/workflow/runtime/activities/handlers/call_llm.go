// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"

	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	cfgpkg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
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
	"github.com/reliant-labs/reliant/internal/threads"
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
	tokenCount        int            // Total tokens (prompt + response + context)
	usage             llm.TokenUsage // Full token usage for analytics and managed spend tracking
	cost              float64        // Request cost in USD returned by the provider response
	workingDir        string         // Working directory for trimming bash commands

	upstreamRequestID  string // Provider response header x-oai-request-id (if available)
	upstreamProxymanID string // Provider response header x-proxyman-id (if available)

	// Delta identity: pre-allocated assistant message id (from
	// RuntimeContext.AssistantMessageID) and the per-message monotonically
	// increasing sequence stamped onto every published delta. Both zero when
	// the caller didn't pre-allocate (legacy histories).
	messageID string
	streamSeq int64

	// inProviderBackoff records that this thread has an OPEN provider-backoff
	// marker, so the marker is cleared exactly once — on the first event that
	// proves the provider answered — rather than on every streamed delta.
	inProviderBackoff bool
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

// explicitCompactionThresholdIsSet reports whether the CallLLM args carry an
// explicit, positive compaction threshold literal. A literal <= 0 (or an unset
// value / CEL expression) is treated as "not set" so the model default can win.
func explicitCompactionThresholdIsSet(args *reliantv1.CallLLMArgs) bool {
	ct := args.GetCompactionThreshold()
	if !model.CelIntIsSet(ct) || model.CelIntIsExpr(ct) {
		return false
	}
	return model.CelIntValue(ct) > 0
}

// explicitCompactionThresholdArg returns the explicit compaction threshold from
// the CallLLM args when set to a positive literal, otherwise the global default.
// The resolved-model default is layered on top of this in executeCore.
func explicitCompactionThresholdArg(args *reliantv1.CallLLMArgs) int32 {
	if explicitCompactionThresholdIsSet(args) {
		return int32(model.CelIntValue(args.GetCompactionThreshold()))
	}
	return DefaultCompactionThreshold
}

// executeCore contains PURE BUSINESS LOGIC only
func (a *CallLLMActivity) executeCore(ctx context.Context, rtx RuntimeContext, args *reliantv1.CallLLMArgs) (*reliantv1.CallLLMOutput, error) {
	logger := activity.GetLogger(ctx)

	// Thread path must be provided - no more "0" default
	thread := rtx.Thread
	if thread == "" {
		return nil, fmt.Errorf("thread is required")
	}

	// Validate thinking level if explicitly provided (empty string means "use model default")
	if model.CelStringIsSet(args.GetThinkingLevel()) && !model.CelStringIsExpr(args.GetThinkingLevel()) {
		if v := model.CelStringValue(args.GetThinkingLevel()); v != "" {
			tl := ThinkingLevel(v)
			if !tl.IsValid() {
				return nil, fmt.Errorf("invalid thinking_level: %s (must be one of: %s)", tl, strings.Join(models.KnownThinkingLevels, ", "))
			}
		}
	}

	// Load chat configuration
	chat, err := a.repo.GetChat(ctx, rtx.ChatID)
	if err != nil {
		return nil, fmt.Errorf("failed to get chat: %w", err)
	}

	// Add userID to context for API key loading
	ctx = context.WithValue(ctx, auth.UserIDContextKey, chat.UserID)

	// Hydrate the user's JWT into the in-memory auth map so the Reliant LLM
	// driver can be resolved on workers that didn't see the originating gRPC
	// request. The JWT travels through workflow inputs; see RuntimeContext.
	if rtx.UserJWT != "" {
		auth.SetUserJWT(chat.UserID, rtx.UserJWT)
	}

	// Deliver anything queued in this thread's mailbox BEFORE reading history,
	// so a message the user sent while the agent was working is part of the
	// turn it is about to take rather than the one after.
	//
	// This is the only delivery point (agent_mailbox.go version 3). Doing it
	// here rather than at the loop boundary is what makes the tool-pairing
	// invariant structural: the history read immediately below is going to a
	// provider, so it must already be consistent, and there is no window in
	// which a delivered message can land between an assistant's tool_calls and
	// its tool_results.
	//
	// Best-effort, exactly as the boundary drain was: a mailbox hiccup must not
	// fail the turn. The rows stay queued (status=1) and the next call picks
	// them up.
	a.drainAgentMailbox(ctx, rtx.ChatID, thread)

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

	// Did anything arrive in the mailbox WHILE we were streaming?
	//
	// The drain above ran before the history read, so a message queued during
	// the response is not part of this turn. That is fine as long as another
	// turn follows — but if the model returned no tool calls, the agent loop is
	// about to exit on a thread that still has an undelivered message, and
	// nothing would ever deliver it. Reporting it here lets the loop's
	// while-condition keep going, which routes back through CallLLM, which
	// delivers it.
	//
	// Cheap: one indexed SELECT against the partial index on queued rows, once
	// per LLM call — negligible next to the call itself.
	output.PendingInbox = output.GetPendingInbox() || a.hasQueuedAgentMessages(ctx, thread)

	logger.Info("[CallLLM] Completed",
		"chatID", rtx.ChatID,
		"toolCalls", len(output.ToolCalls),
		"tokenCount", output.TokenCount,
		"pendingInbox", output.PendingInbox,
		"x_oai_request_id", strings.TrimSpace(output.GetUpstreamRequestId()),
		"x_proxyman_id", strings.TrimSpace(output.GetUpstreamProxymanId()))

	// An interrupted turn persists its own partial before unwinding.
	//
	// There is no way to hand it back through Temporal: the workflow no longer
	// waits for a cancelled activity (see activityOptions), and the SDK discards
	// a cancelled activity's late return regardless. Writing the row here is what
	// makes the turn survive, and the step's position-derived idempotency key is
	// what stops the re-dispatch that follows from adding a second one.
	if ctx.Err() != nil {
		a.persistInterruptedTurn(ctx, rtx, thread, output)
	}
	return output, nil
}

// persistInterruptedTurn saves the partial assistant message of a turn whose
// context was cancelled mid-stream, on a context detached from that
// cancellation.
//
// Best-effort by construction: the turn is already over, and a failure here
// costs a partial message, whereas returning an error would cost the whole
// cancellation path. Both outcomes are logged rather than surfaced.
func (a *CallLLMActivity) persistInterruptedTurn(
	ctx context.Context,
	rtx RuntimeContext,
	thread string,
	output *reliantv1.CallLLMOutput,
) {
	if output == nil || thread == "" {
		return
	}
	// Nothing streamed and no tools requested: an empty assistant row would be
	// noise in the transcript and in the provider history.
	if strings.TrimSpace(output.GetResponseText()) == "" && len(output.GetToolCalls()) == 0 {
		return
	}

	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), interruptedTurnWriteTimeout)
	defer cancel()

	var workflowID *string
	if rtx.WorkflowID != "" {
		workflowID = &rtx.WorkflowID
	}
	var thinking *threads.ThinkingContent
	if t := output.GetThinking(); t != nil && (t.GetContent() != "" || t.GetSignature() != "") {
		thinking = &threads.ThinkingContent{Content: t.GetContent(), Signature: t.GetSignature()}
	}
	activityID := rtx.MessageIdempotencyKey

	if _, err := threads.NewService(a.repo).SaveMessage(writeCtx, threads.SaveMessageOpts{
		ChatID:       rtx.ChatID,
		Thread:       thread,
		Role:         int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
		Content:      output.GetResponseText(),
		ToolCalls:    convertToolCalls(protoToolCallsToMessage(output.GetToolCalls())),
		Thinking:     thinking,
		TokenCount:   int(output.GetTokenCount()),
		Cost:         output.GetCost(),
		Model:        output.GetModel(),
		WorkflowID:   workflowID,
		StepID:       rtx.StepID,
		ActivityID:   &activityID,
		MessageID:    output.GetMessageId(),
		DisplayStyle: int32(reliantv1.DisplayStyle_DISPLAY_STYLE_UNSPECIFIED),
	}); err != nil {
		logging.Warn("[CallLLM] Could not persist the partial turn of an interrupted call",
			"chatID", rtx.ChatID, "thread", thread, "error", err)
	}
}

// interruptedTurnWriteTimeout bounds the detached write of an interrupted
// turn's partial. Short: one insert on a live pool, with the caller unwinding.
const interruptedTurnWriteTimeout = 5 * time.Second

// hasQueuedAgentMessages reports whether thread's mailbox holds undelivered
// messages right now.
//
// Errors resolve to false rather than being surfaced: this only decides
// whether to take one more loop iteration, and failing the whole turn over a
// mailbox probe would be a far worse outcome than the message waiting for the
// user's next send (which absorbs the mailbox anyway).
func (a *CallLLMActivity) hasQueuedAgentMessages(ctx context.Context, thread string) bool {
	if thread == "" {
		return false
	}
	queued, err := a.repo.ListQueuedAgentMessagesForThread(ctx, thread)
	if err != nil {
		logging.Warn("[CallLLM] Could not check the mailbox for late arrivals; "+
			"a message queued during this turn may wait for the next send",
			"thread", thread, "error", err)
		return false
	}
	return len(queued) > 0
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

// drainAgentMailbox folds any queued agent_messages for thread into its
// history. Called immediately before the history read in executeCore.
//
// Reuses DrainAgentMessagesActivity's logic rather than reimplementing it: the
// delivery rules (a hidden envelope for LLM-only framing, visible bodies for
// human messages, hidden bodies plus a system notification for spawn results,
// all in one transaction with the mark-delivered bookkeeping) are subtle and
// must not fork.
//
// Errors are logged, never returned. A turn that cannot deliver its mailbox is
// still a turn worth taking, and the undelivered rows remain queued for the
// next call. Returning an error here would fail the activity and, through the
// step executor, skip the save_message that persists this turn's output.
func (a *CallLLMActivity) drainAgentMailbox(ctx context.Context, chatID, thread string) {
	if thread == "" {
		return
	}

	drain := NewDrainAgentMessagesActivity(a.repo, threads.NewService(a.repo))
	out, err := drain.Execute(ctx, DrainAgentMessagesInput{ChatID: chatID, Thread: thread})
	if err != nil {
		logging.Warn("[CallLLM] Failed to deliver queued agent messages; they stay queued for the next turn",
			"chatID", chatID, "thread", thread, "error", err)
		return
	}
	if out.HasMessages {
		logging.Info("[CallLLM] Delivered queued agent messages into history",
			"chatID", chatID, "thread", thread, "count", out.Count)
	}
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
	// Check if this workflow has reached the maximum spawn depth (prevents unbounded recursive spawn)
	const maxSpawnDepth = 1
	spawnDisabled := rtx.SpawnDepth >= maxSpawnDepth

	// Resolve tools configuration
	tc := args.GetToolsConfig()
	toolsEnabled := tc != nil && tc.GetFilter() != nil

	// Resolve permission level (defaults to mutating when no tools_config)
	permission := tools.PermissionMutating
	if tc != nil && model.CelStringIsSet(tc.GetPermission()) {
		permission = model.CelStringValue(tc.GetPermission())
	}
	// Cap permission to parent's level for spawned workflows.
	// A plan-mode parent should not spawn a child with mutating permission.
	if rtx.ParentPermission != "" && !tools.PermissionAtLeast(rtx.ParentPermission, permission) {
		activity.GetLogger(ctx).Info("[CallLLM] Capping child permission to parent level",
			"child_permission", permission,
			"parent_permission", rtx.ParentPermission,
			"thread", thread)
		permission = rtx.ParentPermission
	}
	if chat != nil {
		tools.GetLoadedToolsStore().SetPermission(chat.ID, permission)
	}

	// Model must be provided via workflow inputs
	if !model.CelModelSelectorIsSet(args.GetModel()) {
		return nil, fmt.Errorf("model is required - must be provided via workflow inputs")
	}

	// effectiveCompactionThreshold is the token count at which the agent loop
	// triggers compaction for the resolved model. Precedence mirrors
	// temperature/thinking_level: an explicit CallLLM arg wins, otherwise the
	// threshold is DERIVED from the resolved model's real context window
	// (models.CompactionThresholdFraction × max_context_window), with a per-model
	// explicit override honored if declared, otherwise the global default when the
	// window is unknown. Emitted on CallLLMOutput so the compact edge reads the
	// per-model value even when the model was selected by tag.
	effectiveCompactionThreshold := explicitCompactionThresholdArg(args)
	protoModel := model.CelModelSelectorValue(args.GetModel())
	modelSelector := models.ModelSelector{}
	if protoModel != nil {
		modelSelector.ID = protoModel.GetId()
		modelSelector.Tags = protoModel.GetTags()
		modelSelector.Providers = protoModel.GetProviders()
	}

	// Load project and project config via provider abstraction. This happens
	// before model/driver resolution so the registry reflects the project's
	// model config and the working directory can be attached to the driver.
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

	// Explicit per-call overrides from workflow args. Zero values mean "use
	// the model default" — resolveLLMCall (the request-construction path
	// shared with Compact) layers model defaults and reconciles the thinking
	// level against model capabilities the same way for every caller.
	var explicitTemperature *float64
	// Temperature 0.0 means "not set, use model default"
	if model.CelDoubleIsSet(args.GetTemperature()) && !model.CelDoubleIsExpr(args.GetTemperature()) && model.CelDoubleValue(args.GetTemperature()) != 0.0 {
		v := model.CelDoubleValue(args.GetTemperature())
		explicitTemperature = &v
	}
	var explicitThinkingLevel string
	if model.CelStringIsSet(args.GetThinkingLevel()) && !model.CelStringIsExpr(args.GetThinkingLevel()) {
		explicitThinkingLevel = model.CelStringValue(args.GetThinkingLevel())
	}
	var explicitMaxTokens *int64
	if model.CelIntIsSet(args.GetMaxTokens()) {
		v := model.CelIntValue(args.GetMaxTokens())
		explicitMaxTokens = &v
	}

	// Resolve model and driver through the shared request-construction path.
	// When a custom driver resolver is injected (e.g., in tests), it skips
	// registry resolution and probes the resolver for its model.
	resolved, err := resolveLLMCall(ctx, a.driverResolver, llmCallSpec{
		UserID:        chat.UserID,
		SessionID:     chat.ID,
		Selector:      modelSelector,
		Temperature:   explicitTemperature,
		ThinkingLevel: explicitThinkingLevel,
		MaxTokens:     explicitMaxTokens,
		WorkingDir:    workingDir,
	})
	if err != nil {
		return nil, err
	}
	driver := resolved.Driver

	// Capture the concrete model that will serve this completion so the inline
	// save_message can persist it onto messages.model. This is the resolved
	// model AFTER tag/selector resolution (e.g. "claude-4.8-opus"), not the raw
	// "tags:[flagship]" selector. Prefer the registry definition ID; fall back
	// to the probed model ID when an injected resolver supplied the driver.
	resolvedModelID := ""
	if resolved.Definition != nil {
		resolvedModelID = resolved.Definition.ID
	} else if resolved.Model.ID != "" {
		resolvedModelID = string(resolved.Model.ID)
	}

	if resolved.Definition != nil {
		activity.GetLogger(ctx).Info("[CallLLM] Resolved model",
			"selector", modelSelector,
			"modelID", resolved.Definition.ID,
			"modelIDWithDriver", resolved.ModelID)

		// Determine effective compaction threshold: an explicit per-node arg
		// (already captured above) wins; otherwise it is DERIVED from the
		// resolved model's REAL context window for the SELECTED provider
		// (CompactionThresholdFraction × EffectiveContextWindow), with a per-model
		// explicit override honored if the definition declares one. Using the
		// provider-effective window is what makes a small-window provider (e.g.
		// "@codex", which caps GPT-5.x far below its platform window) compact
		// before it overflows. See models.CompactionThresholdForProvider.
		if !explicitCompactionThresholdIsSet(args) {
			effectiveCompactionThreshold = int32(models.CompactionThresholdForProvider(resolved.Definition, resolved.ProviderDriver))
		}
	} else {
		activity.GetLogger(ctx).Info("[CallLLM] Using injected driver resolver",
			"modelID", resolved.Model.ID)

		// No registry definition (injected driver resolver, e.g. in tests):
		// derive the threshold from the resolved model's context window when it
		// is known, otherwise the global default stands. An explicit per-node
		// arg still wins.
		if !explicitCompactionThresholdIsSet(args) {
			effectiveCompactionThreshold = int32(models.DeriveCompactionThreshold(int(resolved.Model.ContextWindow)))
		}
	}

	// Observability: record whether extended reasoning is engaged for this call
	// and at what effort. GPT/codex (and every reasoning driver) only emit
	// reasoning when the request carries a reasoning-effort param, which is
	// derived from the effective, capability-reconciled thinking level
	// (resolved.ThinkingLevel → llm.WithReasoningEffort in resolveLLMCall). An
	// empty thinking level on a reasoning model still engages reasoning at the
	// driver's medium default. Logging this here lets a run confirm reasoning was
	// actually requested (no token values or secrets are logged).
	activity.GetLogger(ctx).Info("[CallLLM] Reasoning",
		"chatID", chat.ID,
		"modelID", resolvedModelID,
		"provider", resolved.ProviderDriver,
		"canReason", resolved.Model.CanReason,
		"thinkingLevel", resolved.ThinkingLevel,
		"engaged", resolved.Model.CanReason)

	// Get available tools (filtered by tools_config)
	var availableTools []tools.Tool
	var spawnPresets []string            // Track spawn presets for tool call validation
	var toolsResult toolsWithSpawnResult // Hoisted for deferred tools announcement
	if !toolsEnabled {
		activity.GetLogger(ctx).Info("[CallLLM] Tools disabled")
		availableTools = []tools.Tool{}
	} else {
		toolFilter := model.CelStringListValue(tc.GetFilter())
		// spawn_send is only meaningful to an agent that has a counterpart to
		// message: a sub-agent replying to the parent that spawned it, or an
		// orchestrator actually configured to spawn children. A plain root
		// agent has neither, so offering it there is pure tool-surface noise on
		// every request.
		canSpawnChildren := !spawnDisabled && len(model.CelStringListValue(tc.GetSpawn())) > 0
		mailboxReachable := rtx.SpawnDepth > 0 || canSpawnChildren
		toolsResult = a.getAvailableToolsWithSpawn(ctx, chat, workingDir, projectCfg, toolFilter, thread, mailboxReachable)
		availableTools = toolsResult.Tools

		// Emit warning to chat if MCP servers failed to load
		if len(toolsResult.FailedMCPServers) > 0 {
			a.writeStreamingDelta(ctx, chat.ID, "mcp_warning", map[string]interface{}{
				"message":        fmt.Sprintf("Some MCP servers failed to start: %v. Tools from these servers will be unavailable.", toolsResult.FailedMCPServers),
				"failed_servers": toolsResult.FailedMCPServers,
				"thread":         thread,
			}, nil)
		}

		// Add spawn tools from tools_config.spawn
		// Workflows at max spawn depth should NOT have access to spawn tool to prevent unbounded recursion
		if !spawnDisabled {
			// Parse spawn configs from the dedicated spawn field
			spawnEntries := model.CelStringListValue(tc.GetSpawn())
			for _, entry := range spawnEntries {
				spawnConfig := tools.ParseSpawnEntry(entry)
				if spawnConfig == nil {
					continue
				}
				spawnTool := a.getSpawnToolFromFilterConfig(ctx, chat.ProjectID, *spawnConfig)
				if spawnTool != nil {
					availableTools = append(availableTools, spawnTool)
					spawnPresets = append(spawnPresets, spawnConfig.Presets...)
					activity.GetLogger(ctx).Info("[CallLLM] Added spawn tool from tools_config",
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

	// Repos drive the multi-repo hint in the system prompt. Failures here are
	// non-fatal — the prompt just omits the hint.
	repos, _ := a.repo.ListReposByProject(ctx, project.ID)

	systemPrompts := a.getSystemPrompts(
		chat,
		projectPath,
		worktreePath,
		projectCfg,
		repos,
		celStringValuePtr(args.GetSystemPrompt()),
	)

	// Set deferred tools on the load_tool so its description advertises them
	if toolsEnabled && len(availableTools) > 0 {
		currentToolNames := make([]string, len(availableTools))
		for i, t := range availableTools {
			currentToolNames[i] = t.Name()
		}
		deferred := tools.DeferredToolNames(chat.ID, permission, currentToolNames, toolsResult.AllMCPToolNames)
		if len(deferred) > 0 {
			for _, t := range availableTools {
				if u, ok := t.(interface{ Unwrap() any }); ok {
					if inner, ok := u.Unwrap().(tools.DeferredToolsAware); ok {
						inner.SetDeferredTools(deferred)
						break
					}
				}
			}
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
		messageID:         rtx.AssistantMessageID,
	}

	// Setup cancellation context
	// Temporal's signal-based activity cancellation (workflow.WithCancel + CancelFunc)
	// propagates cancellation through the context when the workflow is paused/cancelled.
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()

	// Build prefix messages that appear before conversation history:
	// 1. Memory (reliant.md) — global/project context
	// 2. Recommended skill bodies from active preset(s)
	// These are NOT saved to DB — re-injected each turn so the Anthropic
	// cache handles deduplication.
	var prefix []message.Message
	if memoryContent := formatStoredMemories(projectCfg); memoryContent != "" {
		prefix = append(prefix, message.Message{
			Role:  message.System,
			Parts: []message.ContentPart{message.TextContent{Text: memoryContent}},
		})
	}
	// Per-repo memory: eagerly inject all sub-repo reliant.md content from
	// the config snapshot. Sorted by repo name for prefix-cache stability.
	if projectCfg != nil && len(projectCfg.RepoMemories) > 0 {
		if memMsgs := formatRepoMemoryMessages(projectCfg.RepoMemories); len(memMsgs) > 0 {
			prefix = append(prefix, memMsgs...)
		}
	}
	if len(prefix) > 0 {
		history = append(prefix, history...)
	}

	// Preloaded skills for THIS call_llm node, declared on args.skills (the
	// unified skill param — presets feed it through their own `skills` param, and
	// a node can also declare it directly for per-phase preloading). The resolved
	// skills are seeded as ONE user turn that reproduces each body verbatim under
	// an explicit harness attribution, where the body is byte-identical to the
	// agent loading the skill by hand (tools.LoadSkillForInjection reuses the
	// skill tool's own resolver). The model reads a preloaded skill as something
	// the harness handed it, NOT as a tool call it made — see
	// preloadedSkillsPreamble for the run that made that distinction load-bearing.
	// The turn is EPHEMERAL — re-seeded each turn, never persisted, so the
	// Anthropic prompt cache dedups the repeated prefix.
	//
	// The seed is spliced in immediately AFTER the first user turn in history, so
	// it sits next to the brief that named the skills. The requested-vs-injected
	// counts are logged whenever skills were requested (even when zero resolve) so
	// a silent no-op — a path that doesn't resolve against the catalog, or a
	// skills arg that never resolved from CEL to a literal — shows up in the logs
	// instead of looking like the agent simply chose to load the same skills by
	// hand.
	requestedSkills := model.CelStringListValue(args.GetSkills())
	// The skills whose bodies this call actually seeded. Read again further
	// down by injectSkillSuggestions, which must not tell the model to load
	// what the seed just told it not to.
	var preloadedSkillNames []string
	if len(requestedSkills) > 0 {
		seededMsgs, injectedSkills, oversizedSkills, missingSkills := buildSeededSkillMessages(projectCfg, requestedSkills)
		preloadedSkillNames = injectedSkills
		catalogSize := 0
		snapshotSynced := false
		if projectCfg != nil {
			catalogSize = len(projectCfg.Skills)
			snapshotSynced = projectCfg.SnapshotSynced
		}
		// Retryable only while no daemon has pushed a snapshot — see
		// preloadSkillMissError, which is where that split lives and is tested.
		if err := preloadSkillMissError(snapshotSynced, catalogSize, requestedSkills, missingSkills); err != nil {
			return nil, err
		}
		if len(missingSkills) > 0 {
			activity.GetLogger(ctx).Warn("[CallLLM] Preloaded skills do not exist in this project's catalog",
				"chatID", chat.ID,
				"missing", missingSkills,
				"requested", len(requestedSkills),
				"catalogSize", catalogSize)
		}
		if len(seededMsgs) > 0 {
			history = insertSeededMessagesAfterFirstUserTurn(history, seededMsgs)
		}
		activity.GetLogger(ctx).Info("[CallLLM] Preload skills",
			"chatID", chat.ID,
			"requested", len(requestedSkills),
			"injected", len(injectedSkills),
			"skills", requestedSkills,
			"catalogSize", catalogSize)
		// Oversize skills degrade every turn that preloads them, so they are
		// surfaced as a warning rather than folded into the info line.
		if len(oversizedSkills) > 0 {
			activity.GetLogger(ctx).Warn("[CallLLM] Preloaded skills exceed delivery budget and were truncated",
				"chatID", chat.ID,
				"skills", oversizedSkills,
				"budgetBytes", tools.MaxSkillBodySize)
		}
	}

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

	if err := validateToolNamesForLLMRequest(availableTools); err != nil {
		activity.GetLogger(ctx).Error("[CallLLM] Tool name invariant violation before LLM request",
			"chatID", chat.ID,
			"thread", thread,
			"error", err,
			"toolNames", summarizeToolNamesForLogging(availableTools))
		return nil, fmt.Errorf("tool name invariant violation: %w", err)
	}

	// Standard pre-request history transforms (shared with Compact):
	// flatten tool blocks when the request carries no tools, trim to fit the
	// context window, and normalize roles for API compatibility. The trim
	// backstop threshold scales with the resolved model's real context window
	// for the SELECTED provider (a small-window provider like "@codex" caps the
	// model below its platform window), matching the compaction trigger above.
	effectiveContextWindow := resolved.Model.ContextWindow
	if resolved.Definition != nil {
		effectiveContextWindow = int64(models.EffectiveContextWindow(resolved.Definition, resolved.ProviderDriver))
	}
	history = prepareHistoryForLLM(chat.ID, history, systemPrompts, availableTools, effectiveContextWindow)

	// Inject skill suggestions into the latest user message based on token matching.
	// Skills come from the synced project config — no filesystem access.
	if projectCfg != nil {
		if n := injectSkillSuggestions(history, projectCfg.Skills, preloadedSkillNames); n > 0 {
			activity.GetLogger(ctx).Debug("[CallLLM] Injected skill suggestions",
				"chatID", chat.ID,
				"count", n)
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

	// Delta identity: open the stream with a message_start carrying the
	// pre-allocated id. A retry re-streams under the same id, and this marker
	// tells consumers to reset any partially rendered blocks for it.
	if streamState.messageID != "" {
		a.writeStreamingDelta(ctx, chat.ID, "message_start", map[string]interface{}{
			"thread": thread,
			"role":   "assistant",
			"model":  resolvedModelID,
		}, streamState)
	}

	// Stream response with cancellation support
	llmCallStart := time.Now()
	eventChan := driver.StreamResponse(streamCtx, systemPrompts, history, availableTools)
	var streamErr error
	streamInterrupted := false

streamLoop:
	for {
		// Events the provider has already handed us are part of this turn, even
		// though it is ending. Go's select picks at random among ready cases, so
		// without this the cancel branch can win while text is still buffered and
		// the partial is silently dropped — the interrupted turn then settles
		// with an empty response_text and the user loses output they watched
		// stream in.
		select {
		case event, ok := <-eventChan:
			if ok {
				if err := a.processStreamEvent(ctx, chat.ID, thread, event, streamState); err != nil {
					streamErr = err
					break streamLoop
				}
				continue
			}
		default:
		}

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
			}, streamState)

			streamErr = errors.New("streaming cancelled by user")
			streamInterrupted = true
			break streamLoop

		case event, ok := <-eventChan:
			if !ok {
				if streamCtx.Err() != nil {
					activity.GetLogger(ctx).Info("[CallLLM] Streaming cancelled as channel closed",
						"chatID", chat.ID,
						"reason", streamCtx.Err())
					a.writeStreamingDelta(ctx, chat.ID, "stream_cancelled", map[string]interface{}{
						"reason": "user_cancelled",
						"thread": thread,
					}, streamState)
					streamErr = errors.New("streaming cancelled by user")
					streamInterrupted = true
				}
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
				}, streamState)
				streamErr = errors.New("streaming cancelled by user")
				streamInterrupted = true
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

	// A cancelled stream closes the channel without a further event, so the
	// backoff marker would otherwise outlive the call that opened it and report a
	// dead thread as parked. Uses ctx, not streamCtx, which may already be dead.
	a.releaseProviderBackoff(ctx, thread, streamState)

	// Track LLM call completion for analytics.
	llmLatencyMs := time.Since(llmCallStart).Milliseconds()
	analyticsErr := streamErr
	if streamInterrupted {
		analyticsErr = nil
	}
	a.trackLLMCallCompleted(ctx, chat, driver, llmLatencyMs, streamState.usage, analyticsErr)

	if streamState.upstreamRequestID != "" || streamState.upstreamProxymanID != "" {
		activity.GetLogger(ctx).Info("[CallLLM] Upstream correlation",
			"chatID", chat.ID,
			"thread", thread,
			"x_oai_request_id", streamState.upstreamRequestID,
			"x_proxyman_id", streamState.upstreamProxymanID)
	}

	// Handle stream error. A user/thread interrupt is not a failed LLM turn: it
	// deliberately stops the stream at the partial output already shown to the
	// user, then lets the workflow persist that partial assistant message and run
	// one more mailbox-draining turn. Provider errors still fail normally.
	if streamErr != nil && !streamInterrupted {
		return nil, streamErr
	}

	// Combine all text parts
	responseText := strings.Join(streamState.textParts, "")

	// Combine all thinking parts
	thinkingText := strings.Join(streamState.thinkingParts, "")

	// Thinking is the ONLY output field whose loss is silent: an empty
	// CallLLMOutput.Thinking is indistinguishable (proto3 omits zero values,
	// and step_executor backfills the key with an empty map) from "this turn
	// did not think". Downstream that means no thinking block is persisted and
	// none is replayed next turn, which breaks the provider's cached prefix.
	// Log what actually reached the output boundary so a future regression is
	// one grep rather than another packet capture.
	activity.GetLogger(ctx).Info("[CallLLM] Thinking captured",
		"chatID", chat.ID,
		"thread", thread,
		"thinkingLen", len(thinkingText),
		"signatureLen", len(streamState.thinkingSignature),
		"thinkingParts", len(streamState.thinkingParts),
	)

	toolCalls := streamState.toolCalls

	// Attach spawn presets to spawn tool calls for preset validation in ExecuteTools.
	// Tool-level permission enforcement is handled by execute_tools using the permission
	// level stored in LoadedToolsStore (set above), not by checking tool name lists.
	if len(spawnPresets) > 0 {
		for i := range toolCalls {
			if toolCalls[i].Name == "spawn" {
				toolCalls[i].AvailablePresets = spawnPresets
			}
		}
	}

	output := &reliantv1.CallLLMOutput{
		ResponseText:       responseText,
		ToolCalls:          messageToolCallsToProto(toolCalls),
		TokenCount:         int32(streamState.tokenCount),
		Cost:               streamState.cost,
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
		PendingInbox:        streamInterrupted,
		CompactionThreshold: effectiveCompactionThreshold,
		Model:               resolvedModelID,
		MessageId:           streamState.messageID,
		LastStreamSeq:       streamState.streamSeq,
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
	AllMCPToolNames  []string // All available MCP tool names (for deferred loading announcement)
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
func (a *CallLLMActivity) getAvailableToolsWithSpawn(ctx context.Context, chat *db.Chat, scopePath string, projectCfg *cfgpkg.Config, toolFilter []string, _ string, mailboxReachable bool) toolsWithSpawnResult {
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
		slog.Debug("[CallLLM] Injecting skills into tools factory", "skillCount", len(projectCfg.Skills))
		projectScopedToolsFactory = projectScopedToolsFactory.WithSkills(projectCfg.Skills)
		// Also store skills in the global store so the executor can access them
		// when creating skill tool instances (the executor uses a different factory).
		tools.GetLoadedToolsStore().SetSkills(chat.ID, projectCfg.Skills)
	} else {
		slog.Debug("[CallLLM] No skills available", "projectCfgNil", projectCfg == nil)
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

	// Record the connected MCP tools (name + description) so load_tool can
	// search them by keyword and verify availability before loading — enabling
	// progressive discovery of MCP tools (e.g. chrome-devtools) that aren't in
	// the static built-in registry.
	if chat != nil {
		mcpToolInfos := make([]tools.MCPToolInfo, 0, len(mcpTools))
		for _, t := range mcpTools {
			mcpToolInfos = append(mcpToolInfos, tools.MCPToolInfo{
				Name:        t.Name(),
				Description: t.Description(),
			})
		}
		tools.GetLoadedToolsStore().SetAvailableMCPTools(chat.ID, mcpToolInfos)
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

	// load_tool must be available to EVERY tool-enabled agent so any preset —
	// including restrictive read-only ones like code_reviewer whose filter omits
	// tag:default — can discover and load additional tools on demand, built-in or
	// MCP (e.g. an agent whose instructions require chrome-devtools can load
	// mcp__chrome-devtools__* even though its filter never listed them). load_tool
	// is read-only and each load is permission-gated per target tool, so granting
	// it universally never escalates privileges. Skip only if the filter already
	// pulled it in (via tag:default or an explicit entry).
	loadToolPresent := false
	for _, t := range toolsList {
		if t.Name() == tools.ToolLoadTool {
			loadToolPresent = true
			break
		}
	}
	if !loadToolPresent {
		toolsList = append(toolsList, projectScopedToolsFactory.LoadTool())
	}

	// spawn_send must reach a depth-1 sub-agent talking back to its parent
	// (spec §4.4), but the "spawn" virtual tool it travels alongside is gated
	// off entirely at max spawn depth and a sub-agent's own preset filter was
	// never written with a mailbox tool in mind. So grant it the way load_tool
	// is granted above — except only where there is actually a counterpart to
	// message (mailboxReachable), since a root agent that cannot spawn has
	// nobody to send to. It remains subject to the same permission gate every
	// other tool goes through in execute_tools (MinimumPermissionForTool is
	// PermissionMutating for spawn_send, so a readonly/plan-mode agent still
	// cannot use it even though the schema is offered).
	if mailboxReachable {
		spawnSendPresent := false
		for _, t := range toolsList {
			if t.Name() == tools.ToolSpawnSend {
				spawnSendPresent = true
				break
			}
		}
		if !spawnSendPresent {
			toolsList = append(toolsList, projectScopedToolsFactory.SpawnSend())
		}
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
		AllMCPToolNames:  mcpToolNames,
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

	description := fmt.Sprintf(`Spawn a sub-agent to delegate a task. The call returns a handle IMMEDIATELY — it does not block. The spawned agent's result is NOT in this tool call's result; you are notified in a later turn when it finishes. You stay free to read files, edit, plan, and spawn more agents while it runs, and because work discovered mid-run can join an in-flight fan-out, you do not have to plan all your parallelism up front.

While a spawned agent runs you can steer and observe it: spawn_status(agent_id, wait: true) to block until it finishes when you genuinely cannot proceed without the answer, or without wait to just check progress; spawn_send to give it new instructions mid-flight.

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

// getAskUserTool returns a schema-only tool that lets the LLM ask the user a question.
// getSystemPrompts generates system prompts for the LLM
func (a *CallLLMActivity) getSystemPrompts(
	chat *db.Chat,
	projectPath string,
	worktreePath string,
	projectCfg *cfgpkg.Config,
	repos []*core.Repo,
	systemPrompt *string,
) []string {
	// Add project context
	workingDir := projectPath
	if worktreePath != "" {
		workingDir = worktreePath
	}
	var bb strings.Builder
	bb.WriteString("You are Reliant, a world class Software Engineer with advanced reasoning and capabilities. Note: it is very likely you are working in parallel with other agents, potentially in the same directory, or across multiple git worktrees. Please be careful of other's work. Be extremely careful with destructive commands that can discard another agent's uncommitted work, such as git checkout, git stash, and git reset.")

	// The working directory is stated as an ABSOLUTE PATH and as the literal
	// root every relative path resolves against, because a thread that does
	// not know it invents one. Measured across two forge-one-shot runs: ten
	// of fifteen spawned units issued reads against `/path/to/project/...` —
	// a placeholder the model supplied itself, never emitted by reliant or
	// forge — producing eighteen File-not-found errors before they recovered.
	//
	// A spawned unit is the case that matters. It starts with no history, so
	// this prompt is the ONLY place the path appears, and "you are working in
	// a git worktree at X" reads as provenance rather than as the answer to
	// "where do I open FORGE_PLAN.md".
	if workingDir != "" {
		bb.WriteString("\n\nIMPORTANT: Your working directory is: ")
		bb.WriteString(workingDir)
		bb.WriteString("\nThis is an absolute path and it is the root of the project you are working on. ")
		bb.WriteString("Every relative path you use resolves against it, and shell commands start there. ")
		bb.WriteString("Read a project file by joining it to this root (")
		bb.WriteString(workingDir)
		bb.WriteString("/README.md) or by its plain relative path (README.md). ")
		bb.WriteString("NEVER guess a project root and never write a placeholder path such as /path/to/project — the real one is on this line.")
	}

	// Multi-repo hint. When the project has more than one repo, every fs/process
	// tool accepts a `repo` param to scope the operation. Naming the available
	// repos here keeps the LLM aware that the choice exists without a separate
	// tool call to discover them.
	if len(repos) > 1 {
		bb.WriteString("\n\nThis project has multiple nested repos. Tools that take a `repo` parameter accept these values:")
		bb.WriteString("\n- root (project root)")
		for _, r := range repos {
			if r == nil || r.Name == "" {
				continue
			}
			bb.WriteString("\n- ")
			bb.WriteString(r.Name)
			if r.RelativePath != "" && r.RelativePath != r.Name {
				bb.WriteString(" (")
				bb.WriteString(r.RelativePath)
				bb.WriteString(")")
			}
		}
		bb.WriteString("\nOmit `repo` to inherit the chat's bound worktree.")
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
	// A driver announces a provider retry BEFORE it sleeps; anything else it
	// sends is proof the provider answered. Recording on the one and releasing on
	// the other keeps the marker true at every instant, at two writes per backoff
	// episode rather than one per delta.
	if event.Type == llm.EventRetryWait {
		a.recordProviderBackoff(ctx, chatID, thread, event.Retry, state)
		return nil
	}
	a.releaseProviderBackoff(ctx, thread, state)

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

// backoffLogger is the subset of both loggers this file's backoff path uses.
type backoffLogger interface {
	Warn(msg string, keyvals ...interface{})
	Error(msg string, keyvals ...interface{})
}

// providerBackoffLogger returns the Temporal activity logger when there is one
// and the process logger otherwise. activity.GetLogger PANICS outside an
// activity context, which would make the backoff path untestable anywhere but
// in a live worker — and an observability fix that cannot be tested is not one.
func providerBackoffLogger(ctx context.Context) backoffLogger {
	if activity.IsActivity(ctx) {
		return activity.GetLogger(ctx)
	}
	return slog.Default()
}

// recordProviderBackoff writes the durable marker that says THIS thread is
// parked in a provider retry ladder, before the driver takes the wait.
//
// This is the only evidence the ladder produces. It runs inside one Temporal
// activity attempt, so nothing else moves while it waits: no message, no step
// execution, no workflow status change, nothing on the update feed. Measured on
// forge-one-shot run b7aa4056, eight of ten fan-out units spent ~113s of their
// ~129s life here and every supervisor — human and agent — read it as the model
// thinking and concluded fan-out was broken.
//
// Failures are logged, never returned: an unobservable backoff is bad, but
// failing the LLM call because its observability write failed is worse.
func (a *CallLLMActivity) recordProviderBackoff(ctx context.Context, chatID, thread string, wait *llm.RetryWait, state *streamProcessingState) {
	if wait == nil || thread == "" {
		return
	}
	log := providerBackoffLogger(ctx)
	log.Warn("[CallLLM] Provider backoff",
		"chatID", chatID,
		"thread", thread,
		"attempt", wait.Attempt,
		"maxAttempts", wait.MaxAttempts,
		"delayMs", wait.Delay.Milliseconds(),
		"statusCode", wait.StatusCode,
		"reason", wait.Reason)

	if err := a.repo.RecordProviderBackoff(ctx, chatID, thread,
		wait.Attempt, wait.MaxAttempts, wait.StatusCode, wait.Reason, wait.Delay, time.Now()); err != nil {
		log.Error("[CallLLM] Failed to record provider backoff marker",
			"chatID", chatID, "thread", thread, "error", err)
		return
	}
	state.inProviderBackoff = true
}

// releaseProviderBackoff clears an open backoff marker once the provider has
// answered. Cheap and idempotent: the flag means the DB is touched twice per
// backoff episode, not once per streamed delta.
func (a *CallLLMActivity) releaseProviderBackoff(ctx context.Context, thread string, state *streamProcessingState) {
	if state == nil || !state.inProviderBackoff {
		return
	}
	state.inProviderBackoff = false
	if err := a.repo.ClearProviderBackoff(ctx, thread, time.Now()); err != nil {
		providerBackoffLogger(ctx).Error("[CallLLM] Failed to clear provider backoff marker",
			"thread", thread, "error", err)
	}
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
	}, state)
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
	}, state)
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
		}, state)
	}

	// Append to thinking content
	state.thinkingParts[0] += event.Thinking

	// Dual-write: Write thinking_block_delta to chat_updates
	a.writeStreamingDelta(ctx, chatID, "thinking_block_delta", map[string]interface{}{
		"block_index": thinkingPos,
		"delta":       event.Thinking,
		"thread":      thread,
	}, state)
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
		}, state)
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
	}, state)
}

// handleComplete handles the completion event with token usage and final tool calls
func (a *CallLLMActivity) handleComplete(ctx context.Context, event llm.DriverEvent, state *streamProcessingState) {
	if event.Response == nil {
		return
	}

	// Collect usage from the final response for analytics, spend tracking, and compaction decisions.
	state.usage = event.Response.Usage
	state.tokenCount = int(event.Response.Usage.TokenCount)
	state.cost = event.Response.Usage.Cost

	// Extract thinking signature from the response (for multi-turn thinking preservation)
	if event.Response.ThinkingSignature != "" {
		state.thinkingSignature = event.Response.ThinkingSignature
	}

	state.upstreamRequestID = strings.TrimSpace(event.Response.UpstreamRequestID)
	state.upstreamProxymanID = strings.TrimSpace(event.Response.UpstreamProxymanID)

	// CRITICAL: Extract complete tool calls with full inputs from the final response
	// This is done here instead of EventToolUseStart because Input is empty at that point
	if len(event.Response.ToolCalls) > 0 {
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
	if event.Response.Content != "" {
		state.textParts = []string{event.Response.Content}
	}
}

// trackLLMCallCompleted fires an analytics event after each LLM API call.
func (a *CallLLMActivity) trackLLMCallCompleted(ctx context.Context, chat *db.Chat, driver llm.Driver, latencyMs int64, usage llm.TokenUsage, streamErr error) {
	model := driver.Model()

	// Per-call usage, logged next to the latency that produced it. This is the
	// only place both are in hand, and without it a stalled turn is
	// unattributable: "[CallLLM] Completed" carries a single summed
	// tokenCount, which cannot distinguish a 200k prompt served from cache
	// (fast) from the same 200k prompt re-read in full (slow). cacheReadPct is
	// the at-a-glance answer — a long thread sitting near 0% is re-reading its
	// whole prefix every turn.
	//
	// Providers whose drivers don't populate the cache split report 0; see
	// llm.TokenUsage. Logged for failures too — a stall that ends in an error
	// still tells you how much prompt was processed first.
	cachedIn := usage.CacheReadInputTokens + usage.CacheCreationInputTokens
	promptTotal := usage.InputTokens + cachedIn
	cacheReadPct := 0
	if promptTotal > 0 {
		cacheReadPct = int(usage.CacheReadInputTokens * 100 / promptTotal)
	}
	logging.Info("[CallLLM] Usage",
		"chatID", chat.ID,
		"provider", driver.Name(),
		"model", string(model.ID),
		"latencyMs", latencyMs,
		"success", streamErr == nil,
		"promptTokens", promptTotal,
		"inputTokens", usage.InputTokens,
		"outputTokens", usage.OutputTokens,
		"cacheReadTokens", usage.CacheReadInputTokens,
		"cacheCreationTokens", usage.CacheCreationInputTokens,
		"cacheReadPct", cacheReadPct,
		"totalTokens", usage.TokenCount,
	)
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
	metrics.InputTokens = int(usage.InputTokens)
	metrics.OutputTokens = int(usage.OutputTokens)
	if metrics.InputTokens == 0 && metrics.OutputTokens == 0 {
		metrics.InputTokens = int(usage.TokenCount)
	}
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

// injectSkillSuggestions scores the project's skill catalog against the last
// user message and appends the top matches to it as a system-reminder,
// returning how many were suggested.
//
// It is a NO-OP when this call preloaded skills, and that is the whole reason
// the decision lives in one named function instead of inline at the call site.
//
// The suggester reads the LAST user message and writes back into it. On a
// forked thread the preload seed IS the last user message — the brief is the
// only other user turn and it comes first, tool results carry role Tool, so the
// seed holds that position from turn one onward. Scoring skill BODIES is not a
// ranking anyone asked for: suggest.Suggest weighs a skill's own name triple and
// every body repeats its name, so the preloaded skills win their own contest.
// The reminder is then appended to the seed itself, and the model reads
// "ALREADY SATISFIED. Do not load it again" immediately followed by "use the
// skill tool to load if needed" naming those same skills. That is the stimulus
// preloadedSkillsPreamble exists to prevent, arriving from the other side.
//
// Skipping — rather than subtracting the preloaded names from the result — is
// deliberate. Subtraction removes the literal contradiction and keeps a ranking
// computed over harness prose the user never wrote, and skills the brief never
// asked for, endorsed directly beneath a block saying the loaded ones are
// right, read as a correction just as loudly. A node that declares its skills
// has already answered the question the suggester asks; the suggester is the
// fallback for a turn where nobody did.
//
// It also leaves the seed byte-identical. The seed is spliced at a stable early
// offset so the prompt cache reuses it; a catalog-dependent suffix appended to
// it breaks that prefix on every turn, and does so hardest while the catalog is
// still filling — which is exactly when the seed matters most.
func injectSkillSuggestions(history []message.Message, catalog []cfgpkg.StoredSkill, preloaded []string) int {
	if len(catalog) == 0 || len(preloaded) > 0 {
		return 0
	}
	latestUserText := getLatestUserMessageText(history)
	if latestUserText == "" {
		return 0
	}
	suggestions := suggest.Suggest(catalog, latestUserText, 5)
	if len(suggestions) == 0 {
		return 0
	}
	injectReminderIntoLastUserMessage(history, buildSkillSuggestionReminder(suggestions))
	return len(suggestions)
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
		fmt.Fprintf(&sb, "- %s: %s\n", s.Skill.Name, desc)
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

	// Runtime capability heads-up for cloud daemons: tell the model up front
	// which operations the serving daemon's sandbox cannot perform. Empty for
	// local/unknown runtimes. Appended into the user-memories block (not a new
	// message) so it rides the same prefix-cache slot.
	runtimeNote := daemonRuntimeLimitationNote(projectCfg.DaemonRuntimeType)

	if len(memories) == 0 && runtimeNote == "" {
		return ""
	}

	var sections []string
	if len(memories) > 0 {
		preamble := "# User defined rules, memories, and context\n\nYou must adhere to the user's defined rules and context below at all times.\n\n"
		sections = append(sections, preamble)
		sections = append(sections, memories...)
	}
	if runtimeNote != "" {
		sections = append(sections, runtimeNote)
	}
	return strings.Join(sections, "\n\n")
}

// formatRepoMemoryMessages converts the config's repo memories map into
// system messages, one per repo. Sorted by repo name for prefix-cache
// stability across turns.
func formatRepoMemoryMessages(repoMemories map[string]string) []message.Message {
	if len(repoMemories) == 0 {
		return nil
	}

	// Sort keys for deterministic ordering (prefix-cache stability).
	keys := make([]string, 0, len(repoMemories))
	for k := range repoMemories {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var msgs []message.Message
	for _, repo := range keys {
		content := strings.TrimSpace(repoMemories[repo])
		if content == "" {
			continue
		}
		body := fmt.Sprintf("<system-memory repo=%s>\n%s\n</system-memory>", repo, content)
		msgs = append(msgs, message.Message{
			Role:  message.System,
			Parts: []message.ContentPart{message.TextContent{Text: body}},
		})
	}
	return msgs
}

// preloadedSkillsPreamble opens the seeded turn by naming who loaded these
// skills. It is the whole reason the seed is not shaped as a tool interaction:
// a fabricated assistant(tool_call) is indistinguishable to the model from one
// it issued itself, so a unit whose brief named skills it never "called"
// concluded it had erred — measured on run b7aa4056, where two fan-out units
// spent their entire lives on "That loaded the wrong skill. Let me load the
// correct ones specified in the brief." and "I loaded the wrong skill by
// mistake." The attribution must be explicit, and it must pre-empt the specific
// wrong conclusion, not merely avoid asserting the right one.
const preloadedSkillsPreamble = `<preloaded-skills>
The Reliant harness loaded the following skills into this conversation before
your turn began. You did NOT call the skill tool for them and you do not need
to — their full contents are reproduced verbatim below, exactly as the skill
tool would have returned them.

If your instructions name any skill listed here, that instruction is ALREADY
SATISFIED. Do not load it again, and do not read its absence from your own tool
calls as a mistake you need to correct.
`

// preloadSkillMissError decides whether preloaded skills that did not resolve
// fail the call, and returns nil when the call must proceed instead.
//
// Only one cause is worth failing over, and the deciding fact is whether a
// daemon has PUSHED a config snapshot — not whether the catalog is empty.
//
// UNSYNCED is TRANSIENT: the daemon has not answered yet, so the catalog is
// empty for a reason the next attempt can fix. One run went 37 -> 86 -> 114
// entries across three consecutive turns, silently dropping `db` and
// `forge/proto` from the very turns that chose the proto surface and authored
// the migrations. Erroring here lets Temporal retry against a warm snapshot.
//
// Anything SYNCED is PERMANENT, whether the catalog is empty or merely lacks
// the path. No number of retries conjures the skill, so failing would spend the
// whole retry budget to kill a run over what is usually a charter typo. The
// caller warns instead, and the seed tells the model exactly what it did not
// get (see buildSeededSkillMessages) so it proceeds knowingly rather than
// either assuming guidance it lacks or hunting a skill that does not exist.
//
// Keying on emptiness alone is what made a config snapshot that could never
// arrive look like one that had not arrived YET: an unsynced-forever project
// failed every attempt with a message promising a retry that could not help.
// The daemon only re-sends a snapshot whose content hash CHANGED
// (runProjectWatcher), so a snapshot the server drops is never re-offered —
// there is no "wait longer" that resolves it.
func preloadSkillMissError(snapshotSynced bool, catalogSize int, requested, missing []string) error {
	if len(missing) == 0 || snapshotSynced {
		return nil
	}
	return fmt.Errorf(
		"preload skills: no daemon has pushed a config snapshot for this project yet, so none of "+
			"the %d requested skills resolved (%v) against the %d-entry catalog; retrying",
		len(requested), missing, catalogSize)
}

// buildSeededSkillMessages resolves each requested skill path against the project
// skill catalog and returns the preloaded-skill seed: a single user turn that
// reproduces each resolved skill's body verbatim under an explicit harness
// attribution (preloadedSkillsPreamble).
//
// The body comes from the skill tool's OWN resolver
// (tools.LoadSkillForInjection), so the bytes a preloaded skill delivers are
// identical to the bytes the agent would get by loading it by hand — only the
// envelope around them differs, and it differs on purpose.
//
// Skills are deduped by resolved skill name (two paths that resolve to the same
// skill inject once); empty-body / unresolvable paths are skipped.
//
// Each body is passed through tools.DeliverSkillContent — the SAME renderer the
// skill tool uses when the agent loads a skill by hand. Without it the two
// delivery paths disagree: a hand-load of an oversize skill arrives capped
// while the seed injects the whole file, so the preloaded copy is both larger
// than the model would ever get on its own and permanently resident in the
// cached prefix of every turn. Sharing the renderer keeps the seeded body
// byte-identical to the hand-loaded one, and its report makes any drop explicit
// instead of letting a skill quietly end mid-sentence.
//
// A preloaded skill that was truncated is just as unreachable as a hand-loaded
// one, so the report names the skill-tool call that fetches the remainder. The
// text is deterministic per skill, so the seeded prefix stays byte-stable and
// prompt-cacheable across turns.
//
// The turn is a USER turn, not a System one. The original reason was a bug —
// the OpenAI-compatible family had no message.System case and dropped System
// history on the floor — and that bug is now fixed in every converter. The
// choice stands on its own merits: a preloaded skill body is context the agent
// is meant to treat as its own working knowledge, and attributing it to a user
// turn (with preloadedSkillsPreamble naming who loaded it) reads correctly to
// the model, where a system note would read as an instruction it must obey.
//
// Returns the seeded messages, the list of injected skill names, the names of
// any skills that had to be truncated, and the requested paths that did NOT
// resolve.
//
// The missing list is the important return. A preload that silently delivers a
// subset is a guard that cannot fail: the node runs, the model answers, and the
// only trace is a count in an INFO line nobody reads. Measured on one run — the
// first three LLM turns of the scaffold phase logged `requested=6 injected=2
// catalogSize=37`, then 86, then 114. The catalog was still filling, and the
// turns that chose the proto surface and the migrations ran without `db` or
// `forge/proto`. Nothing failed and nothing said so.
//
// A miss has two causes that want opposite handling, and the caller classifies
// them by catalog state (preloadSkillMissError) rather than this function
// guessing. The half that lands here is the permanent one: a warm catalog that
// does not hold the path is not retryable, so the run proceeds — and the seed
// below TELLS THE MODEL what it did not get, so it works knowingly instead of
// either assuming the guidance or burning turns trying to load a skill that
// does not exist.
func buildSeededSkillMessages(projectCfg *cfgpkg.Config, skillPaths []string) (msgsOut []message.Message, injected []string, oversized []string, missing []string) {
	if len(skillPaths) == 0 {
		return nil, nil, nil, nil
	}
	// A nil or empty catalog is the cold-catalog case, not "nothing was asked
	// for" — every requested path is missing and the caller must treat it as
	// such rather than reading zero-injected as success. No seed is emitted:
	// the caller is about to fail and retry, and a notice the model never sees
	// is not worth building.
	if projectCfg == nil || len(projectCfg.Skills) == 0 {
		return nil, nil, nil, append([]string(nil), skillPaths...)
	}

	var injectedNames []string
	var oversizedNames []string
	var missingPaths []string
	seen := make(map[string]bool)

	// Deterministic order (the requested order, deduped) so the seeded turn is
	// byte-stable across turns and the Anthropic prompt cache reuses it.
	var b strings.Builder
	b.WriteString(preloadedSkillsPreamble)

	for _, path := range skillPaths {
		name, body, ok := tools.LoadSkillForInjection(projectCfg.Skills, path)
		if !ok {
			missingPaths = append(missingPaths, path)
			continue
		}
		if seen[name] {
			// Two paths resolving to the same skill inject once. Not missing.
			continue
		}
		seen[name] = true
		injectedNames = append(injectedNames, name)

		capped, wasTruncated := tools.DeliverSkillContent(path, body)
		if wasTruncated {
			oversizedNames = append(oversizedNames, name)
		}

		fmt.Fprintf(&b, "\n<skill name=%q path=%q>\n", name, path)
		b.WriteString(capped)
		b.WriteString("\n</skill>\n")
	}

	// Name what was asked for and not delivered, inside the seed the model
	// reads. Without this the model sees a preload that silently covers less
	// than its brief promised, and its two options are both bad: assume the
	// guidance it was told it had, or spend turns hunting a skill that is not
	// there. Neither is recoverable from a log line.
	//
	// The notice must also cancel the preamble for these paths. The preamble
	// says any skill listed here is ALREADY SATISFIED — true of the bodies
	// above it, and exactly the wrong conclusion for a skill that never
	// arrived, so the two must not be left to sit side by side unreconciled.
	if len(missingPaths) > 0 {
		fmt.Fprintf(&b, "\n<unavailable>\nThese skills were requested for you and are NOT available: %s.\n"+
			"Nothing was reproduced for them above, so the ALREADY SATISFIED note does not"+
			" cover them. They do not exist in this project's skill catalog, so do not try to"+
			" load them — the skill tool will fail the same way. Proceed without them and say"+
			" so if a decision genuinely needed one.\n</unavailable>\n",
			strings.Join(missingPaths, ", "))
	}

	// Past the cold-catalog gate every requested path either resolved or was
	// recorded missing, so a warm catalog always has something to say: bodies,
	// the unavailable notice, or both. There is no silent-nil case left.
	b.WriteString("</preloaded-skills>")

	return []message.Message{{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: b.String()}},
	}}, injectedNames, oversizedNames, missingPaths
}

// insertSeededMessagesAfterFirstUserTurn splices `seeded` in immediately after
// the first user turn (a User or Agent role message — Agent normalizes to User
// for the API) in history, pinning the seed to a stable early offset for
// prompt-cache reuse and placing it directly after the brief that names the
// skills. If history has no user turn to anchor to, the seed is dropped: a
// preload with nothing to attach to is the case the requested-vs-injected log
// line exists to make visible.
func insertSeededMessagesAfterFirstUserTurn(history []message.Message, seeded []message.Message) []message.Message {
	if len(seeded) == 0 {
		return history
	}
	for i, msg := range history {
		if msg.Role == message.User || msg.Role == message.Agent {
			out := make([]message.Message, 0, len(history)+len(seeded))
			out = append(out, history[:i+1]...)
			out = append(out, seeded...)
			out = append(out, history[i+1:]...)
			return out
		}
	}
	return history
}

// writeStreamingDelta publishes a streaming delta event to the in-memory hub
// This replaces the previous DB-based streaming_delta writes for better performance.
// When state carries a pre-allocated message id, the delta is stamped with that
// id and the next monotonically increasing stream_seq (delta identity
// protocol). Pass a nil state for non-message deltas (e.g. mcp_warning) —
// those stay id-less.
func (a *CallLLMActivity) writeStreamingDelta(ctx context.Context, chatID string, deltaType string, deltaData map[string]interface{}, state *streamProcessingState) {
	if a.hub == nil {
		return
	}
	// Check if context has been cancelled (via Temporal signal-based activity cancellation).
	// This prevents race conditions where streaming continues after cancellation status is emitted.
	// Allow "stream_cancelled" deltas through so the UI knows streaming stopped.
	if deltaType != "stream_cancelled" && ctx.Err() != nil {
		func() {
			defer func() { _ = recover() }() // safe outside activity context (tests)
			activity.GetLogger(ctx).Info("[STREAMING_DELTA] Dropping delta - context cancelled",
				"delta_type", deltaType,
				"chat_id", chatID)
		}()
		return
	}

	hub := a.hub

	// Build streaming delta from deltaData
	delta := streaming.StreamingDelta{
		DeltaType: streaming.DeltaType(deltaType),
	}

	// Stamp delta identity centrally: every message delta carries the
	// pre-allocated id plus a strictly increasing per-message sequence.
	if state != nil && state.messageID != "" {
		state.streamSeq++
		delta.MessageID = state.messageID
		delta.StreamSeq = state.streamSeq
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
// MESSAGE HISTORY REPAIR (in-memory, at the prompt-assembly boundary)
// ============================================================================

// repairMessageHistory rewrites a history so that every assistant tool_use has a
// matching tool_result in the message IMMEDIATELY after it — the adjacency the
// Anthropic API requires, not merely presence somewhere later.
//
// This is the ONLY repair that runs on the read path, and it is deliberately
// in-memory: it never writes to the database. It exists for orphans we cannot
// legitimately fix at rest — chiefly a fork/branch whose cut point falls between
// an assistant message and its tool message, where the inherited rows belong to
// the parent conversation and must not be rewritten. See the failure-policy note
// on convertAndRepairMessages in db_helpers.go for the full rationale.
//
// Every repair is logged. These are not routine: each one means a conversation
// reached this point in a state the schema and CleanupActivity were supposed to
// prevent, and silence here is what let that go unnoticed before.
//
// This handles:
//  1. Missing tool_results (creates synthetic ones)
//  2. Misplaced tool_results (moves them to the required adjacent position)
//  3. Partial tool_results (adds only the missing ones)
//  4. Orphaned tool_results whose tool_use is gone (drops them — a result the
//     model never asked for is itself a provider error)
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
					// Synthesize the missing result. Loud on purpose: reaching
					// here means a tool call never got a recorded outcome, and
					// the model is about to be told the outcome is unknown.
					logging.Warn("[repairMessageHistory] Synthesizing missing tool_result for orphaned tool_call",
						"tool_call_id", tc.ID,
						"tool_name", tc.Name,
						"assistant_message_id", msg.ID,
					)
					toolResults = append(toolResults, message.ToolResult{
						ToolCallID: tc.ID,
						Name:       tc.Name,
						Content:    InterruptedToolResultContent,
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

// celStringValuePtr returns a *string for the literal value of a CelString, or nil if not set/is an expr.
func celStringValuePtr(c *reliantv1.CelString) *string {
	if !model.CelStringIsSet(c) || model.CelStringIsExpr(c) {
		return nil
	}
	v := model.CelStringValue(c)
	return &v
}

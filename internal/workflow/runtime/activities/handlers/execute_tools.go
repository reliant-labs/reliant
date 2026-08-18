// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"go.temporal.io/sdk/activity"
	structpb "google.golang.org/protobuf/types/known/structpb"
)

// ============================================================================
// TYPES (strongly typed inputs/outputs)
// ============================================================================

// ============================================================================
// TOOL EXECUTION CONTEXT BUILDER
// ============================================================================

// toolExecutionContext holds loaded context for tool execution
// It encapsulates context loading and ToolRequest building
type toolExecutionContext struct {
	// Loaded entities
	chat     *db.Chat
	project  *db.Project
	worktree *rctx.WorktreeInfo

	// repos lists the project's nested repos. Threaded into ToolRequest so
	// tools with a `repo` param can resolve it without a DB call. Empty for
	// single-repo / legacy projects.
	repos []*core.Repo

	// Tool execution parameters (from input, no DB lookup needed)
	chatID     string
	thread     string
	toolName   string
	toolInput  string
	toolCallID string

	// projectPathOverride allows sub-workflows to specify a different working directory
	// When set, this path is used instead of project.Path or worktree.Path
	projectPathOverride string

	// daemonSelector specifies which daemon should execute tools.
	// Set from the workflow or node daemon field. nil means use default resolution.
	daemonSelector *toolexec.DaemonSelector
}

// loadToolExecutionContext loads all required context for tool execution
// Uses chatID and thread directly from input - no message/block lookup required
// projectPathOverride allows sub-workflows to specify a different working directory
// Returns a toolExecutionContext or an error string if loading fails
func (a *ExecuteToolsActivity) loadToolExecutionContext(
	ctx context.Context,
	chatID, thread string,
	toolName, toolInput, toolCallID string,
	projectPathOverride string,
) (*toolExecutionContext, string) {
	tec := &toolExecutionContext{
		chatID:              chatID,
		thread:              thread,
		toolName:            toolName,
		toolInput:           toolInput,
		toolCallID:          toolCallID,
		projectPathOverride: projectPathOverride,
	}

	// Load chat directly from chatID
	chat, err := a.repo.GetChat(ctx, chatID)
	if err != nil {
		return nil, fmt.Sprintf("Failed to load chat context: %v", err)
	}
	if chat.ProjectID == "" {
		return nil, "Chat has no project ID - chats must belong to a project"
	}
	tec.chat = chat

	// Load project
	project, err := a.repo.GetProject(ctx, chat.ProjectID)
	if err != nil {
		return nil, fmt.Sprintf("Failed to load project: %v", err)
	}
	tec.project = project

	// Load worktree (defaults to project path if not set or not found)
	tec.worktree = a.loadWorktreeInfo(ctx, chat, project)

	// Load nested repos. Failures degrade gracefully: tools fall back to
	// single-repo behavior when repos is empty.
	if repos, err := a.repo.ListReposByProject(ctx, project.ID); err == nil {
		tec.repos = repos
	}

	return tec, ""
}

// loadWorktreeInfo loads worktree information, defaulting to project path
func (a *ExecuteToolsActivity) loadWorktreeInfo(ctx context.Context, chat *db.Chat, project *db.Project) *rctx.WorktreeInfo {
	if chat.WorktreeID == nil || *chat.WorktreeID == "" {
		return &rctx.WorktreeInfo{ID: "", Path: project.Path}
	}

	worktree, err := a.repo.GetWorktree(ctx, *chat.WorktreeID)
	if err != nil {
		// Worktree not found, fall back to project path
		return &rctx.WorktreeInfo{ID: "", Path: project.Path}
	}

	daemonID := ""
	if worktree.DaemonID != nil {
		daemonID = *worktree.DaemonID
	}
	return &rctx.WorktreeInfo{ID: worktree.ID, Path: worktree.Path, DaemonID: daemonID}
}

// buildToolRequest creates a ToolRequest from the loaded context
func (tec *toolExecutionContext) buildToolRequest() *toolexec.ToolRequest {
	// Determine effective working directory path
	// Priority: projectPathOverride > worktree.Path > project.Path
	effectiveWorktreePath := tec.worktree.Path
	if tec.projectPathOverride != "" {
		effectiveWorktreePath = tec.projectPathOverride
	}

	return &toolexec.ToolRequest{
		ToolName:       tec.toolName,
		ToolInput:      tec.toolInput,
		ToolCallID:     tec.toolCallID,
		ContentBlockID: "", // Not required - tool calls can be ephemeral
		UserID:         tec.project.UserID,
		ChatID:         tec.chat.ID,
		ProjectID:      tec.project.ID,
		WorktreeID:     tec.worktree.ID,
		Thread:         tec.thread,
		MessageID:      "", // Not required - tool calls can be ephemeral
		ProjectPath:    tec.project.Path,
		ProjectName:    tec.project.Name,
		WorktreePath:   effectiveWorktreePath, // Uses override if set
		Timeout:        toolexec.DefaultToolTimeout,
		DaemonSelector: tec.daemonSelector,
		Repos:          tec.repos,
	}
}

// chatID returns the chat ID for status emissions
func (tec *toolExecutionContext) getChatID() string {
	return tec.chatID
}

// ============================================================================
// ACTIVITY IMPLEMENTATION
// ============================================================================

// ExecuteToolsActivity implements the execute_tools activity.
// This activity executes multiple tool calls in parallel and returns all results
type ExecuteToolsActivity struct {
	repo         db.Repository
	toolExecutor toolexec.ToolExecutor
}

// NewExecuteToolsActivity creates a new ExecuteToolsActivity
func NewExecuteToolsActivity(repo db.Repository, toolExecutor toolexec.ToolExecutor) *ExecuteToolsActivity {
	return &ExecuteToolsActivity{
		repo:         repo,
		toolExecutor: toolExecutor,
	}
}

// Name returns the activity name for registration
func (a *ExecuteToolsActivity) Name() string {
	return "ExecuteTools"
}

// DisplayName returns human-readable name for UI
func (a *ExecuteToolsActivity) DisplayName() string {
	return "Execute Tools"
}

// Description returns what the activity does
func (a *ExecuteToolsActivity) Description() string {
	return "Execute tool calls from the LLM and return results"
}

// Category returns the activity category for UI grouping
func (a *ExecuteToolsActivity) Category() schema.ActivityCategory {
	return schema.CategoryAgentic
}

// Execute contains PURE BUSINESS LOGIC only
func (a *ExecuteToolsActivity) Execute(ctx context.Context, input ActivityInput) (*reliantv1.ExecuteToolsOutput, error) {
	rtx := input.Runtime
	protoArgs := model.GetExecuteToolsArgs(input.Node)
	if protoArgs == nil {
		return nil, fmt.Errorf("expected execute_tools node, got %s", model.NodeType(input.Node))
	}

	// Convert proto ToolCallMsg to message.ToolCall
	resolvedToolCalls := protoToolCallsToMessage(protoArgs.GetResolvedToolCalls())
	expectedResponseTools := protoArgs.GetExpectedResponseTools()
	responseToolSchemas := protoStructMapToGoMap(protoArgs.GetResponseToolSchemas())

	// Resolve the permission level for tool execution enforcement.
	// This was set by call_llm when it resolved tools for the LLM request.
	grantedPermission := tools.GetLoadedToolsStore().GetPermission(rtx.ChatID)

	// Build set for O(1) response tool lookups in worker goroutines
	responseToolSet := make(map[string]bool, len(expectedResponseTools))
	for _, name := range expectedResponseTools {
		responseToolSet[name] = true
	}

	// Debug logging to trace loop context
	logging.Info("[ExecuteToolsActivity] Received input",
		"stepID", rtx.StepID,
		"loopNodeID", rtx.LoopNodeID,
		"loopIteration", rtx.LoopIteration,
		"chatID", rtx.ChatID,
		"thread", rtx.Thread,
		"toolCallCount", len(resolvedToolCalls),
	)

	// Get activity info for idempotency tracking
	activityInfo := activity.GetInfo(ctx)
	activityID := activityInfo.ActivityID
	workflowRunID := activityInfo.WorkflowExecution.RunID
	attemptNumber := int(activityInfo.Attempt)

	// Execute all tool calls in parallel using goroutines
	// This significantly improves performance when multiple tools are called together
	type toolCallJob struct {
		index    int
		toolCall message.ToolCall
	}

	type toolCallResult struct {
		index  int
		result message.ToolResult
	}

	// Create channels for work distribution and result collection
	jobs := make(chan toolCallJob, len(resolvedToolCalls))
	resultsChan := make(chan toolCallResult, len(resolvedToolCalls))

	// Limit parallelism to prevent resource exhaustion from runaway LLM responses
	// Each tool execution may spawn shell processes, open files, etc.
	const maxParallelTools = 10
	numWorkers := len(resolvedToolCalls)
	if numWorkers == 0 {
		numWorkers = 1 // Handle empty case
	}
	if numWorkers > maxParallelTools {
		numWorkers = maxParallelTools
	}

	for w := 0; w < numWorkers; w++ {
		go func() {
			for job := range jobs {
				toolCall := job.toolCall

				// Recover from panics in tool execution to prevent worker death
				func() {
					defer func() {
						if r := recover(); r != nil {
							activity.GetLogger(ctx).Error("[ExecuteTools] Panic in tool execution",
								"tool_call_id", toolCall.ID,
								"panic", r)
							resultsChan <- toolCallResult{
								index: job.index,
								result: message.ToolResult{
									ToolCallID: toolCall.ID,
									Content:    fmt.Sprintf("Internal error: tool execution panic: %v", r),
									IsError:    true,
								},
							}
						}
					}()

					// Use tool info directly from message.ToolCall - no DB lookup required
					toolName := toolCall.Name
					toolInput := toolCall.Input
					toolCallID := toolCall.ID

					// Validate required fields
					if toolName == "" || toolCallID == "" {
						resultsChan <- toolCallResult{
							index: job.index,
							result: message.ToolResult{
								ToolCallID: toolCallID,
								Content:    "incomplete tool call: missing name or id",
								IsError:    true,
							},
						}
						return
					}

					// Enforce permission-based tool access control.
					// The granted permission was set by call_llm from the workflow's permission config.
					requiredPermission := tools.MinimumPermissionForTool(toolName)
					if !tools.PermissionAtLeast(grantedPermission, requiredPermission) {
						resultsChan <- toolCallResult{
							index: job.index,
							result: message.ToolResult{
								ToolCallID: toolCallID,
								Name:       toolName,
								Content:    fmt.Sprintf("Tool '%s' requires '%s' permission, but the current permission level is '%s'.", toolName, requiredPermission, grantedPermission),
								IsError:    true,
							},
						}
						return
					}

					// For spawn tool, validate preset is in available list (if list is provided).
					if toolName == "spawn" && len(toolCall.AvailablePresets) > 0 {
						var inputMap map[string]interface{}
						if err := json.Unmarshal([]byte(toolInput), &inputMap); err == nil {
							if preset, ok := inputMap["preset"].(string); ok {
								presetAllowed := false
								for _, available := range toolCall.AvailablePresets {
									if available == preset {
										presetAllowed = true
										break
									}
								}
								if !presetAllowed {
									resultsChan <- toolCallResult{
										index: job.index,
										result: message.ToolResult{
											ToolCallID: toolCallID,
											Name:       toolName,
											Content:    fmt.Sprintf("Preset '%s' is not available. The LLM may have hallucinated this preset. Available presets: %v", preset, toolCall.AvailablePresets),
											IsError:    true,
										},
									}
									return
								}
							}
						}
					}

					// Response tools: identified via expected_response_tools list from workflow config.
					// Execute inline (return input as metadata); no external tool call.
					if responseToolSet[toolName] {
						var toolSchema map[string]interface{}
						if responseToolSchemas != nil {
							toolSchema = responseToolSchemas[toolName]
						}
						result := executeResponseToolInline(toolCallID, toolName, toolInput, toolSchema)
						resultsChan <- toolCallResult{
							index:  job.index,
							result: result,
						}
						return
					}

					// Execute the tool via normal toolexec path
					result := a.executeSingleTool(
						ctx,
						rtx.ChatID,
						rtx.Thread,
						toolName,
						toolInput,
						toolCallID,
						activityID,
						workflowRunID,
						attemptNumber,
						rtx.ProjectPath,    // Pass project path override for working directory
						rtx.DaemonSelector, // Pass daemon selector for targeted routing
					)

					resultsChan <- toolCallResult{
						index:  job.index,
						result: result,
					}
				}()
			}
		}()
	}

	// Send all jobs to workers
	for i, toolCall := range resolvedToolCalls {
		jobs <- toolCallJob{
			index:    i,
			toolCall: toolCall,
		}
	}
	close(jobs)

	// Collect results from all workers
	// Results are indexed by original position to maintain consistent ordering
	// even though tools execute in parallel
	results := make([]message.ToolResult, len(resolvedToolCalls))
	for i := 0; i < len(resolvedToolCalls); i++ {
		result := <-resultsChan
		results[result.index] = result.result
	}

	// Get current thread token count for compaction decisions
	// This allows edge conditions to check if compaction is needed
	threadTokenCount := 0
	if rtx.ChatID != "" && rtx.Thread != "" {
		contextUsage, err := a.repo.GetContextUsage(ctx, rtx.ChatID, rtx.Thread)
		if err != nil {
			activity.GetLogger(ctx).Warn("[ExecuteTools] Failed to get context usage for token count",
				"error", err)
		} else {
			threadTokenCount = int(contextUsage.ThreadTokenCount)
		}
	}

	// Cap the aggregate batch before it is returned to the workflow and saved as
	// tool_result blocks. Per-tool limits are not enough when one LLM turn asks
	// for several large but individually-valid reads.
	compactionThreshold := resolvedExecuteToolsCompactionThreshold(protoArgs)
	results, totalResultChars, batchTruncated := a.capToolResultBatch(ctx, results, compactionThreshold)
	if batchTruncated {
		activity.GetLogger(ctx).Warn("[ExecuteTools] Tool result batch exceeded budget and was truncated",
			"totalResultChars", totalResultChars,
			"batchLimitBytes", toolResultBatchLimitBytes(compactionThreshold),
			"compactionThreshold", compactionThreshold)
	}

	// Extract response data from tool results with metadata
	// This makes response tool data directly accessible without needing responseData() function
	responseData := make(map[string]interface{})

	// First, ensure expected response tools have entries (with null if not called)
	// This enforces the contract that expected keys always exist in response_data
	for _, expectedTool := range expectedResponseTools {
		responseData[expectedTool] = nil
	}

	// Then populate with actual response data from tool results
	for _, r := range results {
		if r.Metadata != "" && r.Name != "" {
			var data interface{}
			if err := json.Unmarshal([]byte(r.Metadata), &data); err == nil {
				responseData[r.Name] = data
			}
		}
	}

	// Build output with explicit Message initialization
	output := &reliantv1.ExecuteToolsOutput{
		ToolResults:      messageToolResultsToProto(results),
		ThreadTokenCount: int32(threadTokenCount),
		TotalResultChars: int32(totalResultChars),
		ResponseData:     goMapToProtoStruct(responseData),
		Message: &reliantv1.MessageOutput{
			Role: "tool",
			Text: "",
		},
	}

	activity.GetLogger(ctx).Info("[ExecuteTools] Completed",
		"toolResultsCount", len(output.ToolResults),
		"threadTokenCount", threadTokenCount,
		"totalResultChars", totalResultChars)

	return output, nil
}

// ============================================================================
// HELPER METHODS
// ============================================================================

// executeSingleTool executes a single tool and returns the result
func (a *ExecuteToolsActivity) executeSingleTool(
	ctx context.Context,
	chatID string,
	thread string,
	toolName string,
	toolInput string,
	toolCallID string,
	activityID string,
	workflowRunID string,
	attemptNumber int,
	projectPath string, // Override working directory for sub-workflows
	daemonSel *types.DaemonSelector, // Target daemon selector (optional)
) message.ToolResult {
	// Idempotency: a call that already reached a terminal status in a prior
	// dispatch of this same tool_call_id must not run again -- see
	// checkPriorTerminalResult.
	if result, ok := a.checkPriorTerminalResult(ctx, toolCallID, toolName); ok {
		return result
	}

	// Idempotency, the other half: a Temporal ACTIVITY RETRY.
	//
	// checkPriorTerminalResult cannot cover this one. A worker that died
	// mid-tool (crash, OOM, heartbeat timeout) never wrote a terminal row, so
	// the call is still EXECUTING -- deliberately not terminal, because that is
	// also what a legitimately-running call looks like. Temporal re-delivers
	// the SAME activity task with Attempt incremented, and without this the
	// tool runs a second time.
	//
	// Tools are not idempotent, so a redelivered attempt must report what
	// happened rather than repeat it. `ExecuteRunStep` has refused retries this
	// way for shell commands since long before this (run_step.go); this closes
	// the same hole for every tool.
	//
	// Unlike run_step, this does NOT return an error. Failing here would burn
	// the remaining attempts (MaximumAttempts: 5) and eventually kill the step,
	// when the honest outcome is a completed activity carrying an error
	// tool_result: the loop advances, and the model is told the tool was
	// interrupted so it can decide whether to try again.
	if isActivityRetry(attemptNumber) {
		activity.GetLogger(ctx).Warn("[ExecuteTools] Activity retry detected; not re-executing the tool",
			"tool_call_id", toolCallID,
			"tool_name", toolName,
			"attempt", attemptNumber)
		return a.buildToolResult(toolCallID, toolName, InterruptedToolResultContent, "", true, nil)
	}

	// Check for cancellation before starting work
	if ctx.Err() != nil {
		return a.buildToolResult(toolCallID, toolName, fmt.Sprintf("Cancelled: %v", ctx.Err()), "", true, nil)
	}

	// Validate tool input JSON
	var inputMap map[string]interface{}
	if err := json.Unmarshal([]byte(toolInput), &inputMap); err != nil {
		return a.buildToolResult(toolCallID, toolName, fmt.Sprintf("Failed to parse tool inputs: %v", err), "", true, nil)
	}

	// Load execution context (chat -> project -> worktree)
	// projectPath override allows sub-workflows to run tools in a different directory
	tec, errMsg := a.loadToolExecutionContext(ctx, chatID, thread, toolName, toolInput, toolCallID, projectPath)
	if errMsg != "" {
		return a.buildToolResult(toolCallID, toolName, errMsg, "", true, nil)
	}

	// Daemon routing priority: explicit node/workflow selector > the worktree's
	// owning daemon > default resolution. A worktree-bound (e.g. branch) chat
	// must run on the daemon that has its checkout on disk; a nil worktree
	// DaemonID (main checkout / legacy rows) leaves routing at the default.
	if tec.worktree != nil && tec.worktree.DaemonID != "" {
		tec.daemonSelector = &toolexec.DaemonSelector{ID: tec.worktree.DaemonID}
	}
	if daemonSel != nil {
		tec.daemonSelector = &toolexec.DaemonSelector{
			ID:     daemonSel.ID,
			Name:   daemonSel.Name,
			Type:   daemonSel.Type,
			Labels: daemonSel.Labels,
		}
	}

	// The call is about to enter real execution -- record it durably as
	// PENDING before dispatch so a reload mid-execution sees at least this
	// much instead of nothing. Calls that never reach here (response tools,
	// permission/preset rejections) have no chat_updates emission today
	// either, so they get no durable row -- consistent with "persist where
	// status transitions already happen."
	a.upsertToolCall(ctx, tec, core.ToolCallStatusPending, toolCallUpsertOpts{})

	// Execute tool with status tracking
	return a.executeToolWithStatus(ctx, tec)
}

// checkPriorTerminalResult is the fix for the interrupt livelock (chat
// b7cd65c6, specs/interrupt-pause-spec.md #2): a re-dispatched step runs in a
// FRESH, uncancelled context (ThreadInterrupt mints a new WithCancel per
// epoch), so the ctx.Err() short-circuit above never fires on re-entry and a
// tool would otherwise run again from scratch -- restarting a blocking call
// like spawn_status(wait:true) and starving the mailbox for as long as the
// wait takes, every time.
//
// Tools are not idempotent, so a call that already reached a TERMINAL status
// (Completed/Failed/Cancelled -- see core.ToolCallStatus.IsTerminal) on a
// prior dispatch must never execute again. It returns its recorded outcome
// instead, exactly as if the context were still the cancelled one that
// produced that row. Backgrounded is deliberately excluded: the process is
// still running and owes a real outcome later, so treating it as settled here
// would abandon it.
//
// A call with no row, or a non-terminal (Pending/Executing) row, executes
// normally -- this must not break ordinary retries.
func (a *ExecuteToolsActivity) checkPriorTerminalResult(ctx context.Context, toolCallID, toolName string) (message.ToolResult, bool) {
	call, err := a.repo.GetToolCall(ctx, toolCallID)
	if err != nil || call == nil || !call.Status.IsTerminal() {
		return message.ToolResult{}, false
	}

	activity.GetLogger(ctx).Info("[ExecuteTools] Tool call already terminal, returning recorded result instead of re-executing",
		"tool_call_id", toolCallID,
		"status", call.Status)

	result, err := a.repo.GetToolCallResult(ctx, toolCallID)
	if err != nil || result == nil {
		// Terminal row, but no result content survived (e.g. a historical
		// Cancelled row that predates durable status). Same stub every other
		// dangling-tool-call repair path uses -- do NOT re-execute.
		return a.buildToolResult(toolCallID, toolName, InterruptedToolResultContent, "", true, nil), true
	}
	return a.buildToolResult(toolCallID, toolName, result.Content, "", result.IsError, nil), true
}

// executeToolWithStatus handles tool execution with proper status emissions
func (a *ExecuteToolsActivity) executeToolWithStatus(ctx context.Context, tec *toolExecutionContext) message.ToolResult {
	toolCallID := tec.toolCallID
	toolName := tec.toolName

	// Emit "executing" status before starting
	a.emitToolStatus(ctx, tec.getChatID(), toolCallID, toolName, "executing")
	startedAt := time.Now()
	a.upsertToolCall(ctx, tec, core.ToolCallStatusExecuting, toolCallUpsertOpts{startedAt: &startedAt})

	// Execute the tool
	execResult, execErr := a.toolExecutor.ExecuteTool(ctx, tec.buildToolRequest())

	// Handle execution result
	return a.handleToolExecutionResult(ctx, tec, execResult, execErr, startedAt)
}

// handleToolExecutionResult processes the tool execution result and emits appropriate status
func (a *ExecuteToolsActivity) handleToolExecutionResult(
	ctx context.Context,
	tec *toolExecutionContext,
	execResult *toolexec.ToolResult,
	execErr error,
	startedAt time.Time,
) message.ToolResult {
	toolCallID := tec.toolCallID
	toolName := tec.toolName
	chatID := tec.getChatID()

	// Cancelled during execution -- but only if the tool did not already
	// finish. A dead context is not evidence about THIS tool: all of a turn's
	// tool calls run as parallel goroutines sharing one activity context, so a
	// cancellation aimed at one sibling arrives here for every other. Checking
	// ctx.Err() first, before looking at the result already in hand, meant a
	// tool that had completed successfully was reported to the user as
	// cancelled and had its real output thrown away.
	//
	// So a finished execution is reported on its own merits, and cancellation
	// only decides the outcome of a call that has no outcome of its own. The
	// per-tool cancel signal below still distinguishes "this tool was the
	// cancellation target" from "a sibling was."
	if ctx.Err() != nil && (execResult == nil || !execResult.Success) {
		a.emitToolStatus(ctx, chatID, toolCallID, toolName, "cancelled")
		completedAt := time.Now()
		result := a.buildToolResult(toolCallID, toolName, fmt.Sprintf("Tool execution cancelled: %v", ctx.Err()), "", true, nil)
		a.upsertTerminalToolCall(ctx, tec, core.ToolCallStatusCancelled, toolCallUpsertOpts{
			startedAt:    &startedAt,
			completedAt:  &completedAt,
			errorMessage: ctx.Err().Error(),
		}, &toolCallResultWrite{content: result.Content, isError: true})
		return result
	}

	// Check for execution error
	if execErr != nil {
		a.emitToolStatus(ctx, chatID, toolCallID, toolName, "failed")
		result := a.buildToolResult(toolCallID, toolName, fmt.Sprintf("Tool execution failed: %v", execErr), "", true, nil)
		completedAt := time.Now()
		a.upsertTerminalToolCall(ctx, tec, core.ToolCallStatusFailed, toolCallUpsertOpts{
			startedAt:    &startedAt,
			completedAt:  &completedAt,
			errorMessage: execErr.Error(),
		}, &toolCallResultWrite{content: result.Content, isError: true})
		return result
	}

	// Check if tool was backgrounded
	if execResult.Backgrounded {
		a.emitToolStatus(ctx, chatID, toolCallID, toolName, "backgrounded")
		a.upsertToolCall(ctx, tec, core.ToolCallStatusBackgrounded, toolCallUpsertOpts{startedAt: &startedAt})
		return a.buildToolResult(toolCallID, toolName, execResult.Content, execResult.Metadata, false, execResult.BinaryParts)
	}

	// The tool ran. Decide the outcome BEFORE announcing it.
	isError := !execResult.Success || execResult.IsError
	result := a.buildToolResult(toolCallID, toolName, execResult.Content, execResult.Metadata, isError, execResult.BinaryParts)

	status := core.ToolCallStatusCompleted
	errMsg := ""
	if isError {
		status = core.ToolCallStatusFailed
		errMsg = result.Content
	}

	// Announce the SAME outcome that is about to be written durably.
	//
	// This used to emit "completed" unconditionally, before computing status,
	// and a tool whose result was an error then wrote Failed to the row. The
	// two channels disagreed, and the UI reads the live one first and the
	// durable one on reload -- so a cancelled or failed tool rendered green
	// while the user watched and orange when they came back to the chat. The
	// durable row was right both times; the event was lying.
	a.emitToolStatus(ctx, chatID, toolCallID, toolName, toolStatusEvent(status))

	// Emit refetch signal for file-mutating tools so frontend updates without polling
	if isFileMutatingTool(toolName) {
		a.emitWorktreeRefetch(ctx, tec)
	}
	completedAt := time.Now()
	a.upsertTerminalToolCall(ctx, tec, status, toolCallUpsertOpts{
		startedAt:    &startedAt,
		completedAt:  &completedAt,
		errorMessage: errMsg,
	}, &toolCallResultWrite{content: result.Content, isError: isError})

	return result
}

// toolCallResultWrite is the result half of a terminal tool-call write.
type toolCallResultWrite struct {
	content string
	isError bool
}

// upsertTerminalToolCall writes a tool call's terminal STATUS and its RESULT as
// one transaction.
//
// These were two independent best-effort writes, and the gap between them is a
// real failure mode rather than a theoretical one. The two rows answer the same
// question — "how did this tool call end?" — and either half committing alone
// produces a lie:
//
//   - status committed, result lost  -> the call reads as finished with no
//     output, and repairMessageHistory later synthesizes "outcome unknown" for
//     a tool that actually succeeded.
//   - result committed, status lost  -> the call reads as still EXECUTING
//     forever while its answer sits in the database, which is how a completed
//     spawn_status call (status=3, result written 20:30:32.786) left its parent
//     unable to see that its sub-agent was still running.
//
// RunTx is re-entrant, so this joins an ambient transaction when one exists and
// opens its own otherwise. Still best-effort overall — a tool call must not
// fail because its bookkeeping did — but now the bookkeeping is all-or-nothing
// instead of half-applied.
//
// The detached context is preserved for exactly the reason the two writes had
// it individually: a TERMINAL write happens on the paths where the request
// context is most likely already dead (cancellation, termination, timeout), and
// using a cancelled context there leaves the row stuck at EXECUTING forever.
func (a *ExecuteToolsActivity) upsertTerminalToolCall(
	ctx context.Context,
	tec *toolExecutionContext,
	status core.ToolCallStatus,
	opts toolCallUpsertOpts,
	res *toolCallResultWrite,
) {
	detached, cancel := detachedForTerminalWrite(ctx)
	defer cancel()

	if err := a.repo.RunTx(detached, func(txCtx context.Context) error {
		if err := a.upsertToolCallTx(txCtx, tec, status, opts); err != nil {
			return err
		}
		if res == nil {
			return nil
		}
		now := time.Now()
		return a.repo.UpsertToolCallResult(txCtx, &core.ToolCallResult{
			ToolCallID: tec.toolCallID,
			Content:    res.content,
			IsError:    res.isError,
			CreatedAt:  now,
			UpdatedAt:  now,
		})
	}); err != nil {
		// Best-effort: never fail the tool call over its own bookkeeping. The
		// reconciler's stranded-spawn sweeps and the history loader's
		// recoverPersistedToolResults both repair from whatever did land.
		activity.GetLogger(ctx).Error("[TOOL_STATUS] Failed to persist terminal tool call status+result",
			"error", err,
			"tool_call_id", tec.toolCallID,
			"status", status)
	}
}

// buildToolResult creates a message.ToolResult without saving to database
func (a *ExecuteToolsActivity) buildToolResult(
	toolCallID string,
	toolName string,
	content string,
	metadata string,
	isError bool,
	binaryParts []message.BinaryContent,
) message.ToolResult {
	// Ensure content is never empty
	if content == "" {
		if isError {
			content = "Tool execution failed with no error message"
		} else {
			content = "Tool executed successfully with no output"
		}
	}

	return message.ToolResult{
		ToolCallID:  toolCallID,
		Name:        toolName,
		Content:     content,
		Metadata:    metadata,
		IsError:     isError,
		BinaryParts: binaryParts,
	}
}

// emitToolStatus emits a tool execution status update to chat_updates so the
// UI can show real-time tool execution progress. Keyed by the LLM tool-call id
// — the only identifier that exists both while the call is still streaming and
// after its message has been persisted under fresh block UUIDs.
func (a *ExecuteToolsActivity) emitToolStatus(ctx context.Context, chatID, toolCallID, toolName, status string) {
	update := db.ToolCallUpdate{
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Status:     db.ToolCallStatus(status),
		Timestamp:  time.Now().Format(time.RFC3339),
	}

	// A final status is emitted on exactly the paths where the context is most
	// likely already dead — the same reasoning as detachedForTerminalWrite,
	// which protects the durable row. The live event needs it too: a tool that
	// finished while a SIBLING was being cancelled would otherwise have its
	// "completed" event dropped with "context canceled", leaving the UI on a
	// spinner until a reload read the (correct) durable status.
	if isTerminalToolStatusEvent(status) {
		detached, cancel := detachedForTerminalWrite(ctx)
		defer cancel()
		ctx = detached
	}

	// Create chat_update (best-effort, don't fail on error)
	if err := a.repo.EmitToolCallUpdate(ctx, chatID, update); err != nil {
		// Log error but continue - status updates are best-effort
		activity.GetLogger(ctx).Error("[TOOL_STATUS] Failed to create tool status update",
			"error", err,
			"tool_call_id", toolCallID,
			"status", status)
	} else {
		activity.GetLogger(ctx).Info("[TOOL_STATUS] Emitted tool status update",
			"tool_call_id", toolCallID,
			"status", status)
	}
}

// isActivityRetry reports whether this delivery of the activity task is a
// REDELIVERY of one that already ran, rather than its first attempt.
//
// Temporal's Attempt is 1-indexed, so anything above 1 means a previous
// attempt started and did not report a result — a worker crash, an OOM, or a
// heartbeat timeout. The tool may have done all, some or none of its work, and
// since tools are not idempotent the only safe answer is to report rather than
// repeat.
//
// This is deliberately separate from a loop re-dispatch, which is NOT a retry:
// that arrives as a brand-new activity at attempt 1, and is handled by
// checkPriorTerminalResult keyed on the durable row.
func isActivityRetry(attemptNumber int) bool {
	return attemptNumber > 1
}

// toolStatusEvent maps a durable core.ToolCallStatus to the chat_updates
// status string that names the same outcome.
//
// It exists so the live event and the durable row cannot drift: the caller
// computes the status once and derives both from it, instead of writing one
// and hand-picking a string for the other. The two vocabularies are separate
// (see isTerminalToolStatusEvent) and this is the single point of translation.
func toolStatusEvent(status core.ToolCallStatus) string {
	switch status {
	case core.ToolCallStatusCompleted:
		return "completed"
	case core.ToolCallStatusFailed:
		return "failed"
	case core.ToolCallStatusCancelled:
		return "cancelled"
	case core.ToolCallStatusBackgrounded:
		return "backgrounded"
	default:
		return "executing"
	}
}

// isTerminalToolStatusEvent reports whether a chat_updates status string names
// an outcome the tool will not move off. These are the strings emitToolStatus's
// callers pass, not core.ToolCallStatus values — the event stream has its own
// vocabulary, and "backgrounded" is deliberately absent from both: the process
// is still running and owes a real outcome later.
func isTerminalToolStatusEvent(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

// toolCallUpsertOpts carries the optional, status-dependent fields for
// upsertToolCall. Zero value means "leave unset."
type toolCallUpsertOpts struct {
	startedAt    *time.Time
	completedAt  *time.Time
	errorMessage string
}

// upsertToolCall persists a durable tool_calls row alongside the transient
// chat_updates event emitted by emitToolStatus. Best-effort: a failure here
// must never fail the tool call itself, matching emitToolStatus's contract.
// terminalWriteTimeout bounds the detached write below. Short: this is a single
// upsert on a primary key, and the activity is already finishing.
const terminalWriteTimeout = 5 * time.Second

// detachedForTerminalWrite returns a context that survives cancellation of its
// parent, carrying the parent's values (Temporal's activity logger, tx handles)
// but not its Done channel.
//
// A tool call's TERMINAL status is written on exactly the paths where the
// request context is most likely to be dead: user cancellation, workflow
// termination, activity timeout. Using the cancelled context there means the
// write fails with "context canceled" and the row stays at EXECUTING forever —
// the UI then shows a spinner on a tool that finished hours ago. This was
// observed 34 times in one worker log, leaving 46 tool calls stuck, 38 of which
// had already written their result content block.
//
// The result and the status must agree, so the same treatment applies to both.
func detachedForTerminalWrite(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), terminalWriteTimeout)
}

func (a *ExecuteToolsActivity) upsertToolCall(ctx context.Context, tec *toolExecutionContext, status core.ToolCallStatus, opts toolCallUpsertOpts) {
	// Terminal statuses must be recorded even when the caller's context is
	// already cancelled; see detachedForTerminalWrite.
	if status.IsTerminal() {
		detached, cancel := detachedForTerminalWrite(ctx)
		defer cancel()
		ctx = detached
	}

	now := time.Now()
	call := &core.ToolCall{
		ID:          tec.toolCallID,
		ChatID:      tec.getChatID(),
		ToolName:    tec.toolName,
		Input:       toolInputToJSON(tec.toolInput),
		Status:      status,
		StartedAt:   opts.startedAt,
		CompletedAt: opts.completedAt,
		RequestedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if opts.errorMessage != "" {
		call.ErrorMessage = &opts.errorMessage
	}

	if err := db.UpsertToolCallStatus(ctx, a.repo, call); err != nil {
		activity.GetLogger(ctx).Error("[TOOL_STATUS] Failed to persist tool call",
			"error", err,
			"tool_call_id", tec.toolCallID,
			"status", status)
	}
}

// upsertToolCallTx is upsertToolCall's write, minus the context handling and
// error swallowing, so it can participate in a caller's transaction. The
// caller owns the detached context and the best-effort policy; this returns the
// error so a failure can roll the status and result back together.
func (a *ExecuteToolsActivity) upsertToolCallTx(ctx context.Context, tec *toolExecutionContext, status core.ToolCallStatus, opts toolCallUpsertOpts) error {
	now := time.Now()
	call := &core.ToolCall{
		ID:          tec.toolCallID,
		ChatID:      tec.getChatID(),
		ToolName:    tec.toolName,
		Input:       toolInputToJSON(tec.toolInput),
		Status:      status,
		StartedAt:   opts.startedAt,
		CompletedAt: opts.completedAt,
		RequestedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if opts.errorMessage != "" {
		call.ErrorMessage = &opts.errorMessage
	}
	return db.UpsertToolCallStatus(ctx, a.repo, call)
}

// upsertToolCallResult persists the durable tool_call_results row. Content is
// the same string placed in the tool_result content block the LLM sees --
// the durable record and the live conversation must agree.
func (a *ExecuteToolsActivity) upsertToolCallResult(ctx context.Context, toolCallID, content string, isError bool) {
	// A result is only ever written alongside a terminal status, so it needs the
	// same survival guarantee: a row whose status committed but whose result did
	// not reads as "finished with no output".
	detached, cancel := detachedForTerminalWrite(ctx)
	defer cancel()
	ctx = detached

	now := time.Now()
	result := &core.ToolCallResult{
		ToolCallID: toolCallID,
		Content:    content,
		IsError:    isError,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := a.repo.UpsertToolCallResult(ctx, result); err != nil {
		activity.GetLogger(ctx).Error("[TOOL_STATUS] Failed to persist tool call result",
			"error", err,
			"tool_call_id", toolCallID)
	}
}

// toolInputToJSON returns the raw tool input as jsonb-storable bytes, or nil
// if the input is absent or not valid JSON -- input is a best-effort record,
// not something worth failing a tool call over.
func toolInputToJSON(input string) []byte {
	if input == "" || !json.Valid([]byte(input)) {
		return nil
	}
	return []byte(input)
}

// ============================================================================
// RESPONSE TOOL HELPERS
// ============================================================================

// executeResponseToolInline executes a response tool without going through toolexec.
// Response tools simply return their input as both content and metadata, making
// the structured data available to the workflow via response_data.
//
// If a schema is provided, the input is validated against it before returning.
// This catches LLM errors where required fields are missing from structured responses.
// Stringified array/object values (a model failure mode where e.g. an array is
// emitted as a JSON-encoded string) are repaired in place before failing — see
// internal/llm/tools/schema_repair.go.
//
// This mirrors the logic in internal/llm/tools/response_tool.go:Run()
func executeResponseToolInline(toolCallID, toolName, toolInput string, schema map[string]interface{}) message.ToolResult {
	// Parse the input to validate it's proper JSON
	var input map[string]interface{}
	if err := json.Unmarshal([]byte(toolInput), &input); err != nil {
		return message.ToolResult{
			ToolCallID: toolCallID,
			Name:       toolName,
			Content:    "Invalid JSON input: " + err.Error(),
			IsError:    true,
		}
	}

	// Validate against schema if provided, repairing stringified values.
	if schema != nil {
		repairedInput, err := validateResponseToolData(toolName, toolInput, schema)
		if err != nil {
			logging.Warn("[ExecuteTools] Response tool data failed schema validation",
				"tool", toolName,
				"error", err,
				"input", toolInput)
			return message.ToolResult{
				ToolCallID: toolCallID,
				Name:       toolName,
				Content:    fmt.Sprintf("Response tool schema validation failed: %v", err),
				IsError:    true,
			}
		}
		if repairedInput != toolInput {
			// A repair fired — re-parse so the repaired values (not the
			// stringified originals) flow into content/metadata/response_data.
			if err := json.Unmarshal([]byte(repairedInput), &input); err != nil {
				return message.ToolResult{
					ToolCallID: toolCallID,
					Name:       toolName,
					Content:    "Invalid JSON input after repair: " + err.Error(),
					IsError:    true,
				}
			}
		}
	}

	// Return the input as-is - this makes the structured data available
	// to the workflow through both content and metadata
	responseJSON, err := json.Marshal(input)
	if err != nil {
		return message.ToolResult{
			ToolCallID: toolCallID,
			Name:       toolName,
			Content:    "Failed to serialize response: " + err.Error(),
			IsError:    true,
		}
	}

	// Return JSON as both content and metadata
	// This allows workflows to access it via:
	// - nodes.<node_id>.tool_results[*].content (as JSON string)
	// - nodes.<node_id>.response_data.<tool_name> (as parsed object)
	return message.ToolResult{
		ToolCallID: toolCallID,
		Name:       toolName,
		Content:    string(responseJSON),
		Metadata:   string(responseJSON), // Metadata is the key for response_data extraction
		IsError:    false,
	}
}

// validateResponseToolData validates JSON data against a JSON Schema,
// repairing stringified array/object values before failing (shared helper in
// internal/llm/tools/schema_repair.go). Returns the (possibly repaired) JSON
// string; a nil error means the returned JSON validates against the schema.
func validateResponseToolData(toolName, jsonStr string, schema map[string]interface{}) (string, error) {
	// Convert schema map to JSON bytes
	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return jsonStr, fmt.Errorf("failed to marshal schema: %w", err)
	}
	return tools.ValidateJSONWithRepair(toolName, jsonStr, schemaBytes)
}

// fileMutatingTools is the set of tool names that can modify files on disk.
var fileMutatingTools = map[string]bool{
	"write":        true,
	"edit":         true,
	"bash":         true,
	"move_code":    true,
	"find_replace": true,
	"insert_at":    true,
	"edit_lines":   true,
}

func isFileMutatingTool(toolName string) bool {
	return fileMutatingTools[toolName]
}

type toolCallProtoMetadata struct {
	AvailablePresets []string `json:"available_presets,omitempty"`
	SpawnWorkflow    string   `json:"spawn_workflow,omitempty"`
}

type toolCallProtoEnvelope struct {
	Input    string                 `json:"input"`
	Metadata *toolCallProtoMetadata `json:"__reliant_tool_meta__,omitempty"`
}

func encodeToolCallInputForProto(toolCall message.ToolCall) string {
	metadata := &toolCallProtoMetadata{
		AvailablePresets: toolCall.AvailablePresets,
		SpawnWorkflow:    toolCall.SpawnWorkflow,
	}
	if len(metadata.AvailablePresets) == 0 && metadata.SpawnWorkflow == "" {
		return toolCall.Input
	}
	envelope := toolCallProtoEnvelope{
		Input:    toolCall.Input,
		Metadata: metadata,
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return toolCall.Input
	}
	return string(encoded)
}

func decodeToolCallInputFromProto(encodedInput string) (string, *toolCallProtoMetadata) {
	var envelope toolCallProtoEnvelope
	if err := json.Unmarshal([]byte(encodedInput), &envelope); err != nil {
		return encodedInput, nil
	}
	if envelope.Metadata == nil {
		return encodedInput, nil
	}
	return envelope.Input, envelope.Metadata
}

// protoToolCallsToMessage converts proto ToolCallMsg slice to message.ToolCall slice.
func protoToolCallsToMessage(protoTCs []*reliantv1.ToolCallMsg) []message.ToolCall {
	if protoTCs == nil {
		return nil
	}
	result := make([]message.ToolCall, len(protoTCs))
	for i, tc := range protoTCs {
		decodedInput, metadata := decodeToolCallInputFromProto(tc.GetInput())
		result[i] = message.ToolCall{
			ID:    tc.GetId(),
			Name:  tc.GetName(),
			Input: decodedInput,
		}
		if metadata != nil {
			result[i].AvailablePresets = metadata.AvailablePresets
			result[i].SpawnWorkflow = metadata.SpawnWorkflow
		}
	}
	return result
}

// protoToolResultsToMessage converts proto ToolResultMsg slice to message.ToolResult slice.
func protoToolResultsToMessage(protoTRs []*reliantv1.ToolResultMsg) []message.ToolResult {
	if protoTRs == nil {
		return nil
	}
	result := make([]message.ToolResult, len(protoTRs))
	for i, tr := range protoTRs {
		result[i] = message.ToolResult{
			ToolCallID: tr.GetToolCallId(),
			Name:       tr.GetName(),
			Content:    tr.GetContent(),
			IsError:    tr.GetIsError(),
		}
	}
	return result
}

// messageToolCallsToProto converts message.ToolCall slice to proto ToolCallMsg slice.
func messageToolCallsToProto(toolCalls []message.ToolCall) []*reliantv1.ToolCallMsg {
	if toolCalls == nil {
		return nil
	}
	result := make([]*reliantv1.ToolCallMsg, len(toolCalls))
	for i, tc := range toolCalls {
		result[i] = &reliantv1.ToolCallMsg{
			Id:    tc.ID,
			Name:  tc.Name,
			Input: encodeToolCallInputForProto(tc),
		}
	}
	return result
}

// messageToolResultsToProto converts message.ToolResult slice to proto ToolResultMsg slice.
func messageToolResultsToProto(toolResults []message.ToolResult) []*reliantv1.ToolResultMsg {
	if toolResults == nil {
		return nil
	}
	result := make([]*reliantv1.ToolResultMsg, len(toolResults))
	for i, tr := range toolResults {
		result[i] = &reliantv1.ToolResultMsg{
			ToolCallId: tr.ToolCallID,
			Name:       tr.Name,
			Content:    strings.ToValidUTF8(tr.Content, "\uFFFD"),
			IsError:    tr.IsError,
		}
	}
	return result
}

// protoStructMapToGoMap converts map[string]*structpb.Struct to map[string]map[string]interface{}.
func protoStructMapToGoMap(protoMap map[string]*structpb.Struct) map[string]map[string]interface{} {
	if protoMap == nil {
		return nil
	}
	result := make(map[string]map[string]interface{}, len(protoMap))
	for k, v := range protoMap {
		if v != nil {
			result[k] = v.AsMap()
		}
	}
	return result
}

// goMapToProtoStruct converts map[string]interface{} into *structpb.Struct.
func goMapToProtoStruct(data map[string]interface{}) *structpb.Struct {
	if data == nil {
		return nil
	}
	result, err := structpb.NewStruct(data)
	if err != nil {
		logging.Warn("[ExecuteTools] Failed to convert response_data to proto struct", "error", err)
		return nil
	}
	return result
}

// emitWorktreeRefetch emits a refetch signal for worktree changes after a file-mutating tool completes.
func (a *ExecuteToolsActivity) emitWorktreeRefetch(ctx context.Context, tec *toolExecutionContext) {
	if tec.chat == nil {
		return
	}

	worktreeID := ""
	if tec.worktree != nil {
		worktreeID = tec.worktree.ID
	}

	var worktreePtr *string
	if worktreeID != "" {
		worktreePtr = &worktreeID
	}

	var projectPtr *string
	if tec.project != nil && tec.project.ID != "" {
		projectPtr = &tec.project.ID
	}

	err := a.repo.EmitUserRefetch(ctx, tec.chat.UserID, db.RefetchWorktreeChanges, db.RefetchOpts{
		ProjectID:  projectPtr,
		WorktreeID: worktreePtr,
	})
	if err != nil {
		logging.Warn("[ExecuteTools] Failed to emit worktree refetch", "error", err)
	}
}

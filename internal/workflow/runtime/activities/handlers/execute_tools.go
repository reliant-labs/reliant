// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	gojsonschema "github.com/google/jsonschema-go/jsonschema"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
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

	return &rctx.WorktreeInfo{ID: worktree.ID, Path: worktree.Path}
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
		Timeout:        5 * time.Minute,
		DaemonSelector: tec.daemonSelector,
		Repos:          tec.repos,
	}
}

// chatID returns the chat ID for status emissions
func (tec *toolExecutionContext) getChatID() string {
	return tec.chatID
}

// toolCallID returns the tool call ID for status emissions (used when no block exists)
func (tec *toolExecutionContext) getToolCallID() string {
	return tec.toolCallID
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

	// Calculate total result characters for filtering decisions
	totalResultChars := 0
	for _, r := range results {
		totalResultChars += len(r.Content)
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

	// Set daemon selector for targeted routing
	if daemonSel != nil {
		tec.daemonSelector = &toolexec.DaemonSelector{
			ID:     daemonSel.ID,
			Name:   daemonSel.Name,
			Type:   daemonSel.Type,
			Labels: daemonSel.Labels,
		}
	}

	// Execute tool with status tracking
	return a.executeToolWithStatus(ctx, tec)
}

// executeToolWithStatus handles tool execution with proper status emissions
func (a *ExecuteToolsActivity) executeToolWithStatus(ctx context.Context, tec *toolExecutionContext) message.ToolResult {
	toolCallID := tec.toolCallID
	toolName := tec.toolName

	// Emit "executing" status before starting
	// Use toolCallID as the entity ID since we may not have a content block
	a.emitToolStatus(ctx, tec.getChatID(), tec.getToolCallID(), toolCallID, toolName, "executing")

	// Execute the tool
	execResult, execErr := a.toolExecutor.ExecuteTool(ctx, tec.buildToolRequest())

	// Handle execution result
	return a.handleToolExecutionResult(ctx, tec, execResult, execErr)
}

// handleToolExecutionResult processes the tool execution result and emits appropriate status
func (a *ExecuteToolsActivity) handleToolExecutionResult(
	ctx context.Context,
	tec *toolExecutionContext,
	execResult *toolexec.ToolResult,
	execErr error,
) message.ToolResult {
	toolCallID := tec.toolCallID
	toolName := tec.toolName
	chatID := tec.getChatID()
	entityID := tec.getToolCallID() // Use toolCallID as entity ID

	// Check if cancelled during execution
	if ctx.Err() != nil {
		a.emitToolStatus(ctx, chatID, entityID, toolCallID, toolName, "cancelled")
		return a.buildToolResult(toolCallID, toolName, fmt.Sprintf("Tool execution cancelled: %v", ctx.Err()), "", true, nil)
	}

	// Check for execution error
	if execErr != nil {
		a.emitToolStatus(ctx, chatID, entityID, toolCallID, toolName, "failed")
		return a.buildToolResult(toolCallID, toolName, fmt.Sprintf("Tool execution failed: %v", execErr), "", true, nil)
	}

	// Check if tool was backgrounded
	if execResult.Backgrounded {
		a.emitToolStatus(ctx, chatID, entityID, toolCallID, toolName, "backgrounded")
		return a.buildToolResult(toolCallID, toolName, execResult.Content, execResult.Metadata, false, execResult.BinaryParts)
	}

	// Check if user cancelled right before completion
	if shell.GetCancelSignal().IsCancelled(toolCallID) {
		logging.Info("[ExecuteTools] Tool completed but cancel signal was set - not emitting completed status",
			"toolCallID", toolCallID,
			"toolName", toolName)
		shell.GetCancelSignal().ClearCancelled(toolCallID)
		return a.buildToolResult(toolCallID, toolName, "Tool execution cancelled by user", "", true, nil)
	}

	// Tool completed successfully
	a.emitToolStatus(ctx, chatID, entityID, toolCallID, toolName, "completed")

	// Emit refetch signal for file-mutating tools so frontend updates without polling
	if isFileMutatingTool(toolName) {
		a.emitWorktreeRefetch(ctx, tec)
	}

	// Return result (may still be an error from the tool itself)
	return a.buildToolResult(toolCallID, toolName, execResult.Content, execResult.Metadata, !execResult.Success || execResult.IsError, execResult.BinaryParts)
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

// emitToolStatus emits a tool execution status update to chat_updates
// This allows the UI to show real-time tool execution progress
func (a *ExecuteToolsActivity) emitToolStatus(ctx context.Context, chatID, contentBlockID, toolCallID, toolName, status string) {
	update := db.ToolCallUpdate{
		ContentBlockID: contentBlockID,
		ToolCallID:     toolCallID,
		ToolName:       toolName,
		Status:         db.ToolCallStatus(status),
		Timestamp:      time.Now().Format(time.RFC3339),
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
			"status", status,
			"content_block_id", contentBlockID)
	}
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

	// Validate against schema if provided
	if schema != nil {
		if err := validateResponseToolData(toolInput, schema); err != nil {
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

// validateResponseToolData validates JSON data against a JSON Schema.
// Returns nil if valid, or an error describing the validation failure.
func validateResponseToolData(jsonStr string, schema map[string]interface{}) error {
	// Convert schema map to JSON bytes
	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("failed to marshal schema: %w", err)
	}

	// Parse the schema using google/jsonschema-go
	var goSchema gojsonschema.Schema
	if err := json.Unmarshal(schemaBytes, &goSchema); err != nil {
		return fmt.Errorf("failed to unmarshal schema: %w", err)
	}

	// Resolve the schema
	resolved, err := goSchema.Resolve(nil)
	if err != nil {
		return fmt.Errorf("failed to resolve schema: %w", err)
	}

	// Parse the input JSON
	var inputData interface{}
	if err := json.Unmarshal([]byte(jsonStr), &inputData); err != nil {
		return fmt.Errorf("failed to unmarshal input JSON: %w", err)
	}

	// Validate
	if err := resolved.Validate(inputData); err != nil {
		return err
	}

	return nil
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

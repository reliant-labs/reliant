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
	"github.com/reliant-labs/reliant/internal/nomachine"
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

	// dispatchInput is the input the executor runs: toolInput with the
	// turn's bound parameters merged in (boundToolInput). toolInput stays the
	// model's own, which is what the tool_calls row and the transcript show:
	// a bound value can be a secret.
	dispatchInput string

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
		dispatchInput:       toolInput,
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
		ToolName:         tec.toolName,
		ToolInput:        tec.dispatchInput,
		ToolCallID:       tec.toolCallID,
		ContentBlockID:   "", // Not required - tool calls can be ephemeral
		UserID:           tec.project.UserID,
		ChatID:           tec.chat.ID,
		ProjectID:        tec.project.ID,
		WorktreeID:       tec.worktree.ID,
		WorktreeDaemonID: tec.worktree.DaemonID,
		Thread:           tec.thread,
		MessageID:        "", // Not required - tool calls can be ephemeral
		ProjectPath:      tec.project.Path,
		ProjectName:      tec.project.Name,
		WorktreePath:     effectiveWorktreePath, // Uses override if set
		Timeout:          toolexec.DefaultToolTimeout,
		DaemonSelector:   tec.daemonSelector,
		Repos:            tec.repos,
	}
}

// chatID returns the chat ID for status emissions
func (tec *toolExecutionContext) getChatID() string {
	return tec.chatID
}

// threadID is the thread the call runs on, as the record's optional column.
func (tec *toolExecutionContext) threadID() *string {
	if tec.thread == "" {
		return nil
	}
	thread := tec.thread
	return &thread
}

// toolCallIsThisThreads reports whether a recorded call is the one chatID's
// thread is dispatching, rather than another call that happens to share its
// provider-chosen id. A record that never learned its thread is compared on
// the chat alone.
func toolCallIsThisThreads(call *core.ToolCall, chatID, thread string) bool {
	if call.ChatID != chatID {
		return false
	}
	return call.ThreadID == nil || *call.ThreadID == "" || thread == "" || *call.ThreadID == thread
}

func ptrValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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

// OutlivesHeartbeatRPCFailure keeps a running tool alive when a heartbeat RPC
// to the Temporal server merely times out (runtime's
// shieldFromSpuriousHeartbeatCancel).
//
// A tool cannot be retried the way a CallLLM turn can: a redelivered attempt
// deliberately reports "interrupted" rather than run the tool twice
// (isActivityRetry), so an attempt abandoned to a slow heartbeat is a result
// the agent never gets back. Letting the attempt finish is the only way that
// result reaches the conversation without double-executing the tool.
func (a *ExecuteToolsActivity) OutlivesHeartbeatRPCFailure() bool {
	return true
}

// DisplayName returns human-readable name for UI
func (a *ExecuteToolsActivity) DisplayName() string {
	return "Run LLM Tool Calls"
}

// Description returns what the activity does
func (a *ExecuteToolsActivity) Description() string {
	return "Run the tool calls an upstream Call LLM step returned"
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

	// The capability set of the call_llm turn that produced these calls,
	// carried here in the activity's own input (the runtime copies it from
	// that call_llm's recorded output). It is the whole of execution-time
	// enforcement: a call outside it is refused, and load_tool decides what it
	// may grant from the same set. No worker-local state is consulted, so it
	// holds on whichever worker this lands — research/TOOL_CAPABILITIES.md.
	caps := tools.CapabilitiesFromProto(protoArgs.GetCapabilities())
	ctx = tools.WithCapabilities(ctx, caps)

	// Calls the workflow decided must not run: a mutating integration action
	// the person attending did not approve. Each is answered with its reason.
	refusedByWorkflow := protoArgs.GetRefusedToolCalls()

	// Build set for O(1) response tool lookups in worker goroutines
	responseToolSet := make(map[string]bool, len(expectedResponseTools))
	for _, name := range expectedResponseTools {
		responseToolSet[name] = true
	}

	// Debug logging to trace loop context
	logging.Debug("[ExecuteToolsActivity] Received input",
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

	// The directory the batch runs in is checked — and recovered if it has
	// gone missing — once, before any call runs. A retried attempt executes
	// nothing (isActivityRetry), so it has nothing to check.
	workspace := batchWorkspace{noteIndex: -1}
	if !isActivityRetry(attemptNumber) {
		workspace = a.ensureBatchWorkspace(ctx, &rtx, resolvedToolCalls, func(call message.ToolCall) bool {
			return responseToolSet[call.Name] || refusedByWorkflow[call.ID] != ""
		})
	}
	workingPath := rtx.ProjectPath
	if workspace.path != "" {
		workingPath = workspace.path
	}

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

					if reason := refusedByWorkflow[toolCallID]; reason != "" {
						resultsChan <- toolCallResult{
							index:  job.index,
							result: a.refuseToolCall(ctx, rtx, toolCall, reason),
						}
						return
					}

					// The one capability check: was this tool offered in the
					// request that produced this call? That set includes what
					// load_tool granted on earlier turns, so a legitimately
					// loaded tool (generate_image, deliberately outside every
					// default bundle) passes, while a name the model was not
					// shown — carried over from history, or hallucinated — is
					// refused here instead of running. The tier is part of the
					// set: a tool above it is never offered.
					if reason := capabilityRefusal(caps, toolCall); reason != "" {
						resultsChan <- toolCallResult{
							index:  job.index,
							result: a.refuseToolCall(ctx, rtx, toolCall, reason),
						}
						return
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
						caps,
						rtx.ChatID,
						rtx.Thread,
						toolName,
						toolInput,
						toolCallID,
						activityID,
						workflowRunID,
						attemptNumber,
						workingPath,        // Working directory override: the run's, or where ensureBatchWorkspace moved it
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

	// What happened to the workspace is said once, ahead of the first result
	// that ran on the machine, so the model reads it before the output it
	// explains.
	if workspace.note != "" && workspace.noteIndex >= 0 && workspace.noteIndex < len(results) {
		results[workspace.noteIndex].Content = workspace.note + results[workspace.noteIndex].Content
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
	results, totalResultChars, batchTruncated := a.capToolResultBatch(ctx, rtx.ChatID, results, compactionThreshold)
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
			} else {
				// A tool produced metadata the workflow cannot read, so any
				// expression referencing response_data.<tool> silently gets
				// nothing. Swallowing this is what hid the truncation bug.
				activity.GetLogger(ctx).Warn("[ExecuteTools] Tool metadata is not valid JSON; response_data entry dropped",
					"tool", r.Name,
					"error", err,
					"metadataBytes", len(r.Metadata))
			}
		}
	}

	// Build output with explicit Message initialization
	output := &reliantv1.ExecuteToolsOutput{
		ToolResults:      messageToolResultsToProto(results),
		ThreadTokenCount: int32(threadTokenCount),
		TotalResultChars: int32(totalResultChars),
		ResponseData:     goMapToProtoStruct(responseData),
		// What this batch's load_tool calls granted. The workflow records it
		// for the thread, and the thread's next call_llm offers it — the only
		// record of a grant there is.
		GrantedTools: grantedTools(results),
		Message: &reliantv1.MessageOutput{
			Role: "tool",
			Text: "",
		},
	}

	activity.GetLogger(ctx).Debug("[ExecuteTools] Completed",
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
	caps *tools.Capabilities,
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
	if result, ok := a.checkPriorTerminalResult(ctx, chatID, thread, toolCallID, toolName); ok {
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
		result := a.buildToolResult(toolCallID, toolName, InterruptedToolResultContent, "", true, nil, nil)
		a.recordInterruptedRetry(ctx, chatID, thread, toolCallID, toolName, result.Content)
		return result
	}

	// Check for cancellation before starting work
	if ctx.Err() != nil {
		return a.buildToolResult(toolCallID, toolName, fmt.Sprintf("Cancelled: %v", ctx.Err()), "", true, nil, nil)
	}

	// Validate tool input JSON
	var inputMap map[string]interface{}
	if err := json.Unmarshal([]byte(toolInput), &inputMap); err != nil {
		return a.buildToolResult(toolCallID, toolName, fmt.Sprintf("Failed to parse tool inputs: %v", err), "", true, nil, nil)
	}

	// Load execution context (chat -> project -> worktree)
	// projectPath override allows sub-workflows to run tools in a different directory
	tec, errMsg := a.loadToolExecutionContext(ctx, chatID, thread, toolName, toolInput, toolCallID, projectPath)
	if errMsg != "" {
		return a.buildToolResult(toolCallID, toolName, errMsg, "", true, nil, nil)
	}

	// A run with no machine (research/DAEMONLESS_RUNS.md) was never offered a
	// tool that needs one; a call to one anyway — named from history, or
	// hallucinated — is refused here, before dispatch. Recorded FAILED like any
	// refusal. The text is not a daemon-offline result, so the offline breaker
	// stays neutral: there is no machine to wait for.
	//
	// The capability check above already refuses such a call on any turn that
	// recorded a set. This one reads the chat row itself, so it holds on every
	// path — a batch with no recorded set included — and it is the boundary
	// the server's lack of a route to the machine actually depends on.
	if tec.chat.NoMachine {
		ctx = nomachine.With(ctx)
		if tools.NeedsMachine(toolName) {
			result := a.buildToolResult(toolCallID, toolName, nomachine.Refusal(toolName), "", true, nil, nil)
			completedAt := time.Now()
			a.upsertTerminalToolCall(ctx, tec, core.ToolCallStatusFailed, toolCallUpsertOpts{
				completedAt:  &completedAt,
				errorMessage: nomachine.ErrNoMachine.Error(),
			}, &toolCallResultWrite{content: result.Content, isError: true})
			return result
		}
	}

	// Bound parameters take effect here, before dispatch. The executor builds
	// a fresh, unbound tool for the call — on this worker, or on the daemon —
	// so the input it receives has to carry the bound values already. A call
	// the bindings refuse is recorded FAILED with the reason, like the
	// no-machine refusal above.
	dispatchInput, refusal := a.boundToolInput(ctx, caps, tec)
	if refusal != "" {
		result := a.buildToolResult(toolCallID, toolName, refusal, "", true, nil, nil)
		completedAt := time.Now()
		a.upsertTerminalToolCall(ctx, tec, core.ToolCallStatusFailed, toolCallUpsertOpts{
			completedAt:  &completedAt,
			errorMessage: refusal,
		}, &toolCallResultWrite{content: result.Content, isError: true})
		return result
	}
	tec.dispatchInput = dispatchInput

	// The skill tool reads the project's skills, which live on the project's
	// config row. Read here, per call, rather than carried from call_llm: the
	// executor's factory has none of its own, and a worker-local copy is what
	// a restart used to lose.
	if toolName == tools.ToolSkill && tec.project != nil {
		ctx = tools.WithSkills(ctx, a.projectSkills(ctx, tec.project.ID))
	}

	// Daemon routing priority: explicit node/workflow selector > the worktree's
	// owning daemon > default resolution. A worktree-bound (e.g. branch) chat
	// must run on the daemon that has its checkout on disk; a nil worktree
	// DaemonID (main checkout / legacy rows) leaves routing at the default.
	// ensureBatchWorkspace routes with the same function, so the directory it
	// checked is on the disk these calls run on.
	worktreeDaemon := ""
	if tec.worktree != nil {
		worktreeDaemon = tec.worktree.DaemonID
	}
	tec.daemonSelector = toolDaemonSelector(worktreeDaemon, daemonSel)

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
//
// "This same tool_call_id" means this chat's and this thread's. The id is the
// model provider's, not ours: a provider can repeat one across chats (a local
// server numbering calls call_0 in every conversation), or a spawned thread
// can reuse its parent's. Matching on the id alone answered such a call with
// ANOTHER call's recorded output -- another chat's, as readily as this one's --
// without running it. A row from elsewhere is a different call, so it is
// treated as no prior result. A row that never learned its thread (written
// before execute_tools recorded one) is compared on the chat alone.
func (a *ExecuteToolsActivity) checkPriorTerminalResult(ctx context.Context, chatID, thread, toolCallID, toolName string) (message.ToolResult, bool) {
	call, err := a.repo.GetToolCall(ctx, toolCallID)
	if err != nil || call == nil || !call.Status.IsTerminal() {
		return message.ToolResult{}, false
	}
	if !toolCallIsThisThreads(call, chatID, thread) {
		activity.GetLogger(ctx).Warn("[ExecuteTools] Tool call id already used by a call in another chat or thread; executing this one",
			"tool_call_id", toolCallID,
			"tool_name", toolName,
			"chat_id", chatID,
			"thread", thread,
			"recorded_chat_id", call.ChatID,
			"recorded_thread", ptrValue(call.ThreadID))
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
		return a.buildToolResult(toolCallID, toolName, InterruptedToolResultContent, "", true, nil, nil), true
	}
	return a.buildToolResult(toolCallID, toolName, result.Content, recordedGrantMetadata(result.GrantedTools), result.IsError, nil, nil), true
}

// recordedGrantMetadata restores a replayed load_tool result's metadata from
// the grants its row recorded, so the batch reports them to the workflow
// (grantedTools) exactly as the original execution did. "" when it granted
// nothing.
func recordedGrantMetadata(granted []string) string {
	if len(granted) == 0 {
		return ""
	}
	encoded, err := json.Marshal(tools.LoadToolMetadata{LoadedTools: granted})
	if err != nil {
		return ""
	}
	return string(encoded)
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
		result := a.buildToolResult(toolCallID, toolName, fmt.Sprintf("Tool execution cancelled: %v", ctx.Err()), "", true, nil, nil)
		a.upsertTerminalToolCall(ctx, tec, core.ToolCallStatusCancelled, toolCallUpsertOpts{
			startedAt:    &startedAt,
			completedAt:  &completedAt,
			errorMessage: ctx.Err().Error(),
		}, &toolCallResultWrite{content: result.Content, isError: true})
		return result
	}

	a.trackDaemonPending(ctx, chatID, execResult, execErr)

	// Check for execution error
	if execErr != nil {
		a.emitToolStatus(ctx, chatID, toolCallID, toolName, "failed")
		result := a.buildToolResult(toolCallID, toolName, fmt.Sprintf("Tool execution failed: %v", execErr), "", true, nil, nil)
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
		// Record WHERE the process runs. It lives in one daemon's memory, and
		// that daemon is the only party that can say when it ends; without
		// both ids on the row the reconciler has no one to ask, and the call
		// reads as running forever (see reconcileBackgroundedProcesses).
		a.upsertToolCall(ctx, tec, core.ToolCallStatusBackgrounded, toolCallUpsertOpts{
			startedAt:           &startedAt,
			backgroundProcessID: backgroundProcessIDFromMetadata(execResult.Metadata),
			daemonID:            execResult.DaemonID,
		})
		return a.buildToolResult(toolCallID, toolName, execResult.Content, execResult.Metadata, false, execResult.BinaryParts, attachmentIDsFromMetadata(execResult.Metadata))
	}

	// The tool ran. Decide the outcome BEFORE announcing it.
	isError := !execResult.Success || execResult.IsError
	result := a.buildToolResult(toolCallID, toolName, execResult.Content, execResult.Metadata, isError, execResult.BinaryParts, attachmentIDsFromMetadata(execResult.Metadata))

	// What the command actually printed. Captured BEFORE the nudge below,
	// because this is what gets stored and rendered: a tip appended to the
	// durable row would show up in the UI as if the command had emitted it,
	// and would still be there on reload months later.
	durableContent := result.Content

	// A grep for a symbol is a question code_context answers outright. Say so
	// on the result the model is already reading — telling agents this in a
	// description ahead of time has been measured at zero uptake, twice.
	if !isError {
		result.Content += maybeCodeContextNudge(toolName, tec.toolInput, durableContent, tec.thread)
	}

	status := core.ToolCallStatusCompleted
	errMsg := ""
	if isError {
		status = core.ToolCallStatusFailed
		errMsg = durableContent
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
	}, &toolCallResultWrite{
		content:      durableContent,
		isError:      isError,
		grantedTools: grantedByResult(toolName, execResult.Metadata, isError),
	})

	return result
}

// trackDaemonPending keeps chats.daemon_blocked_at in step with what tool calls
// report about the machine: set when a call could not run because it is
// suspended or still starting, cleared by the next call that completed a round
// trip to a daemon. A server-side tool succeeding says nothing about the
// machine, so it does not clear. The marker drives ChatActivity.WAITING_FOR_DAEMON.
//
// Best-effort: a failure to record the marker must not fail the tool call.
func (a *ExecuteToolsActivity) trackDaemonPending(ctx context.Context, chatID string, res *toolexec.ToolResult, execErr error) {
	var blocked, ran bool
	switch {
	case toolexec.IsDaemonPending(execErr):
		blocked = true
	case execErr != nil:
		return
	case res != nil && res.DaemonPending:
		blocked = true
	case res != nil && res.RanOnDaemon:
		ran = true
	default:
		return
	}
	if chatID == "" || (!blocked && !ran) {
		return
	}
	if err := a.repo.SetChatDaemonBlocked(ctx, chatID, blocked); err != nil {
		activity.GetLogger(ctx).Warn("[ExecuteTools] Failed to record daemon-pending state",
			"chatID", chatID, "blocked", blocked, "error", err)
	}
}

// toolCallResultWrite is the result half of a terminal tool-call write.
type toolCallResultWrite struct {
	content string
	isError bool
	// grantedTools is what the result granted the call's thread (a load_tool
	// result's loaded names). Recorded with the content so a run restarted
	// from its checkpoint gets back the grants its history says it has.
	grantedTools []string
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
		return a.repo.UpsertToolCallResult(txCtx, tec.getChatID(), &core.ToolCallResult{
			ToolCallID:   tec.toolCallID,
			Content:      res.content,
			IsError:      res.isError,
			GrantedTools: res.grantedTools,
			CreatedAt:    now,
			UpdatedAt:    now,
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

// attachmentIDsFromMetadata reads the attachment ids a tool reported in its
// structured metadata, so SaveMessage can render them as image blocks.
//
// The metadata JSON is the tool's own structured output — the same value that
// already feeds response_data — so a tool that persists an attachment says so
// once, in the result it already returns, rather than through a second channel
// added to every executor between here and the tool.
//
// Both `attachment_id` (one) and `attachment_ids` (several) are read: the
// single-image case is by far the common one and forcing it to spell a
// one-element array would be a trap for the next tool that generates media.
func attachmentIDsFromMetadata(metadata string) []string {
	if metadata == "" {
		return nil
	}

	var envelope struct {
		AttachmentID  string   `json:"attachment_id"`
		AttachmentIDs []string `json:"attachment_ids"`
	}
	if err := json.Unmarshal([]byte(metadata), &envelope); err != nil {
		// Not every tool's metadata is an object, and that is fine — this is
		// an opt-in read, not a schema. A tool with no attachments simply has
		// nothing here.
		return nil
	}

	ids := make([]string, 0, len(envelope.AttachmentIDs)+1)
	if envelope.AttachmentID != "" {
		ids = append(ids, envelope.AttachmentID)
	}
	for _, id := range envelope.AttachmentIDs {
		if id != "" && id != envelope.AttachmentID {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}

// buildToolResult creates a message.ToolResult without saving to database
func (a *ExecuteToolsActivity) buildToolResult(
	toolCallID string,
	toolName string,
	content string,
	metadata string,
	isError bool,
	binaryParts []message.BinaryContent,
	attachmentIDs []string,
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
		ToolCallID:    toolCallID,
		Name:          toolName,
		Content:       content,
		Metadata:      metadata,
		IsError:       isError,
		BinaryParts:   binaryParts,
		AttachmentIDs: attachmentIDs,
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
		activity.GetLogger(ctx).Debug("[TOOL_STATUS] Emitted tool status update",
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
	// backgroundProcessID and daemonID say where a backgrounded call's
	// process runs. Set only on the BACKGROUNDED transition.
	backgroundProcessID string
	daemonID            string
}

// backgroundProcessIDFromMetadata reads the process id a backgrounded shell
// call reports in its structured metadata (llm/tools.ShellResponseMetadata).
// Empty when the metadata is absent or carries none.
func backgroundProcessIDFromMetadata(metadata string) string {
	if metadata == "" {
		return ""
	}
	var parsed struct {
		ProcessID string `json:"process_id"`
	}
	if err := json.Unmarshal([]byte(metadata), &parsed); err != nil {
		return ""
	}
	return parsed.ProcessID
}

// recordInterruptedRetry closes the durable row of a call whose activity was
// re-delivered by Temporal.
//
// The retry path tells the model the call was interrupted, and it has to say
// the same thing durably: before this it returned the interrupted result and
// left the row at EXECUTING, where it stayed forever — a running tool with a
// live Cancel button for a call that will never report again. Observed on
// chat 8bb0a875: three shell calls from 01:46-01:49 still "executing" two days
// later, each carrying exactly this interrupted result block.
//
// Failed, not Cancelled: the call's outcome is unknown, which is an error the
// model was shown, not a stop anyone asked for.
//
// A call attempt 1 already BACKGROUNDED is left alone. That process is still
// running somewhere, and the reconciler closes it from the process's real
// outcome; writing Failed here would report a live dev server as dead.
// UpsertToolCallStatus already refuses to walk a terminal row backwards, so a
// call attempt 1 finished keeps its real outcome.
func (a *ExecuteToolsActivity) recordInterruptedRetry(ctx context.Context, chatID, thread, toolCallID, toolName, content string) {
	if a.repo == nil || chatID == "" || toolCallID == "" {
		return
	}
	if existing, err := a.repo.GetToolCall(ctx, toolCallID); err == nil && existing != nil &&
		existing.ChatID == chatID && existing.Status == core.ToolCallStatusBackgrounded {
		return
	}

	completedAt := time.Now()
	a.upsertTerminalToolCall(ctx, &toolExecutionContext{
		chatID:     chatID,
		thread:     thread,
		toolName:   toolName,
		toolCallID: toolCallID,
	}, core.ToolCallStatusFailed, toolCallUpsertOpts{
		completedAt:  &completedAt,
		errorMessage: content,
	}, &toolCallResultWrite{content: content, isError: true})
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
		ThreadID:    tec.threadID(),
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
	if opts.backgroundProcessID != "" {
		call.BackgroundProcessID = &opts.backgroundProcessID
	}
	if opts.daemonID != "" {
		call.DaemonID = &opts.daemonID
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
		ThreadID:    tec.threadID(),
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
func (a *ExecuteToolsActivity) upsertToolCallResult(ctx context.Context, chatID, toolCallID, content string, isError bool) {
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
	if err := a.repo.UpsertToolCallResult(ctx, chatID, result); err != nil {
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
				"tool_call_id", toolCallID,
				"error", err,
				"inputBytes", len(toolInput))
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
	"shell":        true,
	"move_code":    true,
	"find_replace": true,
	"insert_at":    true,
	"edit_lines":   true,
}

func isFileMutatingTool(toolName string) bool {
	return fileMutatingTools[toolName]
}

// toolCallInputEnvelope is the wrapper call_llm used to put around a spawn
// call's input to carry the spawn tool's preset list
// (`__reliant_tool_meta__`). The preset list now travels in the turn's
// capability set and nothing writes the wrapper any more, but tool calls
// recorded in histories from before that still carry it, so an input is
// unwrapped on the way in.
type toolCallInputEnvelope struct {
	Input    string          `json:"input"`
	Metadata json.RawMessage `json:"__reliant_tool_meta__,omitempty"`
}

func decodeToolCallInputFromProto(encodedInput string) string {
	var envelope toolCallInputEnvelope
	if err := json.Unmarshal([]byte(encodedInput), &envelope); err != nil {
		return encodedInput
	}
	if len(envelope.Metadata) == 0 || string(envelope.Metadata) == "null" {
		return encodedInput
	}
	return envelope.Input
}

// protoToolCallsToMessage converts proto ToolCallMsg slice to message.ToolCall slice.
func protoToolCallsToMessage(protoTCs []*reliantv1.ToolCallMsg) []message.ToolCall {
	if protoTCs == nil {
		return nil
	}
	result := make([]message.ToolCall, len(protoTCs))
	for i, tc := range protoTCs {
		result[i] = message.ToolCall{
			ID:               tc.GetId(),
			Name:             tc.GetName(),
			Input:            decodeToolCallInputFromProto(tc.GetInput()),
			ThoughtSignature: tc.GetThoughtSignature(),
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
			ToolCallID:    tr.GetToolCallId(),
			Name:          tr.GetName(),
			Content:       tr.GetContent(),
			IsError:       tr.GetIsError(),
			AttachmentIDs: tr.GetAttachmentIds(),
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
			Id:               tc.ID,
			Name:             tc.Name,
			Input:            tc.Input,
			ThoughtSignature: tc.ThoughtSignature,
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
			ToolCallId:    tr.ToolCallID,
			Name:          tr.Name,
			Content:       strings.ToValidUTF8(tr.Content, "\uFFFD"),
			IsError:       tr.IsError,
			AttachmentIds: tr.AttachmentIDs,
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

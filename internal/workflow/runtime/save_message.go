// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/models/message"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/types/known/structpb"
)

// toInt converts various numeric types to int for token count extraction
func toInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	}
	return 0
}

// toFloat64 converts various numeric types to float64 for usage cost extraction.
func toFloat64(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f
		}
	}
	return 0
}

// evaluateSaveMessageConfig evaluates a SaveMessageConfig's CEL expressions against
// the activity output and workflow context, returning a types.SaveMessageInput.
//
// Thread Resolution (with save_message.thread field):
//   - If config.Thread is specified, it's evaluated and used as the target thread
//   - Thread is always inherited from execution context (workflowThread parameter)
//
// The evaluated thread is exposed in the result so callers can:
//   - Include it in step output (nodes.<id>.thread)
//
// The `output` namespace is populated with the activity's output fields:
//   - output.message.role, output.message.text (standard MessageOutput)
//   - output.tool_calls, output.tool_results, output.input_tokens, etc.
//
// Default values are used when fields are not specified in the config:
//   - role: output.message.role
//   - content: output.message.text
//
// Parameters:
//   - workflowThread: The workflow's thread (required, used if config.Thread is empty)
//   - execContext: Execution context for thread.* access in CEL (optional but recommended)
func evaluateSaveMessageConfig(
	config *reliantv1.SaveMessageConfig,
	activityOutput map[string]interface{},
	workflowContext map[string]interface{},
	nodeOutputs map[string]interface{},
	chatID string,
	workflowThread string,
	workflowID string,
	stepID string,
	execContext *ExecutionContext,
) (*types.SaveMessageInput, error) {
	if config == nil {
		return nil, nil
	}

	// Extract inputs from workflowContext map
	var inputs map[string]interface{}
	if i, ok := workflowContext[workflowContextKeyInputs].(map[string]interface{}); ok {
		inputs = i
	}

	// Build typed CEL context for save_message evaluation
	ctx := &wfcel.PostActivityContext{
		Output:   activityOutput,
		Inputs:   inputs,
		Nodes:    nodeOutputs,
		Workflow: workflowContextToTyped(workflowContext),
	}

	// Helper to evaluate a CEL expression and return the value
	evalString := func(expr string, defaultExpr string) (string, error) {
		if expr == "" {
			expr = defaultExpr
		}
		if expr == "" {
			return "", nil
		}

		// Check if it's a CEL template (wrapped in {{ }})
		val, err := wfcel.EvaluateTemplate(expr, ctx)
		if err != nil {
			return "", fmt.Errorf("evaluating %q: %w", expr, err)
		}

		if val == nil {
			return "", nil
		}

		switch v := val.(type) {
		case string:
			return v, nil
		default:
			return fmt.Sprintf("%v", v), nil
		}
	}

	evalArray := func(expr string) ([]map[string]interface{}, error) {
		if expr == "" {
			return nil, nil
		}

		val, err := wfcel.EvaluateTemplate(expr, ctx)
		if err != nil {
			return nil, fmt.Errorf("evaluating %q: %w", expr, err)
		}

		if val == nil {
			return nil, nil
		}

		// Convert to []map[string]interface{}
		switch v := val.(type) {
		case []interface{}:
			result := make([]map[string]interface{}, 0, len(v))
			for _, item := range v {
				if m, ok := item.(map[string]interface{}); ok {
					result = append(result, m)
				} else {
					// Try JSON roundtrip for struct types
					jsonBytes, err := json.Marshal(item)
					if err != nil {
						return nil, fmt.Errorf("failed to marshal item: %w", err)
					}
					var m map[string]interface{}
					if err := json.Unmarshal(jsonBytes, &m); err != nil {
						return nil, fmt.Errorf("failed to unmarshal item: %w", err)
					}
					result = append(result, m)
				}
			}
			return result, nil
		case []map[string]interface{}:
			return v, nil
		default:
			return nil, fmt.Errorf("expected array, got %T", val)
		}
	}

	evalStringArray := func(expr string) ([]string, error) {
		if expr == "" {
			return nil, nil
		}

		val, err := wfcel.EvaluateTemplate(expr, ctx)
		if err != nil {
			return nil, fmt.Errorf("evaluating %q: %w", expr, err)
		}

		if val == nil {
			return nil, nil
		}

		switch v := val.(type) {
		case []interface{}:
			result := make([]string, 0, len(v))
			for _, item := range v {
				if s, ok := item.(string); ok {
					result = append(result, s)
				} else {
					result = append(result, fmt.Sprintf("%v", item))
				}
			}
			return result, nil
		case []string:
			return v, nil
		case structpb.NullValue:
			// CEL null value - treat as nil (no attachments)
			return nil, nil
		default:
			return nil, fmt.Errorf("expected string array, got %T", val)
		}
	}

	// Evaluate all fields - no defaults, empty config means no save
	// This allows save_message: {} to be a no-op
	role, err := evalString(model.CelStringRaw(config.GetRole()), "")
	if err != nil {
		return nil, fmt.Errorf("role: %w", err)
	}

	content, err := evalString(model.CelStringRaw(config.GetContent()), "")
	if err != nil {
		return nil, fmt.Errorf("content: %w", err)
	}

	displayStyle, err := evalString(model.CelStringRaw(config.GetDisplayStyle()), "")
	if err != nil {
		return nil, fmt.Errorf("display_style: %w", err)
	}

	toolCallMaps, err := evalArray(model.CelStringRaw(config.GetToolCalls()))
	if err != nil {
		return nil, fmt.Errorf("tool_calls: %w", err)
	}
	toolCalls, err := convertToToolCalls(toolCallMaps)
	if err != nil {
		return nil, fmt.Errorf("tool_calls: %w", err)
	}

	toolResultMaps, err := evalArray(model.CelStringRaw(config.GetToolResults()))
	if err != nil {
		return nil, fmt.Errorf("tool_results: %w", err)
	}
	toolResults, err := convertToToolResults(toolResultMaps)
	if err != nil {
		return nil, fmt.Errorf("tool_results: %w", err)
	}

	// Auto-extract usage from activity output if present.
	var tokenCount int
	if v, ok := activityOutput["token_count"]; ok && v != nil {
		tokenCount = toInt(v)
	}
	var cost float64
	if v, ok := activityOutput["cost"]; ok && v != nil {
		cost = toFloat64(v)
	}

	attachments, err := evalStringArray(model.CelStringRaw(config.GetAttachments()))
	if err != nil {
		return nil, fmt.Errorf("attachments: %w", err)
	}

	// Auto-extract thinking from activity output if present
	// Thinking is automatically persisted when the activity output contains a valid ThinkingOutput struct
	// This ensures extended thinking is always saved without requiring explicit workflow configuration
	var thinkingOutput types.ThinkingOutput
	if thinking, hasThinking := activityOutput["thinking"]; hasThinking && thinking != nil {
		// Validate thinking is a properly structured ThinkingOutput (map with content/signature)
		thinkingMap, ok := thinking.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("thinking: expected map[string]interface{}, got %T", thinking)
		}

		// Content is required for thinking output
		content, hasContent := thinkingMap["content"]
		if hasContent && content != nil {
			contentStr, ok := content.(string)
			if !ok {
				return nil, fmt.Errorf("thinking.content: expected string, got %T", content)
			}
			if contentStr != "" {
				thinkingOutput.Content = contentStr

				// Signature is optional but should be present for Claude's extended thinking
				if sig, hasSig := thinkingMap["signature"]; hasSig && sig != nil {
					sigStr, ok := sig.(string)
					if !ok {
						return nil, fmt.Errorf("thinking.signature: expected string, got %T", sig)
					}
					thinkingOutput.Signature = sigStr
				}
			}
		}
	}

	// Thread is always inherited from the workflow's execution context
	// No explicit thread field - messages are saved to the current thread

	return &types.SaveMessageInput{
		ChatID:       chatID,
		Thread:       workflowThread,
		StepID:       stepID,
		Role:         role,
		DisplayStyle: displayStyle,
		Content:      content,
		Attachments:  attachments,
		ToolResults:  toolResults,
		ToolCalls:    toolCalls,
		TokenCount:   tokenCount,
		Cost:         cost,
		WorkflowID:   workflowID,
		Thinking:     thinkingOutput,
	}, nil
}

// convertToToolCalls converts a slice of map[string]interface{} to []message.ToolCall
// This bridges the gap between CEL evaluation (which returns maps) and the typed struct.
// Returns an error if critical fields (id, name) have incorrect types.
func convertToToolCalls(maps []map[string]interface{}) ([]message.ToolCall, error) {
	if maps == nil {
		return nil, nil
	}
	result := make([]message.ToolCall, 0, len(maps))
	for i, m := range maps {
		tc := message.ToolCall{}

		// id is critical - tool calls must have an ID
		id, ok := m["id"].(string)
		if !ok {
			return nil, fmt.Errorf("tool_calls[%d].id: expected string, got %T", i, m["id"])
		}
		tc.ID = id

		// name is critical - tool calls must have a name
		name, ok := m["name"].(string)
		if !ok {
			return nil, fmt.Errorf("tool_calls[%d].name: expected string, got %T", i, m["name"])
		}
		tc.Name = name

		// input is optional - some tools may not have input
		if input, ok := m["input"].(string); ok {
			tc.Input = input
		} else if m["input"] != nil {
			return nil, fmt.Errorf("tool_calls[%d].input: expected string, got %T", i, m["input"])
		}

		// thought_signature is optional
		if sig, ok := m["thought_signature"].(string); ok {
			tc.ThoughtSignature = sig
		} else if m["thought_signature"] != nil {
			return nil, fmt.Errorf("tool_calls[%d].thought_signature: expected string, got %T", i, m["thought_signature"])
		}

		result = append(result, tc)
	}
	return result, nil
}

// convertToToolResults converts a slice of map[string]interface{} to []message.ToolResult
// This bridges the gap between CEL evaluation (which returns maps) and the typed struct.
// Returns an error if critical fields (tool_call_id, content) have incorrect types.
func convertToToolResults(maps []map[string]interface{}) ([]message.ToolResult, error) {
	if maps == nil {
		return nil, nil
	}
	result := make([]message.ToolResult, 0, len(maps))
	for i, m := range maps {
		tr := message.ToolResult{}

		// tool_call_id is critical - results must reference a tool call
		id, ok := m["tool_call_id"].(string)
		if !ok {
			return nil, fmt.Errorf("tool_results[%d].tool_call_id: expected string, got %T", i, m["tool_call_id"])
		}
		tr.ToolCallID = id

		// content is critical - results must have content
		content, ok := m["content"].(string)
		if !ok {
			return nil, fmt.Errorf("tool_results[%d].content: expected string, got %T", i, m["content"])
		}
		tr.Content = content

		// name is optional but should be a string if present
		if name, ok := m["name"].(string); ok {
			tr.Name = name
		} else if m["name"] != nil {
			return nil, fmt.Errorf("tool_results[%d].name: expected string, got %T", i, m["name"])
		}

		// is_error is optional, defaults to false
		if isError, ok := m["is_error"].(bool); ok {
			tr.IsError = isError
		} else if m["is_error"] != nil {
			return nil, fmt.Errorf("tool_results[%d].is_error: expected bool, got %T", i, m["is_error"])
		}

		result = append(result, tr)
	}
	return result, nil
}

// ExecuteSaveMessageForNode is a convenience wrapper for executeSaveMessageInline that handles
// building the workflow context with thread from execContext. This reduces code duplication
// in loop_executor and inline_workflow_executor.
//
// Parameters:
//   - ctx: Temporal workflow context
//   - node: The proto node with SaveMessage config
//   - output: The activity/workflow output (accessible via output.* in CEL)
//   - nodeOutputs: Completed step outputs (accessible via nodes.* in CEL)
//   - workflowID, workflowName, chatID: Workflow identifiers
//   - inputs: Workflow inputs
//   - execContext: Execution context (provides thread)
//   - loopNodeID, loopIteration: Loop context (can be empty/0 if not in loop)
func ExecuteSaveMessageForNode(
	ctx workflow.Context,
	node *reliantv1.Node,
	output map[string]interface{},
	nodeOutputs map[string]interface{},
	workflowID string,
	workflowName string,
	chatID string,
	inputs map[string]interface{},
	execContext *ExecutionContext,
	loopNodeID string,
	loopIteration int,
) (map[string]interface{}, error) {
	if node.GetSaveMessage() == nil {
		return nil, nil
	}

	logger := workflow.GetLogger(ctx)
	logger.Info("[SaveMessage] Executing save_message for node",
		"nodeID", node.GetId(),
	)

	// Build workflow context for CEL evaluation
	workflowContext := buildWorkflowContext(
		workflowID,
		workflowName,
		chatID,
		inputs,
	)

	// Add thread to context from execContext
	if execContext != nil && execContext.Thread != "" {
		if ctxInputs, ok := workflowContext[workflowContextKeyInputs].(map[string]interface{}); ok {
			ctxInputs["thread"] = execContext.Thread
		} else {
			workflowContext[workflowContextKeyInputs] = map[string]interface{}{"thread": execContext.Thread}
		}
	}

	return executeSaveMessageInline(
		ctx,
		node,
		output,
		workflowContext,
		nodeOutputs,
		chatID,
		workflowID,
		loopNodeID,
		loopIteration,
		execContext, // Pass through for thread.* namespace access in CEL
	)
}

// executeSaveMessageInline executes the SaveMessage activity inline after another activity completes.
// This is called from handleActivityCompletion when a node has save_message config.
// Returns the SaveMessage output (contains thread_token_count, message_id, etc.) for merging into step output.
//
// This is the PREFERRED pattern for message persistence in workflows.
// Inline save_message enables proper frontend activity indicator integration.
// See docs/action-changes-spec.md for usage guidance.
//
// The execContext parameter is optional but recommended for inline workflow nodes.
// It provides thread context for proper message persistence.
func executeSaveMessageInline(
	ctx workflow.Context,
	node *reliantv1.Node,
	activityOutput map[string]interface{},
	workflowContext map[string]interface{},
	nodeOutputs map[string]interface{},
	chatID string,
	workflowID string,
	loopNodeID string,
	loopIteration int,
	execContext *ExecutionContext,
) (map[string]interface{}, error) {
	sm := node.GetSaveMessage()
	if sm == nil {
		return nil, nil
	}
	nid := node.GetId()

	logger := workflow.GetLogger(ctx)

	// Check condition if specified
	condStr := model.CelStringRaw(sm.GetCondition())
	if condStr != "" {
		// Extract inputs from workflowContext for typed CEL context
		var condInputs map[string]interface{}
		if inputs, ok := workflowContext[workflowContextKeyInputs].(map[string]interface{}); ok {
			condInputs = inputs
		}
		// Build typed CEL context for condition evaluation
		condCtx := &wfcel.PostActivityContext{
			Output:   activityOutput,
			Inputs:   condInputs,
			Nodes:    nodeOutputs,
			Workflow: workflowContextToTyped(workflowContext),
		}
		conditionResult, err := wfcel.EvaluateTemplate(condStr, condCtx)
		if err != nil {
			logger.Error("[SaveMessage] Failed to evaluate condition",
				"stepID", nid,
				"condition", condStr,
				"error", err,
			)
			return nil, fmt.Errorf("failed to evaluate save_message condition: %w", err)
		}

		// Check if condition is truthy
		shoudSave := false
		switch v := conditionResult.(type) {
		case bool:
			shoudSave = v
		case nil:
			shoudSave = false
		default:
			// Non-nil, non-bool values are truthy
			shoudSave = true
		}

		if !shoudSave {
			logger.Info("[SaveMessage] Skipping save - condition not met",
				"stepID", nid,
				"condition", condStr,
			)
			return nil, nil
		}
	}

	logger.Info("[SaveMessage] Evaluating inline save_message config",
		"stepID", nid,
		"chatID", chatID,
	)

	// Get thread from workflow inputs - required
	var thread string
	if wfInputs, ok := workflowContext[workflowContextKeyInputs].(map[string]interface{}); ok {
		if tp, ok := wfInputs["thread"].(string); ok && tp != "" {
			thread = tp
		}
	}
	if thread == "" {
		return nil, fmt.Errorf("thread not found in workflow inputs for save_message (node: %s)", nid)
	}

	// Evaluate the save_message config
	evalResult, err := evaluateSaveMessageConfig(
		sm,
		activityOutput,
		workflowContext,
		nodeOutputs,
		chatID,
		thread,
		workflowID,
		nid+"-save", // Step ID with -save suffix to indicate inline save
		execContext, // Pass through for thread.* namespace access
	)
	if err != nil {
		logger.Error("[SaveMessage] Failed to evaluate save_message config",
			"stepID", nid,
			"error", err,
		)
		return nil, fmt.Errorf("failed to evaluate save_message config: %w", err)
	}

	// Skip if no result or no role (indicates nothing to save)
	if evalResult == nil || evalResult.Role == "" {
		logger.Debug("[SaveMessage] Skipping save - no role specified",
			"stepID", nid,
		)
		return nil, nil
	}

	saveInput := evalResult

	// Inject loop context if executing within a loop
	if loopNodeID != "" {
		saveInput.LoopNodeID = loopNodeID
		saveInput.LoopIteration = loopIteration
	}

	logger.Info("[SaveMessage] Executing inline SaveMessage",
		"stepID", nid,
		"role", saveInput.Role,
		"thread", saveInput.Thread,
		"contentLen", len(saveInput.Content),
		"thinkingLen", len(saveInput.Thinking.Content),
		"hasThinkingSig", saveInput.Thinking.Signature != "",
		"toolCalls", len(saveInput.ToolCalls),
		"toolResults", len(saveInput.ToolResults),
		"loopNodeID", loopNodeID,
		"loopIteration", loopIteration,
	)

	// Build structured input: RuntimeContext + proto Node.
	rtx := types.RuntimeContext{
		ChatID:     saveInput.ChatID,
		Thread:     saveInput.Thread,
		WorkflowID: saveInput.WorkflowID,
		StepID:     saveInput.StepID,
	}
	if saveInput.LoopNodeID != "" {
		rtx.LoopNodeID = saveInput.LoopNodeID
		rtx.LoopIteration = saveInput.LoopIteration
	}
	v3Input := types.ActivityInput{Runtime: rtx, Node: buildSaveMessageNode(saveInput)}

	// Execute SaveMessage activity
	// Let Temporal auto-generate ActivityID for deterministic replay.
	// The activity handler gets the ID via activity.GetInfo(ctx).ActivityID.
	saveCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    5,
		},
	})

	var saveOutput map[string]interface{}
	err = workflow.ExecuteActivity(saveCtx, "SaveMessage", v3Input).Get(ctx, &saveOutput)
	if err != nil {
		logger.Error("[SaveMessage] Inline SaveMessage failed",
			"stepID", nid,
			"error", err,
		)
		return nil, fmt.Errorf("inline SaveMessage failed: %w", err)
	}

	logger.Info("[SaveMessage] Inline SaveMessage completed",
		"stepID", nid,
		"messageID", saveOutput["message_id"],
		"thread", saveInput.Thread,
		"threadTokenCount", saveOutput["thread_token_count"],
	)

	// Add thread to output so it's available as nodes.<id>.thread
	saveOutput["thread"] = saveInput.Thread

	return saveOutput, nil
}

// buildSaveMessageNode constructs a proto Node with SaveMessageNodeArgs from a SaveMessageInput.
// This is used to pass a proper proto Node to ActivityInput for Temporal serialization,
// avoiding the map[string]interface{} → JSON → protojson roundtrip.
func buildSaveMessageNode(input *types.SaveMessageInput) *reliantv1.Node {
	args := &reliantv1.SaveMessageNodeArgs{
		ResolvedRole:         input.Role,
		ResolvedContent:      input.Content,
		ResolvedDisplayStyle: input.DisplayStyle,
		ResolvedAttachments:  input.Attachments,
		TokenCount:           int32(input.TokenCount),
		Cost:                 input.Cost,
	}

	// Convert tool calls
	for _, tc := range input.ToolCalls {
		args.ResolvedToolCalls = append(args.ResolvedToolCalls, &reliantv1.ToolCallMsg{
			Id:    tc.ID,
			Name:  tc.Name,
			Input: tc.Input,
		})
	}

	// Convert tool results
	for _, tr := range input.ToolResults {
		args.ResolvedToolResults = append(args.ResolvedToolResults, &reliantv1.ToolResultMsg{
			ToolCallId: tr.ToolCallID,
			Name:       tr.Name,
			Content:    strings.ToValidUTF8(tr.Content, "\uFFFD"),
			IsError:    tr.IsError,
		})
	}

	// Convert thinking
	if input.Thinking.Content != "" || input.Thinking.Signature != "" {
		args.ResolvedThinking = &reliantv1.ThinkingOutput{
			Content:   input.Thinking.Content,
			Signature: input.Thinking.Signature,
		}
	}

	// Convert inject files
	for _, f := range input.InjectFiles {
		args.ResolvedInjectFiles = append(args.ResolvedInjectFiles, &reliantv1.InjectFileMsg{
			Filename: f.Filename,
			MimeType: f.MIMEType,
			Data:     f.Data,
		})
	}

	return &reliantv1.Node{
		Type: model.NodeTypeSaveMessage,
		Args: &reliantv1.Node_SaveMessageNode{SaveMessageNode: args},
	}
}

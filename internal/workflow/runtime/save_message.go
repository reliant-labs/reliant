// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
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

// saveMessageScope is everything a save_message may read besides `output`,
// plus the identifiers the resulting message is written under.
//
// The save_message CEL environment is exactly output, inputs, workflow and
// iter. `nodes` is deliberately absent: a node's save_message is written by
// whoever executes the node, which for an activity is the worker, and the
// worker has no view of sibling node outputs (reading them at completion time
// was racy anyway). The same scope is used whether the workflow or the worker
// performs the save, so a template means the same thing in both places.
type saveMessageScope struct {
	Inputs    map[string]interface{}
	Workflow  *model.WorkflowContext
	Iter      *model.IterContext
	AgentName string

	ChatID     string
	Thread     string
	WorkflowID string
	StepID     string // "<node>-save"
}

// celContext is the CEL activation for a save_message template or condition.
func (s saveMessageScope) celContext(output map[string]interface{}) *wfcel.PostActivityContext {
	return &wfcel.PostActivityContext{
		Output:   output,
		Inputs:   s.Inputs,
		Workflow: s.Workflow,
		Iter:     s.Iter,
	}
}

// newWorkflowSaveMessageScope builds the scope for a save the workflow performs
// itself, from the workflow context map.
func newWorkflowSaveMessageScope(workflowContext map[string]interface{}, chatID, workflowID, stepID string, iter *model.IterContext) saveMessageScope {
	inputs, _ := workflowContext[workflowContextKeyInputs].(map[string]interface{})
	thread, _ := inputs["thread"].(string)
	return saveMessageScope{
		Inputs:     inputs,
		Workflow:   workflowContextToTyped(workflowContext),
		Iter:       iter,
		AgentName:  saveMessageAgentName(workflowContext),
		ChatID:     chatID,
		Thread:     thread,
		WorkflowID: workflowID,
		StepID:     stepID,
	}
}

// saveMessageAgentName is the agent/workflow identity persisted to
// messages.agent: an explicit agent_name input, else the workflow name (e.g.
// "builtin://agent", "get-it-right"). Empty when neither is present.
func saveMessageAgentName(workflowContext map[string]interface{}) string {
	if v, ok := workflowContext[workflowContextKeyAgentName].(string); ok && v != "" {
		return v
	}
	v, _ := workflowContext[workflowContextKeyName].(string)
	return v
}

// evaluateSaveMessageConfig evaluates a SaveMessageConfig's CEL expressions
// against the activity output and the scope, returning a types.SaveMessageInput.
//
// The `output` namespace is populated with the activity's output fields:
//   - output.message.role, output.message.text (standard MessageOutput)
//   - output.tool_calls, output.tool_results, output.input_tokens, etc.
//
// Every field is evaluated as written; an empty config evaluates to a message
// with no role, which callers treat as "nothing to save".
func evaluateSaveMessageConfig(
	config *reliantv1.SaveMessageConfig,
	activityOutput map[string]interface{},
	scope saveMessageScope,
) (*types.SaveMessageInput, error) {
	if config == nil {
		return nil, nil
	}

	ctx := scope.celContext(activityOutput)

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

	// Auto-extract the resolved model from the activity output (CallLLMOutput.model).
	// This is the concrete model that served the completion, captured after tag
	// resolution, so messages.model records the actual model rather than the
	// "tags:[...]" selector. Mirrors the token_count/cost auto-extraction above.
	var modelName string
	if v, ok := activityOutput["model"]; ok && v != nil {
		if s, ok := v.(string); ok {
			modelName = s
		}
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

		// Each field stands on its own. The signature used to be read only
		// when content was non-empty, which silently discarded the exact case
		// this fix exists for: a provider that signs a thinking block but
		// returns no readable text for it. The signature is what lets the next
		// turn replay the block, so it has to survive without the text.
		content, hasContent := thinkingMap["content"]
		if hasContent && content != nil {
			contentStr, ok := content.(string)
			if !ok {
				return nil, fmt.Errorf("thinking.content: expected string, got %T", content)
			}
			thinkingOutput.Content = contentStr
		}

		if sig, hasSig := thinkingMap["signature"]; hasSig && sig != nil {
			sigStr, ok := sig.(string)
			if !ok {
				return nil, fmt.Errorf("thinking.signature: expected string, got %T", sig)
			}
			thinkingOutput.Signature = sigStr
		}

		if redacted, hasRedacted := thinkingMap["redacted"]; hasRedacted && redacted != nil {
			redactedStr, ok := redacted.(string)
			if !ok {
				return nil, fmt.Errorf("thinking.redacted: expected string, got %T", redacted)
			}
			thinkingOutput.Redacted = redactedStr
		}
	}

	// Thread is always inherited from the executing thread; there is no
	// explicit thread field — messages are saved to the current thread.
	return &types.SaveMessageInput{
		ChatID:       scope.ChatID,
		Thread:       scope.Thread,
		StepID:       scope.StepID,
		Role:         role,
		DisplayStyle: displayStyle,
		Content:      content,
		Attachments:  attachments,
		ToolResults:  toolResults,
		ToolCalls:    toolCalls,
		TokenCount:   tokenCount,
		Cost:         cost,
		Model:        modelName,
		Agent:        scope.AgentName,
		WorkflowID:   scope.WorkflowID,
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

		// attachment_ids is optional. It must survive this conversion or the
		// image a tool produced is stored but never rendered: SaveMessage
		// materializes one IMAGE content block per id, and a dropped id means
		// the tool_result block lands alone and the picture is invisible in
		// the transcript even though the bytes are durable and addressable.
		//
		// CEL hands values back as []interface{} of string, not []string, so
		// the element type has to be asserted per item rather than on the
		// slice. A non-string element is a malformed payload, not something to
		// skip quietly — silently dropping it reproduces exactly the bug this
		// field exists to fix.
		if raw, present := m["attachment_ids"]; present && raw != nil {
			ids, ok := raw.([]interface{})
			if !ok {
				return nil, fmt.Errorf("tool_results[%d].attachment_ids: expected array, got %T", i, raw)
			}
			for j, rawID := range ids {
				id, ok := rawID.(string)
				if !ok {
					return nil, fmt.Errorf("tool_results[%d].attachment_ids[%d]: expected string, got %T", i, j, rawID)
				}
				if id != "" {
					tr.AttachmentIDs = append(tr.AttachmentIDs, id)
				}
			}
		}

		result = append(result, tr)
	}
	return result, nil
}

// saveMessageSkip names why a resolved save_message writes nothing. Empty
// means there is a message to write.
type saveMessageSkip string

const (
	saveSkipNone        saveMessageSkip = ""
	saveSkipCondition   saveMessageSkip = "condition not met"
	saveSkipNoRole      saveMessageSkip = "no role"
	saveSkipContentFree saveMessageSkip = "content-free assistant message"
	saveSkipNoConfig    saveMessageSkip = "no save_message"
)

// logSaveMessageSkip records why a save_message wrote nothing. A content-free
// assistant turn should be impossible upstream (except the deliberate
// assistant-tail yield in call_llm), so it stays greppable at WARN.
func logSaveMessageSkip(logger interface {
	Info(string, ...interface{})
	Warn(string, ...interface{})
}, stepID string, skip saveMessageSkip) {
	if skip == saveSkipContentFree {
		logger.Warn("[SaveMessage] Skipping an assistant message with no content, tool calls or thinking — "+
			"there is no row to write. Expected only for the assistant-tail yield in call_llm; "+
			"anywhere else it means a turn reached save with nothing in it.",
			"stepID", stepID)
		return
	}
	logger.Info("[SaveMessage] Skipping save", "stepID", stepID, "reason", string(skip))
}

// resolveSaveMessage is THE save_message decision, made identically by the
// workflow (for outputs it assembles) and by the ActivityWrapper (for the
// activity it just ran): evaluate the condition, evaluate the config, and
// decline the rows that must not be written. It never writes anything.
//
// Returns the message to write, or a skip reason with a nil message.
func resolveSaveMessage(
	config *reliantv1.SaveMessageConfig,
	output map[string]interface{},
	scope saveMessageScope,
) (*types.SaveMessageInput, saveMessageSkip, error) {
	if config == nil {
		return nil, saveSkipNoConfig, nil
	}

	// The condition is raw CEL that must return bool, like every other
	// condition field (node condition, edge case, loop while). A non-bool
	// result is an error, never "truthy": a condition that cannot decide must
	// not silently save.
	if condExpr := model.DirectCelExpr(config.GetCondition()); condExpr != "" {
		shouldSave, err := wfcel.EvaluateBool(condExpr, scope.celContext(output))
		if err != nil {
			return nil, saveSkipNone, fmt.Errorf("evaluating save_message condition %q: %w", condExpr, err)
		}
		if !shouldSave {
			return nil, saveSkipCondition, nil
		}
	}

	if scope.Thread == "" {
		return nil, saveSkipNone, fmt.Errorf("no thread to save the message to (step %s)", scope.StepID)
	}

	saveInput, err := evaluateSaveMessageConfig(config, output, scope)
	if err != nil {
		return nil, saveSkipNone, fmt.Errorf("evaluating save_message: %w", err)
	}
	if saveInput == nil || saveInput.Role == "" {
		return nil, saveSkipNoRole, nil
	}

	// An assistant message with nothing in it cannot be written — SaveMessage's
	// validator refuses the row, because a blockless assistant row is durable
	// poison for the thread. Attempting the write anyway buys five failing
	// attempts, an ERROR per attempt, and an error banner the user cannot act
	// on.
	//
	// Since call_llm substitutes text for any turn the provider left empty, the
	// only thing that still arrives here content-free is the deliberate
	// assistant-tail yield, which HAS nothing to save. Declining the write says
	// that outright instead of expressing it as a failed write.
	//
	// A thinking signature or a sealed redacted block counts as content, and
	// must mirror the write guard in threads.validateSaveMessageOpts exactly.
	// If this predicate is stricter than that one, the row is dropped here
	// without ever reaching the validator — which is precisely how a
	// signature-bearing turn used to vanish with only a WARN to show for it.
	if strings.EqualFold(saveInput.Role, "assistant") &&
		saveInput.Content == "" &&
		len(saveInput.ToolCalls) == 0 &&
		saveInput.Thinking.Content == "" &&
		saveInput.Thinking.Signature == "" &&
		saveInput.Thinking.Redacted == "" {
		return nil, saveSkipContentFree, nil
	}
	return saveInput, saveSkipNone, nil
}

// saveMessageRuntimeContext is the RuntimeContext a resolved message is
// written under — the same shape the SaveMessage activity receives.
func saveMessageRuntimeContext(saveInput *types.SaveMessageInput) types.RuntimeContext {
	rtx := types.RuntimeContext{
		ChatID:             saveInput.ChatID,
		Thread:             saveInput.Thread,
		WorkflowID:         saveInput.WorkflowID,
		StepID:             saveInput.StepID,
		AssistantMessageID: saveInput.AssistantMessageID,
	}
	if saveInput.LoopNodeID != "" {
		rtx.LoopNodeID = saveInput.LoopNodeID
		rtx.LoopIteration = saveInput.LoopIteration
	}
	return rtx
}

// ExecuteSaveMessageForNode runs the save_message of a node whose output the
// WORKFLOW assembled (loop and workflow nodes). The workflow is the executor
// that produced that output, so it writes the message itself via the
// SaveMessage activity.
//
// Parameters:
//   - output: The workflow-assembled output (accessible via output.* in CEL)
//   - inputs: Workflow inputs
//   - execContext: Execution context (provides the thread)
//   - loopNodeID, loopIteration: Loop context (empty/0 outside a loop)
func ExecuteSaveMessageForNode(
	ctx workflow.Context,
	node *reliantv1.Node,
	output map[string]interface{},
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
	thread := ""
	if execContext != nil {
		thread = execContext.Thread
	}
	// The scope's full iteration context is the `iter` its loop published into
	// the inputs; the loop node id is the authoritative "in a loop" signal.
	var iterCtx *model.IterContext
	if loopNodeID != "" {
		iterMap, _ := inputs["iter"].(map[string]interface{})
		iterCtx = iterContextFromMap(iterMap)
		if iterCtx == nil {
			iterCtx = &model.IterContext{Iteration: loopIteration, Index: loopIteration}
		}
	}
	return executeSaveMessageInline(ctx, node, output,
		buildSaveMessageWorkflowContext(workflowID, workflowName, chatID, inputs, thread),
		chatID, workflowID, loopNodeID, loopIteration, iterCtx, "")
}

// buildSaveMessageWorkflowContext is the workflow context a save_message sees:
// buildWorkflowContext with inputs.thread set to the executing thread. The
// inputs map is copied, never mutated — it is shared with the rest of the run.
func buildSaveMessageWorkflowContext(workflowID, workflowName, chatID string, inputs map[string]interface{}, thread string) map[string]interface{} {
	if thread != "" {
		withThread := copyMap(inputs)
		withThread["thread"] = thread
		inputs = withThread
	}
	return buildWorkflowContext(workflowID, workflowName, chatID, inputs)
}

// executeSaveMessageInline performs a save_message from the workflow: resolve
// it (resolveSaveMessage), then dispatch the SaveMessage activity. Used only
// for outputs the workflow itself assembled — an activity-backed node's
// save_message is written by the ActivityWrapper instead.
//
// Returns the SaveMessage output (thread_token_count, message_id, ...).
func executeSaveMessageInline(
	ctx workflow.Context,
	node *reliantv1.Node,
	activityOutput map[string]interface{},
	workflowContext map[string]interface{},
	chatID string,
	workflowID string,
	loopNodeID string,
	loopIteration int,
	iterCtx *model.IterContext,
	preallocatedMessageID string,
) (map[string]interface{}, error) {
	sm := node.GetSaveMessage()
	if sm == nil {
		return nil, nil
	}
	nid := node.GetId()
	logger := workflow.GetLogger(ctx)

	// iterCtx is the enclosing loop's full iteration context (iteration,
	// index, item, key) — the same `iter` the node's config resolved against —
	// or nil outside a loop, in which case iter defaults to iteration 0.
	// Deriving it from loopIteration alone dropped item/key, so
	// "{{iter.item.filename}}" failed and the message was never saved.

	scope := newWorkflowSaveMessageScope(workflowContext, chatID, workflowID, nid+"-save", iterCtx)
	saveInput, skip, err := resolveSaveMessage(sm, activityOutput, scope)
	if err != nil {
		logger.Error("[SaveMessage] Failed to resolve save_message", "stepID", nid, "error", err)
		return nil, fmt.Errorf("failed to resolve save_message: %w", err)
	}
	if saveInput == nil {
		logSaveMessageSkip(logger, nid, skip)
		return nil, nil
	}

	// Delta identity: persist the assistant message under its pre-allocated
	// streaming id. The activity honors this only when Role is "assistant".
	saveInput.AssistantMessageID = preallocatedMessageID
	if loopNodeID != "" {
		saveInput.LoopNodeID = loopNodeID
		saveInput.LoopIteration = loopIteration
	}

	logger.Info("[SaveMessage] Executing inline SaveMessage",
		"stepID", nid,
		"role", saveInput.Role,
		"thread", saveInput.Thread,
		"contentLen", len(saveInput.Content),
		"toolCalls", len(saveInput.ToolCalls),
		"toolResults", len(saveInput.ToolResults),
		"loopNodeID", loopNodeID,
		"loopIteration", loopIteration,
	)

	v3Input := types.ActivityInput{Runtime: saveMessageRuntimeContext(saveInput), Node: buildSaveMessageNode(saveInput)}

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
		ResolvedModel:        input.Model,
		ResolvedAgent:        input.Agent,
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
			ToolCallId:    tr.ToolCallID,
			Name:          tr.Name,
			AttachmentIds: tr.AttachmentIDs,
			Content:       strings.ToValidUTF8(tr.Content, "\uFFFD"),
			IsError:       tr.IsError,
		})
	}

	// Convert thinking
	if input.Thinking.Content != "" || input.Thinking.Signature != "" || input.Thinking.Redacted != "" {
		args.ResolvedThinking = &reliantv1.ThinkingOutput{
			Content:   input.Thinking.Content,
			Signature: input.Thinking.Signature,
			Redacted:  input.Thinking.Redacted,
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

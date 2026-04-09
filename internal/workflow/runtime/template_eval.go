// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/cel-go/common/types/ref"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"google.golang.org/protobuf/types/known/structpb"
)

// Template evaluation uses the CEL context defined in cel_env.go.
// Data from triggering nodes is accessed via nodes.<node_id>.* namespace

// templateMatch represents a matched template expression
type templateMatch struct {
	full  string // The full match including {{}}
	expr  string // The extracted expression
	start int    // Start position in the input string
	end   int    // End position in the input string
}

// extractTemplateExpressions finds all {{...}} template expressions using a balanced brace parser.
// This correctly handles nested braces in CEL expressions like: {{ items.map(x, {id: x.id}) }}
func extractTemplateExpressions(input string) []templateMatch {
	var matches []templateMatch
	i := 0

	for i < len(input)-1 {
		// Find {{
		if input[i] == '{' && input[i+1] == '{' {
			start := i
			exprStart := i + 2

			// Count braces to find matching }}
			braceCount := 2
			j := exprStart
			for j < len(input) && braceCount > 0 {
				switch input[j] {
				case '{':
					braceCount++
				case '}':
					braceCount--
				}
				j++
			}

			if braceCount == 0 {
				// Found matching }}
				expr := input[exprStart : j-2]
				matches = append(matches, templateMatch{
					full:  input[start:j],
					expr:  strings.TrimSpace(expr),
					start: start,
					end:   j,
				})
				i = j
			} else {
				// Unmatched {{, skip it
				i++
			}
		} else {
			i++
		}
	}

	return matches
}

// isPureExpressionWithWhitespace checks if the input string contains only a single
// template expression surrounded by optional whitespace. This handles YAML literal
// blocks (|) that add trailing newlines.
//
// Examples that return true:
//   - "{{expr}}"           - exact match, no whitespace
//   - "{{expr}}\n"         - trailing newline from YAML |
//   - "  {{expr}}  \n"     - whitespace on both sides
//   - "{{\n  expr\n}}\n"   - multiline CEL with trailing newline
//
// Examples that return false:
//   - "prefix {{expr}}"    - non-whitespace before
//   - "{{expr}} suffix"    - non-whitespace after
//   - "{{a}} {{b}}"        - multiple expressions (handled by caller)
func isPureExpressionWithWhitespace(input string, match templateMatch) bool {
	// Check that any text BEFORE the expression is only whitespace
	if match.start > 0 {
		before := input[:match.start]
		if strings.TrimSpace(before) != "" {
			return false
		}
	}

	// Check that any text AFTER the expression is only whitespace
	if match.end < len(input) {
		after := input[match.end:]
		if strings.TrimSpace(after) != "" {
			return false
		}
	}

	return true
}

// valueToInterpolatedString converts a value to a string for template interpolation.
// Arrays/slices are joined with commas (useful for spawn:workflow({{presets}}) syntax).
// Other types use their default string representation.
func valueToInterpolatedString(value interface{}) string {
	if value == nil {
		return ""
	}

	// Handle slices/arrays - join with commas
	switch v := value.(type) {
	case []string:
		return strings.Join(v, ",")
	case []interface{}:
		parts := make([]string, len(v))
		for i, elem := range v {
			parts[i] = fmt.Sprintf("%v", elem)
		}
		return strings.Join(parts, ",")
	default:
		return fmt.Sprintf("%v", value)
	}
}

// convertCELToNative recursively converts CEL types to native Go types.
// CEL's result.Value() returns a Go value but may contain nested ref.Val types
// (e.g., []ref.Val, map[ref.Val]ref.Val) which don't serialize properly to JSON.
// This function ensures all CEL internal types are converted to native types.
func convertCELToNative(v interface{}) interface{} {
	switch val := v.(type) {
	case ref.Val:
		// CEL value - extract native and recurse
		return convertCELToNative(val.Value())
	case []ref.Val:
		// Slice of CEL values
		result := make([]interface{}, len(val))
		for i, item := range val {
			result[i] = convertCELToNative(item)
		}
		return result
	case []interface{}:
		// Slice that might contain CEL values
		result := make([]interface{}, len(val))
		for i, item := range val {
			result[i] = convertCELToNative(item)
		}
		return result
	case map[string]interface{}:
		// Map that might contain CEL values
		result := make(map[string]interface{})
		for k, item := range val {
			result[k] = convertCELToNative(item)
		}
		return result
	case map[ref.Val]ref.Val:
		// CEL map type
		result := make(map[string]interface{})
		for k, item := range val {
			keyStr, _ := k.Value().(string)
			result[keyStr] = convertCELToNative(item)
		}
		return result
	default:
		// Already native type
		return v
	}
}

// evaluateCELTemplate evaluates a string that may contain {{...}} template expressions.
// - Strings without {{...}} are returned as-is (literals)
// - Strings with {{...}} have those sections CEL-evaluated and interpolated
// - Pure {{expression}} strings return the expression's native type (not string)
//
// NOTE: Handles YAML multi-line strings (| operator) by trimming leading/trailing whitespace
// before checking for pure expressions. This allows {{expr}} to be recognized as a native type
// even when surrounded by YAML indentation.
func evaluateCELTemplate(expr string, context map[string]interface{}) (interface{}, error) {
	if expr == "" {
		return "", nil
	}

	// Trim whitespace to handle YAML multi-line strings (| operator).
	// This is critical for pure expression detection - YAML indentation should not
	// cause {{expr}} to be treated as a mixed string.
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return "", nil
	}

	// Find all template expressions in the trimmed string
	matches := extractTemplateExpressions(trimmed)

	// No template expressions - return trimmed string as-is
	if len(matches) == 0 {
		return trimmed, nil
	}

	// Check if this is a pure expression (entire trimmed string is just {{expr}})
	// In this case, we return the expression's native type, not a string
	if len(matches) == 1 && isPureExpressionWithWhitespace(trimmed, matches[0]) {
		return evaluateCELValue(matches[0].expr, context)
	}

	// Mixed string with embedded expressions - interpolate to string
	var result strings.Builder
	lastEnd := 0

	for _, match := range matches {
		// Append literal text before this expression
		result.WriteString(trimmed[lastEnd:match.start])

		// Extract and evaluate the expression
		value, err := evaluateCELValue(match.expr, context)
		if err != nil {
			return nil, fmt.Errorf("template expression '{{%s}}': %w", match.expr, err)
		}

		// Convert value to string for interpolation
		// Arrays are joined with commas (useful for spawn:workflow({{presets}}) syntax)
		if value != nil {
			result.WriteString(valueToInterpolatedString(value))
		}

		lastEnd = match.end
	}

	// Append remaining literal text after last expression
	result.WriteString(trimmed[lastEnd:])

	return result.String(), nil
}

// EvaluateNodeConfig evaluates node config using wfcel.ResolveCELFields.
// This walks the proto node message, finds all CelX wrapper fields with
// expr set, evaluates them, and sets the literal value.
// Returns a resolved proto node with concrete values.
//
// Uses EXPLICIT NAMESPACE MODEL:
// - workflow.*: Workflow metadata (id, name only)
// - inputs.*: User-provided inputs (thread, auto_approve, etc.)
// - nodes.*: Step outputs via nodes.<step_id>.<field>
// - iter.*: Loop iteration context (iter.iteration) - only inside loops
// - outputs.*: Previous loop iteration's evaluated outputs (only inside loops)
//
// iterContext: Loop iteration context as map. Pass nil when not in a loop.
// loopOutputs: Previous iteration's evaluated workflow outputs. Pass nil when not in a loop.
// execContext: ExecutionContext for thread/message/loop data.
func EvaluateNodeConfig(
	node *reliantv1.Node,
	nodeOutputs map[string]interface{}, // All completed step outputs for nodes.* namespace
	workflowID string, // workflow.id
	workflowName string, // workflow.name
	inputs map[string]interface{}, // inputs.* namespace
	iterContext map[string]interface{}, // Loop iteration context (nil if not in a loop)
	loopOutputs map[string]interface{}, // Previous iteration outputs for outputs.* namespace (nil if not in a loop)
	execContext *ExecutionContext, // Execution context (thread, message, loop, parent)
) (*reliantv1.Node, error) {
	// Build CEL evaluator using centralized builder.
	// CELContextBuilder implements wfcel.CELEvaluator (EvalString).
	builder := NewCELContextBuilder().
		WithWorkflow(workflowID, workflowName).
		WithInputs(inputs).
		WithNodeOutputs(nodeOutputs)

	if execContext != nil {
		builder = builder.WithExecContext(execContext)
	}
	if iterContext != nil {
		iter := &model.IterContext{}
		if iterVal, ok := iterContext["iteration"].(int); ok {
			iter.Iteration = iterVal
			iter.Index = iterVal
		}
		if indexVal, ok := iterContext["index"].(int); ok {
			iter.Index = indexVal
		}
		if itemVal, ok := iterContext["item"]; ok {
			iter.Item = itemVal
		}
		if keyVal, ok := iterContext["key"].(string); ok {
			iter.Key = keyVal
		}
		builder = builder.WithIter(iter)
	}
	if loopOutputs != nil {
		builder = builder.WithOutputs(loopOutputs)
	} else if iterContext != nil {
		// In a loop but no previous iteration outputs yet (iteration 0).
		// Declare outputs as an empty map so CEL compilation succeeds for
		// expressions that reference outputs.* behind a ternary guard.
		builder = builder.WithOutputs(make(map[string]interface{}))
	}

	// Apply model defaults for node types that have a model field.
	// The router node uses its model for the routing LLM call; if omitted,
	// fall back to inputs.model — the same convention every call_llm node uses.
	node = applyModelDefault(node)

	// ResolveCELFields walks the proto node message, evaluates all CelX wrapper fields,
	// and returns a deep copy with resolved concrete values.
	resolvedMsg, err := wfcel.ResolveCELFields(node, builder)
	if err != nil {
		return nil, err
	}
	resolvedNode, ok := resolvedMsg.(*reliantv1.Node)
	if !ok {
		return nil, fmt.Errorf("ResolveCELFields returned %T, expected *reliantv1.Node", resolvedMsg)
	}

	// Post-resolution: populate typed repeated fields from resolved CelString literals.
	// For example, ExecuteToolsArgs.ToolCalls (CelString) resolves to a JSON array string;
	// we parse it into ExecuteToolsArgs.ResolvedToolCalls ([]*ToolCallMsg).
	populateResolvedFields(resolvedNode)

	// Resolve ResponseTool schema CEL expressions. The schema field is a
	// google.protobuf.Struct which ResolveCELFields skips. The YAML parser stores
	// CEL expressions as a sentinel Struct with key "__cel_expr__"; evaluate it now.
	resolveResponseToolSchema(resolvedNode, builder)

	return resolvedNode, nil
}

// defaultModelExpr is the CEL expression used when a node's model field is not set.
// This matches the convention used by all call_llm nodes: model: "{{inputs.model}}".
var defaultModelExpr = &reliantv1.CelModelSelector{
	Value: &reliantv1.CelModelSelector_Expr{Expr: "{{inputs.model}}"},
}

// applyModelDefault returns a node with model defaults applied for node types
// that have a model field. If the model is already set, the original node is
// returned unmodified. Otherwise the default expression is set on the node so
// that ResolveCELFields can evaluate it normally.
// NOTE: This mutates the input node's args. This is safe because
// ResolveCELFields deep-copies the node before resolution, and setting an
// idempotent default expression is harmless on repeated calls.
func applyModelDefault(node *reliantv1.Node) *reliantv1.Node {
	switch node.GetType() {
	case model.NodeTypeRouter:
		if args := node.GetRouter(); args != nil && !model.CelModelSelectorIsSet(args.GetModel()) {
			args.Model = defaultModelExpr
		}
	}
	return node
}

// resolveResponseToolSchema unwraps the "__cel_expr__" sentinel from ResponseTool.Schema.
//
// The YAML parser stores CEL schema expressions (e.g. "{{inputs.response_schema}}")
// as a sentinel Struct: {"__cel_expr__": "{{inputs.response_schema}}"}.
//
// ResolveCELFields walks into google.protobuf.Struct via resolveStructPBValueTemplates
// and evaluates the CEL string, producing: {"__cel_expr__": {resolved map}}.
// The key persists — this function unwraps it.
func resolveResponseToolSchema(node *reliantv1.Node, builder *CELContextBuilder) {
	callLLMArgs := node.GetCallLlm()
	if callLLMArgs == nil {
		return
	}
	rt := callLLMArgs.GetResponseTool()
	if rt == nil || rt.GetSchema() == nil {
		return
	}
	schemaMap := rt.GetSchema().AsMap()
	sentinelVal, hasSentinel := schemaMap["__cel_expr__"]
	if !hasSentinel {
		return // Not a sentinel — it's a real schema
	}

	var resultMap map[string]interface{}

	switch v := sentinelVal.(type) {
	case map[string]interface{}:
		// ResolveCELFields already evaluated the expression; unwrap it.
		resultMap = v
	case string:
		// Expression wasn't resolved yet (shouldn't happen, but handle it).
		result, err := builder.EvalString(v)
		if err != nil {
			rt.Schema = nil
			return
		}
		var ok bool
		resultMap, ok = result.(map[string]interface{})
		if !ok {
			rt.Schema = nil
			return
		}
	default:
		rt.Schema = nil
		return
	}

	if s, err := structpb.NewStruct(resultMap); err == nil {
		rt.Schema = s
	} else {
		rt.Schema = nil
	}
}

// populateResolvedFields parses resolved CelString literals into typed repeated fields.
// After CEL resolution, some node types have CelString fields that evaluate to JSON array
// strings (e.g. ExecuteToolsArgs.ToolCalls → "[{\"id\":\"...\"}]"). The corresponding
// typed repeated fields (e.g. ResolvedToolCalls) need to be populated from these literals.
func populateResolvedFields(node *reliantv1.Node) {
	if etArgs := node.GetExecuteTools(); etArgs != nil {
		populateExecuteToolsResolved(etArgs)
	}
	if smArgs := node.GetSaveMessageNode(); smArgs != nil {
		populateSaveMessageResolved(smArgs)
	}
}

// populateSaveMessageResolved copies CelString literal values into the resolved scalar fields.
// For standalone save_message nodes, the CEL resolver populates CelString wrapper fields
// (role, content, display_style) but the activity handler reads the resolved_* scalar fields.
func populateSaveMessageResolved(args *reliantv1.SaveMessageNodeArgs) {
	if args.ResolvedRole == "" {
		args.ResolvedRole = model.CelStringValue(args.GetRole())
	}
	if args.ResolvedContent == "" {
		args.ResolvedContent = model.CelStringValue(args.GetContent())
	}
	if args.ResolvedDisplayStyle == "" {
		args.ResolvedDisplayStyle = model.CelStringValue(args.GetDisplayStyle())
	}

	// Parse ToolCalls CelString into ResolvedToolCalls.
	if len(args.GetResolvedToolCalls()) == 0 {
		toolCallsJSON := model.CelStringValue(args.GetToolCalls())
		if toolCallsJSON != "" {
			var rawToolCalls []json.RawMessage
			if err := json.Unmarshal([]byte(toolCallsJSON), &rawToolCalls); err == nil {
				for _, raw := range rawToolCalls {
					var tc struct {
						ID    string `json:"id"`
						Name  string `json:"name"`
						Input string `json:"input"`
					}
					if err := json.Unmarshal(raw, &tc); err != nil {
						continue
					}
					if tc.ID == "" || tc.Name == "" {
						continue
					}
					args.ResolvedToolCalls = append(args.ResolvedToolCalls, &reliantv1.ToolCallMsg{
						Id:    tc.ID,
						Name:  tc.Name,
						Input: tc.Input,
					})
				}
			}
		}
	}

	// Parse ToolResults CelString into ResolvedToolResults.
	if len(args.GetResolvedToolResults()) == 0 {
		toolResultsJSON := model.CelStringValue(args.GetToolResults())
		if toolResultsJSON != "" {
			var rawToolResults []json.RawMessage
			if err := json.Unmarshal([]byte(toolResultsJSON), &rawToolResults); err == nil {
				for _, raw := range rawToolResults {
					var tr struct {
						ToolCallID string `json:"tool_call_id"`
						Name       string `json:"name"`
						Content    string `json:"content"`
						IsError    bool   `json:"is_error"`
					}
					if err := json.Unmarshal(raw, &tr); err != nil {
						continue
					}
					args.ResolvedToolResults = append(args.ResolvedToolResults, &reliantv1.ToolResultMsg{
						ToolCallId: tr.ToolCallID,
						Name:       tr.Name,
						Content:    strings.ToValidUTF8(tr.Content, "\uFFFD"),
						IsError:    tr.IsError,
					})
				}
			}
		}
	}

	// Parse Attachments CelString into ResolvedAttachments.
	if len(args.GetResolvedAttachments()) == 0 {
		attachmentsJSON := model.CelStringValue(args.GetAttachments())
		if attachmentsJSON != "" {
			var attachments []string
			if err := json.Unmarshal([]byte(attachmentsJSON), &attachments); err == nil {
				args.ResolvedAttachments = attachments
			}
		}
	}
}

// populateExecuteToolsResolved parses the ToolCalls CelString literal into ResolvedToolCalls.
func populateExecuteToolsResolved(args *reliantv1.ExecuteToolsArgs) {
	// Only populate if ResolvedToolCalls is empty and ToolCalls has a literal value.
	if len(args.GetResolvedToolCalls()) > 0 {
		return
	}

	toolCallsJSON := model.CelStringValue(args.GetToolCalls())
	if toolCallsJSON == "" {
		return
	}

	// Parse the JSON array of tool call objects.
	var rawToolCalls []json.RawMessage
	if err := json.Unmarshal([]byte(toolCallsJSON), &rawToolCalls); err != nil {
		// Not a JSON array — might be a single tool call or invalid.
		return
	}

	for _, raw := range rawToolCalls {
		var tc struct {
			ID               string   `json:"id"`
			Name             string   `json:"name"`
			Input            string   `json:"input"`
			AvailableTools   []string `json:"available_tools"`
			AvailablePresets []string `json:"available_presets"`
		}
		if err := json.Unmarshal(raw, &tc); err != nil {
			continue
		}
		if tc.ID == "" || tc.Name == "" {
			continue
		}
		msg := &reliantv1.ToolCallMsg{
			Id:    tc.ID,
			Name:  tc.Name,
			Input: tc.Input,
		}
		args.ResolvedToolCalls = append(args.ResolvedToolCalls, msg)
	}
}

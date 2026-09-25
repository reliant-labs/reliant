// Copyright (c) 2025 Reliant Labs
//
// Temporal workflow/activity code. The exported functions are registered with
// the Temporal SDK by name and invoked by the runtime, not through a Go
// interface a caller could substitute. Determinism constraints, not an
// interface, define this boundary.
//
//forge:exclude-contract: pure static and input validation of v2 workflows
package validation

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"
)

// celString extracts the raw string from a proto CelString wrapper.
// Returns the expression if it's a CEL expression, otherwise the literal value.
func celString(c *reliantv1.CelString) string {
	return model.CelStringRaw(c)
}

// =============================================================================
// CEL SEMANTIC VALIDATION
// =============================================================================

type semanticError struct {
	message    string
	suggestion string
}

// getAvailableToolNames returns a list of response tool names from the schema map.
func getAvailableToolNames(schemas map[string]*ResponseToolSchema) []string {
	names := make([]string, 0, len(schemas))
	for name := range schemas {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// =============================================================================
// CONDITIONAL NODE ACCESS WARNINGS
// =============================================================================

// isNullLiteral checks if an expression is a null literal.
func isNullLiteral(e ast.Expr) bool {
	if e.Kind() != ast.LiteralKind {
		return false
	}
	lit := e.AsLiteral()
	return lit.Type() == types.NullType
}

var (
	toolCallsSourceNodePattern = regexp.MustCompile(`^\s*(?:\{\{\s*)?nodes\.([a-zA-Z_][a-zA-Z0-9_]*)\.tool_calls(?:\s*\}\})?\s*$`)
	toolCallsSourceLoopPattern = regexp.MustCompile(`^\s*(?:\{\{\s*)?nodes\.([a-zA-Z_][a-zA-Z0-9_]*)\.outputs\.tool_calls(?:\s*\}\})?\s*$`)
)

// buildResponseToolContext maps execute_tools nodes to response tool schemas from their tool_calls source.
func buildResponseToolContext(wf *reliantv1.Workflow) *ResponseToolContext {
	if wf == nil {
		return nil
	}

	ctx := &ResponseToolContext{
		AvailableTools:  make(map[string]map[string]*ResponseToolSchema),
		ToolCallSources: make(map[string]ToolCallSource),
	}

	nodeByID := make(map[string]*reliantv1.Node, len(wf.GetNodes()))
	for _, node := range wf.GetNodes() {
		nodeByID[node.GetId()] = node
	}

	for _, node := range wf.GetNodes() {
		executeArgs := node.GetExecuteTools()
		if executeArgs == nil {
			continue
		}

		nodeID := node.GetId()
		rawToolCalls := model.CelStringRaw(executeArgs.GetToolCalls())
		source := resolveToolCallsSource(rawToolCalls)
		if source.Type == SourceDynamic && strings.TrimSpace(rawToolCalls) == "" {
			if inferred, ok := inferToolCallsSourceFromEdges(wf, nodeID, nodeByID); ok {
				source = inferred
			} else if inferred, ok := inferToolCallsSourceFromNodeOrder(wf, nodeID); ok {
				source = inferred
			}
		}
		ctx.ToolCallSources[nodeID] = source

		if source.Type != SourceNode {
			continue
		}

		sourceNode := nodeByID[source.NodeID]
		if sourceNode == nil {
			continue
		}
		callLLMArgs := sourceNode.GetCallLlm()
		if callLLMArgs == nil {
			continue
		}

		responseTool := callLLMArgs.GetResponseTool()
		if responseTool == nil {
			continue
		}

		toolName := strings.TrimSpace(model.CelStringRaw(responseTool.GetName()))
		if toolName == "" || containsTemplate(toolName) {
			continue
		}

		ctx.AvailableTools[nodeID] = map[string]*ResponseToolSchema{
			toolName: responseToolSchemaFromProto(toolName, source.NodeID, responseTool.GetSchema()),
		}
	}

	return ctx
}

func resolveToolCallsSource(toolCallsExpr string) ToolCallSource {
	if matches := toolCallsSourceNodePattern.FindStringSubmatch(toolCallsExpr); len(matches) == 2 {
		return ToolCallSource{Type: SourceNode, NodeID: matches[1]}
	}
	if matches := toolCallsSourceLoopPattern.FindStringSubmatch(toolCallsExpr); len(matches) == 2 {
		return ToolCallSource{Type: SourceLoop, LoopNodeID: matches[1]}
	}
	return ToolCallSource{Type: SourceDynamic, Expression: toolCallsExpr}
}

func inferToolCallsSourceFromEdges(wf *reliantv1.Workflow, executeNodeID string, nodeByID map[string]*reliantv1.Node) (ToolCallSource, bool) {
	if wf == nil {
		return ToolCallSource{}, false
	}

	upstream := make(map[string]bool)
	for _, edge := range wf.GetEdges() {
		if edgeTargetsNode(edge, executeNodeID) {
			upstream[edge.GetFrom()] = true
		}
	}

	if len(upstream) != 1 {
		return ToolCallSource{}, false
	}

	var sourceNodeID string
	for id := range upstream {
		sourceNodeID = id
	}

	sourceNode := nodeByID[sourceNodeID]
	if sourceNode == nil {
		return ToolCallSource{}, false
	}
	if sourceNode.GetCallLlm() != nil {
		return ToolCallSource{Type: SourceNode, NodeID: sourceNodeID}, true
	}
	if sourceNode.GetLoop() != nil {
		return ToolCallSource{Type: SourceLoop, LoopNodeID: sourceNodeID}, true
	}

	return ToolCallSource{}, false
}

func inferToolCallsSourceFromNodeOrder(wf *reliantv1.Workflow, executeNodeID string) (ToolCallSource, bool) {
	if wf == nil {
		return ToolCallSource{}, false
	}
	nodes := wf.GetNodes()
	executeIndex := -1
	for i, node := range nodes {
		if node.GetId() == executeNodeID {
			executeIndex = i
			break
		}
	}
	if executeIndex <= 0 {
		return ToolCallSource{}, false
	}
	for i := executeIndex - 1; i >= 0; i-- {
		node := nodes[i]
		if node.GetCallLlm() != nil {
			return ToolCallSource{Type: SourceNode, NodeID: node.GetId()}, true
		}
	}
	return ToolCallSource{}, false
}

func edgeTargetsNode(edge *reliantv1.Edge, nodeID string) bool {
	if edge == nil {
		return false
	}
	for _, target := range edge.GetDefault() {
		if target == nodeID {
			return true
		}
	}
	for _, c := range edge.GetCases() {
		for _, target := range c.GetTo() {
			if target == nodeID {
				return true
			}
		}
	}
	return false
}

func responseToolSchemaFromProto(toolName, sourceNodeID string, schema *structpb.Struct) *ResponseToolSchema {
	result := &ResponseToolSchema{
		ToolName:     toolName,
		SourceNodeID: sourceNodeID,
		Fields:       make(map[string]*FieldInfo),
	}
	if schema == nil {
		return result
	}
	// `required` decides which fields the LLM may omit (absent at run time).
	if requiredRaw, ok := schema.AsMap()["required"].([]interface{}); ok {
		result.HasRequired = true
		result.Required = make(map[string]bool, len(requiredRaw))
		for _, r := range requiredRaw {
			if name, ok := r.(string); ok {
				result.Required[name] = true
			}
		}
	}
	propertiesRaw, ok := schema.AsMap()["properties"]
	if !ok {
		return result
	}
	properties, ok := propertiesRaw.(map[string]interface{})
	if !ok {
		return result
	}
	for fieldName, fieldSchemaRaw := range properties {
		fieldSchemaMap, ok := fieldSchemaRaw.(map[string]interface{})
		if !ok {
			result.Fields[fieldName] = &FieldInfo{Name: fieldName, Kind: reflect.Interface, IsDynamic: true}
			continue
		}
		result.Fields[fieldName] = jsonSchemaMapToFieldInfo(fieldName, fieldSchemaMap)
	}
	return result
}

func jsonSchemaMapToFieldInfo(name string, schema map[string]interface{}) *FieldInfo {
	info := &FieldInfo{Name: name}
	typeName, _ := schema["type"].(string)
	switch typeName {
	case "string":
		info.Kind = reflect.String
	case "integer":
		info.Kind = reflect.Int64
	case "number":
		info.Kind = reflect.Float64
	case "boolean":
		info.Kind = reflect.Bool
	case "array":
		info.Kind = reflect.Slice
		info.IsSlice = true
	case "object":
		info.Kind = reflect.Map
		info.IsMap = true
		if propertiesRaw, ok := schema["properties"].(map[string]interface{}); ok && len(propertiesRaw) > 0 {
			info.Properties = make(map[string]*FieldInfo, len(propertiesRaw))
			for propertyName, propertySchema := range propertiesRaw {
				propertySchemaMap, ok := propertySchema.(map[string]interface{})
				if !ok {
					info.Properties[propertyName] = &FieldInfo{Name: propertyName, Kind: reflect.Interface, IsDynamic: true}
					continue
				}
				info.Properties[propertyName] = jsonSchemaMapToFieldInfo(propertyName, propertySchemaMap)
			}
			info.IsDynamic = false
		} else {
			info.IsDynamic = true
		}
	default:
		info.Kind = reflect.Interface
		info.IsDynamic = true
	}
	return info
}

// protoInputTypeName returns a human-readable type name for a proto input.
func protoInputTypeName(input *reliantv1.Input) string {
	switch input.GetType() {
	case "string":
		return "string"
	case "integer":
		return "int"
	case "number":
		return "float"
	case "boolean":
		return "bool"
	case "array":
		return "array"
	case "object":
		return "object"
	case "enum":
		return "string"
	case "message":
		return "string"
	default:
		return ""
	}
}

// sharedRegistry is the package-level TypeRegistry used for node output type resolution.
// Built from proto descriptors at first use.
var sharedRegistry = wfcel.NewTypeRegistry()

// registryOutputFieldNames returns the output field names for a node type from the registry.
// Handles name normalization (e.g., wfv2 "save_message" → proto oneof "save_message_node").
func registryOutputFieldNames(registry *wfcel.TypeRegistry, nodeType string) []string {
	fields := registryOutputFields(registry, nodeType)
	if len(fields) == 0 {
		return nil
	}
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	return names
}

// registryOutputFields returns wfcel.FieldInfo for a node type's outputs.
// Tries the node type directly, then with "_node" suffix for proto name mismatches.
func registryOutputFields(registry *wfcel.TypeRegistry, nodeType string) []wfcel.FieldInfo {
	if registry == nil {
		return nil
	}
	if fields := registry.OutputFieldsForNodeType(nodeType); len(fields) > 0 {
		return fields
	}
	// Proto oneof field names may differ from wfv2 constants (e.g., save_message_node vs save_message)
	return registry.OutputFieldsForNodeType(nodeType + "_node")
}

// registryHasOutput returns true if the registry knows about outputs for this node type.
func registryHasOutput(registry *wfcel.TypeRegistry, nodeType string) bool {
	if registry == nil {
		return false
	}
	if _, ok := registry.OutputForNodeType(nodeType); ok {
		return true
	}
	_, ok := registry.OutputForNodeType(nodeType + "_node")
	return ok
}

// getProtoNodeOutputFields returns the known output field names for a proto node
// using the TypeRegistry, with special handling for workflow/loop inline outputs.
func getProtoNodeOutputFields(node *reliantv1.Node, loader WorkflowLoader) []string {
	nodeType := node.GetType()

	// Workflow, loop, and router nodes have dynamic outputs that aren't fully
	// described by the proto output type (child outputs are flattened to top level).
	switch nodeType {
	case model.NodeTypeWorkflow:
		if wfArgs := node.GetWorkflow(); wfArgs != nil && wfArgs.GetInline() != nil {
			var fields []string
			for key := range wfArgs.GetInline().GetOutputs() {
				fields = append(fields, key)
			}
			fields = append(fields, "outputs")
			return fields
		}
		// Ref-based workflow nodes: resolve via loader if available.
		if wfArgs := node.GetWorkflow(); wfArgs != nil && loader != nil {
			ref := model.CelStringValue(wfArgs.GetRef())
			if ref != "" && !containsTemplate(ref) {
				childWf, err := loader(ref)
				if err == nil && childWf != nil {
					var fields []string
					for key := range childWf.GetOutputs() {
						fields = append(fields, key)
					}
					fields = append(fields, "outputs")
					return fields
				}
			}
		}
		return nil
	case model.NodeTypeLoop:
		fields := []string{"_iterations", "_completed", "_failed"}
		// Parallel loops additionally produce _results (keyed map) and _parallel (bool)
		if loopArgs := node.GetLoop(); loopArgs != nil && model.CelBoolValue(loopArgs.GetParallel()) {
			fields = append(fields, "_results", "_parallel")
		}
		if loopArgs := node.GetLoop(); loopArgs != nil && loopArgs.GetInline() != nil {
			for key := range loopArgs.GetInline().GetOutputs() {
				fields = append(fields, key)
			}
		} else if loopArgs := node.GetLoop(); loopArgs != nil && loader != nil {
			// Ref-based loop nodes: resolve via loader if available.
			ref := model.CelStringValue(loopArgs.GetRef())
			if ref != "" && !containsTemplate(ref) {
				childWf, err := loader(ref)
				if err == nil && childWf != nil {
					for key := range childWf.GetOutputs() {
						fields = append(fields, key)
					}
				}
			}
		}
		return fields
	case model.NodeTypeRouter:
		// Router has fixed top-level fields; child outputs are nested under `outputs`.
		regFields := registryOutputFieldNames(sharedRegistry, model.NodeTypeRouter)
		if len(regFields) == 0 {
			return nil
		}
		// When router declares outputs, those keys become top-level fields.
		if routerArgs := node.GetRouter(); routerArgs != nil {
			for key := range routerArgs.GetOutputs() {
				regFields = append(regFields, key)
			}
		}
		return regFields
	}

	// All other node types: use the registry.
	return registryOutputFieldNames(sharedRegistry, nodeType)
}

// =============================================================================
// HELPERS
// =============================================================================

var (
	// nodeFieldRegex captures nodes.<nodeID>.<fieldPath> where fieldPath can include dots and brackets
	// e.g., nodes.call_llm.message.content, nodes.call_llm.tool_calls[0].name
	nodeFieldRegex = regexp.MustCompile(`nodes\.([a-zA-Z_][a-zA-Z0-9_]*)\.([a-zA-Z_][a-zA-Z0-9_.\[\]]*)`)

	// responseDataFieldRegex captures response_data.<tool_name>.<field> patterns
	// Used to validate access to response tool data
	responseDataFieldRegex = regexp.MustCompile(`response_data\.([a-zA-Z_][a-zA-Z0-9_]*)\.([a-zA-Z_][a-zA-Z0-9_]*)`)
)

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

func containsTemplate(s string) bool {
	return strings.Contains(s, "{{") && strings.Contains(s, "}}")
}

func suggestSimilar(target string, candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}

	best := ""
	bestDist := len(target) + 1

	for _, c := range candidates {
		dist := levenshtein(target, c)
		if dist < bestDist && dist <= 3 {
			bestDist = dist
			best = c
		}
	}

	if best != "" {
		return fmt.Sprintf("did you mean '%s'?", best)
	}

	if len(candidates) <= 5 {
		return fmt.Sprintf("valid options: %s", strings.Join(candidates, ", "))
	}
	return ""
}

func suggestNamespace(ident string) string {
	suggestions := map[string]string{
		"input":     "did you mean 'inputs' (plural)?",
		"node":      "did you mean 'nodes' (plural)?",
		"workflows": "did you mean 'workflow' (singular)?",
		"step":      "did you mean 'nodes'?",
		"steps":     "did you mean 'nodes'?",
		"outputs":   "did you mean 'output'? (output.* is only available in save_message)",
	}
	return suggestions[strings.ToLower(ident)]
}

var (
	// celUndeclaredRefRegex extracts the identifier from CEL "undeclared reference to 'X'" errors.
	celUndeclaredRefRegex = regexp.MustCompile(`undeclared reference to '([^']+)'`)
	// celUndefinedFieldRegex extracts the field name from CEL "undefined field 'X'" errors.
	celUndefinedFieldRegex = regexp.MustCompile(`undefined field '([^']+)'`)
)

// suggestForCELCompilationError examines a CEL compilation error message and returns
// a suggestion string (e.g. "did you mean 'inputs'?") using the same Levenshtein and
// namespace heuristics as the regex validation path.
func suggestForCELCompilationError(msg string, typeCtx *WorkflowTypeContext) string {
	// Try undeclared reference: "undeclared reference to 'X'"
	if m := celUndeclaredRefRegex.FindStringSubmatch(msg); len(m) == 2 {
		ident := m[1]

		// Check for common namespace typos (input→inputs, node→nodes, etc.)
		if s := suggestNamespace(ident); s != "" {
			return s
		}

		// Try matching against known input names and node IDs
		if typeCtx != nil {
			var candidates []string
			for name := range typeCtx.InputFields {
				candidates = append(candidates, name)
			}
			for name := range typeCtx.InputGroups {
				candidates = append(candidates, name)
			}
			for id := range typeCtx.NodeOutputs {
				candidates = append(candidates, id)
			}
			if s := suggestSimilar(ident, candidates); s != "" {
				return s
			}
		}
	}

	// Try undefined field: "undefined field 'X'"
	if m := celUndefinedFieldRegex.FindStringSubmatch(msg); len(m) == 2 {
		fieldName := m[1]

		if typeCtx != nil {
			// Collect all known field names from node outputs and input fields
			var candidates []string
			for _, fields := range typeCtx.NodeOutputs {
				for name := range fields {
					candidates = append(candidates, name)
				}
			}
			for name := range typeCtx.InputFields {
				candidates = append(candidates, name)
			}
			if s := suggestSimilar(fieldName, candidates); s != "" {
				return s
			}
		}
	}

	return ""
}

// extractTopLevelField extracts the first segment from a field path.
// e.g., "message.content" -> "message", "tool_calls[0].name" -> "tool_calls"
func extractTopLevelField(fieldPath string) string {
	// Find first . or [
	for i, c := range fieldPath {
		if c == '.' || c == '[' {
			return fieldPath[:i]
		}
	}
	return fieldPath
}

func levenshtein(a, b string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	matrix := make([][]int, len(a)+1)
	for i := range matrix {
		matrix[i] = make([]int, len(b)+1)
		matrix[i][0] = i
	}
	for j := 0; j <= len(b); j++ {
		matrix[0][j] = j
	}

	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			min := matrix[i-1][j] + 1
			if matrix[i][j-1]+1 < min {
				min = matrix[i][j-1] + 1
			}
			if matrix[i-1][j-1]+cost < min {
				min = matrix[i-1][j-1] + cost
			}
			matrix[i][j] = min
		}
	}

	return matrix[len(a)][len(b)]
}

// =============================================================================
// CEL COMPILATION-BASED VALIDATION
// =============================================================================
//
// This section provides CEL validation using actual compilation instead of regex.
// Benefits:
// - Uses CEL's native type checker for accurate error detection
// - Catches invalid field access like "nodes.loop.iterations" (should be "_iterations")
// - Provides detailed error messages with position information
// - Infers output types for SubWorkflow type propagation
//
// The approach:
// 1. Build a typed CEL environment with registered node output types
// 2. Compile each CEL expression
// 3. Report compilation errors (which include type errors)
// 4. For SubWorkflow outputs, infer types via ast.OutputType()

// BuildWorkflowTypeContext creates a WorkflowTypeContext from a proto V2Workflow.
// This provides type information for CEL compilation-based validation.
func BuildWorkflowTypeContext(wf *reliantv1.Workflow, loader WorkflowLoader) *WorkflowTypeContext {
	if wf == nil {
		return nil
	}

	ctx := &WorkflowTypeContext{
		InputFields:      make(map[string]*FieldInfo),
		InputGroups:      make(map[string]map[string]*FieldInfo),
		NodeOutputs:      make(map[string]map[string]*FieldInfo),
		OutputFields:     make(map[string]*FieldInfo),
		NodeTypes:        make(map[string]string),
		Registry:         sharedRegistry,
		ConditionalNodes: make(map[string]string),
	}

	// Extract input types from proto
	for name, input := range wf.GetInputs() {
		if input == nil {
			continue
		}
		if model.IsGroupInput(input) {
			groupFields := make(map[string]*FieldInfo)
			for fieldName, nested := range model.GetGroupInputs(input) {
				groupFields[fieldName] = protoInputToFieldInfo(fieldName, nested)
			}
			ctx.InputGroups[name] = groupFields
		} else {
			ctx.InputFields[name] = protoInputToFieldInfo(name, input)
		}
	}

	// Reserved system inputs (chat_id, workflow_id, unique_activity_id) are
	// engine-provided execution-context values the runtime injects unconditionally,
	// so they are always present. Declare them as typed, first-class members of the
	// type context (unless the workflow explicitly declared an input of the same
	// name, which then wins) so a template can reference e.g. `inputs.chat_id` and
	// have it type-check — while an undeclared `inputs.<name>` still errors. See
	// reservedSystemInputTypes; keep in sync with workflow.RuntimeInjectedInputs.
	for name, kind := range reservedSystemInputTypes {
		if _, declared := ctx.InputFields[name]; declared {
			continue
		}
		if _, declared := ctx.InputGroups[name]; declared {
			continue
		}
		ctx.InputFields[name] = &FieldInfo{Name: name, Kind: kind}
	}

	// Extract node types and output fields
	extendedOutputNodes := make(map[string]bool)
	for _, node := range wf.GetNodes() {
		nodeID := node.GetId()
		nodeType := node.GetType()
		ctx.NodeTypes[nodeID] = nodeType

		// Track conditional nodes (exclude join which uses condition differently)
		if condExpr := model.ConditionExpr(node); condExpr != "" && nodeType != model.NodeTypeJoin {
			ctx.ConditionalNodes[nodeID] = condExpr
		}

		// Track nodes with response tools (structured output extends node outputs).
		if callLLM := node.GetCallLlm(); callLLM != nil && callLLM.GetResponseTool() != nil {
			extendedOutputNodes[nodeID] = true
		}
		if execTools := node.GetExecuteTools(); execTools != nil && len(execTools.GetResponseToolSchemas()) > 0 {
			extendedOutputNodes[nodeID] = true
		}

		// Get output fields using the known output type for this node type
		outputFields := getProtoNodeOutputFieldInfos(node, loader)
		if outputFields != nil {
			ctx.NodeOutputs[nodeID] = outputFields
		}
	}

	if len(extendedOutputNodes) > 0 {
		ctx.NodesWithExtendedOutputs = extendedOutputNodes
	}

	// Build response tool context for response_data validation
	ctx.ResponseTools = buildResponseToolContext(wf)

	return ctx
}

// protoInputToFieldInfo converts a proto V2Input to FieldInfo.
func protoInputToFieldInfo(name string, input *reliantv1.Input) *FieldInfo {
	info := &FieldInfo{Name: name}
	if input == nil {
		info.Kind = reflect.Interface
		info.IsDynamic = true
		return info
	}

	switch input.GetType() {
	case "string":
		info.Kind = reflect.String
	case "number":
		info.Kind = reflect.Float64
	case "integer":
		info.Kind = reflect.Int64
	case "boolean":
		info.Kind = reflect.Bool
	case "array":
		info.Kind = reflect.Slice
		info.IsSlice = true
	case "object":
		info.Kind = reflect.Map
		info.IsMap = true
		// Extract properties if available
		if cfg, ok := input.GetConfig().(*reliantv1.Input_ObjectInput); ok && cfg.ObjectInput != nil {
			props := cfg.ObjectInput.GetProperties()
			if len(props) > 0 {
				info.Properties = make(map[string]*FieldInfo, len(props))
				for pName, pSchema := range props {
					info.Properties[pName] = protoPropertySchemaToFieldInfo(pName, pSchema)
				}
				info.IsDynamic = false
				if cfg.ObjectInput.AdditionalProperties != nil && cfg.ObjectInput.GetAdditionalProperties() {
					info.AdditionalPropertiesAllowed = true
				}
			} else {
				info.IsDynamic = true
			}
		} else {
			info.IsDynamic = true
		}
	case "enum":
		// Check if multi-select
		if cfg, ok := input.GetConfig().(*reliantv1.Input_EnumInput); ok && cfg.EnumInput != nil && cfg.EnumInput.GetMulti() {
			info.Kind = reflect.Slice
		} else {
			info.Kind = reflect.String
		}
	case "any":
		info.Kind = reflect.Interface
		info.IsDynamic = true
	case "model":
		info.Kind = reflect.Map
		info.IsMap = true
		info.IsDynamic = true
	case "tools", "attachments":
		info.Kind = reflect.Slice
		info.IsSlice = true
	default:
		// message, preset, unknown — treat as string
		info.Kind = reflect.String
	}

	return info
}

// protoPropertySchemaToFieldInfo converts a proto V2PropertySchema to FieldInfo.
func protoPropertySchemaToFieldInfo(name string, schema *reliantv1.PropertySchema) *FieldInfo {
	if schema == nil {
		return &FieldInfo{Name: name, Kind: reflect.Interface, IsDynamic: true}
	}

	info := &FieldInfo{
		Name:        name,
		Description: schema.GetDescription(),
	}

	switch schema.GetType() {
	case "string":
		info.Kind = reflect.String
	case "integer":
		info.Kind = reflect.Int64
	case "number":
		info.Kind = reflect.Float64
	case "boolean":
		info.Kind = reflect.Bool
	case "array":
		info.Kind = reflect.Slice
		info.IsSlice = true
	case "object":
		info.Kind = reflect.Map
		info.IsMap = true
		// Extract nested properties if available
		if props := schema.GetProperties(); len(props) > 0 {
			info.Properties = make(map[string]*FieldInfo, len(props))
			for pName, pSchema := range props {
				info.Properties[pName] = protoPropertySchemaToFieldInfo(pName, pSchema)
			}
			info.IsDynamic = false
		} else {
			info.IsDynamic = true
		}
	default:
		info.Kind = reflect.Interface
		info.IsDynamic = true
	}

	return info
}

// getProtoNodeOutputFieldInfos returns the FieldInfo map for a proto node's output type.
// Uses the TypeRegistry for standard node types and special handling for workflow/loop.
func getProtoNodeOutputFieldInfos(node *reliantv1.Node, loader WorkflowLoader) map[string]*FieldInfo {
	nodeType := node.GetType()

	// Workflow/loop have dynamic inline outputs — get names and mark as dynamic.
	if nodeType == model.NodeTypeWorkflow || nodeType == model.NodeTypeLoop {
		names := getProtoNodeOutputFields(node, loader)
		if len(names) == 0 {
			return nil
		}
		result := make(map[string]*FieldInfo, len(names))
		for _, name := range names {
			if name == "_iterations" {
				result[name] = &FieldInfo{
					Name: name,
					Kind: reflect.Int64,
				}
			} else {
				result[name] = &FieldInfo{
					Name:      name,
					Kind:      reflect.Interface,
					IsDynamic: true,
				}
			}
		}
		return result
	}

	// Router nodes: use registry for typed fields, then enrich `outputs` sub-field
	// with the union of all candidate workflow output fields when a loader is available.
	if nodeType == model.NodeTypeRouter {
		return getRouterOutputFieldInfos(node, loader)
	}

	// Standard node types: use registry for typed field info.
	regFields := registryOutputFields(sharedRegistry, nodeType)
	if len(regFields) == 0 {
		return nil
	}

	// Get the output message descriptor for sub-field extraction.
	outputMD := registryOutputDescriptor(sharedRegistry, nodeType)

	result := make(map[string]*FieldInfo, len(regFields))
	for _, rf := range regFields {
		info := v2celFieldInfoToValidation(&rf)
		// For message-type fields, extract sub-fields from the proto descriptor.
		if rf.Type == "message" && !rf.IsRepeated && outputMD != nil {
			if props := extractMessageFieldProperties(outputMD, rf.Name); len(props) > 0 {
				info.Properties = props
			}
		}
		result[rf.Name] = info
	}
	return result
}

// getRouterOutputFieldInfos builds typed FieldInfo for a router node's outputs.
// Router has 5 fixed top-level proto fields. Child workflow outputs are accessed
// via the `outputs` sub-field (e.g. nodes.router.outputs.message).
//
// When a loader is available, candidate workflow outputs are resolved and added
// as Properties of the `outputs` field. When no loader is available, the `outputs`
// field is marked IsDynamic with AdditionalPropertiesAllowed to allow any sub-field access.
func getRouterOutputFieldInfos(node *reliantv1.Node, loader WorkflowLoader) map[string]*FieldInfo {
	regFields := registryOutputFields(sharedRegistry, model.NodeTypeRouter)
	if len(regFields) == 0 {
		return nil
	}

	outputMD := registryOutputDescriptor(sharedRegistry, model.NodeTypeRouter)

	result := make(map[string]*FieldInfo, len(regFields))
	for _, rf := range regFields {
		info := v2celFieldInfoToValidation(&rf)
		if rf.Type == "message" && !rf.IsRepeated && outputMD != nil {
			if props := extractMessageFieldProperties(outputMD, rf.Name); len(props) > 0 {
				info.Properties = props
			}
		}
		result[rf.Name] = info
	}

	// Enrich the `outputs` sub-field with resolved candidate workflow outputs.
	var unionProps map[string]*FieldInfo
	if loader != nil {
		if routerArgs := node.GetRouter(); routerArgs != nil {
			unionProps = make(map[string]*FieldInfo)
			for _, candidate := range routerArgs.GetWorkflows() {
				ref := candidate.GetRef()
				if ref == "" {
					continue
				}
				childWf, err := loader(ref)
				if err != nil || childWf == nil {
					continue
				}
				for key := range childWf.GetOutputs() {
					if _, exists := unionProps[key]; !exists {
						unionProps[key] = &FieldInfo{
							Name:      key,
							Kind:      reflect.Interface,
							IsDynamic: true,
						}
					}
				}
			}
			if len(unionProps) == 0 {
				unionProps = nil
			}
		}
	}

	outputsInfo := result["outputs"]
	if outputsInfo != nil {
		if unionProps != nil {
			if outputsInfo.Properties == nil {
				outputsInfo.Properties = make(map[string]*FieldInfo)
			}
			for k, v := range unionProps {
				outputsInfo.Properties[k] = v
			}
		}
		// Always allow dynamic sub-field access on outputs (child outputs vary by selected workflow).
		outputsInfo.IsDynamic = true
		outputsInfo.AdditionalPropertiesAllowed = true
	}

	// Add declared outputs as top-level fields so `nodes.router_id.my_field` resolves.
	if routerArgs := node.GetRouter(); routerArgs != nil {
		for key := range routerArgs.GetOutputs() {
			result[key] = &FieldInfo{
				Name:      key,
				Kind:      reflect.Interface,
				IsDynamic: true,
			}
		}
	}

	return result
}

// registryOutputDescriptor returns the proto MessageDescriptor for a node type's output.
// Tries the node type directly, then with "_node" suffix for proto name mismatches.
func registryOutputDescriptor(registry *wfcel.TypeRegistry, nodeType string) protoreflect.MessageDescriptor {
	if registry == nil {
		return nil
	}
	if md, ok := registry.OutputForNodeType(nodeType); ok {
		return md
	}
	if md, ok := registry.OutputForNodeType(nodeType + "_node"); ok {
		return md
	}
	return nil
}

// extractMessageFieldProperties extracts sub-fields from a message-type field in a proto output.
// Given the output message descriptor and the field name, it finds the field's message type
// and returns its fields as a FieldInfo Properties map.
func extractMessageFieldProperties(outputMD protoreflect.MessageDescriptor, fieldName string) map[string]*FieldInfo {
	if outputMD == nil {
		return nil
	}

	// Find the field descriptor within the output message
	fd := outputMD.Fields().ByName(protoreflect.Name(fieldName))
	if fd == nil || fd.Kind() != protoreflect.MessageKind {
		return nil
	}

	// Get the sub-message descriptor
	subMD := fd.Message()
	if subMD == nil {
		return nil
	}

	// Skip well-known wrapper types that should be treated as dynamic maps,
	// not as objects with fixed fields. google.protobuf.Struct is a map<string, Value>
	// at runtime, and extracting its internal proto fields ("fields") would break
	// operators like `in` and `[]` that expect map/dyn types.
	if subMD.FullName() == "google.protobuf.Struct" {
		return nil
	}

	// Extract fields from the sub-message
	fields := subMD.Fields()
	props := make(map[string]*FieldInfo, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		subFD := fields.Get(i)
		name := string(subFD.Name())
		props[name] = protoFieldDescriptorToFieldInfo(subFD)
	}
	return props
}

// protoFieldDescriptorToFieldInfo converts a protoreflect.FieldDescriptor to a validation FieldInfo.
func protoFieldDescriptorToFieldInfo(fd protoreflect.FieldDescriptor) *FieldInfo {
	info := &FieldInfo{
		Name:    string(fd.Name()),
		IsSlice: fd.IsList(),
		IsMap:   fd.IsMap(),
	}

	if fd.IsMap() {
		info.Kind = reflect.Map
		info.IsMap = true
		return info
	}

	switch fd.Kind() {
	case protoreflect.BoolKind:
		info.Kind = reflect.Bool
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		info.Kind = reflect.Int64
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		info.Kind = reflect.Float64
	case protoreflect.StringKind:
		info.Kind = reflect.String
	case protoreflect.BytesKind:
		info.Kind = reflect.Slice
	default:
		// message, enum, group — treat as dynamic
		info.Kind = reflect.Interface
		info.IsDynamic = true
	}

	if fd.IsList() {
		info.IsSlice = true
	}

	return info
}

// v2celFieldInfoToValidation converts a wfcel.FieldInfo to validation FieldInfo.
func v2celFieldInfoToValidation(rf *wfcel.FieldInfo) *FieldInfo {
	info := &FieldInfo{
		Name:    rf.Name,
		IsSlice: rf.IsRepeated,
		IsMap:   rf.IsMap,
	}
	switch rf.Type {
	case "string", "model_selector":
		info.Kind = reflect.String
	case "bool":
		info.Kind = reflect.Bool
	case "int":
		info.Kind = reflect.Int64
	case "double":
		info.Kind = reflect.Float64
	case "bytes":
		info.Kind = reflect.Slice
	case "string_list":
		info.Kind = reflect.Slice
		info.IsSlice = true
	case "map":
		info.Kind = reflect.Map
		info.IsMap = true
	default:
		// message, enum, unknown — treat as dynamic
		info.Kind = reflect.Interface
		info.IsDynamic = true
	}
	return info
}

// rewriteNodesAccess lowers user-facing node access syntax into encoded synthetic
// validator variables for CEL compilation.
//
// It is source-aware rather than simple substring replacement so we only rewrite
// actual `nodes.<nodeID>` accesses, preserve string literals, and keep
// `has(nodes.<nodeID>)` intact because CEL presence macros require a field
// selection expression rather than a bare synthetic variable.
func rewriteNodesAccess(expr string, nodeIDs []string) string {
	orderedNodeIDs := make([]string, len(nodeIDs))
	copy(orderedNodeIDs, nodeIDs)
	sort.SliceStable(orderedNodeIDs, func(i, j int) bool {
		return len(orderedNodeIDs[i]) > len(orderedNodeIDs[j])
	})

	var result strings.Builder
	result.Grow(len(expr))

	inSingleQuote := false
	inDoubleQuote := false
	escaped := false

	for i := 0; i < len(expr); {
		ch := expr[i]

		if escaped {
			result.WriteByte(ch)
			escaped = false
			i++
			continue
		}

		if ch == '\\' && (inSingleQuote || inDoubleQuote) {
			result.WriteByte(ch)
			escaped = true
			i++
			continue
		}

		if !inDoubleQuote && ch == '\'' {
			inSingleQuote = !inSingleQuote
			result.WriteByte(ch)
			i++
			continue
		}
		if !inSingleQuote && ch == '"' {
			inDoubleQuote = !inDoubleQuote
			result.WriteByte(ch)
			i++
			continue
		}

		if !inSingleQuote && !inDoubleQuote && strings.HasPrefix(expr[i:], "nodes.") {
			matchedNodeID := ""
			for _, nodeID := range orderedNodeIDs {
				if strings.HasPrefix(expr[i+len("nodes."):], nodeID) {
					matchedNodeID = nodeID
					break
				}
			}
			if matchedNodeID != "" {
				nextIndex := i + len("nodes.") + len(matchedNodeID)
				nextChar, hasNextChar := byte(0), false
				if nextIndex < len(expr) {
					nextChar = expr[nextIndex]
					hasNextChar = true
				}

				if !hasNextChar || isNodeAccessTerminator(nextChar) || nextChar == '.' {
					preserveBareHasAccess := nextChar != '.' && hasDirectHasPrefix(expr, i)
					if preserveBareHasAccess {
						result.WriteString("nodes.")
						result.WriteString(matchedNodeID)
					} else {
						result.WriteString(nodeCELVariableName(matchedNodeID))
					}
					i = nextIndex
					continue
				}
			}
		}

		result.WriteByte(ch)
		i++
	}

	return result.String()
}

func isNodeAccessTerminator(ch byte) bool {
	switch ch {
	case ' ', '\t', '\n', '\r', ')', ']', '}', ',', '?', ':', '+', '-', '*', '/', '%', '<', '>', '=', '!', '&', '|':
		return true
	default:
		return false
	}
}

func hasDirectHasPrefix(expr string, pos int) bool {
	j := pos - 1
	for j >= 0 && (expr[j] == ' ' || expr[j] == '\t' || expr[j] == '\n' || expr[j] == '\r') {
		j--
	}
	if j < 3 {
		return false
	}
	return expr[j-3:j+1] == "has("
}

// rejectSaveMessageNodesRefs reports every save_message field that reads the
// `nodes` namespace. A node's save_message is written by whoever executes the
// node — for an activity, the worker — which has no view of other nodes'
// outputs, so save_message may read only output, inputs, workflow and iter.
func rejectSaveMessageNodesRefs(fields map[string]string, condition string, basePath []string, result *Result) {
	check := func(fieldName string, exprs []string) {
		if len(exprs) == 0 {
			return
		}
		refs, err := wfcel.ReferencesOf(exprs, wfcel.CELNodes)
		if err != nil || !refs.Referenced {
			// A parse error is reported by the compile pass.
			return
		}
		result.Add(&Error{
			Severity: SeverityError,
			Category: CategoryCELSemantic,
			Path:     append(append([]string{}, basePath...), fieldName),
			Message: fmt.Sprintf("save_message field '%s' references `nodes`, which is not available in save_message. "+
				"A node's message is written by whoever executes the node, so save_message can read only "+
				"`output` (this node's result), `inputs`, `workflow` and `iter`", fieldName),
			Suggestion: "read the value from `output` (expose it on this node's output if needed) or pass it through `inputs`",
		})
	}
	for fieldName, value := range fields {
		check(fieldName, wfcel.TemplateExpressions(value, false))
	}
	if condition != "" && len(extractTemplateExpressions(condition)) == 0 {
		// A {{ }}-wrapped condition is reported by validateSaveMessageCondition.
		check("condition", []string{condition})
	}
}

// rejectTemplateDelimitersInRawCEL reports a raw-CEL field (node condition,
// edge case condition, switch case condition, loop while, save_message
// condition) whose value contains {{ }} template delimiters, and returns true
// when it did. These fields are evaluated as CEL directly, never as templates:
// a wrapped value is not "the same expression" — it either fails to compile
// or, worse, parses as something else. One behavior everywhere: reject, with
// the unwrapped expression as the fix. Nothing strips the delimiters.
func rejectTemplateDelimitersInRawCEL(expr string, path []string, result *Result) bool {
	matches := extractTemplateExpressions(expr)
	if len(matches) == 0 {
		return false
	}
	message := "conditions are raw CEL; remove the {{ }}"
	suggestion := "write the expression without {{ }}"
	if trimmed := strings.TrimSpace(expr); len(matches) == 1 && matches[0].full == trimmed && matches[0].expr != "" {
		message += ": " + matches[0].expr
		suggestion = "use: " + matches[0].expr
	}
	result.Add(&Error{
		Severity:   SeverityError,
		Category:   CategoryCELSemantic,
		Path:       append([]string{}, path...),
		Message:    message,
		Suggestion: suggestion,
	})
	return true
}

// validateSwitchCaseConditions applies the raw-CEL rule to the switch case
// conditions persisted in the workflow's UI metadata.
func validateSwitchCaseConditions(wf *reliantv1.Workflow, basePath []string, result *Result) {
	switches := wf.GetUi().GetSwitches()
	for _, key := range sortedSwitchKeys(switches) {
		for j, c := range switches[key].GetCases() {
			expr := model.DirectCelExpr(c.GetCondition())
			if expr == "" {
				continue
			}
			path := append(append([]string{}, basePath...), "ui", "switches", key, "cases", fmt.Sprintf("[%d]", j), "condition")
			rejectTemplateDelimitersInRawCEL(expr, path, result)
		}
	}
}

func sortedSwitchKeys(m map[string]*reliantv1.SwitchMetadata) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// isSaveMessageListField returns true if the field expects a list type.
func isSaveMessageListField(fieldName string) bool {
	return fieldName == "tool_calls" || fieldName == "tool_results" || fieldName == "attachments"
}

// validateSaveMessageFieldReturnType validates that a save_message field's CEL expression
// returns the correct type.
func validateSaveMessageFieldReturnType(fieldName, value string, path []string, env *cel.Env, nodeIDs []string, result *Result) {
	// Extract CEL expressions from the template
	matches := extractTemplateExpressions(value)

	// If the value is a single CEL expression (entire field is just {{expr}}), validate its return type.
	// If it has multiple expressions or mixed text, individual expressions will be converted to strings,
	// so we only validate type compatibility for single-expression fields.
	isSingleExpression := len(matches) == 1 && value == matches[0].full

	for _, match := range matches {
		expr := match.expr
		if expr == "" {
			continue
		}

		// Rewrite nodes.X.field to nodes_X.field for typed validation
		rewrittenExpr := rewriteNodesAccess(expr, nodeIDs)

		// Compile the expression to get its output type
		celAst, issues := env.Compile(rewrittenExpr)
		if issues != nil && issues.Err() != nil {
			// Compilation errors are already reported by validateCELTemplateStringWithCompilation
			continue
		}

		if celAst == nil {
			continue
		}

		// Get the output type from the AST
		actualType := celAst.OutputType()
		if actualType == nil {
			continue
		}

		// Only validate return type for single-expression fields.
		// For multi-expression or mixed text templates, expressions will be converted to strings.
		if !isSingleExpression {
			continue
		}

		// Check if the type is compatible with the expected type for this field
		if !isSaveMessageFieldTypeCompatible(fieldName, actualType) {
			expectedType := expectedSaveMessageFieldType(fieldName)
			actualTypeStr := formatCELType(actualType)

			suggestion := getSaveMessageTypeSuggestion(fieldName, actualTypeStr)
			result.Add(&Error{
				Severity: SeverityError,
				Category: CategoryCELSemantic,
				Path:     path,
				Message:  fmt.Sprintf("save_message field '%s' expects type '%s' but CEL expression returns '%s'. %s", fieldName, expectedType, actualTypeStr, suggestion),
			})
		}
	}
}

// saveMessageFieldTypeInfo describes expected CEL evaluation types for SaveMessageConfig fields.
// This is domain knowledge that can't be inferred via reflection since all fields are string templates.
type saveMessageFieldTypeInfo struct {
	expectedType   string                         // Human-readable type for error messages
	typeCheckFunc  func(celType *cel.Type) bool   // Function to check type compatibility
	suggestionFunc func(actualType string) string // Function to generate helpful suggestions
}

// saveMessageFieldTypes maps SaveMessageConfig field names to their expected CEL evaluation types.
// New fields added to SaveMessageConfig will automatically be validated for CEL syntax,
// but won't have strict type checking unless added to this map.
var saveMessageFieldTypes = map[string]*saveMessageFieldTypeInfo{
	"role": {
		expectedType: "string",
		typeCheckFunc: func(celType *cel.Type) bool {
			return celType != nil && normalizeOptionalType(celType.String()) == "string"
		},
		suggestionFunc: func(actualType string) string {
			if strings.HasPrefix(actualType, "list(") {
				return "Use a string expression like {{output.message.role}} or {{output.role}}"
			}
			return "Ensure the expression returns a string value"
		},
	},
	"content": {
		expectedType: "string",
		typeCheckFunc: func(celType *cel.Type) bool {
			return celType != nil && normalizeOptionalType(celType.String()) == "string"
		},
		suggestionFunc: func(actualType string) string {
			if strings.HasPrefix(actualType, "list(") {
				return "Use a string expression like {{output.message.text}} or {{output.content}}"
			}
			return "Ensure the expression returns a string value"
		},
	},
	"tool_calls": {
		expectedType: "list(ToolCall)",
		typeCheckFunc: func(celType *cel.Type) bool {
			if celType == nil {
				return false
			}
			typeStr := normalizeOptionalType(celType.String())
			return strings.HasPrefix(typeStr, "list(") && strings.Contains(typeStr, "ToolCall")
		},
		suggestionFunc: func(actualType string) string {
			if actualType == "string" {
				return "tool_calls must be a list. Use {{output.tool_calls}} from a call_llm node"
			}
			if strings.Contains(actualType, "ToolResult") {
				return "This appears to be tool_results. Use the 'tool_results' field instead"
			}
			return "Use {{output.tool_calls}} from a call_llm node"
		},
	},
	"tool_results": {
		expectedType: "list(ToolResult)",
		typeCheckFunc: func(celType *cel.Type) bool {
			if celType == nil {
				return false
			}
			typeStr := normalizeOptionalType(celType.String())
			return strings.HasPrefix(typeStr, "list(") && strings.Contains(typeStr, "ToolResult")
		},
		suggestionFunc: func(actualType string) string {
			if actualType == "string" {
				return "tool_results must be a list. Use {{output.tool_results}} from an execute_tools node"
			}
			if strings.Contains(actualType, "ToolCall") {
				return "This appears to be tool_calls. Use the 'tool_calls' field instead"
			}
			return "Use {{output.tool_results}} from an execute_tools node"
		},
	},
	"attachments": {
		expectedType: "list(string)",
		typeCheckFunc: func(celType *cel.Type) bool {
			return celType != nil && normalizeOptionalType(celType.String()) == "list(string)"
		},
		suggestionFunc: func(actualType string) string {
			if strings.HasPrefix(actualType, "list(") && !strings.Contains(actualType, "string") {
				return "attachments must be a list of strings"
			}
			if actualType == "string" {
				return "attachments expects a list. Wrap the string in a list: [\"{{...}}\"]"
			}
			return "Ensure the expression returns a list of strings"
		},
	},
}

// normalizeOptionalType strips optional type wrappers to get the inner type.
// Handles "null|T" and "optional_type(T)" patterns.
func normalizeOptionalType(typeStr string) string {
	if strings.Contains(typeStr, "null|") {
		typeStr = strings.TrimPrefix(typeStr, "null|")
	}
	// Note: CEL's optional_type representation may vary; handle as needed
	return typeStr
}

// expectedSaveMessageFieldType returns the expected type string for a save_message field.
func expectedSaveMessageFieldType(fieldName string) string {
	if info, ok := saveMessageFieldTypes[fieldName]; ok {
		return info.expectedType
	}
	return "any" // New fields without type info are allowed
}

// isSaveMessageFieldTypeCompatible checks if a CEL type is compatible with a save_message field.
func isSaveMessageFieldTypeCompatible(fieldName string, actualType *cel.Type) bool {
	if actualType == nil {
		return false
	}

	// dyn means the type cannot be determined at compile time;
	// it is not an error — the expression may be perfectly valid at runtime.
	if actualType.String() == "dyn" {
		return true
	}

	// If we have type info for this field, use it
	if info, ok := saveMessageFieldTypes[fieldName]; ok {
		return info.typeCheckFunc(actualType)
	}

	// New fields without type info are allowed (forward compatibility)
	return true
}

// formatCELType formats a CEL type for display in error messages.
func formatCELType(celType *cel.Type) string {
	if celType == nil {
		return "unknown"
	}
	typeStr := celType.String()

	// Simplify long package paths for readability
	typeStr = strings.ReplaceAll(typeStr, "github.com/reliant-labs/reliant/internal/models/message.", "message.")

	return typeStr
}

// getSaveMessageTypeSuggestion provides a helpful suggestion for fixing type mismatches.
func getSaveMessageTypeSuggestion(fieldName, actualType string) string {
	if info, ok := saveMessageFieldTypes[fieldName]; ok {
		return info.suggestionFunc(actualType)
	}
	return "" // No suggestion for unknown fields
}

// validateCELExpressionWithCompilationAndSchema validates a CEL expression by compiling it
// and then running AST-based schema type checking.
func validateCELExpressionWithCompilationAndSchema(origExpr, rewrittenExpr string, path []string, env *cel.Env, schemaTypeChecker *SchemaTypeChecker, typeCtx *WorkflowTypeContext, result *Result) {
	celAst, issues := env.Compile(rewrittenExpr)

	// Report ALL CEL compilation errors with suggestions
	if issues != nil && issues.Err() != nil {
		for _, issue := range issues.Errors() {
			suggestion := suggestForCELCompilationError(issue.Message, typeCtx)
			result.Add(&Error{
				Severity:   SeverityError,
				Category:   CategoryCELSemantic,
				Path:       path,
				Message:    fmt.Sprintf("CEL compilation error: %s", issue.Message),
				Suggestion: suggestion,
			})
		}
	}

	// Run AST-based schema type checking if compilation succeeded and we have schemas
	if celAst != nil && schemaTypeChecker != nil && schemaTypeChecker.HasSchemas() {
		schemaTypeChecker.Check(celAst, origExpr, path, result)
	}
}

// validateConditionReturnType validates that a condition expression returns a boolean type.
// Condition expressions (edge conditions and node conditions) must evaluate to bool.
func validateConditionReturnType(celAst *cel.Ast, origExpr string, path []string, result *Result) {
	if celAst == nil {
		return
	}

	// Get the output type of the expression
	outputType := celAst.OutputType()
	if outputType == nil {
		return
	}

	// Check if the type is boolean
	// CEL uses types.BoolType for boolean types
	typeStr := outputType.String()
	if typeStr != "bool" && outputType != types.BoolType {
		// Generate helpful suggestion based on the actual type
		suggestion := generateBooleanSuggestion(origExpr, typeStr)

		result.Add(&Error{
			Severity:   SeverityError,
			Category:   CategoryCELSemantic,
			Path:       path,
			Message:    fmt.Sprintf("condition must return bool, but expression returns '%s'", typeStr),
			Suggestion: suggestion,
		})
	}
}

// validateOutputNotAlwaysNull flags output expressions whose static CEL type is
// exactly null_type — i.e. the expression can never produce anything but null
// (a literal `null`, or a ternary where every branch is null).
//
// Why a warning and not an error: null outputs are LEGAL and fully representable
// at runtime — CEL null is normalized to Go nil (wfcel.ConvertToNative) and
// structpb natively represents nil as NullValue, so an output expression that
// yields null at runtime (e.g. `cond ? nodes.x.field : null`) works end-to-end.
// Such conditionally-null expressions type as dyn or the branch type, never as
// null_type, so they are NOT flagged here. Only an always-null expression is
// statically detectable, and it is almost certainly a workflow-authoring mistake.
// Proving nullability of dyn expressions statically is impossible; the runtime
// null normalization is the actual guarantee against null-related failures.
func validateOutputNotAlwaysNull(celAst *cel.Ast, origExpr string, path []string, result *Result) {
	if celAst == nil {
		return
	}

	outputType := celAst.OutputType()
	if outputType == nil {
		return
	}

	if outputType == types.NullType || outputType.String() == "null_type" {
		result.Add(&Error{
			Severity:   SeverityWarning,
			Category:   CategoryCELSemantic,
			Path:       path,
			Message:    fmt.Sprintf("output expression is always null: '%s' — likely a mistake", origExpr),
			Suggestion: "reference a node output or input, or remove this output if it is not needed",
		})
	}
}

// generateBooleanSuggestion generates a helpful suggestion for converting non-boolean expressions to boolean.
func generateBooleanSuggestion(expr, actualType string) string {
	switch actualType {
	case "int", "uint", "double":
		return fmt.Sprintf("Use a comparison like: %s > 0", expr)
	case "string":
		return fmt.Sprintf("Use a comparison like: %s != '' or %s == 'expected_value'", expr, expr)
	case "list(dyn)", "list":
		// Check if expression is already using size()
		if strings.Contains(expr, "size(") {
			return fmt.Sprintf("Use a comparison like: %s > 0", expr)
		}
		return fmt.Sprintf("Check list size: size(%s) > 0", expr)
	default:
		// Check if expression looks like it's accessing a size or count
		if strings.Contains(expr, "size(") {
			return fmt.Sprintf("Use a comparison like: %s > 0", expr)
		}
		return "Use a boolean comparison or condition"
	}
}

// =============================================================================
// INPUT PROPERTY ACCESS VALIDATION
// =============================================================================

// inputPropertyAccessRegex matches inputs.X.Y patterns where X is the input name and Y is the property.
// Captures: 1=input name, 2=property name
var inputPropertyAccessRegex = regexp.MustCompile(`inputs\.([a-zA-Z_][a-zA-Z0-9_]*)\.([a-zA-Z_][a-zA-Z0-9_]*)`)

// validateInputPropertyAccess validates that property accesses on object inputs with schemas
// reference valid properties. This catches errors like inputs.config.unknown_field when
// config has a defined schema.
func validateInputPropertyAccess(expr string, path []string, typeCtx *WorkflowTypeContext, result *Result) {
	if typeCtx == nil {
		return
	}

	// Skip for inline workflows that receive inputs dynamically via args
	if typeCtx.LenientInputs {
		return
	}

	// Find all inputs.X.Y patterns in the expression
	matches := inputPropertyAccessRegex.FindAllStringSubmatch(expr, -1)
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		inputName := match[1]
		propertyName := match[2]

		// Check if this input exists and has property info
		fieldInfo, exists := typeCtx.InputFields[inputName]
		if !exists {
			// Input doesn't exist - this will be caught elsewhere
			continue
		}

		// Only validate if the input has defined properties (schema-based object)
		if len(fieldInfo.Properties) == 0 {
			// No schema defined - dynamic access is allowed
			continue
		}

		// Check if additional properties are allowed
		if fieldInfo.AdditionalPropertiesAllowed {
			// additionalProperties: true - any field access is allowed
			continue
		}

		// Check if the property exists in the schema
		_, propExists := fieldInfo.Properties[propertyName]
		if !propExists {
			// Build list of valid properties for suggestion
			validProps := make([]string, 0, len(fieldInfo.Properties))
			for name := range fieldInfo.Properties {
				validProps = append(validProps, name)
			}
			result.Add(&Error{
				Severity:   SeverityError,
				Category:   CategoryCELSemantic,
				Path:       path,
				Message:    fmt.Sprintf("undefined property '%s' on input '%s'", propertyName, inputName),
				Suggestion: fmt.Sprintf("valid properties are: %s", strings.Join(validProps, ", ")),
			})
		}
	}
}

// validateResponseDataAccessFromExpr scans a CEL expression for nodes.X.response_data.Y.Z
// patterns and validates tool name/field access against the known response tool schemas.
// This is the compilation-path equivalent of the response_data check in validateCELSemantics.
func validateResponseDataAccessFromExpr(expr string, path []string, typeCtx *WorkflowTypeContext, result *Result) {
	if typeCtx == nil || typeCtx.ResponseTools == nil {
		return
	}

	// Find all nodes.<nodeID>.<fieldPath> patterns in the expression
	for _, match := range nodeFieldRegex.FindAllStringSubmatch(expr, -1) {
		nodeID := match[1]
		fieldPath := match[2]

		topLevelField := extractTopLevelField(fieldPath)
		if topLevelField != "response_data" {
			continue
		}

		if err := validateResponseDataAccessWithContext(nodeID, fieldPath, typeCtx.ResponseTools); err != nil {
			errorEntry := &Error{
				Severity: SeverityError,
				Category: CategoryCELSemantic,
				Path:     path,
				Message:  err.message,
			}
			if err.suggestion != "" {
				errorEntry.Suggestion = err.suggestion
			}
			result.Add(errorEntry)
		}
	}
}

// validateResponseDataAccessWithContext validates access patterns like response_data.<tool>.<field>
// against the known response tool schemas.
func validateResponseDataAccessWithContext(nodeID, fieldPath string, responseTools *ResponseToolContext) *semanticError {
	if responseTools == nil {
		return nil
	}

	// Get available tools for this execute_tools node
	toolSchemas, hasSchemas := responseTools.AvailableTools[nodeID]
	if !hasSchemas || len(toolSchemas) == 0 {
		// No response tool info available - can't validate (e.g., MCP tools, dynamic source, or no response_tool)
		return nil
	}

	// Get the source information for better error messages
	source, hasSource := responseTools.ToolCallSources[nodeID]

	// Parse the field path: response_data.<tool>.<field>
	matches := responseDataFieldRegex.FindStringSubmatch(fieldPath)
	if matches == nil {
		// Just accessing response_data or response_data.<tool> without a field - allow it
		// But we can still validate the tool name
		parts := strings.SplitN(fieldPath, ".", 3)
		if len(parts) >= 2 {
			toolName := parts[1]
			if _, ok := toolSchemas[toolName]; !ok {
				availableToolNames := getAvailableToolNames(toolSchemas)
				message := fmt.Sprintf("unknown response tool '%s' in response_data", toolName)
				if hasSource && source.Type == SourceNode {
					message = fmt.Sprintf(
						"unknown response tool '%s'\n"+
							"  Available tools: %v\n"+
							"  Defined in: nodes.%s",
						toolName,
						availableToolNames,
						source.NodeID,
					)
				}
				return &semanticError{
					message:    message,
					suggestion: suggestSimilar(toolName, availableToolNames),
				}
			}
		}
		return nil
	}

	toolName := matches[1]
	fieldName := matches[2]

	// Validate tool name exists
	schema, ok := toolSchemas[toolName]
	if !ok {
		availableToolNames := getAvailableToolNames(toolSchemas)
		message := fmt.Sprintf("unknown response tool '%s' in response_data", toolName)
		if hasSource && source.Type == SourceNode {
			message = fmt.Sprintf(
				"unknown response tool '%s'\n"+
					"  Available tools: %v\n"+
					"  Defined in: nodes.%s",
				toolName,
				availableToolNames,
				source.NodeID,
			)
		}
		return &semanticError{
			message:    message,
			suggestion: suggestSimilar(toolName, availableToolNames),
		}
	}

	// Validate field exists in schema
	if len(schema.Fields) > 0 {
		if _, fieldExists := schema.Fields[fieldName]; !fieldExists {
			validFields := make([]string, 0, len(schema.Fields))
			for f := range schema.Fields {
				validFields = append(validFields, f)
			}
			sort.Strings(validFields)
			message := fmt.Sprintf("response tool '%s' has no field '%s' in response_data", toolName, fieldName)
			if hasSource && source.Type == SourceNode {
				message = fmt.Sprintf(
					"response tool '%s' has no field '%s' in response_data\n"+
						"  Available fields: %v\n"+
						"  Tool schema source: nodes.%s.response_tool.schema",
					toolName,
					fieldName,
					validFields,
					source.NodeID,
				)
			}
			return &semanticError{
				message:    message,
				suggestion: suggestSimilar(fieldName, validFields),
			}
		}
	}

	return nil
}

// =============================================================================
// LOOP iter.item TYPE INFERENCE
// =============================================================================

// inferLoopItemFields attempts to infer typed field info for iter.item by tracing
// the loop's items expression back to its source schema.
//
// Supported patterns:
//   - items references a node output that is a typed array with known item properties
//   - items references a workflow node output (e.g., nodes.X.response.Y) where the
//     workflow node has a response_schema arg containing the array's JSON Schema
func inferLoopItemFields(loopArgs *reliantv1.LoopArgs, wf *reliantv1.Workflow, typeCtx *WorkflowTypeContext) map[string]*FieldInfo {
	if loopArgs == nil || !model.CelStringIsSet(loopArgs.GetItems()) {
		return nil
	}

	itemsExpr := model.CelStringRaw(loopArgs.GetItems())
	if itemsExpr == "" {
		return nil
	}

	// Parse the items expression to extract the source node and field path.
	// Expected patterns: "nodes.<nodeID>.<field1>.<field2>..." or similar.
	parts := parseNodeFieldPath(itemsExpr)
	if parts == nil {
		return nil
	}
	nodeID := parts.nodeID
	fieldPath := parts.fieldPath

	// Try to resolve through the node's output type info first.
	if typeCtx != nil {
		if fields := resolveArrayItemFieldsFromNodeOutputs(nodeID, fieldPath, typeCtx); fields != nil {
			return fields
		}
	}

	// For workflow/loop nodes, try to infer from the response_schema arg.
	if wf != nil {
		if fields := resolveArrayItemFieldsFromResponseSchema(nodeID, fieldPath, wf); fields != nil {
			return fields
		}
	}

	return nil
}

// nodeFieldPath represents a parsed "nodes.<nodeID>.<field1>.<field2>..." expression.
type nodeFieldPath struct {
	nodeID    string
	fieldPath []string // e.g., ["response", "waves"]
}

// nodeFieldPathPattern matches nodes.<nodeID>.<fieldPath...> CEL expressions.
var nodeFieldPathPattern = regexp.MustCompile(`^nodes\.([a-zA-Z_][a-zA-Z0-9_-]*)(\.([a-zA-Z_][a-zA-Z0-9_.]*))$`)

// parseNodeFieldPath extracts the node ID and field path from a "nodes.X.Y.Z" expression.
func parseNodeFieldPath(expr string) *nodeFieldPath {
	matches := nodeFieldPathPattern.FindStringSubmatch(expr)
	if matches == nil {
		return nil
	}
	nodeID := matches[1]
	fieldStr := matches[3] // e.g., "response.waves"
	if fieldStr == "" {
		return nil
	}
	return &nodeFieldPath{
		nodeID:    nodeID,
		fieldPath: strings.Split(fieldStr, "."),
	}
}

// resolveArrayItemFieldsFromNodeOutputs tries to resolve array item fields
// from the node's statically known output type info.
func resolveArrayItemFieldsFromNodeOutputs(nodeID string, fieldPath []string, typeCtx *WorkflowTypeContext) map[string]*FieldInfo {
	nodeOutputs, ok := typeCtx.NodeOutputs[nodeID]
	if !ok || len(fieldPath) == 0 {
		return nil
	}

	// Walk the field path to find the target field.
	var current *FieldInfo
	for i, part := range fieldPath {
		if i == 0 {
			current, ok = nodeOutputs[part]
			if !ok || current == nil {
				return nil
			}
		} else {
			if current.Properties == nil {
				return nil
			}
			current, ok = current.Properties[part]
			if !ok || current == nil {
				return nil
			}
		}
	}

	// The target field should be an array/slice type with known item properties.
	if !current.IsSlice {
		return nil
	}
	if len(current.Properties) > 0 {
		return current.Properties
	}
	return nil
}

// resolveArrayItemFieldsFromResponseSchema tries to infer array item fields from
// a workflow/loop node's response_schema arg. This handles the common pattern where
// a structured-agent sub-workflow receives a response_schema input that defines the
// schema of its response output, and the loop iterates over an array field in that response.
func resolveArrayItemFieldsFromResponseSchema(nodeID string, fieldPath []string, wf *reliantv1.Workflow) map[string]*FieldInfo {
	if len(fieldPath) < 2 {
		return nil
	}

	// Find the source node in the workflow.
	var sourceNode *reliantv1.Node
	for _, n := range wf.GetNodes() {
		if n.GetId() == nodeID {
			sourceNode = n
			break
		}
	}
	if sourceNode == nil {
		return nil
	}

	// Get the args map from the workflow or loop node.
	var argsMap map[string]*structpb.Value
	if wfArgs := sourceNode.GetWorkflow(); wfArgs != nil {
		argsMap = wfArgs.GetArgs()
	} else if loopArgs := sourceNode.GetLoop(); loopArgs != nil {
		argsMap = loopArgs.GetArgs()
	}
	if argsMap == nil {
		return nil
	}

	// Look for a response_schema arg.
	schemaVal, ok := argsMap["response_schema"]
	if !ok || schemaVal == nil {
		return nil
	}

	// The response_schema is stored as a Value with a StructValue (JSON object).
	schemaStruct := schemaVal.GetStructValue()
	if schemaStruct == nil {
		return nil
	}
	schemaMap := schemaStruct.AsMap()

	// The first element of fieldPath is typically "response" (the output name),
	// which maps to the response_schema itself. The remaining parts navigate
	// into the schema's properties to find the array field.
	// e.g., fieldPath = ["response", "waves"] → look for waves in schema properties.
	arrayFieldPath := fieldPath[1:] // skip "response" (or the output name)

	// Navigate into the schema to find the target array field.
	currentSchema := schemaMap
	for _, part := range arrayFieldPath {
		props, ok := currentSchema["properties"].(map[string]interface{})
		if !ok {
			return nil
		}
		fieldSchema, ok := props[part].(map[string]interface{})
		if !ok {
			return nil
		}
		currentSchema = fieldSchema
	}

	// The current schema should be an array type.
	if typeName, _ := currentSchema["type"].(string); typeName != "array" {
		return nil
	}

	// Extract the items schema.
	itemsSchema, ok := currentSchema["items"].(map[string]interface{})
	if !ok {
		return nil
	}

	// The items schema should be an object with properties.
	if typeName, _ := itemsSchema["type"].(string); typeName != "object" {
		return nil
	}

	props, ok := itemsSchema["properties"].(map[string]interface{})
	if !ok || len(props) == 0 {
		return nil
	}

	// Convert each property to FieldInfo.
	result := make(map[string]*FieldInfo, len(props))
	for propName, propSchemaRaw := range props {
		propSchema, ok := propSchemaRaw.(map[string]interface{})
		if !ok {
			result[propName] = &FieldInfo{Name: propName, Kind: reflect.Interface, IsDynamic: true}
			continue
		}
		result[propName] = jsonSchemaMapToFieldInfo(propName, propSchema)
	}
	return result
}

// =============================================================================
// LOOP WHILE CONDITION VALIDATION
// =============================================================================

// validateLoopWhileCondition checks for constant-true or static loop conditions.
// Issues a warning for conditions that will cause infinite loops.
// Also validates that inline loops with outputs.* references have an outputs section.
func validateLoopWhileCondition(loopArgs *reliantv1.LoopArgs, path []string, result *Result) {
	// Raw CEL; a {{ }}-wrapped value is rejected before this runs.
	expr := strings.TrimSpace(model.DirectCelExpr(loopArgs.GetWhile()))

	// Check for literal true
	if expr == "true" {
		result.Add(&Error{
			Severity:   SeverityWarning,
			Category:   CategoryCELSemantic,
			Path:       path,
			Message:    "loop condition is always true - this will loop until max_turns limit",
			Suggestion: "use a condition that references iter.* or outputs.* to control loop termination",
		})
		return
	}

	// Check for self-evident truths like "1 == 1", "true == true", "'a' == 'a'"
	if isAlwaysTrueExpression(expr) {
		result.Add(&Error{
			Severity:   SeverityWarning,
			Category:   CategoryCELSemantic,
			Path:       path,
			Message:    fmt.Sprintf("loop condition '%s' is always true - this will loop until max_turns limit", expr),
			Suggestion: "use a condition that references iter.* or outputs.* to control loop termination",
		})
		return
	}

	// Check if condition doesn't reference iter.* or outputs.* (the only things that change)
	if !referencesLoopState(expr) {
		result.Add(&Error{
			Severity:   SeverityWarning,
			Category:   CategoryCELSemantic,
			Path:       path,
			Message:    "loop condition doesn't reference 'iter' or 'outputs' - condition may never change",
			Suggestion: "loop conditions should typically reference 'iter.iteration', 'outputs.*', or use 'iter.first' for first-iteration checks",
		})
	}

	// Check that inline loops with outputs.* references have an outputs section declared
	if referencesOutputs(expr) && loopArgs.GetInline() != nil && len(loopArgs.GetInline().GetOutputs()) == 0 {
		result.Add(&Error{
			Severity:   SeverityError,
			Category:   CategoryCELSemantic,
			Path:       path,
			Message:    "loop condition references 'outputs.*' but inline workflow has no 'outputs' section",
			Suggestion: "add an 'outputs' section to the inline workflow that maps inner node outputs to named fields (e.g., outputs: { tool_calls: '{{nodes.call_llm.tool_calls}}' })",
		})
	}
}

// referencesOutputs checks if an expression references outputs.* (outputs namespace).
func referencesOutputs(expr string) bool {
	// Match outputs.something or outputs["something"]
	outputsPattern := regexp.MustCompile(`\boutputs\s*[.\[]`)
	return outputsPattern.MatchString(expr)
}

// isAlwaysTrueExpression detects simple always-true patterns.
func isAlwaysTrueExpression(expr string) bool {
	// Patterns that are obviously always true
	alwaysTruePatterns := []string{
		"1 == 1", "1==1",
		"0 == 0", "0==0",
		"true == true", "true==true",
		"false == false", "false==false",
		"1 != 0", "1!=0",
		"0 != 1", "0!=1",
		"true != false", "true!=false",
		"false != true", "false!=true",
		"1 > 0", "1>0",
		"0 < 1", "0<1",
		"1 >= 1", "1>=1",
		"0 <= 0", "0<=0",
	}

	normalized := strings.ReplaceAll(expr, " ", "")
	for _, pattern := range alwaysTruePatterns {
		patternNorm := strings.ReplaceAll(pattern, " ", "")
		if normalized == patternNorm {
			return true
		}
	}

	// Check for same-value equality: "x == x" pattern
	// This is a simplified check - matches patterns like "foo == foo"
	eqParts := strings.Split(expr, "==")
	if len(eqParts) == 2 {
		left := strings.TrimSpace(eqParts[0])
		right := strings.TrimSpace(eqParts[1])
		if left == right && left != "" {
			return true
		}
	}

	return false
}

// referencesLoopState checks if expression references iter.* or outputs.*
// which are the only namespaces that change between loop iterations.
func referencesLoopState(expr string) bool {
	return strings.Contains(expr, "iter.") ||
		strings.Contains(expr, "iter[") ||
		strings.Contains(expr, "outputs.") ||
		strings.Contains(expr, "outputs[")
}

// =============================================================================
// SCHEMA TYPE CHECKER - AST-BASED TYPE VALIDATION
// =============================================================================
//
// The SchemaTypeChecker validates CEL expressions against ObjectInput schemas
// by walking the CEL AST after compilation. This catches errors that the
// standard CEL type checker misses because we use MapType(StringType, DynType)
// for dynamic inputs.
//
// Examples of errors caught:
// - config.timeout + " seconds" → type mismatch: cannot add integer and string
// - config.name + config.timeout → type mismatch: cannot add string and integer
// - config.unknown_field → undefined property (when additionalProperties: false)

// SchemaTypeChecker validates CEL expressions against ObjectInput schemas.
type SchemaTypeChecker struct {
	// objectSchemas maps variable paths to their proto ObjectInputConfig schemas.
	// e.g., "inputs.config" -> ObjectInputConfig schema
	objectSchemas map[string]*reliantv1.ObjectInputConfig
}

// NewSchemaTypeCheckerFromProto creates a new SchemaTypeChecker from a proto workflow.
func NewSchemaTypeCheckerFromProto(wf *reliantv1.Workflow) *SchemaTypeChecker {
	return &SchemaTypeChecker{
		objectSchemas: buildProtoObjectSchemas(wf),
	}
}

// buildProtoObjectSchemas extracts ObjectInputConfig schemas from proto workflow inputs.
func buildProtoObjectSchemas(wf *reliantv1.Workflow) map[string]*reliantv1.ObjectInputConfig {
	if wf == nil {
		return nil
	}

	schemas := make(map[string]*reliantv1.ObjectInputConfig)
	for name, input := range wf.GetInputs() {
		if input == nil {
			continue
		}
		if cfg, ok := input.GetConfig().(*reliantv1.Input_ObjectInput); ok && cfg.ObjectInput != nil {
			if len(cfg.ObjectInput.GetProperties()) > 0 {
				schemas["inputs."+name] = cfg.ObjectInput
			}
		}
	}
	return schemas
}

// HasSchemas returns true if there are any object schemas to validate against.
func (tc *SchemaTypeChecker) HasSchemas() bool {
	return len(tc.objectSchemas) > 0
}

// Check validates a CEL expression against object input schemas.
// It walks the AST and checks for type mismatches and undefined properties.
func (tc *SchemaTypeChecker) Check(celAst *cel.Ast, originalExpr string, path []string, result *Result) {
	if celAst == nil || !tc.HasSchemas() {
		return
	}

	// Get the native CEL AST representation
	nativeAST := celAst.NativeRep()
	if nativeAST == nil {
		return
	}

	expr := nativeAST.Expr()
	if expr == nil {
		return
	}

	tc.checkExpr(expr, originalExpr, path, result)
}

// schemaType represents the inferred type from a schema.
type schemaType string

const (
	schemaTypeString  schemaType = "string"
	schemaTypeInteger schemaType = "integer"
	schemaTypeNumber  schemaType = "number"
	schemaTypeBoolean schemaType = "boolean"
	schemaTypeArray   schemaType = "array"
	schemaTypeObject  schemaType = "object"
	schemaTypeDyn     schemaType = "dyn" // dynamic/unknown type
)

// checkExpr recursively walks the AST and validates types.
// Returns the inferred type of the expression.
func (tc *SchemaTypeChecker) checkExpr(e ast.Expr, originalExpr string, path []string, result *Result) schemaType {
	if e == nil {
		return schemaTypeDyn
	}

	switch e.Kind() {
	case ast.SelectKind:
		return tc.checkSelectExpr(e.AsSelect(), originalExpr, path, result)

	case ast.CallKind:
		return tc.checkCallExpr(e.AsCall(), originalExpr, path, result)

	case ast.IdentKind:
		return tc.checkIdentExpr(e.AsIdent())

	case ast.LiteralKind:
		return tc.checkLiteralExpr(e.AsLiteral())

	case ast.ListKind:
		return schemaTypeArray

	case ast.MapKind, ast.StructKind:
		return schemaTypeObject

	case ast.ComprehensionKind:
		// List comprehensions return arrays
		return schemaTypeArray

	default:
		return schemaTypeDyn
	}
}

// checkSelectExpr validates field access expressions like "inputs.config.timeout".
func (tc *SchemaTypeChecker) checkSelectExpr(sel ast.SelectExpr, originalExpr string, path []string, result *Result) schemaType {
	if sel == nil {
		return schemaTypeDyn
	}

	// Build the full path by traversing the select chain
	fullPath := tc.buildSelectPath(sel)
	if fullPath == "" {
		return schemaTypeDyn
	}

	// Check if this is accessing an object input property
	for schemaPath, schema := range tc.objectSchemas {
		if strings.HasPrefix(fullPath, schemaPath+".") {
			// Extract the property path after the schema path
			propertyPath := strings.TrimPrefix(fullPath, schemaPath+".")
			return tc.validatePropertyAccess(schema, propertyPath, fullPath, path, result)
		}
	}

	return schemaTypeDyn
}

// buildSelectPath builds the full path for a select expression.
// e.g., for "inputs.config.timeout", returns "inputs.config.timeout"
func (tc *SchemaTypeChecker) buildSelectPath(sel ast.SelectExpr) string {
	if sel == nil {
		return ""
	}

	field := sel.FieldName()
	operand := sel.Operand()

	if operand == nil {
		return field
	}

	switch operand.Kind() {
	case ast.IdentKind:
		return operand.AsIdent() + "." + field
	case ast.SelectKind:
		parentPath := tc.buildSelectPath(operand.AsSelect())
		if parentPath == "" {
			return ""
		}
		return parentPath + "." + field
	default:
		return ""
	}
}

// validatePropertyAccess validates access to a property in a proto ObjectInputConfig schema.
func (tc *SchemaTypeChecker) validatePropertyAccess(schema *reliantv1.ObjectInputConfig, propertyPath, fullPath string, path []string, result *Result) schemaType {
	// Split the property path for nested access
	parts := strings.Split(propertyPath, ".")

	// Navigate through nested properties
	currentProps := schema.GetProperties()
	currentAdditional := schema.AdditionalProperties != nil && schema.GetAdditionalProperties()
	currentType := schemaTypeDyn

	for i, part := range parts {
		if len(currentProps) == 0 {
			// No more schema info, treat as dynamic
			return schemaTypeDyn
		}

		propSchema, exists := currentProps[part]
		if !exists {
			// Property doesn't exist in schema
			// Check if additionalProperties is allowed
			if !currentAdditional {
				validProps := make([]string, 0, len(currentProps))
				for name := range currentProps {
					validProps = append(validProps, name)
				}
				// Extract the input name from the full path
				inputName := ""
				if strings.HasPrefix(fullPath, "inputs.") {
					pathParts := strings.Split(fullPath, ".")
					if len(pathParts) >= 2 {
						inputName = pathParts[1]
					}
				}
				result.Add(&Error{
					Severity:   SeverityError,
					Category:   CategoryCELSemantic,
					Path:       path,
					Message:    fmt.Sprintf("undefined property '%s' on input '%s'", part, inputName),
					Suggestion: fmt.Sprintf("available properties: %s", strings.Join(validProps, ", ")),
				})
			}
			return schemaTypeDyn
		}

		// Get the type of this property
		currentType = schemaTypeFromString(propSchema.GetType())

		// If this is not the last part and the type is object, continue navigating
		if i < len(parts)-1 {
			if propSchema.GetType() == "object" && len(propSchema.GetProperties()) > 0 {
				currentProps = propSchema.GetProperties()
				currentAdditional = false // Nested objects default strict
			} else {
				// Trying to access a property on a non-object type
				return schemaTypeDyn
			}
		}
	}

	return currentType
}

// schemaTypeFromString converts a JSON Schema type string to schemaType.
func schemaTypeFromString(typeStr string) schemaType {
	switch typeStr {
	case "string":
		return schemaTypeString
	case "integer":
		return schemaTypeInteger
	case "number":
		return schemaTypeNumber
	case "boolean":
		return schemaTypeBoolean
	case "array":
		return schemaTypeArray
	case "object":
		return schemaTypeObject
	default:
		return schemaTypeDyn
	}
}

// checkCallExpr validates function call expressions.
func (tc *SchemaTypeChecker) checkCallExpr(call ast.CallExpr, originalExpr string, path []string, result *Result) schemaType {
	if call == nil {
		return schemaTypeDyn
	}

	funcName := call.FunctionName()
	args := call.Args()

	// Handle binary operators
	switch funcName {
	case "_+_":
		return tc.checkBinaryAdd(args, originalExpr, path, result)

	case "_-_", "_*_", "_/_", "_%_":
		// Arithmetic operators: check that operands are numeric
		return tc.checkArithmeticOp(args, funcName, originalExpr, path, result)

	case "_>_", "_>=_", "_<_", "_<=_":
		// Comparison operators: return boolean, operands should be compatible
		tc.checkComparisonOp(args, funcName, originalExpr, path, result)
		return schemaTypeBoolean

	case "_==_", "_!=_":
		// Equality operators: return boolean
		// Type checking is more lenient for equality
		for _, arg := range args {
			tc.checkExpr(arg, originalExpr, path, result)
		}
		return schemaTypeBoolean

	case "_&&_", "_||_", "!_", "_?_:_":
		// Logical operators: return boolean
		for _, arg := range args {
			tc.checkExpr(arg, originalExpr, path, result)
		}
		return schemaTypeBoolean

	case "size":
		// size() returns integer
		for _, arg := range args {
			tc.checkExpr(arg, originalExpr, path, result)
		}
		return schemaTypeInteger

	case "string":
		return schemaTypeString

	case "int", "uint":
		return schemaTypeInteger

	case "double":
		return schemaTypeNumber

	case "bool":
		return schemaTypeBoolean

	default:
		// For other functions, check arguments but return dyn
		for _, arg := range args {
			tc.checkExpr(arg, originalExpr, path, result)
		}
		return schemaTypeDyn
	}
}

// checkBinaryAdd validates the + operator which works for both strings and numbers.
func (tc *SchemaTypeChecker) checkBinaryAdd(args []ast.Expr, originalExpr string, path []string, result *Result) schemaType {
	if len(args) != 2 {
		return schemaTypeDyn
	}

	leftType := tc.checkExpr(args[0], originalExpr, path, result)
	rightType := tc.checkExpr(args[1], originalExpr, path, result)

	// Both operands are dynamic - can't determine type
	if leftType == schemaTypeDyn && rightType == schemaTypeDyn {
		return schemaTypeDyn
	}

	// String concatenation: both must be strings (or one is dyn)
	if leftType == schemaTypeString || rightType == schemaTypeString {
		if leftType == schemaTypeString && rightType == schemaTypeString {
			return schemaTypeString
		}
		if leftType == schemaTypeDyn || rightType == schemaTypeDyn {
			// One is string, one is dyn - assume string
			return schemaTypeString
		}
		// Type mismatch: string + non-string
		leftDesc := tc.exprDescription(args[0], leftType)
		rightDesc := tc.exprDescription(args[1], rightType)
		result.Add(&Error{
			Severity:   SeverityError,
			Category:   CategoryCELSemantic,
			Path:       path,
			Message:    fmt.Sprintf("type mismatch in '%s': cannot add %s and %s", originalExpr, leftDesc, rightDesc),
			Suggestion: "use string() to convert non-string values before concatenation",
		})
		return schemaTypeDyn
	}

	// Numeric addition
	if isNumericType(leftType) && isNumericType(rightType) {
		// If either is a float/number, result is number
		if leftType == schemaTypeNumber || rightType == schemaTypeNumber {
			return schemaTypeNumber
		}
		return schemaTypeInteger
	}

	// One is numeric, one is something else (not dyn)
	if isNumericType(leftType) && !isNumericType(rightType) && rightType != schemaTypeDyn {
		leftDesc := tc.exprDescription(args[0], leftType)
		rightDesc := tc.exprDescription(args[1], rightType)
		result.Add(&Error{
			Severity:   SeverityError,
			Category:   CategoryCELSemantic,
			Path:       path,
			Message:    fmt.Sprintf("type mismatch in '%s': cannot add %s and %s", originalExpr, leftDesc, rightDesc),
			Suggestion: "ensure both operands are numbers or both are strings",
		})
		return schemaTypeDyn
	}

	if !isNumericType(leftType) && leftType != schemaTypeDyn && isNumericType(rightType) {
		leftDesc := tc.exprDescription(args[0], leftType)
		rightDesc := tc.exprDescription(args[1], rightType)
		result.Add(&Error{
			Severity:   SeverityError,
			Category:   CategoryCELSemantic,
			Path:       path,
			Message:    fmt.Sprintf("type mismatch in '%s': cannot add %s and %s", originalExpr, leftDesc, rightDesc),
			Suggestion: "ensure both operands are numbers or both are strings",
		})
		return schemaTypeDyn
	}

	return schemaTypeDyn
}

// checkArithmeticOp validates arithmetic operators (-, *, /, %).
func (tc *SchemaTypeChecker) checkArithmeticOp(args []ast.Expr, funcName string, originalExpr string, path []string, result *Result) schemaType {
	if len(args) != 2 {
		return schemaTypeDyn
	}

	leftType := tc.checkExpr(args[0], originalExpr, path, result)
	rightType := tc.checkExpr(args[1], originalExpr, path, result)

	// Both must be numeric (or dyn)
	if leftType != schemaTypeDyn && !isNumericType(leftType) {
		leftDesc := tc.exprDescription(args[0], leftType)
		result.Add(&Error{
			Severity:   SeverityError,
			Category:   CategoryCELSemantic,
			Path:       path,
			Message:    fmt.Sprintf("type mismatch in '%s': %s is not numeric (got %s)", originalExpr, leftDesc, leftType),
			Suggestion: "arithmetic operators require numeric operands",
		})
		return schemaTypeDyn
	}

	if rightType != schemaTypeDyn && !isNumericType(rightType) {
		rightDesc := tc.exprDescription(args[1], rightType)
		result.Add(&Error{
			Severity:   SeverityError,
			Category:   CategoryCELSemantic,
			Path:       path,
			Message:    fmt.Sprintf("type mismatch in '%s': %s is not numeric (got %s)", originalExpr, rightDesc, rightType),
			Suggestion: "arithmetic operators require numeric operands",
		})
		return schemaTypeDyn
	}

	// If either is number (float), result is number
	if leftType == schemaTypeNumber || rightType == schemaTypeNumber {
		return schemaTypeNumber
	}

	// Division always returns number
	if funcName == "_/_" {
		return schemaTypeNumber
	}

	return schemaTypeInteger
}

// checkComparisonOp validates comparison operators (>, >=, <, <=).
func (tc *SchemaTypeChecker) checkComparisonOp(args []ast.Expr, funcName string, originalExpr string, path []string, result *Result) {
	if len(args) != 2 {
		return
	}

	leftType := tc.checkExpr(args[0], originalExpr, path, result)
	rightType := tc.checkExpr(args[1], originalExpr, path, result)

	// Skip if either is dyn
	if leftType == schemaTypeDyn || rightType == schemaTypeDyn {
		return
	}

	// Both should be comparable types (numeric or string for ordering)
	if isNumericType(leftType) && isNumericType(rightType) {
		return // OK
	}

	if leftType == schemaTypeString && rightType == schemaTypeString {
		return // OK - string comparison
	}

	// Type mismatch
	leftDesc := tc.exprDescription(args[0], leftType)
	rightDesc := tc.exprDescription(args[1], rightType)
	result.Add(&Error{
		Severity:   SeverityError,
		Category:   CategoryCELSemantic,
		Path:       path,
		Message:    fmt.Sprintf("type mismatch in comparison: cannot compare %s with %s", leftDesc, rightDesc),
		Suggestion: "ensure operands have compatible types for comparison",
	})
}

// checkIdentExpr returns the type for an identifier.
func (tc *SchemaTypeChecker) checkIdentExpr(ident string) schemaType {
	// Check if this is a known object input
	for schemaPath := range tc.objectSchemas {
		if ident == strings.TrimPrefix(schemaPath, "inputs.") {
			return schemaTypeObject
		}
	}
	return schemaTypeDyn
}

// checkLiteralExpr returns the type for a literal expression.
func (tc *SchemaTypeChecker) checkLiteralExpr(lit ref.Val) schemaType {
	if lit == nil {
		return schemaTypeDyn
	}

	valType := lit.Type()
	switch valType {
	case types.StringType:
		return schemaTypeString
	case types.IntType, types.UintType:
		return schemaTypeInteger
	case types.DoubleType:
		return schemaTypeNumber
	case types.BoolType:
		return schemaTypeBoolean
	case types.BytesType:
		return schemaTypeArray
	case types.NullType:
		return schemaTypeDyn // null is compatible with any nullable type
	default:
		return schemaTypeDyn
	}
}

// isNumericType returns true if the type is numeric.
func isNumericType(t schemaType) bool {
	return t == schemaTypeInteger || t == schemaTypeNumber
}

// exprDescription returns a human-readable description of an expression.
func (tc *SchemaTypeChecker) exprDescription(e ast.Expr, inferredType schemaType) string {
	if e == nil {
		return string(inferredType)
	}

	switch e.Kind() {
	case ast.SelectKind:
		sel := e.AsSelect()
		path := tc.buildSelectPath(sel)
		return fmt.Sprintf("%s (%s)", path, inferredType)

	case ast.LiteralKind:
		return string(inferredType)

	case ast.IdentKind:
		return fmt.Sprintf("%s (%s)", e.AsIdent(), inferredType)

	default:
		return string(inferredType)
	}
}

// CheckCELExpressionWithSchema validates a CEL expression with schema type checking.
// This should be called after standard CEL compilation succeeds.
func (tc *SchemaTypeChecker) CheckCELExpressionWithSchema(expr string, celAst *cel.Ast, path []string, result *Result) {
	tc.Check(celAst, expr, path, result)
}

// ValidateCELWithCompilation validates every CEL expression in the workflow
// by compiling it in the environment the runtime evaluates it in (see
// cel_sites.go), then running the guard analysis (guarded_access.go).
func ValidateCELWithCompilation(wf *reliantv1.Workflow, result *Result, loader WorkflowLoader) {
	typeCtx := BuildWorkflowTypeContext(wf, loader)
	if typeCtx == nil {
		return
	}
	scope := newWorkflowScope(wf, []string{wf.GetName()}, typeCtx, nil)
	scope.root = true
	validateWorkflowScope(scope, result)
}

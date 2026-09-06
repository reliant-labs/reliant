// Package reference provides workflow schema reference documentation.
//
// Data is populated from proto descriptors at init() time. The proto
// annotations ([(reliant)]) are the single source of truth for field
// descriptions, enum values, UI hints, and other metadata.
//
// This package is consumed by:
//   - tools/docgen/types (types reference markdown)
//   - tools/docgen/assembler (workflow builder prompt)
//   - tools/docgen/celref (CEL reference code generation)
//   - tools/docgen/refcheck (reference data validation)
//   - llm/tools (runtime schema queries)
//
// forge:exclude-contract
//
// Registry/lookup-table package: CELNamespaces, CELFunctions and CELHelperTypes
// are slices populated once by this package's own init() from proto descriptors
// and read-only thereafter. A getter would return the same slice header, so it
// moves the mutation surface without narrowing it — and every consumer above
// reads the tables directly by name.
package reference

import (
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// =============================================================================
// CEL TYPES
// =============================================================================

// CELNamespace describes a namespace accessible in CEL expressions.
type CELNamespace struct {
	Name        string
	Description string
	IsDynamic   bool
	Fields      []CELField
}

// CELField describes a field within a CEL namespace.
type CELField struct {
	Name        string
	Type        string
	Description string
}

// CELFunction describes a custom CEL function.
type CELFunction struct {
	Name        string
	Signature   string
	Description string
	Example     string
}

// CELHelperType describes a nested/helper type used in node outputs.
type CELHelperType struct {
	Name        string
	Description string
	AccessPath  string
	Fields      []CELField
}

// CELNamespaces is the list of available CEL namespaces.
var CELNamespaces []CELNamespace

// CELFunctions is the list of available custom CEL functions.
var CELFunctions []CELFunction

// CELHelperTypes is the list of helper types used in node outputs.
var CELHelperTypes []CELHelperType

// GetCELHelperType returns info about a specific helper type.
func GetCELHelperType(name string) (CELHelperType, bool) {
	for _, ht := range CELHelperTypes {
		if ht.Name == name {
			return ht, true
		}
	}
	return CELHelperType{}, false
}

// =============================================================================
// NODE TYPES
// =============================================================================

// NodeTypeInfo contains documentation for a node type.
type NodeTypeInfo struct {
	Name         string
	TypeName     string
	Summary      string
	Description  string
	Example      string
	Fields       []NodeFieldInfo
	OutputFields []NodeFieldInfo
}

// NodeFieldInfo contains documentation for a node field.
type NodeFieldInfo struct {
	Name        string
	Type        string
	Required    bool
	Description string
	EnumValues  []string
}

// nodeTypes is the registry of node type documentation.
var nodeTypes = make(map[string]NodeTypeInfo)

// ListNodeTypes returns all registered node type names.
func ListNodeTypes() []string {
	names := make([]string, 0, len(nodeTypes))
	for name := range nodeTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GetNodeType returns info for a node type by name.
func GetNodeType(name string) (NodeTypeInfo, bool) {
	info, ok := nodeTypes[name]
	return info, ok
}

// =============================================================================
// INPUT TYPES
// =============================================================================

// InputTypeInfo contains documentation for an input type.
type InputTypeInfo struct {
	Name        string
	TypeName    string
	Summary     string
	Description string
	Example     string
	Fields      []InputFieldInfo
}

// InputFieldInfo contains documentation for an input field.
type InputFieldInfo struct {
	Name        string
	Type        string
	Required    bool
	Description string
}

// inputTypes is the registry of input type documentation.
var inputTypes = make(map[string]InputTypeInfo)

// ListInputTypes returns all registered input type names.
func ListInputTypes() []string {
	names := make([]string, 0, len(inputTypes))
	for name := range inputTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GetInputType returns info for an input type by name.
func GetInputType(name string) (InputTypeInfo, bool) {
	info, ok := inputTypes[name]
	return info, ok
}

// =============================================================================
// SHARED TYPES
// =============================================================================

// SharedTypeInfo contains documentation for a shared type.
type SharedTypeInfo struct {
	Name        string
	Summary     string
	Description string
	Example     string
	Fields      []SharedFieldInfo
}

// SharedFieldInfo contains documentation for a shared type field.
type SharedFieldInfo struct {
	Name        string
	Type        string
	Required    bool
	Description string
}

// sharedTypes is the registry of shared type documentation.
var sharedTypes = make(map[string]SharedTypeInfo)

// ListSharedTypes returns all registered shared type names.
func ListSharedTypes() []string {
	names := make([]string, 0, len(sharedTypes))
	for name := range sharedTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GetSharedType returns info for a shared type by name.
func GetSharedType(name string) (SharedTypeInfo, bool) {
	info, ok := sharedTypes[name]
	return info, ok
}

// =============================================================================
// INITIALIZATION FROM PROTO DESCRIPTORS
// =============================================================================

func init() {
	populateCELNamespaces()
	populateCELFunctions()
	populateCELHelperTypes()
	populateNodeTypes()
	populateInputTypes()
	populateSharedTypes()
}

// populateCELNamespaces builds namespace info from v2cel constants and wfv2 context types.
func populateCELNamespaces() {
	// Static namespace: workflow — fields from model.WorkflowContext
	CELNamespaces = append(CELNamespaces, CELNamespace{
		Name:        "workflow",
		Description: "Workflow execution context (id, name, run_id, etc.)",
		IsDynamic:   false,
		Fields: []CELField{
			{Name: "id", Type: "string", Description: "Workflow execution ID (unique per run)"},
			{Name: "name", Type: "string", Description: "Workflow definition name"},
			{Name: "run_id", Type: "string", Description: "Workflow run ID (Temporal run ID)"},
			{Name: "session_id", Type: "string", Description: "Session ID for the workflow"},
			{Name: "path", Type: "string", Description: "Working directory path"},
			{Name: "worktree_path", Type: "string", Description: "Git worktree path (if in a worktree)"},
			{Name: "branch", Type: "string", Description: "Current git branch (empty if not in git repo)"},
			{Name: "mode", Type: "string", Description: "Execution mode (auto, manual, plan)"},
		},
	})

	// Static namespace: iter — fields from model.IterContext
	CELNamespaces = append(CELNamespaces, CELNamespace{
		Name:        "iter",
		Description: "Loop iteration context (iteration count, first/last flags)",
		IsDynamic:   false,
		Fields: []CELField{
			{Name: "iteration", Type: "int", Description: "Current loop iteration (0-indexed)"},
		},
	})

	// Dynamic namespaces — fields depend on the specific workflow
	dynamicNamespaces := []struct {
		name string
		desc string
	}{
		{"inputs", "Workflow input values passed at invocation"},
		{"nodes", "Output from completed nodes (nodes.<id>.<field>)"},
		{"output", "Current activity output (for save_message context)"},
		{"outputs", "Loop iteration outputs for while condition evaluation"},
		{"trigger", "Trigger context (message, attachments) for triggered workflows"},
		{"thread", "Current thread context (token_count, message_count)"},
	}

	for _, dn := range dynamicNamespaces {
		CELNamespaces = append(CELNamespaces, CELNamespace{
			Name:        dn.name,
			Description: dn.desc,
			IsDynamic:   true,
		})
	}

	sort.Slice(CELNamespaces, func(i, j int) bool {
		return CELNamespaces[i].Name < CELNamespaces[j].Name
	})
}

// populateCELFunctions defines the custom CEL functions available in workflows.
// These match the functions registered in v2cel/env.go.
func populateCELFunctions() {
	CELFunctions = []CELFunction{
		// Custom functions (registered in cel/env.go)
		{Name: "parseJson", Signature: "parseJson(string) -> dyn", Description: "Parse a JSON string into a dynamic value", Example: "parseJson(nodes.run.stdout)"},
		{Name: "toJson", Signature: "toJson(dyn) -> string", Description: "Serialize a value to a JSON string", Example: "toJson(nodes.llm.tool_calls)"},
		{Name: "coalesce", Signature: "coalesce(dyn, dyn) -> dyn", Description: "Return first non-null/non-empty argument", Example: "coalesce(inputs.name, \"default\")"},
		{Name: "getOrDefault", Signature: "getOrDefault(map, key, default) -> dyn", Description: "Safely access a map key with a fallback default value", Example: "getOrDefault(inputs, \"mode\", \"auto\")"},
		{Name: "now", Signature: "now() -> string", Description: "Return current time as RFC3339 string", Example: "now()"},
		{Name: "parseDuration", Signature: "parseDuration(string) -> double", Description: "Parse a Go duration string and return seconds as a number", Example: "parseDuration(\"5m\") == 300.0"},
		{Name: "spawn", Signature: "spawn(string, list) -> string", Description: "Generate a spawn directive for a child workflow with presets", Example: "spawn(\"builtin://agent\", [\"general\", \"researcher\"])"},
		// ext.Strings() functions
		{Name: "trimPrefix", Signature: "string.trimPrefix(string) -> string", Description: "Remove prefix from string", Example: "nodes.run.stdout.trimPrefix(\"Error: \")"},
		{Name: "trimSuffix", Signature: "string.trimSuffix(string) -> string", Description: "Remove suffix from string", Example: "nodes.run.stdout.trimSuffix(\"\\n\")"},
		{Name: "replace", Signature: "string.replace(string, string) -> string", Description: "Replace all occurrences of old with new", Example: "nodes.llm.response_text.replace(\"\\n\", \" \")"},
		{Name: "split", Signature: "string.split(string) -> list(string)", Description: "Split string by separator", Example: "nodes.run.stdout.split(\"\\n\")"},
		{Name: "join", Signature: "list.join(string) -> string", Description: "Join list elements with separator", Example: "[\"a\", \"b\"].join(\", \")"},
		{Name: "format", Signature: "string.format(list) -> string", Description: "Format a string with positional arguments", Example: "\"Hello %s, you have %d items\".format([name, count])"},
	}
}

// populateCELHelperTypes builds helper type info from proto output message descriptors.
func populateCELHelperTypes() {
	CELHelperTypes = []CELHelperType{
		buildHelperTypeFromProto("MessageOutput", "Standardized output for activities that produce messages",
			"nodes.<id>.message.*", (&reliantv1.MessageOutput{}).ProtoReflect().Descriptor()),
		buildHelperTypeFromProto("ThinkingOutput", "Extended thinking content from LLM (when thinking_level is set)",
			"nodes.<id>.thinking.*", (&reliantv1.ThinkingOutput{}).ProtoReflect().Descriptor()),
		{
			Name:        "ToolCall",
			Description: "Tool invocation request from the LLM",
			AccessPath:  "nodes.<id>.tool_calls[*].*",
			Fields: protoFieldsToCELFields(
				(&reliantv1.ToolCallMsg{}).ProtoReflect().Descriptor()),
		},
		{
			Name:        "ToolResult",
			Description: "Result from executing a tool call",
			AccessPath:  "nodes.<id>.tool_results[*].*",
			Fields: protoFieldsToCELFields(
				(&reliantv1.ToolResultMsg{}).ProtoReflect().Descriptor()),
		},
	}
}

func buildHelperTypeFromProto(name, desc, accessPath string, md protoreflect.MessageDescriptor) CELHelperType {
	return CELHelperType{
		Name:        name,
		Description: desc,
		AccessPath:  accessPath,
		Fields:      protoFieldsToCELFields(md),
	}
}

// protoFieldsToCELFields converts proto message fields to CELField list.
func protoFieldsToCELFields(md protoreflect.MessageDescriptor) []CELField {
	fields := md.Fields()
	result := make([]CELField, 0, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		result = append(result, CELField{
			Name:        string(fd.Name()),
			Type:        protoKindToSimpleType(fd),
			Description: getProtoFieldComment(fd),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// protoKindToSimpleType returns a simple type string for a proto field.
func protoKindToSimpleType(fd protoreflect.FieldDescriptor) string {
	if fd.IsList() {
		return protoKindToSimpleType_inner(fd) + "[]"
	}
	if fd.IsMap() {
		return "object"
	}
	return protoKindToSimpleType_inner(fd)
}

func protoKindToSimpleType_inner(fd protoreflect.FieldDescriptor) string {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return "bool"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return "int"
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return "double"
	case protoreflect.StringKind:
		return "string"
	case protoreflect.BytesKind:
		return "bytes"
	case protoreflect.EnumKind:
		return "enum"
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return "object"
	default:
		return "unknown"
	}
}

// getProtoFieldComment extracts the leading comment from a proto field descriptor.
func getProtoFieldComment(fd protoreflect.FieldDescriptor) string {
	// Proto field descriptors from source info carry comments, but the
	// generated descriptor doesn't always have them. Fall back to empty.
	loc := fd.ParentFile().SourceLocations().ByDescriptor(fd)
	if loc.LeadingComments != "" {
		return cleanProtoComment(loc.LeadingComments)
	}
	return ""
}

func cleanProtoComment(s string) string {
	s = strings.TrimSpace(s)
	// Remove leading "// " from each line
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(strings.TrimSpace(line), "// ")
	}
	return strings.Join(lines, " ")
}

// =============================================================================
// NODE TYPE POPULATION
// =============================================================================

func populateNodeTypes() {
	// Use TypeRegistry to dynamically discover both args and output descriptors.
	registry := wfcel.NewTypeRegistry()

	// Auto-discover node types from NodeMeta proto annotations on V2Node.args oneof.
	nodeDesc := (&reliantv1.Node{}).ProtoReflect().Descriptor()
	argsOneof := nodeDesc.Oneofs().ByName("args")
	if argsOneof == nil {
		return
	}

	fields := argsOneof.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		msgDesc := fd.Message()
		if msgDesc == nil {
			continue
		}

		// Read NodeMeta from message options.
		opts := msgDesc.Options()
		if opts == nil {
			continue
		}
		ext := proto.GetExtension(opts, reliantv1.E_NodeMeta)
		if ext == nil {
			continue
		}
		meta, ok := ext.(*reliantv1.NodeMeta)
		if !ok || meta == nil || meta.NodeType == "" {
			continue
		}

		info := NodeTypeInfo{
			Name:        meta.DisplayName,
			TypeName:    meta.NodeType,
			Summary:     meta.Description,
			Description: meta.Description,
		}

		// Extract input fields from proto annotations on the args message.
		info.Fields = protoFieldsToNodeFields(msgDesc)

		// Extract output fields dynamically from the TypeRegistry.
		if outputDesc, exists := registry.OutputForNodeType(meta.NodeType); exists {
			info.OutputFields = protoFieldsToNodeFields(outputDesc)
		}

		nodeTypes[meta.NodeType] = info
	}
}

// protoFieldsToNodeFields converts proto message fields to NodeFieldInfo using wfcel.ExtractFieldInfo.
func protoFieldsToNodeFields(md protoreflect.MessageDescriptor) []NodeFieldInfo {
	celFields := wfcel.ExtractFieldInfo(md)
	var result []NodeFieldInfo
	for _, f := range celFields {
		if f.Hidden {
			continue
		}
		result = append(result, NodeFieldInfo{
			Name:        f.Name,
			Type:        f.Type,
			Required:    !f.IsRepeated && f.DefaultValue == "",
			Description: f.Description,
			EnumValues:  f.EnumValues,
		})
	}
	return result
}

// =============================================================================
// INPUT TYPE POPULATION
// =============================================================================

func populateInputTypes() {
	// Input types map: type name -> description + representative config message
	inputDefs := []struct {
		typeName    string
		displayName string
		description string
		configMsg   protoreflect.MessageDescriptor
	}{
		{"string", "StringInput", "Text string input with optional validation",
			(&reliantv1.StringInputConfig{}).ProtoReflect().Descriptor()},
		{"number", "NumberInput", "Decimal number input with optional min/max",
			(&reliantv1.NumberInputConfig{}).ProtoReflect().Descriptor()},
		{"integer", "IntegerInput", "Whole number input with optional min/max",
			(&reliantv1.IntegerInputConfig{}).ProtoReflect().Descriptor()},
		{"boolean", "BooleanInput", "Boolean toggle input",
			(&reliantv1.BooleanInputConfig{}).ProtoReflect().Descriptor()},
		{"enum", "EnumInput", "Dropdown with predefined values",
			(&reliantv1.EnumInputConfig{}).ProtoReflect().Descriptor()},
		{"model", "ModelInput", "Model selector dropdown",
			(&reliantv1.ModelInputConfig{}).ProtoReflect().Descriptor()},
		{"message", "MessageInput", "Primary user message/prompt input",
			(&reliantv1.MessageInputConfig{}).ProtoReflect().Descriptor()},
		{"attachments", "AttachmentsInput", "File attachment input",
			(&reliantv1.AttachmentsInputConfig{}).ProtoReflect().Descriptor()},
		{"tools", "ToolsInput", "Tool selector input",
			(&reliantv1.ToolsInputConfig{}).ProtoReflect().Descriptor()},
		{"array", "ArrayInput", "Generic array input",
			(&reliantv1.ArrayInputConfig{}).ProtoReflect().Descriptor()},
		{"object", "ObjectInput", "Structured object with JSON Schema validation",
			(&reliantv1.ObjectInputConfig{}).ProtoReflect().Descriptor()},
		{"any", "AnyInput", "Generic input accepting any JSON value",
			(&reliantv1.AnyInputConfig{}).ProtoReflect().Descriptor()},
		{"group", "GroupInput", "Input group for organizing related inputs with preset matching",
			(&reliantv1.GroupInputConfig{}).ProtoReflect().Descriptor()},
		{"preset", "PresetInput", "Dynamic preset picker filtered by tags",
			(&reliantv1.PresetInputConfig{}).ProtoReflect().Descriptor()},
	}

	for _, def := range inputDefs {
		info := InputTypeInfo{
			Name:        def.displayName,
			TypeName:    def.typeName,
			Summary:     def.description,
			Description: def.description,
		}

		// Extract fields from InputBase + config-specific fields
		if def.configMsg != nil {
			info.Fields = protoFieldsToInputFields(def.configMsg)
		}

		inputTypes[def.typeName] = info
	}
}

func protoFieldsToInputFields(md protoreflect.MessageDescriptor) []InputFieldInfo {
	fields := md.Fields()
	var result []InputFieldInfo
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)

		// Expand InputBase inline
		if fd.Kind() == protoreflect.MessageKind && fd.Message().FullName() == "reliant.v1.InputBase" {
			baseFields := fd.Message().Fields()
			for j := 0; j < baseFields.Len(); j++ {
				bf := baseFields.Get(j)
				result = append(result, InputFieldInfo{
					Name:        string(bf.Name()),
					Type:        protoKindToSimpleType(bf),
					Description: getProtoFieldComment(bf),
				})
			}
			continue
		}

		result = append(result, InputFieldInfo{
			Name:        string(fd.Name()),
			Type:        protoKindToSimpleType(fd),
			Description: getProtoFieldComment(fd),
		})
	}
	return result
}

// =============================================================================
// SHARED TYPE POPULATION
// =============================================================================

func populateSharedTypes() {
	sharedDefs := []struct {
		name string
		desc string
		msg  protoreflect.MessageDescriptor
	}{
		{"ThreadConfig", "Thread configuration for node execution",
			(&reliantv1.ThreadConfig{}).ProtoReflect().Descriptor()},
		{"InjectConfig", "Message injection into a thread",
			(&reliantv1.InjectConfig{}).ProtoReflect().Descriptor()},
		{"SaveMessageConfig", "Automatic message saving after node completion",
			(&reliantv1.SaveMessageConfig{}).ProtoReflect().Descriptor()},
		{"ProjectConfig", "Project path override for sub-workflows",
			(&reliantv1.ProjectConfig{}).ProtoReflect().Descriptor()},
		{"ResponseTool", "Custom tool for structured LLM output",
			(&reliantv1.ResponseTool{}).ProtoReflect().Descriptor()},
		{"SubWorkflow", "Sub-workflow invocation configuration",
			(&reliantv1.SubWorkflowArgs{}).ProtoReflect().Descriptor()},
		{"ModelSelector", "Model selector using tags or explicit ID",
			(&reliantv1.ModelSelector{}).ProtoReflect().Descriptor()},
		// Output types as shared types (for CEL access documentation)
		{"MessageOutput", "Standardized output for activities that produce messages",
			(&reliantv1.MessageOutput{}).ProtoReflect().Descriptor()},
		{"ThinkingOutput", "Extended thinking content from LLM",
			(&reliantv1.ThinkingOutput{}).ProtoReflect().Descriptor()},
		{"CallLLMOutput", "Output from LLM inference calls",
			(&reliantv1.CallLLMOutput{}).ProtoReflect().Descriptor()},
		{"ExecuteToolsOutput", "Output from tool execution",
			(&reliantv1.ExecuteToolsOutput{}).ProtoReflect().Descriptor()},
		{"RunOutput", "Output from shell command execution",
			(&reliantv1.RunOutput{}).ProtoReflect().Descriptor()},
		{"WorkflowOutput", "Output from sub-workflow execution",
			(&reliantv1.WorkflowOutput{}).ProtoReflect().Descriptor()},
		{"LoopOutput", "Output from loop execution",
			(&reliantv1.LoopOutput{}).ProtoReflect().Descriptor()},
		{"JoinOutput", "Output from join node",
			(&reliantv1.JoinOutput{}).ProtoReflect().Descriptor()},
		{"ApprovalOutput", "Output from approval requests",
			(&reliantv1.ApprovalOutput{}).ProtoReflect().Descriptor()},
		{"SaveMessageOutput", "Output from saving messages to thread",
			(&reliantv1.SaveMessageOutput{}).ProtoReflect().Descriptor()},
		{"CompactOutput", "Output from thread compaction",
			(&reliantv1.CompactOutput{}).ProtoReflect().Descriptor()},
		{"CreateWorktreeOutput", "Output from creating git worktrees",
			(&reliantv1.CreateWorktreeOutput{}).ProtoReflect().Descriptor()},
		{"DeleteWorktreeOutput", "Output from deleting git worktrees",
			(&reliantv1.DeleteWorktreeOutput{}).ProtoReflect().Descriptor()},
		{"SkippedOutput", "Output for nodes skipped due to conditions",
			(&reliantv1.SkippedOutput{}).ProtoReflect().Descriptor()},
		// Top-level structural types. get_schema advertises these by name, so
		// they must resolve here or its own examples fail.
		{"Workflow", "Top-level workflow definition: nodes, edges, inputs, and outputs",
			(&reliantv1.Workflow{}).ProtoReflect().Descriptor()},
		{"Edge", "Connects a source node to destination(s) with conditional routing",
			(&reliantv1.Edge{}).ProtoReflect().Descriptor()},
		{"EdgeCase", "One conditional routing path from an edge",
			(&reliantv1.EdgeCase{}).ProtoReflect().Descriptor()},
	}

	for _, def := range sharedDefs {
		info := SharedTypeInfo{
			Name:        def.name,
			Summary:     def.desc,
			Description: def.desc,
		}

		if def.msg != nil {
			info.Fields = protoFieldsToSharedFields(def.msg)
		}

		sharedTypes[def.name] = info
	}
}

func protoFieldsToSharedFields(md protoreflect.MessageDescriptor) []SharedFieldInfo {
	celFields := wfcel.ExtractFieldInfo(md)
	var result []SharedFieldInfo
	for _, f := range celFields {
		if f.Hidden {
			continue
		}
		result = append(result, SharedFieldInfo{
			Name:        f.Name,
			Type:        f.Type,
			Description: f.Description,
		})
	}
	return result
}

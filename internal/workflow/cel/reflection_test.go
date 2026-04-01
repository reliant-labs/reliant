package wfcel

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

// =============================================================================
// FIELD INFO EXTRACTION TESTS
// =============================================================================

func TestExtractFieldInfo_CallLLMArgs(t *testing.T) {
	md := (&reliantv1.CallLLMArgs{}).ProtoReflect().Descriptor()
	fields := ExtractFieldInfo(md)

	if len(fields) == 0 {
		t.Fatal("expected fields for CallLLMArgs, got none")
	}

	fieldMap := make(map[string]FieldInfo)
	for _, f := range fields {
		fieldMap[f.Name] = f
	}

	// model is a CelModelSelector
	model, ok := fieldMap["model"]
	if !ok {
		t.Fatal("expected 'model' field")
	}
	if !model.IsCEL {
		t.Error("expected model.IsCEL = true")
	}
	if model.Type != "model_selector" {
		t.Errorf("expected model.Type = %q, got %q", "model_selector", model.Type)
	}
	if model.Description == "" {
		t.Error("expected model to have a description")
	}

	// temperature is a CelDouble
	temp, ok := fieldMap["temperature"]
	if !ok {
		t.Fatal("expected 'temperature' field")
	}
	if !temp.IsCEL {
		t.Error("expected temperature.IsCEL = true")
	}
	if temp.Type != "double" {
		t.Errorf("expected temperature.Type = %q, got %q", "double", temp.Type)
	}

	// max_tokens is a CelInt
	mt, ok := fieldMap["max_tokens"]
	if !ok {
		t.Fatal("expected 'max_tokens' field")
	}
	if !mt.IsCEL {
		t.Error("expected max_tokens.IsCEL = true")
	}
	if mt.Type != "int" {
		t.Errorf("expected max_tokens.Type = %q, got %q", "int", mt.Type)
	}

	// thinking_level has enum_values
	tl, ok := fieldMap["thinking_level"]
	if !ok {
		t.Fatal("expected 'thinking_level' field")
	}
	if len(tl.EnumValues) == 0 {
		t.Error("expected thinking_level to have enum values")
	}
	// Check that the enum values match "none|low|medium|high|xhigh"
	expectedEnums := []string{"none", "low", "medium", "high", "xhigh"}
	if len(tl.EnumValues) != len(expectedEnums) {
		t.Errorf("expected %d enum values, got %d: %v", len(expectedEnums), len(tl.EnumValues), tl.EnumValues)
	} else {
		for i, ev := range expectedEnums {
			if tl.EnumValues[i] != ev {
				t.Errorf("enum value %d: expected %q, got %q", i, ev, tl.EnumValues[i])
			}
		}
	}

	// system_prompt has ui_hint = "textarea"
	sp, ok := fieldMap["system_prompt"]
	if !ok {
		t.Fatal("expected 'system_prompt' field")
	}
	if sp.UIHint != "textarea" {
		t.Errorf("expected system_prompt.UIHint = %q, got %q", "textarea", sp.UIHint)
	}
	if !sp.IsCEL {
		t.Error("expected system_prompt.IsCEL = true")
	}
	if sp.Type != "string" {
		t.Errorf("expected system_prompt.Type = %q, got %q", "string", sp.Type)
	}

	// tools is a CelBool
	tools, ok := fieldMap["tools"]
	if !ok {
		t.Fatal("expected 'tools' field")
	}
	if !tools.IsCEL {
		t.Error("expected tools.IsCEL = true")
	}
	if tools.Type != "bool" {
		t.Errorf("expected tools.Type = %q, got %q", "bool", tools.Type)
	}

	// tool_filter is a CelStringList (CelX wrapper, not a repeated string)
	tf, ok := fieldMap["tool_filter"]
	if !ok {
		t.Fatal("expected 'tool_filter' field")
	}
	if !tf.IsCEL {
		t.Error("expected tool_filter.IsCEL = true")
	}
	if tf.IsRepeated {
		t.Error("expected tool_filter.IsRepeated = false")
	}
	if tf.Type != "string_list" {
		t.Errorf("expected tool_filter.Type = %q, got %q", "string_list", tf.Type)
	}

	// response_tool is a message (not CelX)
	rt, ok := fieldMap["response_tool"]
	if !ok {
		t.Fatal("expected 'response_tool' field")
	}
	if rt.IsCEL {
		t.Error("expected response_tool.IsCEL = false")
	}
	if rt.Type != "message" {
		t.Errorf("expected response_tool.Type = %q, got %q", "message", rt.Type)
	}
}

func TestExtractFieldInfo_SaveMessageNodeArgs_Hidden(t *testing.T) {
	md := (&reliantv1.SaveMessageNodeArgs{}).ProtoReflect().Descriptor()
	fields := ExtractFieldInfo(md)

	fieldMap := make(map[string]FieldInfo)
	for _, f := range fields {
		fieldMap[f.Name] = f
	}

	// attachments has hidden = true
	att, ok := fieldMap["attachments"]
	if !ok {
		t.Fatal("expected 'attachments' field")
	}
	if !att.Hidden {
		t.Error("expected attachments.Hidden = true")
	}

	// resolved_role has hidden = true
	rr, ok := fieldMap["resolved_role"]
	if !ok {
		t.Fatal("expected 'resolved_role' field")
	}
	if !rr.Hidden {
		t.Error("expected resolved_role.Hidden = true")
	}

	// role should not be hidden
	role, ok := fieldMap["role"]
	if !ok {
		t.Fatal("expected 'role' field")
	}
	if role.Hidden {
		t.Error("expected role.Hidden = false")
	}
	if role.Type != "string" {
		t.Errorf("expected role.Type = %q, got %q", "string", role.Type)
	}
	if !role.IsCEL {
		t.Error("expected role.IsCEL = true")
	}

	// role has enum_values "user|assistant|system|tool"
	expectedEnums := []string{"user", "assistant", "system", "tool"}
	if len(role.EnumValues) != len(expectedEnums) {
		t.Errorf("expected %d enum values for role, got %d: %v", len(expectedEnums), len(role.EnumValues), role.EnumValues)
	}
}

func TestExtractFieldInfo_V2Node(t *testing.T) {
	md := (&reliantv1.Node{}).ProtoReflect().Descriptor()
	fields := ExtractFieldInfo(md)

	fieldMap := make(map[string]FieldInfo)
	for _, f := range fields {
		fieldMap[f.Name] = f
	}

	// condition is DirectCelBool
	cond, ok := fieldMap["condition"]
	if !ok {
		t.Fatal("expected 'condition' field")
	}
	if !cond.IsCEL {
		t.Error("expected condition.IsCEL = true")
	}
	if !cond.IsDirect {
		t.Error("expected condition.IsDirect = true")
	}

	// timeout is CelString
	timeout, ok := fieldMap["timeout"]
	if !ok {
		t.Fatal("expected 'timeout' field")
	}
	if !timeout.IsCEL {
		t.Error("expected timeout.IsCEL = true")
	}
	if timeout.Type != "string" {
		t.Errorf("expected timeout.Type = %q, got %q", "string", timeout.Type)
	}
}

func TestExtractFieldInfoMap(t *testing.T) {
	md := (&reliantv1.RunArgs{}).ProtoReflect().Descriptor()
	fieldMap := ExtractFieldInfoMap(md)

	if len(fieldMap) == 0 {
		t.Fatal("expected fields for RunArgs, got none")
	}

	cmd, ok := fieldMap["command"]
	if !ok {
		t.Fatal("expected 'command' field")
	}
	if !cmd.IsCEL {
		t.Error("expected command.IsCEL = true")
	}
	if cmd.Type != "string" {
		t.Errorf("expected command.Type = %q, got %q", "string", cmd.Type)
	}

	env, ok := fieldMap["env"]
	if !ok {
		t.Fatal("expected 'env' field")
	}
	if env.IsMap {
		// env is a map<string, string>
		if env.Type != "map" {
			t.Errorf("expected env.Type = %q, got %q", "map", env.Type)
		}
	}
}

// =============================================================================
// TYPE REGISTRY TESTS
// =============================================================================

func TestTypeRegistry_NodeTypes(t *testing.T) {
	reg := NewTypeRegistry()
	types := reg.NodeTypes()

	if len(types) == 0 {
		t.Fatal("expected registered node types, got none")
	}

	// Verify known node types exist.
	typeSet := make(map[string]bool)
	for _, nt := range types {
		typeSet[nt] = true
	}

	expectedTypes := []string{
		"call_llm", "execute_tools", "compact", "approval",
		"save_message_node", "create_worktree", "run",
		"workflow", "loop", "join",
	}
	for _, et := range expectedTypes {
		if !typeSet[et] {
			t.Errorf("expected node type %q to be registered", et)
		}
	}
}

func TestTypeRegistry_ArgsForNodeType(t *testing.T) {
	reg := NewTypeRegistry()

	md, ok := reg.ArgsForNodeType("call_llm")
	if !ok {
		t.Fatal("expected args descriptor for 'call_llm'")
	}
	if string(md.FullName()) != "reliant.v1.CallLLMArgs" {
		t.Errorf("expected full name = %q, got %q", "reliant.v1.CallLLMArgs", md.FullName())
	}
}

func TestTypeRegistry_OutputForNodeType(t *testing.T) {
	reg := NewTypeRegistry()

	md, ok := reg.OutputForNodeType("call_llm")
	if !ok {
		t.Fatal("expected output descriptor for 'call_llm'")
	}
	if string(md.FullName()) != "reliant.v1.CallLLMOutput" {
		t.Errorf("expected full name = %q, got %q", "reliant.v1.CallLLMOutput", md.FullName())
	}
}

func TestTypeRegistry_FieldsForNodeType(t *testing.T) {
	reg := NewTypeRegistry()
	fields := reg.FieldsForNodeType("call_llm")

	if len(fields) == 0 {
		t.Fatal("expected fields for call_llm, got none")
	}

	// Verify model field exists.
	found := false
	for _, f := range fields {
		if f.Name == "model" {
			found = true
			if !f.IsCEL {
				t.Error("expected model.IsCEL = true")
			}
			break
		}
	}
	if !found {
		t.Error("expected 'model' field in call_llm args")
	}
}

func TestTypeRegistry_OutputFieldsForNodeType(t *testing.T) {
	reg := NewTypeRegistry()
	fields := reg.OutputFieldsForNodeType("run")

	if len(fields) == 0 {
		t.Fatal("expected output fields for run, got none")
	}

	fieldMap := make(map[string]FieldInfo)
	for _, f := range fields {
		fieldMap[f.Name] = f
	}

	if _, ok := fieldMap["exit_code"]; !ok {
		t.Error("expected 'exit_code' output field for run")
	}
	if _, ok := fieldMap["stdout"]; !ok {
		t.Error("expected 'stdout' output field for run")
	}
	if _, ok := fieldMap["stderr"]; !ok {
		t.Error("expected 'stderr' output field for run")
	}
}

func TestTypeRegistry_UnknownNodeType(t *testing.T) {
	reg := NewTypeRegistry()

	_, ok := reg.ArgsForNodeType("nonexistent")
	if ok {
		t.Error("expected ArgsForNodeType to return false for unknown type")
	}

	_, ok = reg.OutputForNodeType("nonexistent")
	if ok {
		t.Error("expected OutputForNodeType to return false for unknown type")
	}

	fields := reg.FieldsForNodeType("nonexistent")
	if fields != nil {
		t.Error("expected FieldsForNodeType to return nil for unknown type")
	}
}

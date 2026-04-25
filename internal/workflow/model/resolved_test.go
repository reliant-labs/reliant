package model

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestNodeThreadMode(t *testing.T) {
	if NodeThreadMode(nil) != ThreadModeInherit {
		t.Error("nil should default to inherit")
	}

	if NodeThreadMode(&reliantv1.Node{}) != ThreadModeInherit {
		t.Errorf("no thread = %q", NodeThreadMode(&reliantv1.Node{}))
	}

	nodeWithEmptyMode := &reliantv1.Node{
		Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
			Thread: &reliantv1.ThreadConfig{},
		}},
	}
	if NodeThreadMode(nodeWithEmptyMode) != ThreadModeInherit {
		t.Errorf("empty mode = %q", NodeThreadMode(nodeWithEmptyMode))
	}

	nodeWithForkMode := &reliantv1.Node{
		Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
			Thread: &reliantv1.ThreadConfig{Mode: "fork"},
		}},
	}
	if NodeThreadMode(nodeWithForkMode) != "fork" {
		t.Errorf("fork mode = %q", NodeThreadMode(nodeWithForkMode))
	}

	loopWithNewThread := &reliantv1.Node{
		Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{
			Thread: &reliantv1.ThreadConfig{Mode: "new"},
		}},
	}
	if NodeThreadMode(loopWithNewThread) != "new" {
		t.Errorf("loop thread mode = %q", NodeThreadMode(loopWithNewThread))
	}
}

func TestNodeInjectConfig(t *testing.T) {
	if NodeInjectConfig(nil) != nil {
		t.Error("nil should return nil")
	}

	if NodeInjectConfig(&reliantv1.Node{}) != nil {
		t.Error("no thread should return nil")
	}

	inject := &reliantv1.InjectConfig{
		Role:    &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "user"}},
		Content: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "hello"}},
	}
	nodeWithInject := &reliantv1.Node{
		Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
			Thread: &reliantv1.ThreadConfig{Inject: inject},
		}},
	}
	if NodeInjectConfig(nodeWithInject) != inject {
		t.Error("should return inject config")
	}

	loopWithInject := &reliantv1.Node{
		Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{
			Thread: &reliantv1.ThreadConfig{Inject: inject},
		}},
	}
	if NodeInjectConfig(loopWithInject) != inject {
		t.Error("loop should return inject config")
	}
}

func TestNodeThreadMemo(t *testing.T) {
	if NodeThreadMemo(nil) != false {
		t.Error("nil should return false")
	}

	if NodeThreadMemo(&reliantv1.Node{}) != false {
		t.Error("no thread should return false")
	}

	nodeWithMemo := &reliantv1.Node{
		Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
			Thread: &reliantv1.ThreadConfig{
				Memo: &reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}},
			},
		}},
	}
	if NodeThreadMemo(nodeWithMemo) != true {
		t.Error("memo true should return true")
	}

	loopWithMemo := &reliantv1.Node{
		Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{
			Thread: &reliantv1.ThreadConfig{
				Memo: &reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}},
			},
		}},
	}
	if NodeThreadMemo(loopWithMemo) != true {
		t.Error("loop memo true should return true")
	}
}

func TestNodeCommand(t *testing.T) {
	if NodeCommand(nil) != "" {
		t.Error("nil should return empty")
	}

	nonRunNode := &reliantv1.Node{Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{}}}
	if NodeCommand(nonRunNode) != "" {
		t.Error("non-run should return empty")
	}

	runNode := &reliantv1.Node{
		Args: &reliantv1.Node_Run{Run: &reliantv1.RunArgs{
			Command: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "echo hello"}},
		}},
	}
	if NodeCommand(runNode) != "echo hello" {
		t.Errorf("got %q", NodeCommand(runNode))
	}
}

func TestNodeLogFile(t *testing.T) {
	if NodeLogFile(nil) != "" {
		t.Error("nil should return empty")
	}

	nonRunNode := &reliantv1.Node{Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{}}}
	if NodeLogFile(nonRunNode) != "" {
		t.Error("non-run should return empty")
	}

	// Run node without log_file
	noLogFile := &reliantv1.Node{
		Args: &reliantv1.Node_Run{Run: &reliantv1.RunArgs{
			Command: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "echo hi"}},
		}},
	}
	if NodeLogFile(noLogFile) != "" {
		t.Error("run without log_file should return empty")
	}

	// Run node with log_file
	withLogFile := &reliantv1.Node{
		Args: &reliantv1.Node_Run{Run: &reliantv1.RunArgs{
			Command: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "echo hi"}},
			LogFile: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "/tmp/output.log"}},
		}},
	}
	if NodeLogFile(withLogFile) != "/tmp/output.log" {
		t.Errorf("got %q", NodeLogFile(withLogFile))
	}
}

func TestNodeRef(t *testing.T) {
	if NodeRef(nil) != "" {
		t.Error("nil should return empty")
	}

	workflowNode := &reliantv1.Node{
		Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
			Ref: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://agent"}},
		}},
	}
	if NodeRef(workflowNode) != "builtin://agent" {
		t.Errorf("workflow ref = %q", NodeRef(workflowNode))
	}

	loopNode := &reliantv1.Node{
		Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{
			Ref: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "project://loop-body"}},
		}},
	}
	if NodeRef(loopNode) != "project://loop-body" {
		t.Errorf("loop ref = %q", NodeRef(loopNode))
	}

	if NodeRef(&reliantv1.Node{Args: &reliantv1.Node_Run{Run: &reliantv1.RunArgs{}}}) != "" {
		t.Error("run should return empty ref")
	}
}

func TestNodePresets(t *testing.T) {
	if NodePresets(nil) != nil {
		t.Error("nil should return nil")
	}

	presets := map[string]string{"default": "my-preset"}
	workflowNode := &reliantv1.Node{
		Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{Presets: presets}},
	}
	got := NodePresets(workflowNode)
	if got["default"] != "my-preset" {
		t.Errorf("presets = %v", got)
	}

	loopNode := &reliantv1.Node{
		Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{
			Presets: map[string]string{"group1": "preset1"},
		}},
	}
	got = NodePresets(loopNode)
	if got["group1"] != "preset1" {
		t.Errorf("loop presets = %v", got)
	}
}

func TestNodeProjectPath(t *testing.T) {
	if NodeProjectPath(nil) != "" {
		t.Error("nil should return empty")
	}

	workflowWithProject := &reliantv1.Node{
		Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
			Project: &reliantv1.ProjectConfig{
				Path: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "/path/to/project"}},
			},
		}},
	}
	if NodeProjectPath(workflowWithProject) != "/path/to/project" {
		t.Errorf("project path = %q", NodeProjectPath(workflowWithProject))
	}

	loopWithProject := &reliantv1.Node{
		Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{
			Project: &reliantv1.ProjectConfig{
				Path: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "/loop/path"}},
			},
		}},
	}
	if NodeProjectPath(loopWithProject) != "/loop/path" {
		t.Errorf("loop project path = %q", NodeProjectPath(loopWithProject))
	}

	workflowWithoutProject := &reliantv1.Node{
		Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{}},
	}
	if NodeProjectPath(workflowWithoutProject) != "" {
		t.Error("no project should return empty")
	}
}

func TestNodeInlineWorkflow(t *testing.T) {
	if NodeInlineWorkflow(nil) != nil {
		t.Error("nil should return nil")
	}

	inlineWorkflow := &reliantv1.Workflow{Name: "inline-wf"}
	workflowNode := &reliantv1.Node{
		Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{Inline: inlineWorkflow}},
	}
	if NodeInlineWorkflow(workflowNode) != inlineWorkflow {
		t.Error("should return inline workflow")
	}

	loopInlineWorkflow := &reliantv1.Workflow{Name: "loop-body"}
	loopNode := &reliantv1.Node{
		Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{Inline: loopInlineWorkflow}},
	}
	if NodeInlineWorkflow(loopNode) != loopInlineWorkflow {
		t.Error("should return loop inline workflow")
	}
}

func TestNodeWhileExpr(t *testing.T) {
	if NodeWhileExpr(nil) != "" {
		t.Error("nil should return empty")
	}

	loopNode := &reliantv1.Node{
		Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{
			While: &reliantv1.DirectCelBool{Expr: "iter.iteration < 10"},
		}},
	}
	if NodeWhileExpr(loopNode) != "iter.iteration < 10" {
		t.Errorf("while = %q", NodeWhileExpr(loopNode))
	}

	nonLoopNode := &reliantv1.Node{Args: &reliantv1.Node_Run{Run: &reliantv1.RunArgs{}}}
	if NodeWhileExpr(nonLoopNode) != "" {
		t.Error("non-loop should return empty")
	}
}

func TestNodeYieldExpr(t *testing.T) {
	if NodeYieldExpr(nil) != "" {
		t.Error("nil should return empty")
	}

	loopNode := &reliantv1.Node{
		Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{Yield: "outputs.needs_input"}},
	}
	if NodeYieldExpr(loopNode) != "outputs.needs_input" {
		t.Errorf("yield = %q", NodeYieldExpr(loopNode))
	}
}

func TestNodeArgsAsMap(t *testing.T) {
	mapped, err := NodeArgsAsMap(nil)
	if err != nil || mapped != nil {
		t.Error("nil should return nil, nil")
	}

	runNode := &reliantv1.Node{
		Type: "run",
		Args: &reliantv1.Node_Run{Run: &reliantv1.RunArgs{
			Command: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "echo hi"}},
		}},
	}
	mapped, err = NodeArgsAsMap(runNode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mapped == nil {
		t.Fatal("map should not be nil")
	}
	if _, ok := mapped["command"]; !ok {
		t.Errorf("map missing command key, got %v", mapped)
	}
}

// TestNodeArgsAsMap_CelStringUnwrap verifies that CelString wrapper types are
// unwrapped to their literal values, preventing the proto serialization mismatch
// that caused "proto: syntax error: unexpected token {" in ExecuteTools activity.
func TestNodeArgsAsMap_CelStringUnwrap(t *testing.T) {
	// CelString literal should unwrap to plain string
	runNode := &reliantv1.Node{
		Type: "run",
		Args: &reliantv1.Node_Run{Run: &reliantv1.RunArgs{
			Command: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "echo hello"}},
		}},
	}
	mapped, err := NodeArgsAsMap(runNode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmd, ok := mapped["command"]
	if !ok {
		t.Fatal("map missing command key")
	}
	// Must be a string, NOT a map like {"literal": "echo hello"}
	cmdStr, isStr := cmd.(string)
	if !isStr {
		t.Fatalf("command should be string, got %T: %v", cmd, cmd)
	}
	if cmdStr != "echo hello" {
		t.Errorf("command = %q, want %q", cmdStr, "echo hello")
	}
}

// TestNodeArgsAsMap_CelStringExprUnwrap verifies expr variant unwrapping.
func TestNodeArgsAsMap_CelStringExprUnwrap(t *testing.T) {
	runNode := &reliantv1.Node{
		Type: "run",
		Args: &reliantv1.Node_Run{Run: &reliantv1.RunArgs{
			Command: &reliantv1.CelString{Value: &reliantv1.CelString_Expr{Expr: "{{inputs.cmd}}"}},
		}},
	}
	mapped, err := NodeArgsAsMap(runNode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmd, ok := mapped["command"]
	if !ok {
		t.Fatal("map missing command key")
	}
	cmdStr, isStr := cmd.(string)
	if !isStr {
		t.Fatalf("command should be string, got %T: %v", cmd, cmd)
	}
	if cmdStr != "{{inputs.cmd}}" {
		t.Errorf("command = %q, want %q", cmdStr, "{{inputs.cmd}}")
	}
}

// TestNodeArgsAsMap_CelBoolUnwrap verifies CelBool unwrapping.
func TestNodeArgsAsMap_CelBoolUnwrap(t *testing.T) {
	node := &reliantv1.Node{
		Type: "create_worktree",
		Args: &reliantv1.Node_CreateWorktree{CreateWorktree: &reliantv1.CreateWorktreeArgs{
			Force: &reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}},
		}},
	}
	mapped, err := NodeArgsAsMap(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	force, ok := mapped["force"]
	if !ok {
		t.Fatal("map missing force key")
	}
	forceBool, isBool := force.(bool)
	if !isBool {
		t.Fatalf("force should be bool, got %T: %v", force, force)
	}
	if !forceBool {
		t.Error("force should be true")
	}
}

// TestNodeArgsAsMap_ExecuteToolsWithResolvedCalls is the critical regression test.
// It verifies that ExecuteToolsArgs with resolved_tool_calls produces a proper
// array in the map, not a CelString wrapper.
func TestNodeArgsAsMap_ExecuteToolsWithResolvedCalls(t *testing.T) {
	node := &reliantv1.Node{
		Type: "execute_tools",
		Args: &reliantv1.Node_ExecuteTools{ExecuteTools: &reliantv1.ExecuteToolsArgs{
			ToolCalls: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{
				Literal: `[{"id":"tc1","name":"bash","input":"{\"command\":\"ls\"}"}]`,
			}},
			ResolvedToolCalls: []*reliantv1.ToolCallMsg{
				{Id: "tc1", Name: "bash", Input: `{"command":"ls"}`},
				{Id: "tc2", Name: "edit", Input: `{"file":"foo.go"}`},
			},
		}},
	}
	mapped, err := NodeArgsAsMap(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// tool_calls should be unwrapped from CelString to a plain string
	tc, ok := mapped["tool_calls"]
	if !ok {
		t.Fatal("map missing tool_calls key")
	}
	if _, isStr := tc.(string); !isStr {
		t.Fatalf("tool_calls should be string, got %T: %v", tc, tc)
	}

	// resolved_tool_calls should be an array, NOT a CelString wrapper
	rtc, ok := mapped["resolved_tool_calls"]
	if !ok {
		t.Fatal("map missing resolved_tool_calls key")
	}
	rtcArray, isArray := rtc.([]interface{})
	if !isArray {
		t.Fatalf("resolved_tool_calls should be []interface{}, got %T: %v", rtc, rtc)
	}
	if len(rtcArray) != 2 {
		t.Fatalf("resolved_tool_calls should have 2 elements, got %d", len(rtcArray))
	}

	// Each element should be a map with string values
	for i, elem := range rtcArray {
		elemMap, isMap := elem.(map[string]interface{})
		if !isMap {
			t.Fatalf("resolved_tool_calls[%d] should be map, got %T", i, elem)
		}
		if _, ok := elemMap["input"]; !ok {
			t.Fatalf("resolved_tool_calls[%d] missing input key", i)
		}
		// input must be a string, not a parsed JSON object
		if _, isStr := elemMap["input"].(string); !isStr {
			t.Fatalf("resolved_tool_calls[%d].input should be string, got %T: %v", i, elemMap["input"], elemMap["input"])
		}
	}
}

// TestNodeArgsAsMap_CallLLMAllCelTypes verifies all CelX types unwrap correctly.
func TestNodeArgsAsMap_CallLLMAllCelTypes(t *testing.T) {
	node := &reliantv1.Node{
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
			Model:         &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Expr{Expr: "{{inputs.model}}"}},
			Temperature:   &reliantv1.CelDouble{Value: &reliantv1.CelDouble_Literal{Literal: 0.7}},
			MaxTokens:     &reliantv1.CelInt{Value: &reliantv1.CelInt_Literal{Literal: 4096}},
			ThinkingLevel: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "medium"}},
			SystemPrompt:  &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "You are helpful"}},
		}},
	}
	mapped, err := NodeArgsAsMap(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// CelDouble → float64
	if temp, ok := mapped["temperature"]; ok {
		if _, isFloat := temp.(float64); !isFloat {
			t.Errorf("temperature should be float64, got %T: %v", temp, temp)
		}
	}

	// CelInt → string (protojson encodes int64 as string to preserve precision)
	if mt, ok := mapped["max_tokens"]; ok {
		if _, isStr := mt.(string); !isStr {
			t.Errorf("max_tokens should be string (protojson int64 encoding), got %T: %v", mt, mt)
		}
	}

	// CelString → string
	if tl, ok := mapped["thinking_level"]; ok {
		if _, isStr := tl.(string); !isStr {
			t.Errorf("thinking_level should be string, got %T: %v", tl, tl)
		}
	}

	// CelBool → bool
	if tools, ok := mapped["tools"]; ok {
		if _, isBool := tools.(bool); !isBool {
			t.Errorf("tools should be bool, got %T: %v", tools, tools)
		}
	}

	// CelModelSelector expr → string (expr value, since it's an expression)
	if model, ok := mapped["model"]; ok {
		if _, isStr := model.(string); !isStr {
			t.Errorf("model should be string, got %T: %v", model, model)
		}
	}
}

// TestNodeArgsAsMap_CelModelSelectorLiteral verifies that CelModelSelector with
// a literal ModelSelector (containing tags) is NOT unwrapped, preserving the
// {"literal": {...}} wrapper needed for protojson round-tripping.
func TestNodeArgsAsMap_CelModelSelectorLiteral(t *testing.T) {
	node := &reliantv1.Node{
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
			Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Literal{
				Literal: &reliantv1.ModelSelector{
					Tags: []string{"flagship"},
				},
			}},
			Temperature: &reliantv1.CelDouble{Value: &reliantv1.CelDouble_Literal{Literal: 0.7}},
		}},
	}
	mapped, err := NodeArgsAsMap(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// model should remain wrapped as {"literal": {"tags": ["flagship"]}}
	// so protojson can re-hydrate it into CelModelSelector.
	modelVal, ok := mapped["model"]
	if !ok {
		t.Fatal("expected model field in mapped output")
	}
	modelMap, isMap := modelVal.(map[string]interface{})
	if !isMap {
		t.Fatalf("model should be a map (wrapper preserved), got %T: %v", modelVal, modelVal)
	}
	if _, hasLiteral := modelMap["literal"]; !hasLiteral {
		t.Errorf("model wrapper should have 'literal' key, got: %v", modelMap)
	}

	// temperature (scalar CelDouble) should be unwrapped to float64
	if temp, ok := mapped["temperature"]; ok {
		if _, isFloat := temp.(float64); !isFloat {
			t.Errorf("temperature should be float64, got %T: %v", temp, temp)
		}
	}
}

// TestNodeArgsAsMap_NestedCelUnwrap verifies CelX unwrapping in nested structures.
func TestNodeArgsAsMap_NestedCelUnwrap(t *testing.T) {
	node := &reliantv1.Node{
		Type: "save_message",
		Args: &reliantv1.Node_SaveMessageNode{SaveMessageNode: &reliantv1.SaveMessageNodeArgs{
			Role:         &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "assistant"}},
			Content:      &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "hello world"}},
			DisplayStyle: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "info"}},
		}},
	}
	mapped, err := NodeArgsAsMap(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// All CelString fields should be plain strings
	for _, key := range []string{"role", "content", "display_style"} {
		val, ok := mapped[key]
		if !ok {
			continue // field may not be present if empty
		}
		if _, isStr := val.(string); !isStr {
			t.Errorf("%s should be string, got %T: %v", key, val, val)
		}
	}
}

func TestNodeArgsAsMapAllTypes(t *testing.T) {
	nodes := []*reliantv1.Node{
		{Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{}}},
		{Type: "execute_tools", Args: &reliantv1.Node_ExecuteTools{ExecuteTools: &reliantv1.ExecuteToolsArgs{}}},
		{Type: "compact", Args: &reliantv1.Node_Compact{Compact: &reliantv1.CompactArgs{}}},
		{Type: "approval", Args: &reliantv1.Node_Approval{Approval: &reliantv1.ApprovalArgs{}}},
		{Type: "save_message", Args: &reliantv1.Node_SaveMessageNode{SaveMessageNode: &reliantv1.SaveMessageNodeArgs{}}},
		{Type: "create_worktree", Args: &reliantv1.Node_CreateWorktree{CreateWorktree: &reliantv1.CreateWorktreeArgs{}}},
		{Type: "run", Args: &reliantv1.Node_Run{Run: &reliantv1.RunArgs{}}},
		{Type: "workflow", Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{}}},
		{Type: "loop", Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{}}},
		{Type: "join", Args: &reliantv1.Node_Join{Join: &reliantv1.JoinArgs{}}},
	}
	for _, node := range nodes {
		_, err := NodeArgsAsMap(node)
		if err != nil {
			t.Errorf("NodeArgsAsMap() for %s: %v", node.Type, err)
		}
	}
}

func TestNodeMergedSubWorkflowInputs(t *testing.T) {
	if NodeMergedSubWorkflowInputs(nil) != nil {
		t.Error("nil should return nil")
	}

	workflowWithNoArgs := &reliantv1.Node{
		Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{}},
	}
	if NodeMergedSubWorkflowInputs(workflowWithNoArgs) != nil {
		t.Error("no args should return nil")
	}

	stringValue, _ := structpb.NewValue("hello")
	numberValue, _ := structpb.NewValue(42.0)
	workflowWithArgs := &reliantv1.Node{
		Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
			Args: map[string]*structpb.Value{
				"name":  stringValue,
				"count": numberValue,
			},
		}},
	}
	mergedInputs := NodeMergedSubWorkflowInputs(workflowWithArgs)
	if mergedInputs["name"] != "hello" {
		t.Errorf("name = %v", mergedInputs["name"])
	}
	if mergedInputs["count"] != 42.0 {
		t.Errorf("count = %v", mergedInputs["count"])
	}

	loopWithArgs := &reliantv1.Node{
		Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{
			Args: map[string]*structpb.Value{
				"input": stringValue,
			},
		}},
	}
	mergedInputs = NodeMergedSubWorkflowInputs(loopWithArgs)
	if mergedInputs["input"] != "hello" {
		t.Errorf("input = %v", mergedInputs["input"])
	}
}

// =============================================================================
// CelX wrapper unwrapping unit tests
// =============================================================================

func TestIsCelWrapper(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]interface{}
		want  bool
	}{
		{"literal only", map[string]interface{}{"literal": "hello"}, true},
		{"expr only", map[string]interface{}{"expr": "{{inputs.x}}"}, true},
		{"both literal and expr", map[string]interface{}{"literal": "val", "expr": "ex"}, true},
		{"bool literal", map[string]interface{}{"literal": true}, true},
		{"float literal", map[string]interface{}{"literal": 0.7}, true},
		{"empty map", map[string]interface{}{}, false},
		{"extra keys", map[string]interface{}{"literal": "v", "extra": "k"}, false},
		{"non-cel map", map[string]interface{}{"id": "tc1", "name": "bash"}, false},
		{"three keys", map[string]interface{}{"literal": "v", "expr": "e", "other": "o"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isCelWrapper(tc.input)
			if got != tc.want {
				t.Errorf("isCelWrapper(%v) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestUnwrapCelWrappers(t *testing.T) {
	t.Run("unwraps literal strings", func(t *testing.T) {
		m := map[string]interface{}{
			"command": map[string]interface{}{"literal": "echo hello"},
			"role":    map[string]interface{}{"literal": "assistant"},
		}
		unwrapCelWrappers(m)
		if m["command"] != "echo hello" {
			t.Errorf("command = %v", m["command"])
		}
		if m["role"] != "assistant" {
			t.Errorf("role = %v", m["role"])
		}
	})

	t.Run("unwraps expr strings", func(t *testing.T) {
		m := map[string]interface{}{
			"tool_calls": map[string]interface{}{"expr": "{{nodes.call_llm.tool_calls}}"},
		}
		unwrapCelWrappers(m)
		if m["tool_calls"] != "{{nodes.call_llm.tool_calls}}" {
			t.Errorf("tool_calls = %v", m["tool_calls"])
		}
	})

	t.Run("unwraps bool literals", func(t *testing.T) {
		m := map[string]interface{}{
			"tools": map[string]interface{}{"literal": true},
		}
		unwrapCelWrappers(m)
		if m["tools"] != true {
			t.Errorf("tools = %v", m["tools"])
		}
	})

	t.Run("unwraps float literals", func(t *testing.T) {
		m := map[string]interface{}{
			"temperature": map[string]interface{}{"literal": 0.7},
		}
		unwrapCelWrappers(m)
		if m["temperature"] != 0.7 {
			t.Errorf("temperature = %v", m["temperature"])
		}
	})

	t.Run("does not unwrap non-cel maps", func(t *testing.T) {
		original := map[string]interface{}{"id": "tc1", "name": "bash", "input": `{"command":"ls"}`}
		m := map[string]interface{}{
			"tool_call": original,
		}
		unwrapCelWrappers(m)
		// Should remain a map, not unwrapped
		if _, isMap := m["tool_call"].(map[string]interface{}); !isMap {
			t.Errorf("tool_call should remain a map, got %T", m["tool_call"])
		}
	})

	t.Run("preserves non-map values", func(t *testing.T) {
		m := map[string]interface{}{
			"plain_string": "hello",
			"number":       42.0,
			"bool":         true,
			"array":        []interface{}{"a", "b"},
		}
		unwrapCelWrappers(m)
		if m["plain_string"] != "hello" {
			t.Errorf("plain_string changed")
		}
		if m["number"] != 42.0 {
			t.Errorf("number changed")
		}
	})

	t.Run("prefers literal over expr", func(t *testing.T) {
		m := map[string]interface{}{
			"field": map[string]interface{}{"literal": "resolved_value", "expr": "{{inputs.x}}"},
		}
		unwrapCelWrappers(m)
		if m["field"] != "resolved_value" {
			t.Errorf("field = %v, want resolved_value", m["field"])
		}
	})
}

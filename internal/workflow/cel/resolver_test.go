package wfcel

import (
	"fmt"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// =============================================================================
// MOCK EVALUATOR
// =============================================================================

type mockEvaluator struct {
	values map[string]interface{} // expr → result
}

func newMockEvaluator(values map[string]interface{}) *mockEvaluator {
	return &mockEvaluator{values: values}
}

func (m *mockEvaluator) EvalString(expr string) (interface{}, error) {
	if v, ok := m.values[expr]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("unknown expression: %s", expr)
}

func (m *mockEvaluator) EvalBool(expr string) (bool, error) {
	if v, ok := m.values[expr]; ok {
		if b, ok := v.(bool); ok {
			return b, nil
		}
		return false, fmt.Errorf("expression %s did not return bool, got %T", expr, v)
	}
	return false, fmt.Errorf("unknown expression: %s", expr)
}

// =============================================================================
// RESOLVER TESTS
// =============================================================================

func TestResolveCELFields_CelStringLiteral(t *testing.T) {
	// A CelString with literal set should be left unchanged.
	node := &reliantv1.Node{
		Id:   "test-literal",
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{
			CallLlm: &reliantv1.CallLLMArgs{
				SystemPrompt: &reliantv1.CelString{
					Value: &reliantv1.CelString_Literal{Literal: "You are helpful."},
				},
			},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	args := resolved.GetCallLlm()
	if args.SystemPrompt.GetLiteral() != "You are helpful." {
		t.Errorf("expected literal = %q, got %q", "You are helpful.", args.SystemPrompt.GetLiteral())
	}
}

func TestResolveCELFields_CelStringExpr(t *testing.T) {
	node := &reliantv1.Node{
		Id:   "test-string-expr",
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{
			CallLlm: &reliantv1.CallLLMArgs{
				SystemPrompt: &reliantv1.CelString{
					Value: &reliantv1.CelString_Expr{Expr: "{{inputs.prompt}}"},
				},
			},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{
		"{{inputs.prompt}}": "You are a helpful assistant.",
	})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	args := resolved.GetCallLlm()
	if args.SystemPrompt.GetLiteral() != "You are a helpful assistant." {
		t.Errorf("expected literal = %q, got %q", "You are a helpful assistant.", args.SystemPrompt.GetLiteral())
	}
	// Expr case should be cleared (oneof switch to literal)
	if args.SystemPrompt.GetExpr() != "" {
		t.Errorf("expected expr to be cleared, got %q", args.SystemPrompt.GetExpr())
	}
}

func TestResolveCELFields_CelBoolExpr(t *testing.T) {
	node := &reliantv1.Node{
		Id:   "test-bool",
		Type: "create_worktree",
		Args: &reliantv1.Node_CreateWorktree{
			CreateWorktree: &reliantv1.CreateWorktreeArgs{
				Force: &reliantv1.CelBool{
					Value: &reliantv1.CelBool_Expr{Expr: "{{inputs.force}}"},
				},
			},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{
		"{{inputs.force}}": true,
	})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	args := resolved.GetCreateWorktree()
	if args.Force.GetLiteral() != true {
		t.Errorf("expected force literal = true, got %v", args.Force.GetLiteral())
	}
}

func TestResolveCELFields_CelIntExpr(t *testing.T) {
	node := &reliantv1.Node{
		Id:   "test-int",
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{
			CallLlm: &reliantv1.CallLLMArgs{
				MaxTokens: &reliantv1.CelInt{
					Value: &reliantv1.CelInt_Expr{Expr: "{{inputs.max_tokens}}"},
				},
			},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{
		// CEL typically returns float64 for numbers from JSON
		"{{inputs.max_tokens}}": float64(4096),
	})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	args := resolved.GetCallLlm()
	if args.MaxTokens.GetLiteral() != 4096 {
		t.Errorf("expected max_tokens literal = 4096, got %v", args.MaxTokens.GetLiteral())
	}
}

func TestResolveCELFields_CelDoubleExpr(t *testing.T) {
	node := &reliantv1.Node{
		Id:   "test-double",
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{
			CallLlm: &reliantv1.CallLLMArgs{
				Temperature: &reliantv1.CelDouble{
					Value: &reliantv1.CelDouble_Expr{Expr: "{{inputs.temp}}"},
				},
			},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{
		"{{inputs.temp}}": 0.7,
	})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	args := resolved.GetCallLlm()
	if args.Temperature.GetLiteral() != 0.7 {
		t.Errorf("expected temperature literal = 0.7, got %v", args.Temperature.GetLiteral())
	}
}

func TestResolveCELFields_CelModelSelectorExpr(t *testing.T) {
	node := &reliantv1.Node{
		Id:   "test-model",
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{
			CallLlm: &reliantv1.CallLLMArgs{
				Model: &reliantv1.CelModelSelector{
					Value: &reliantv1.CelModelSelector_Expr{Expr: "{{inputs.model}}"},
				},
			},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{
		"{{inputs.model}}": map[string]interface{}{
			"tags": []interface{}{"flagship", "reasoning"},
		},
	})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	args := resolved.GetCallLlm()
	ms := args.Model.GetLiteral()
	if ms == nil {
		t.Fatal("expected model literal to be set")
	}
	if len(ms.Tags) != 2 || ms.Tags[0] != "flagship" || ms.Tags[1] != "reasoning" {
		t.Errorf("expected model tags = [flagship, reasoning], got %v", ms.Tags)
	}
}

func TestResolveCELFields_NestedMessage(t *testing.T) {
	// V2SaveMessageConfig fields are resolved later in save_message handling when output.* exists.
	node := &reliantv1.Node{
		Id:   "test-nested",
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{
			CallLlm: &reliantv1.CallLLMArgs{},
		},
		SaveMessage: &reliantv1.SaveMessageConfig{
			Role: &reliantv1.CelString{
				Value: &reliantv1.CelString_Literal{Literal: "assistant"},
			},
			Content: &reliantv1.CelString{
				Value: &reliantv1.CelString_Expr{Expr: "{{output.message.content}}"},
			},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{
		"{{output.message.content}}": "Here is my response.",
	})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	sm := resolved.GetSaveMessage()
	if sm == nil {
		t.Fatal("expected SaveMessage to be present")
	}
	if sm.Role.GetLiteral() != "assistant" {
		t.Errorf("expected role literal = %q, got %q", "assistant", sm.Role.GetLiteral())
	}
	if sm.Content.GetExpr() == "" {
		t.Fatalf("expected save_message content expression to remain unresolved")
	}
}

func TestResolveCELFields_DirectCelBool_Skipped(t *testing.T) {
	// DirectCelBool (conditions) should NOT be resolved — they are evaluated
	// separately by the engine.
	node := &reliantv1.Node{
		Id:   "test-condition",
		Type: "call_llm",
		Condition: &reliantv1.DirectCelBool{
			Expr: "nodes.prev.response_text != ''",
		},
		Args: &reliantv1.Node_CallLlm{
			CallLlm: &reliantv1.CallLLMArgs{},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	if resolved.Condition.GetExpr() != "nodes.prev.response_text != ''" {
		t.Errorf("expected condition expr to be preserved, got %q", resolved.Condition.GetExpr())
	}
}

func TestResolveCELFields_DeepCopySafety(t *testing.T) {
	// Original message must not be modified after resolution.
	original := &reliantv1.Node{
		Id:   "test-deepcopy",
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{
			CallLlm: &reliantv1.CallLLMArgs{
				SystemPrompt: &reliantv1.CelString{
					Value: &reliantv1.CelString_Expr{Expr: "{{inputs.prompt}}"},
				},
				Model: &reliantv1.CelModelSelector{
					Value: &reliantv1.CelModelSelector_Literal{
						Literal: &reliantv1.ModelSelector{Tags: []string{"fast"}},
					},
				},
			},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{
		"{{inputs.prompt}}": "Resolved prompt",
	})

	result, err := ResolveCELFields(original, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Original should still have the expression.
	origArgs := original.GetCallLlm()
	if origArgs.SystemPrompt.GetExpr() != "{{inputs.prompt}}" {
		t.Errorf("original expr modified: got %q", origArgs.SystemPrompt.GetExpr())
	}

	// Resolved should have the literal.
	resolved := result.(*reliantv1.Node)
	resolvedArgs := resolved.GetCallLlm()
	if resolvedArgs.SystemPrompt.GetLiteral() != "Resolved prompt" {
		t.Errorf("expected resolved literal = %q, got %q", "Resolved prompt", resolvedArgs.SystemPrompt.GetLiteral())
	}

	// Verify they are different objects.
	if original == resolved {
		t.Error("original and resolved should be different objects")
	}
}

func TestResolveCELFields_NilCelFields(t *testing.T) {
	// Nil CelX fields should be handled gracefully.
	node := &reliantv1.Node{
		Id:   "test-nil",
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{
			CallLlm: &reliantv1.CallLLMArgs{
				// All CelX fields are nil — should not panic.
			},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	if resolved.GetCallLlm() == nil {
		t.Fatal("expected call_llm args to be present")
	}
}

func TestResolveCELFields_UnsetOneofValue(t *testing.T) {
	// A CelString with no value set (neither literal nor expr) should be handled.
	node := &reliantv1.Node{
		Id:   "test-unset",
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{
			CallLlm: &reliantv1.CallLLMArgs{
				SystemPrompt: &reliantv1.CelString{
					// Value is nil — no oneof case set
				},
			},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	args := resolved.GetCallLlm()
	// Should remain unset.
	if args.SystemPrompt.GetLiteral() != "" && args.SystemPrompt.GetExpr() != "" {
		t.Error("expected both literal and expr to be empty for unset CelString")
	}
}

func TestResolveCELFields_ErrorInEvaluation(t *testing.T) {
	node := &reliantv1.Node{
		Id:   "test-error",
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{
			CallLlm: &reliantv1.CallLLMArgs{
				SystemPrompt: &reliantv1.CelString{
					Value: &reliantv1.CelString_Expr{Expr: "{{inputs.nonexistent}}"},
				},
			},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{})

	_, err := ResolveCELFields(node, eval)
	if err == nil {
		t.Fatal("expected error for unknown expression, got nil")
	}
	if !strings.Contains(err.Error(), "inputs.nonexistent") {
		t.Errorf("expected error to mention the expression, got: %v", err)
	}
}

func TestResolveCELFields_TypeConversionError(t *testing.T) {
	node := &reliantv1.Node{
		Id:   "test-type-error",
		Type: "create_worktree",
		Args: &reliantv1.Node_CreateWorktree{
			CreateWorktree: &reliantv1.CreateWorktreeArgs{
				Force: &reliantv1.CelBool{
					Value: &reliantv1.CelBool_Expr{Expr: "{{inputs.not_a_bool}}"},
				},
			},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{
		"{{inputs.not_a_bool}}": "this is a string, not bool",
	})

	_, err := ResolveCELFields(node, eval)
	if err == nil {
		t.Fatal("expected type conversion error, got nil")
	}
	if !strings.Contains(err.Error(), "bool") {
		t.Errorf("expected error to mention bool conversion, got: %v", err)
	}
}

func TestResolveCELFields_ThreadConfig(t *testing.T) {
	// Thread config has nested CelX fields (memo is CelBool, inject has CelString fields).
	node := &reliantv1.Node{
		Id:   "test-thread",
		Type: "workflow",
		Args: &reliantv1.Node_Workflow{
			Workflow: &reliantv1.SubWorkflowArgs{
				Thread: &reliantv1.ThreadConfig{
					Mode: "new",
					Memo: &reliantv1.CelBool{
						Value: &reliantv1.CelBool_Literal{Literal: true},
					},
					Inject: &reliantv1.InjectConfig{
						Role: &reliantv1.CelString{
							Value: &reliantv1.CelString_Literal{Literal: "user"},
						},
						Content: &reliantv1.CelString{
							Value: &reliantv1.CelString_Expr{Expr: "{{inputs.context}}"},
						},
					},
				},
			},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{
		"{{inputs.context}}": "Review this code please.",
	})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	thread := resolved.GetWorkflow().GetThread()
	if thread == nil || thread.Inject == nil {
		t.Fatal("expected Thread.Inject to be present")
	}
	if thread.Inject.Content.GetLiteral() != "Review this code please." {
		t.Errorf("expected inject content = %q, got %q", "Review this code please.", thread.Inject.Content.GetLiteral())
	}
	if thread.Inject.Role.GetLiteral() != "user" {
		t.Errorf("expected inject role = %q, got %q", "user", thread.Inject.Role.GetLiteral())
	}
	if thread.Memo.GetLiteral() != true {
		t.Errorf("expected memo literal = true, got %v", thread.Memo.GetLiteral())
	}
}

func TestResolveCELFields_ThreadInjectLiteralTemplate(t *testing.T) {
	node := &reliantv1.Node{
		Id:   "test-thread-inject-literal",
		Type: "workflow",
		Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
			Thread: &reliantv1.ThreadConfig{
				Mode: "new",
				Inject: &reliantv1.InjectConfig{
					Role:    &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "user"}},
					Content: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "Review: {{nodes.planner.response_text}}"}},
				},
			},
		}},
	}

	eval := newMockEvaluator(map[string]interface{}{
		"Review: {{nodes.planner.response_text}}": "Review: Plan details",
	})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	got := resolved.GetWorkflow().GetThread().GetInject().GetContent().GetLiteral()
	if got != "Review: Plan details" {
		t.Fatalf("expected evaluated inject literal, got %q", got)
	}
}

func TestResolveCELFields_TimeoutField(t *testing.T) {
	node := &reliantv1.Node{
		Id:   "test-timeout",
		Type: "call_llm",
		Timeout: &reliantv1.CelString{
			Value: &reliantv1.CelString_Expr{Expr: "{{inputs.timeout}}"},
		},
		Args: &reliantv1.Node_CallLlm{
			CallLlm: &reliantv1.CallLLMArgs{},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{
		"{{inputs.timeout}}": "5m",
	})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	if resolved.Timeout.GetLiteral() != "5m" {
		t.Errorf("expected timeout literal = %q, got %q", "5m", resolved.Timeout.GetLiteral())
	}
}

func TestResolveCELFields_RunNodeCommand(t *testing.T) {
	node := &reliantv1.Node{
		Id:   "test-run",
		Type: "run",
		Args: &reliantv1.Node_Run{
			Run: &reliantv1.RunArgs{
				Command: &reliantv1.CelString{
					Value: &reliantv1.CelString_Expr{Expr: "{{inputs.cmd}}"},
				},
				WorkDir: &reliantv1.CelString{
					Value: &reliantv1.CelString_Literal{Literal: "/home/user"},
				},
			},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{
		"{{inputs.cmd}}": "go test ./...",
	})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	args := resolved.GetRun()
	if args.Command.GetLiteral() != "go test ./..." {
		t.Errorf("expected command = %q, got %q", "go test ./...", args.Command.GetLiteral())
	}
	if args.WorkDir.GetLiteral() != "/home/user" {
		t.Errorf("expected work_dir = %q, got %q", "/home/user", args.WorkDir.GetLiteral())
	}
}

func TestResolveCELFields_CreateWorktreeNode(t *testing.T) {
	node := &reliantv1.Node{
		Id:   "test-worktree",
		Type: "create_worktree",
		Args: &reliantv1.Node_CreateWorktree{
			CreateWorktree: &reliantv1.CreateWorktreeArgs{
				Name: &reliantv1.CelString{
					Value: &reliantv1.CelString_Expr{Expr: "{{inputs.name}}"},
				},
				BaseBranch: &reliantv1.CelString{
					Value: &reliantv1.CelString_Expr{Expr: "{{inputs.base}}"},
				},
				Force: &reliantv1.CelBool{
					Value: &reliantv1.CelBool_Expr{Expr: "{{inputs.force}}"},
				},
			},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{
		"{{inputs.name}}":  "feature-auth",
		"{{inputs.base}}":  "main",
		"{{inputs.force}}": true,
	})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	args := resolved.GetCreateWorktree()
	if args.Name.GetLiteral() != "feature-auth" {
		t.Errorf("expected name = %q, got %q", "feature-auth", args.Name.GetLiteral())
	}
	if args.BaseBranch.GetLiteral() != "main" {
		t.Errorf("expected base_branch = %q, got %q", "main", args.BaseBranch.GetLiteral())
	}
	if args.Force.GetLiteral() != true {
		t.Errorf("expected force = true, got %v", args.Force.GetLiteral())
	}
}

func TestResolveCELFields_LoopWhileSkipped(t *testing.T) {
	// Loop.while is a DirectCelBool — should be skipped like conditions.
	node := &reliantv1.Node{
		Id:   "test-loop",
		Type: "loop",
		Args: &reliantv1.Node_Loop{
			Loop: &reliantv1.LoopArgs{
				Ref: &reliantv1.CelString{
					Value: &reliantv1.CelString_Expr{Expr: "{{inputs.ref}}"},
				},
				While: &reliantv1.DirectCelBool{
					Expr: "iter.iteration < 5",
				},
			},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{
		"{{inputs.ref}}": "builtin://agent",
	})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	args := resolved.GetLoop()
	// Ref should be resolved.
	if args.Ref.GetLiteral() != "builtin://agent" {
		t.Errorf("expected ref = %q, got %q", "builtin://agent", args.Ref.GetLiteral())
	}
	// While (DirectCelBool) should be preserved.
	if args.While.GetExpr() != "iter.iteration < 5" {
		t.Errorf("expected while expr preserved, got %q", args.While.GetExpr())
	}
}

func TestResolveCELFields_MultipleFieldsResolved(t *testing.T) {
	// Multiple expr fields on the same message should all be resolved.
	node := &reliantv1.Node{
		Id:   "test-multi",
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{
			CallLlm: &reliantv1.CallLLMArgs{
				SystemPrompt: &reliantv1.CelString{
					Value: &reliantv1.CelString_Expr{Expr: "{{inputs.prompt}}"},
				},
				ThinkingLevel: &reliantv1.CelString{
					Value: &reliantv1.CelString_Expr{Expr: "{{inputs.thinking}}"},
				},
				Temperature: &reliantv1.CelDouble{
					Value: &reliantv1.CelDouble_Expr{Expr: "{{inputs.temp}}"},
				},
				MaxTokens: &reliantv1.CelInt{
					Value: &reliantv1.CelInt_Expr{Expr: "{{inputs.tokens}}"},
				},
			},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{
		"{{inputs.prompt}}":   "Be concise.",
		"{{inputs.thinking}}": "high",
		"{{inputs.temp}}":     0.3,
		"{{inputs.tokens}}":   int64(2048),
	})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	args := resolved.GetCallLlm()

	if args.SystemPrompt.GetLiteral() != "Be concise." {
		t.Errorf("system_prompt: expected %q, got %q", "Be concise.", args.SystemPrompt.GetLiteral())
	}
	if args.ThinkingLevel.GetLiteral() != "high" {
		t.Errorf("thinking_level: expected %q, got %q", "high", args.ThinkingLevel.GetLiteral())
	}
	if args.Temperature.GetLiteral() != 0.3 {
		t.Errorf("temperature: expected 0.3, got %v", args.Temperature.GetLiteral())
	}
	if args.MaxTokens.GetLiteral() != 2048 {
		t.Errorf("max_tokens: expected 2048, got %v", args.MaxTokens.GetLiteral())
	}
}

func TestResolveCELFields_SaveMessageNodeArgs(t *testing.T) {
	node := &reliantv1.Node{
		Id:   "test-save-msg",
		Type: "save_message_node",
		Args: &reliantv1.Node_SaveMessageNode{
			SaveMessageNode: &reliantv1.SaveMessageNodeArgs{
				Role: &reliantv1.CelString{
					Value: &reliantv1.CelString_Expr{Expr: "{{output.role}}"},
				},
				Content: &reliantv1.CelString{
					Value: &reliantv1.CelString_Expr{Expr: "{{output.message.content}}"},
				},
			},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{
		"{{output.role}}":            "assistant",
		"{{output.message.content}}": "Here is my response.",
	})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	args := resolved.GetSaveMessageNode()
	if args.Role.GetLiteral() != "assistant" {
		t.Errorf("expected role = %q, got %q", "assistant", args.Role.GetLiteral())
	}
	if args.Content.GetLiteral() != "Here is my response." {
		t.Errorf("expected content = %q, got %q", "Here is my response.", args.Content.GetLiteral())
	}
}

func TestResolveCELFields_SaveMessageConfigSkipped(t *testing.T) {
	node := &reliantv1.Node{
		Id:   "test-save-message-skip",
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{}},
		SaveMessage: &reliantv1.SaveMessageConfig{
			Role:    &reliantv1.CelString{Value: &reliantv1.CelString_Expr{Expr: "{{output.role}}"}},
			Content: &reliantv1.CelString{Value: &reliantv1.CelString_Expr{Expr: "{{output.message.text}}"}},
		},
	}

	eval := newMockEvaluator(map[string]interface{}{})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	if resolved.GetSaveMessage().GetRole().GetExpr() == "" {
		t.Fatalf("expected save_message.role expr to remain unresolved")
	}
	if resolved.GetSaveMessage().GetContent().GetExpr() == "" {
		t.Fatalf("expected save_message.content expr to remain unresolved")
	}
}

func TestResolveCELFields_NestedInlineSaveMessageConfigSkipped(t *testing.T) {
	node := &reliantv1.Node{
		Id:   "parent-workflow",
		Type: "workflow",
		Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
			Inline: &reliantv1.Workflow{
				Name:  "inline-child",
				Entry: []string{"child-call"},
				Nodes: []*reliantv1.Node{
					{
						Id:   "child-call",
						Type: "call_llm",
						Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{}},
						SaveMessage: &reliantv1.SaveMessageConfig{
							Role:    &reliantv1.CelString{Value: &reliantv1.CelString_Expr{Expr: "{{output.role}}"}},
							Content: &reliantv1.CelString{Value: &reliantv1.CelString_Expr{Expr: "{{output.response_text}}"}},
						},
					},
				},
			},
		}},
	}

	eval := newMockEvaluator(map[string]interface{}{})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	inlineNode := resolved.GetWorkflow().GetInline().GetNodes()[0]
	if inlineNode.GetSaveMessage().GetRole().GetExpr() == "" {
		t.Fatalf("expected nested save_message.role expr to remain unresolved")
	}
	if inlineNode.GetSaveMessage().GetContent().GetExpr() == "" {
		t.Fatalf("expected nested save_message.content expr to remain unresolved")
	}
}

func TestResolveCELFields_ProtoCloneIndependence(t *testing.T) {
	// Verify proto.Clone creates an independent copy.
	original := &reliantv1.Node{
		Id:   "test-clone",
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{
			CallLlm: &reliantv1.CallLLMArgs{
				Model: &reliantv1.CelModelSelector{
					Value: &reliantv1.CelModelSelector_Literal{
						Literal: &reliantv1.ModelSelector{
							Tags: []string{"fast"},
						},
					},
				},
			},
		},
	}

	cloned := proto.Clone(original).(*reliantv1.Node)

	// Mutate the clone.
	cloned.GetCallLlm().Model.GetLiteral().Tags = []string{"flagship"}

	// Original should be unchanged.
	origTags := original.GetCallLlm().Model.GetLiteral().Tags
	if len(origTags) != 1 || origTags[0] != "fast" {
		t.Errorf("original mutated after clone, tags = %v", origTags)
	}
}

func TestResolveCELFields_MapStructPBValueTemplates(t *testing.T) {
	node := &reliantv1.Node{
		Id:   "test-loop-args-templates",
		Type: "loop",
		Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{
			Args: map[string]*structpb.Value{
				"ask":       structpb.NewStringValue("{{inputs.ask}}"),
				"max_turns": structpb.NewStringValue("{{inputs.max_turns}}"),
				"nested": structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{
					"flag": structpb.NewStringValue("{{inputs.ask}}"),
				}}),
			},
		}},
	}

	eval := newMockEvaluator(map[string]interface{}{
		"{{inputs.ask}}":       true,
		"{{inputs.max_turns}}": int64(200),
	})

	result, err := ResolveCELFields(node, eval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved := result.(*reliantv1.Node)
	resolvedArgs := resolved.GetLoop().GetArgs()

	if got := resolvedArgs["ask"].AsInterface(); got != true {
		t.Fatalf("expected ask bool true, got %T (%v)", got, got)
	}
	if got := resolvedArgs["max_turns"].AsInterface(); got != float64(200) {
		t.Fatalf("expected max_turns numeric 200, got %T (%v)", got, got)
	}
	nested, ok := resolvedArgs["nested"].AsInterface().(map[string]interface{})
	if !ok {
		t.Fatalf("expected nested map, got %T", resolvedArgs["nested"].AsInterface())
	}
	if got := nested["flag"]; got != true {
		t.Fatalf("expected nested.flag bool true, got %T (%v)", got, got)
	}
}

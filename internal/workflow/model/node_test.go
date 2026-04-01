package model

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

func TestNodeType(t *testing.T) {
	if NodeType(nil) != "" {
		t.Error("nil should return empty")
	}
	n := &reliantv1.Node{Type: "call_llm"}
	if NodeType(n) != "call_llm" {
		t.Errorf("got %q", NodeType(n))
	}
}

func TestNodeID(t *testing.T) {
	if NodeID(nil) != "" {
		t.Error("nil should return empty")
	}
	n := &reliantv1.Node{Id: "my-node"}
	if NodeID(n) != "my-node" {
		t.Errorf("got %q", NodeID(n))
	}
}

func TestConditionExpr(t *testing.T) {
	if ConditionExpr(nil) != "" {
		t.Error("nil should return empty")
	}
	// No condition
	n := &reliantv1.Node{}
	if ConditionExpr(n) != "" {
		t.Error("no condition should return empty")
	}
	// With condition
	n = &reliantv1.Node{
		Condition: &reliantv1.DirectCelBool{Expr: "nodes.prev.success"},
	}
	if ConditionExpr(n) != "nodes.prev.success" {
		t.Errorf("got %q", ConditionExpr(n))
	}
}

func TestTimeoutExpr(t *testing.T) {
	if TimeoutExpr(nil) != "" {
		t.Error("nil should return empty")
	}
	// No timeout
	n := &reliantv1.Node{}
	if TimeoutExpr(n) != "" {
		t.Error("no timeout should return empty")
	}
	// Literal timeout
	n = &reliantv1.Node{
		Timeout: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "5m"}},
	}
	if TimeoutExpr(n) != "5m" {
		t.Errorf("literal: got %q", TimeoutExpr(n))
	}
	// Expr timeout
	n = &reliantv1.Node{
		Timeout: &reliantv1.CelString{Value: &reliantv1.CelString_Expr{Expr: "inputs.timeout"}},
	}
	if TimeoutExpr(n) != "inputs.timeout" {
		t.Errorf("expr: got %q", TimeoutExpr(n))
	}
}

func TestGetCallLLMArgs(t *testing.T) {
	if GetCallLLMArgs(nil) != nil {
		t.Error("nil should return nil")
	}
	// Wrong type
	n := &reliantv1.Node{
		Args: &reliantv1.Node_Run{Run: &reliantv1.RunArgs{}},
	}
	if GetCallLLMArgs(n) != nil {
		t.Error("run node should return nil for GetCallLLMArgs")
	}
	// Correct type
	args := &reliantv1.CallLLMArgs{}
	n = &reliantv1.Node{
		Args: &reliantv1.Node_CallLlm{CallLlm: args},
	}
	if GetCallLLMArgs(n) != args {
		t.Error("should return the args")
	}
}

func TestGetRunArgs(t *testing.T) {
	if GetRunArgs(nil) != nil {
		t.Error("nil should return nil")
	}
	args := &reliantv1.RunArgs{
		Command: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "echo hello"}},
	}
	n := &reliantv1.Node{
		Args: &reliantv1.Node_Run{Run: args},
	}
	if GetRunArgs(n) != args {
		t.Error("should return run args")
	}
}

func TestGetLoopArgs(t *testing.T) {
	if GetLoopArgs(nil) != nil {
		t.Error("nil should return nil")
	}
	args := &reliantv1.LoopArgs{
		While: &reliantv1.DirectCelBool{Expr: "iter.iteration < 10"},
	}
	n := &reliantv1.Node{
		Args: &reliantv1.Node_Loop{Loop: args},
	}
	if GetLoopArgs(n) != args {
		t.Error("should return loop args")
	}
}

func TestGetSubWorkflowArgs(t *testing.T) {
	if GetSubWorkflowArgs(nil) != nil {
		t.Error("nil should return nil")
	}
	args := &reliantv1.SubWorkflowArgs{
		Ref: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://agent"}},
	}
	n := &reliantv1.Node{
		Args: &reliantv1.Node_Workflow{Workflow: args},
	}
	if GetSubWorkflowArgs(n) != args {
		t.Error("should return sub-workflow args")
	}
}

func TestGetJoinArgs(t *testing.T) {
	if GetJoinArgs(nil) != nil {
		t.Error("nil should return nil")
	}
	args := &reliantv1.JoinArgs{}
	n := &reliantv1.Node{
		Args: &reliantv1.Node_Join{Join: args},
	}
	if GetJoinArgs(n) != args {
		t.Error("should return join args")
	}
}

func TestGetExecuteToolsArgs(t *testing.T) {
	if GetExecuteToolsArgs(nil) != nil {
		t.Error("nil should return nil")
	}
	args := &reliantv1.ExecuteToolsArgs{}
	n := &reliantv1.Node{
		Args: &reliantv1.Node_ExecuteTools{ExecuteTools: args},
	}
	if GetExecuteToolsArgs(n) != args {
		t.Error("should return execute tools args")
	}
}

func TestGetCompactArgs(t *testing.T) {
	if GetCompactArgs(nil) != nil {
		t.Error("nil should return nil")
	}
	args := &reliantv1.CompactArgs{}
	n := &reliantv1.Node{
		Args: &reliantv1.Node_Compact{Compact: args},
	}
	if GetCompactArgs(n) != args {
		t.Error("should return compact args")
	}
}

func TestGetApprovalArgs(t *testing.T) {
	if GetApprovalArgs(nil) != nil {
		t.Error("nil should return nil")
	}
	args := &reliantv1.ApprovalArgs{}
	n := &reliantv1.Node{
		Args: &reliantv1.Node_Approval{Approval: args},
	}
	if GetApprovalArgs(n) != args {
		t.Error("should return approval args")
	}
}

func TestGetSaveMessageNodeArgs(t *testing.T) {
	if GetSaveMessageNodeArgs(nil) != nil {
		t.Error("nil should return nil")
	}
	args := &reliantv1.SaveMessageNodeArgs{
		Role: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "assistant"}},
	}
	n := &reliantv1.Node{
		Args: &reliantv1.Node_SaveMessageNode{SaveMessageNode: args},
	}
	if GetSaveMessageNodeArgs(n) != args {
		t.Error("should return save message args")
	}
}

func TestGetCreateWorktreeArgs(t *testing.T) {
	if GetCreateWorktreeArgs(nil) != nil {
		t.Error("nil should return nil")
	}
	args := &reliantv1.CreateWorktreeArgs{
		Name: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "feature-x"}},
	}
	n := &reliantv1.Node{
		Args: &reliantv1.Node_CreateWorktree{CreateWorktree: args},
	}
	if GetCreateWorktreeArgs(n) != args {
		t.Error("should return create worktree args")
	}
}

func TestFindNode(t *testing.T) {
	if FindNode(nil, "x") != nil {
		t.Error("nil workflow should return nil")
	}

	n1 := &reliantv1.Node{Id: "node-1", Type: "call_llm"}
	n2 := &reliantv1.Node{Id: "node-2", Type: "run"}
	n3 := &reliantv1.Node{Id: "node-3", Type: "loop"}

	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{n1, n2, n3},
	}

	if FindNode(wf, "node-1") != n1 {
		t.Error("should find node-1")
	}
	if FindNode(wf, "node-2") != n2 {
		t.Error("should find node-2")
	}
	if FindNode(wf, "node-3") != n3 {
		t.Error("should find node-3")
	}
	if FindNode(wf, "nonexistent") != nil {
		t.Error("should not find nonexistent")
	}
}

func TestGetArgsForWrongType(t *testing.T) {
	// A call_llm node should return nil for all non-call_llm getters
	n := &reliantv1.Node{
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{}},
	}
	if GetRunArgs(n) != nil {
		t.Error("call_llm should return nil for GetRunArgs")
	}
	if GetLoopArgs(n) != nil {
		t.Error("call_llm should return nil for GetLoopArgs")
	}
	if GetSubWorkflowArgs(n) != nil {
		t.Error("call_llm should return nil for GetSubWorkflowArgs")
	}
	if GetJoinArgs(n) != nil {
		t.Error("call_llm should return nil for GetJoinArgs")
	}
	if GetExecuteToolsArgs(n) != nil {
		t.Error("call_llm should return nil for GetExecuteToolsArgs")
	}
	if GetCompactArgs(n) != nil {
		t.Error("call_llm should return nil for GetCompactArgs")
	}
	if GetApprovalArgs(n) != nil {
		t.Error("call_llm should return nil for GetApprovalArgs")
	}
	if GetSaveMessageNodeArgs(n) != nil {
		t.Error("call_llm should return nil for GetSaveMessageNodeArgs")
	}
	if GetCreateWorktreeArgs(n) != nil {
		t.Error("call_llm should return nil for GetCreateWorktreeArgs")
	}
}

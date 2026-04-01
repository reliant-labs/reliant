package model

import (
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

// NodeType returns the type string from a V2Node.
// Reads from the Type field (set during YAML parse).
func NodeType(n *reliantv1.Node) string {
	if n == nil {
		return ""
	}
	return n.GetType()
}

// NodeID returns the ID string from a V2Node.
func NodeID(n *reliantv1.Node) string {
	if n == nil {
		return ""
	}
	return n.GetId()
}

// ConditionExpr returns the condition expression string, or "" if not set.
func ConditionExpr(n *reliantv1.Node) string {
	if n == nil {
		return ""
	}
	return DirectCelExpr(n.GetCondition())
}

// TimeoutExpr returns the timeout expression or literal, or "" if not set.
func TimeoutExpr(n *reliantv1.Node) string {
	if n == nil {
		return ""
	}
	return CelStringRaw(n.GetTimeout())
}

// GetCallLLMArgs returns the CallLLMArgs if this is a call_llm node, nil otherwise.
func GetCallLLMArgs(n *reliantv1.Node) *reliantv1.CallLLMArgs {
	if n == nil {
		return nil
	}
	return n.GetCallLlm()
}

// GetExecuteToolsArgs returns the ExecuteToolsArgs if this is an execute_tools node, nil otherwise.
func GetExecuteToolsArgs(n *reliantv1.Node) *reliantv1.ExecuteToolsArgs {
	if n == nil {
		return nil
	}
	return n.GetExecuteTools()
}

// GetCompactArgs returns the CompactArgs if this is a compact node, nil otherwise.
func GetCompactArgs(n *reliantv1.Node) *reliantv1.CompactArgs {
	if n == nil {
		return nil
	}
	return n.GetCompact()
}

// GetApprovalArgs returns the ApprovalArgs if this is an approval node, nil otherwise.
func GetApprovalArgs(n *reliantv1.Node) *reliantv1.ApprovalArgs {
	if n == nil {
		return nil
	}
	return n.GetApproval()
}

// GetSaveMessageNodeArgs returns the SaveMessageNodeArgs if this is a save_message node, nil otherwise.
func GetSaveMessageNodeArgs(n *reliantv1.Node) *reliantv1.SaveMessageNodeArgs {
	if n == nil {
		return nil
	}
	return n.GetSaveMessageNode()
}

// GetCreateWorktreeArgs returns the CreateWorktreeArgs if this is a create_worktree node, nil otherwise.
func GetCreateWorktreeArgs(n *reliantv1.Node) *reliantv1.CreateWorktreeArgs {
	if n == nil {
		return nil
	}
	return n.GetCreateWorktree()
}

// GetRunArgs returns the RunArgs if this is a run node, nil otherwise.
func GetRunArgs(n *reliantv1.Node) *reliantv1.RunArgs {
	if n == nil {
		return nil
	}
	return n.GetRun()
}

// GetSubWorkflowArgs returns the SubWorkflowArgs if this is a workflow node, nil otherwise.
func GetSubWorkflowArgs(n *reliantv1.Node) *reliantv1.SubWorkflowArgs {
	if n == nil {
		return nil
	}
	return n.GetWorkflow()
}

// GetLoopArgs returns the LoopArgs if this is a loop node, nil otherwise.
func GetLoopArgs(n *reliantv1.Node) *reliantv1.LoopArgs {
	if n == nil {
		return nil
	}
	return n.GetLoop()
}

// GetJoinArgs returns the JoinArgs if this is a join node, nil otherwise.
func GetJoinArgs(n *reliantv1.Node) *reliantv1.JoinArgs {
	if n == nil {
		return nil
	}
	return n.GetJoin()
}

// FindNode finds a node by ID in a workflow. Returns nil if not found.
func FindNode(wf *reliantv1.Workflow, id string) *reliantv1.Node {
	if wf == nil {
		return nil
	}
	for _, n := range wf.GetNodes() {
		if n.GetId() == id {
			return n
		}
	}
	return nil
}

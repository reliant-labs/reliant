package model

import "testing"

func TestIsActivityNode(t *testing.T) {
	activityTypes := []string{
		NodeTypeCallLLM,
		NodeTypeExecuteTools,
		NodeTypeCompact,
		NodeTypeApproval,
		NodeTypeSaveMessage,
		NodeTypeCreateWorktree,
	}
	for _, nt := range activityTypes {
		if !IsActivityNode(nt) {
			t.Errorf("IsActivityNode(%q) = false, want true", nt)
		}
	}

	nonActivityTypes := []string{
		NodeTypeRun,
		NodeTypeWorkflow,
		NodeTypeLoop,
		NodeTypeJoin,
		"unknown",
		"",
	}
	for _, nt := range nonActivityTypes {
		if IsActivityNode(nt) {
			t.Errorf("IsActivityNode(%q) = true, want false", nt)
		}
	}
}

func TestIsStructuralNode(t *testing.T) {
	structuralTypes := []string{
		NodeTypeRun,
		NodeTypeWorkflow,
		NodeTypeLoop,
		NodeTypeJoin,
	}
	for _, nt := range structuralTypes {
		if !IsStructuralNode(nt) {
			t.Errorf("IsStructuralNode(%q) = false, want true", nt)
		}
	}

	nonStructuralTypes := []string{
		NodeTypeCallLLM,
		NodeTypeExecuteTools,
		NodeTypeCompact,
		NodeTypeApproval,
		NodeTypeSaveMessage,
		NodeTypeCreateWorktree,
		"unknown",
		"",
	}
	for _, nt := range nonStructuralTypes {
		if IsStructuralNode(nt) {
			t.Errorf("IsStructuralNode(%q) = true, want false", nt)
		}
	}
}

func TestNodeTypeConstants(t *testing.T) {
	// Verify values match expected strings
	tests := map[string]string{
		"NodeTypeCallLLM":        NodeTypeCallLLM,
		"NodeTypeExecuteTools":   NodeTypeExecuteTools,
		"NodeTypeCompact":        NodeTypeCompact,
		"NodeTypeApproval":       NodeTypeApproval,
		"NodeTypeSaveMessage":    NodeTypeSaveMessage,
		"NodeTypeCreateWorktree": NodeTypeCreateWorktree,
		"NodeTypeRun":            NodeTypeRun,
		"NodeTypeWorkflow":       NodeTypeWorkflow,
		"NodeTypeLoop":           NodeTypeLoop,
		"NodeTypeJoin":           NodeTypeJoin,
	}
	expected := map[string]string{
		"NodeTypeCallLLM":        "call_llm",
		"NodeTypeExecuteTools":   "execute_tools",
		"NodeTypeCompact":        "compact",
		"NodeTypeApproval":       "approval",
		"NodeTypeSaveMessage":    "save_message",
		"NodeTypeCreateWorktree": "create_worktree",
		"NodeTypeRun":            "run",
		"NodeTypeWorkflow":       "workflow",
		"NodeTypeLoop":           "loop",
		"NodeTypeJoin":           "join",
	}
	for name, got := range tests {
		if want := expected[name]; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestThreadModeConstants(t *testing.T) {
	if ThreadModeInherit != "inherit" {
		t.Errorf("ThreadModeInherit = %q", ThreadModeInherit)
	}
	if ThreadModeNew != "new" {
		t.Errorf("ThreadModeNew = %q", ThreadModeNew)
	}
	if ThreadModeFork != "fork" {
		t.Errorf("ThreadModeFork = %q", ThreadModeFork)
	}
}

func TestIsKnownNodeType(t *testing.T) {
	// All defined node types should be known
	allTypes := []string{
		NodeTypeCallLLM,
		NodeTypeExecuteTools,
		NodeTypeCompact,
		NodeTypeApproval,
		NodeTypeSaveMessage,
		NodeTypeCreateWorktree,
		NodeTypeRun,
		NodeTypeWorkflow,
		NodeTypeLoop,
		NodeTypeJoin,
	}
	for _, nt := range allTypes {
		if !IsKnownNodeType(nt) {
			t.Errorf("IsKnownNodeType(%q) = false, want true", nt)
		}
	}

	// Unknown types should not be known
	unknownTypes := []string{
		"null",
		"Null",
		"nonexistent",
		"foo_bar",
		"",
	}
	for _, nt := range unknownTypes {
		if IsKnownNodeType(nt) {
			t.Errorf("IsKnownNodeType(%q) = true, want false", nt)
		}
	}
}

func TestKnownNodeTypes(t *testing.T) {
	types := KnownNodeTypes()
	if len(types) == 0 {
		t.Fatal("KnownNodeTypes() returned empty")
	}

	// Should include the core types
	typeSet := make(map[string]bool)
	for _, nt := range types {
		typeSet[nt] = true
	}
	for _, required := range []string{NodeTypeCallLLM, NodeTypeLoop, NodeTypeRun, NodeTypeJoin} {
		if !typeSet[required] {
			t.Errorf("KnownNodeTypes() missing %q", required)
		}
	}

	// Should NOT include unknown types
	if typeSet["null"] {
		t.Error("KnownNodeTypes() should not include 'null'")
	}
}

func TestIsValidThinkingLevel(t *testing.T) {
	valid := []string{"low", "medium", "high", "xhigh", ""}
	for _, l := range valid {
		if !IsValidThinkingLevel(l) {
			t.Errorf("IsValidThinkingLevel(%q) = false, want true", l)
		}
	}

	invalid := []string{"none", "ultra", "max", "invalid"}
	for _, l := range invalid {
		if IsValidThinkingLevel(l) {
			t.Errorf("IsValidThinkingLevel(%q) = true, want false", l)
		}
	}
}

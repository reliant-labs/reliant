package model

import "testing"

func TestBuildIterContext(t *testing.T) {
	ctx := BuildIterContext(0)
	if ctx["iteration"] != 0 {
		t.Errorf("iteration = %v, want 0", ctx["iteration"])
	}

	ctx = BuildIterContext(5)
	if ctx["iteration"] != 5 {
		t.Errorf("iteration = %v, want 5", ctx["iteration"])
	}
}

func TestIterContext(t *testing.T) {
	ic := IterContext{Iteration: 3}
	if ic.Iteration != 3 {
		t.Errorf("Iteration = %d, want 3", ic.Iteration)
	}
}

func TestWorkflowContext(t *testing.T) {
	wc := WorkflowContext{
		ID:           "wf-1",
		Name:         "test-workflow",
		Path:         "/path/to/workflow",
		Branch:       "main",
		Mode:         "default",
		RunID:        "run-123",
		SessionID:    "session-456",
		WorktreePath: "/path/to/worktree",
	}
	if wc.ID != "wf-1" {
		t.Errorf("ID = %q", wc.ID)
	}
	if wc.Name != "test-workflow" {
		t.Errorf("Name = %q", wc.Name)
	}
	if wc.RunID != "run-123" {
		t.Errorf("RunID = %q", wc.RunID)
	}
}

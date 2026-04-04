package wfyaml

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
)

func TestParseParallelLoopNode(t *testing.T) {
	yaml := `
name: test-parallel-loop
entry: [implement_all]
nodes:
  - id: implement_all
    type: loop
    parallel: true
    items: "{{inputs.components}}"
    key: "{{iter.item.name}}"
    on_failure: continue
    ref: builtin://get-it-right
    thread:
      mode: new
    args:
      prompt: "{{iter.item.spec}}"
`

	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(wf.GetNodes()) != 1 {
		t.Fatalf("expected 1 node, got %d", len(wf.GetNodes()))
	}

	node := wf.GetNodes()[0]
	if node.GetId() != "implement_all" {
		t.Errorf("node id = %q, want implement_all", node.GetId())
	}
	if node.GetType() != "loop" {
		t.Errorf("node type = %q, want loop", node.GetType())
	}

	la := model.GetLoopArgs(node)
	if la == nil {
		t.Fatal("expected loop args, got nil")
	}

	// Check parallel field
	if !model.CelBoolValue(la.GetParallel()) {
		t.Error("parallel should be true")
	}

	// Check items field
	itemsRaw := model.CelStringRaw(la.GetItems())
	if itemsRaw != "{{inputs.components}}" {
		t.Errorf("items = %q, want {{inputs.components}}", itemsRaw)
	}

	// Check key field
	if la.GetKey() != "{{iter.item.name}}" {
		t.Errorf("key = %q, want {{iter.item.name}}", la.GetKey())
	}

	// Check on_failure field
	if la.GetOnFailure() != "continue" {
		t.Errorf("on_failure = %q, want continue", la.GetOnFailure())
	}

	// Check ref field
	refRaw := model.CelStringRaw(la.GetRef())
	if refRaw != "builtin://get-it-right" {
		t.Errorf("ref = %q, want builtin://get-it-right", refRaw)
	}

	// Check thread field
	tc := la.GetThread()
	if tc == nil {
		t.Fatal("expected thread config, got nil")
	}
	if tc.GetMode() != "new" {
		t.Errorf("thread.mode = %q, want new", tc.GetMode())
	}
}

func TestParseSequentialLoopUnchanged(t *testing.T) {
	yaml := `
name: test-sequential-loop
entry: [my_loop]
nodes:
  - id: my_loop
    type: loop
    while: "iter.iteration < 3"
    ref: builtin://agent
    args:
      prompt: "do something"
`

	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	node := wf.GetNodes()[0]
	la := model.GetLoopArgs(node)
	if la == nil {
		t.Fatal("expected loop args")
	}

	// Parallel should be unset/false
	if model.CelBoolValue(la.GetParallel()) {
		t.Error("sequential loop should not be parallel")
	}

	// While should be set
	whileExpr := model.DirectCelExpr(la.GetWhile())
	if whileExpr != "iter.iteration < 3" {
		t.Errorf("while = %q, want iter.iteration < 3", whileExpr)
	}

	// Items should be unset
	if model.CelStringIsSet(la.GetItems()) {
		t.Error("sequential loop should not have items")
	}
}

package wfyaml

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Sequence sugar tests
// ---------------------------------------------------------------------------

func TestSequence_BasicThreeNodes(t *testing.T) {
	yaml := `
name: seq-test
sequence:
  - id: step1
    type: workflow
    ref: builtin://agent
  - id: step2
    type: workflow
    ref: builtin://agent
  - id: step3
    type: workflow
    ref: builtin://structured-agent
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Entry should be the first node
	if len(wf.Entry) != 1 || wf.Entry[0] != "step1" {
		t.Errorf("entry: got %v, want [step1]", wf.Entry)
	}

	// Should have 3 nodes
	if len(wf.Nodes) != 3 {
		t.Fatalf("nodes: got %d, want 3", len(wf.Nodes))
	}
	for i, wantID := range []string{"step1", "step2", "step3"} {
		if wf.Nodes[i].GetId() != wantID {
			t.Errorf("nodes[%d].id: got %q, want %q", i, wf.Nodes[i].GetId(), wantID)
		}
	}

	// Should have 2 sequential edges: step1→step2, step2→step3
	if len(wf.Edges) != 2 {
		t.Fatalf("edges: got %d, want 2", len(wf.Edges))
	}
	wantEdges := [][2]string{{"step1", "step2"}, {"step2", "step3"}}
	for i, want := range wantEdges {
		e := wf.Edges[i]
		if e.From != want[0] {
			t.Errorf("edges[%d].from: got %q, want %q", i, e.From, want[0])
		}
		if len(e.Default) != 1 || e.Default[0] != want[1] {
			t.Errorf("edges[%d].default: got %v, want [%s]", i, e.Default, want[1])
		}
	}
}

func TestSequence_WithAdditionalNodesAndEdges(t *testing.T) {
	yaml := `
name: mixed-test
sequence:
  - id: step1
    type: workflow
    ref: builtin://agent
  - id: step2
    type: workflow
    ref: builtin://agent
nodes:
  - id: extra
    type: workflow
    ref: builtin://agent
edges:
  - from: step2
    cases:
      - condition: "{{output.needs_extra}}"
        to: extra
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Entry from sequence
	if len(wf.Entry) != 1 || wf.Entry[0] != "step1" {
		t.Errorf("entry: got %v, want [step1]", wf.Entry)
	}

	// 2 sequence nodes + 1 extra node = 3
	if len(wf.Nodes) != 3 {
		t.Fatalf("nodes: got %d, want 3", len(wf.Nodes))
	}

	// Sequence nodes first, then extra
	wantIDs := []string{"step1", "step2", "extra"}
	for i, wantID := range wantIDs {
		if wf.Nodes[i].GetId() != wantID {
			t.Errorf("nodes[%d].id: got %q, want %q", i, wf.Nodes[i].GetId(), wantID)
		}
	}

	// 1 sequential edge + 1 explicit edge = 2
	// But the explicit edge came from "edges:" which is parsed before sequence edges get prepended.
	// Actually: sequence edges are prepended, then explicit edges follow.
	if len(wf.Edges) != 2 {
		t.Fatalf("edges: got %d, want 2", len(wf.Edges))
	}
	// First edge: step1→step2 (from sequence)
	if wf.Edges[0].From != "step1" || wf.Edges[0].Default[0] != "step2" {
		t.Errorf("edges[0]: got from=%q default=%v, want step1→step2",
			wf.Edges[0].From, wf.Edges[0].Default)
	}
	// Second edge: step2 with case→extra (from explicit edges)
	if wf.Edges[1].From != "step2" {
		t.Errorf("edges[1].from: got %q, want step2", wf.Edges[1].From)
	}
	if len(wf.Edges[1].Cases) != 1 || wf.Edges[1].Cases[0].To[0] != "extra" {
		t.Errorf("edges[1].cases: expected case to extra, got %v", wf.Edges[1].Cases)
	}
}

func TestSequence_EntryConflict(t *testing.T) {
	yaml := `
name: conflict-test
entry: [something]
sequence:
  - id: step1
    type: workflow
    ref: builtin://agent
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for sequence + entry conflict")
	}
	if !strings.Contains(err.Error(), "cannot use both") {
		t.Errorf("error should mention conflict, got: %v", err)
	}
}

func TestSequence_Empty(t *testing.T) {
	yaml := `
name: empty-test
sequence: []
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for empty sequence")
	}
	if !strings.Contains(err.Error(), "at least one node") {
		t.Errorf("error should mention empty, got: %v", err)
	}
}

func TestSequence_SingleNode(t *testing.T) {
	yaml := `
name: single-test
sequence:
  - id: only
    type: workflow
    ref: builtin://agent
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(wf.Entry) != 1 || wf.Entry[0] != "only" {
		t.Errorf("entry: got %v, want [only]", wf.Entry)
	}
	if len(wf.Nodes) != 1 {
		t.Errorf("nodes: got %d, want 1", len(wf.Nodes))
	}
	// No edges for a single-node sequence
	if len(wf.Edges) != 0 {
		t.Errorf("edges: got %d, want 0", len(wf.Edges))
	}
}

// ---------------------------------------------------------------------------
// Parallel sugar tests
// ---------------------------------------------------------------------------

func TestParallel_BasicDesugaring(t *testing.T) {
	yaml := `
name: parallel-test
entry: [trigger]
nodes:
  - id: trigger
    type: workflow
    ref: builtin://agent
  - id: explore
    type: parallel
    branches:
      - id: research
        type: workflow
        ref: builtin://agent
      - id: design
        type: workflow
        ref: builtin://agent
edges:
  - from: trigger
    default: explore
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Should have 4 nodes: trigger, research, design, explore (join)
	if len(wf.Nodes) != 4 {
		t.Fatalf("nodes: got %d, want 4", len(wf.Nodes))
	}

	nodeMap := map[string]int{}
	for i, n := range wf.Nodes {
		nodeMap[n.GetId()] = i
	}

	// Check the join node
	joinIdx, ok := nodeMap["explore"]
	if !ok {
		t.Fatal("missing join node 'explore'")
	}
	joinNode := wf.Nodes[joinIdx]
	if joinNode.Type != "join" {
		t.Errorf("explore.type: got %q, want 'join'", joinNode.Type)
	}
	if joinNode.Condition == nil || joinNode.Condition.Expr != "all" {
		t.Errorf("explore.condition: got %v, want expr='all'", joinNode.Condition)
	}
	if joinNode.GetJoin() == nil {
		t.Error("explore.args: expected JoinArgs")
	}

	// Check branch nodes exist
	for _, branchID := range []string{"research", "design"} {
		if _, ok := nodeMap[branchID]; !ok {
			t.Errorf("missing branch node %q", branchID)
		}
	}

	// Check that the edge from trigger was rewritten to target branches
	var triggerEdge *struct {
		from     string
		defaults []string
	}
	for _, e := range wf.Edges {
		if e.From == "trigger" {
			triggerEdge = &struct {
				from     string
				defaults []string
			}{e.From, e.Default}
			break
		}
	}
	if triggerEdge == nil {
		t.Fatal("missing edge from trigger")
	}
	// Should target both branches, not 'explore'
	if len(triggerEdge.defaults) != 2 {
		t.Fatalf("trigger edge default: got %v, want 2 branches", triggerEdge.defaults)
	}
	branchSet := map[string]bool{}
	for _, d := range triggerEdge.defaults {
		branchSet[d] = true
	}
	if !branchSet["research"] || !branchSet["design"] {
		t.Errorf("trigger edge defaults: got %v, want [research, design]", triggerEdge.defaults)
	}

	// Check branch→join edges exist
	branchToJoinCount := 0
	for _, e := range wf.Edges {
		if (e.From == "research" || e.From == "design") &&
			len(e.Default) == 1 && e.Default[0] == "explore" {
			branchToJoinCount++
		}
	}
	if branchToJoinCount != 2 {
		t.Errorf("branch→join edges: got %d, want 2", branchToJoinCount)
	}
}

func TestParallel_EdgeCaseRewriting(t *testing.T) {
	yaml := `
name: parallel-case-test
entry: [trigger]
nodes:
  - id: trigger
    type: workflow
    ref: builtin://agent
  - id: explore
    type: parallel
    branches:
      - id: research
        type: workflow
        ref: builtin://agent
      - id: design
        type: workflow
        ref: builtin://agent
edges:
  - from: trigger
    cases:
      - condition: "{{output.ready}}"
        to: explore
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// The case targeting "explore" should be rewritten to target branches
	var triggerEdge *struct{ cases [][]string }
	for _, e := range wf.Edges {
		if e.From == "trigger" && len(e.Cases) > 0 {
			var caseTo [][]string
			for _, c := range e.Cases {
				caseTo = append(caseTo, c.To)
			}
			triggerEdge = &struct{ cases [][]string }{caseTo}
			break
		}
	}
	if triggerEdge == nil {
		t.Fatal("missing edge from trigger with cases")
	}
	if len(triggerEdge.cases) != 1 {
		t.Fatalf("cases: got %d, want 1", len(triggerEdge.cases))
	}
	to := triggerEdge.cases[0]
	if len(to) != 2 {
		t.Fatalf("case to: got %v, want 2 branches", to)
	}
	toSet := map[string]bool{}
	for _, target := range to {
		toSet[target] = true
	}
	if !toSet["research"] || !toSet["design"] {
		t.Errorf("case to: got %v, want [research, design]", to)
	}
}

func TestParallel_EntryRewriting(t *testing.T) {
	yaml := `
name: parallel-entry-test
entry: [explore]
nodes:
  - id: explore
    type: parallel
    branches:
      - id: research
        type: workflow
        ref: builtin://agent
      - id: design
        type: workflow
        ref: builtin://agent
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Entry should be rewritten to branches
	if len(wf.Entry) != 2 {
		t.Fatalf("entry: got %v, want 2 branches", wf.Entry)
	}
	entrySet := map[string]bool{}
	for _, e := range wf.Entry {
		entrySet[e] = true
	}
	if !entrySet["research"] || !entrySet["design"] {
		t.Errorf("entry: got %v, want [research, design]", wf.Entry)
	}
}

func TestParallel_ErrorNoID(t *testing.T) {
	yaml := `
name: no-id-test
entry: [x]
nodes:
  - type: parallel
    branches:
      - id: a
        type: workflow
        ref: builtin://agent
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for parallel without id")
	}
	if !strings.Contains(err.Error(), "must have an id") {
		t.Errorf("error should mention id, got: %v", err)
	}
}

func TestParallel_ErrorNoBranches(t *testing.T) {
	yaml := `
name: no-branches-test
entry: [x]
nodes:
  - id: par
    type: parallel
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for parallel without branches")
	}
	if !strings.Contains(err.Error(), "must have branches") {
		t.Errorf("error should mention branches, got: %v", err)
	}
}

func TestParallel_ErrorEmptyBranches(t *testing.T) {
	yaml := `
name: empty-branches-test
entry: [x]
nodes:
  - id: par
    type: parallel
    branches: []
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for parallel with empty branches")
	}
	if !strings.Contains(err.Error(), "at least one node") {
		t.Errorf("error should mention empty branches, got: %v", err)
	}
}

func TestSequence_WithParallel(t *testing.T) {
	// Test the combination: sequence with a parallel node mixed in via nodes:
	yaml := `
name: seq-parallel-test
sequence:
  - id: plan
    type: workflow
    ref: builtin://agent
  - id: review
    type: workflow
    ref: builtin://agent
nodes:
  - id: explore
    type: parallel
    branches:
      - id: research
        type: workflow
        ref: builtin://agent
      - id: design
        type: workflow
        ref: builtin://agent
edges:
  - from: plan
    default: explore
  - from: explore
    default: review
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Should have 5 nodes: plan, review (from sequence), research, design, explore/join (from parallel)
	if len(wf.Nodes) != 5 {
		t.Fatalf("nodes: got %d, want 5", len(wf.Nodes))
	}

	nodeMap := map[string]bool{}
	for _, n := range wf.Nodes {
		nodeMap[n.GetId()] = true
	}
	for _, id := range []string{"plan", "review", "research", "design", "explore"} {
		if !nodeMap[id] {
			t.Errorf("missing node %q", id)
		}
	}

	// Entry should be from sequence (plan)
	if len(wf.Entry) != 1 || wf.Entry[0] != "plan" {
		t.Errorf("entry: got %v, want [plan]", wf.Entry)
	}

	// The edge "from: plan default: explore" should be rewritten to target branches
	for _, e := range wf.Edges {
		if e.From == "plan" && len(e.Default) > 0 {
			// Could be the sequence edge (plan→review) or the explicit edge (plan→explore→branches)
			// The explicit edge targeting explore should be rewritten
			defaults := map[string]bool{}
			for _, d := range e.Default {
				defaults[d] = true
			}
			if defaults["explore"] {
				t.Error("edge from plan should not target 'explore' directly; should be rewritten to branches")
			}
		}
	}
}

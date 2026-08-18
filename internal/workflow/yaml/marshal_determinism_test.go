// Copyright (c) 2025 Reliant Labs
package wfyaml

import (
	"os"
	"path/filepath"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// MarshalWorkflow must be a pure function of its input: the same workflow has
// to produce byte-identical YAML every time. Go randomizes map iteration
// order, so any map emitted in range order makes the output vary run to run.
//
// This is not cosmetic. load_workflow.go re-marshals a stored workflow on
// every run, so unstable bytes mean a workflow nobody edited looks changed —
// defeating any hash, cache key or diff taken over the definition.
//
// Each case fills one map-valued field with six keys, so a single repeat
// reorders with probability 1-1/6!, and the loop runs 50 of them.
func TestMarshalWorkflowIsDeterministic(t *testing.T) {
	const repeats = 50

	keys := func(prefix string) []string {
		return []string{prefix + "_e", prefix + "_a", prefix + "_d", prefix + "_b", prefix + "_c", prefix + "_f"}
	}

	tests := []struct {
		name string
		mut  func(wf *reliantv1.Workflow)
	}{
		{
			name: "outputs",
			mut: func(wf *reliantv1.Workflow) {
				wf.Outputs = map[string]string{}
				for _, k := range keys("out") {
					wf.Outputs[k] = "value-" + k
				}
			},
		},
		{
			name: "inputs",
			mut: func(wf *reliantv1.Workflow) {
				wf.Inputs = map[string]*reliantv1.Input{}
				for _, k := range keys("in") {
					wf.Inputs[k] = &reliantv1.Input{
						Type: "string",
						Config: &reliantv1.Input_StringInput{
							StringInput: &reliantv1.StringInputConfig{
								Base: &reliantv1.InputBase{Description: "desc-" + k},
							},
						},
					}
				}
			},
		},
		{
			name: "ui positions",
			mut: func(wf *reliantv1.Workflow) {
				wf.Ui = &reliantv1.WorkflowUI{Positions: map[string]*reliantv1.Position{}}
				for i, k := range keys("node") {
					wf.Ui.Positions[k] = &reliantv1.Position{X: float64(i), Y: float64(i * 2)}
				}
			},
		},
		{
			name: "ui switches",
			mut: func(wf *reliantv1.Workflow) {
				wf.Ui = &reliantv1.WorkflowUI{Switches: map[string]*reliantv1.SwitchMetadata{}}
				for _, k := range keys("sw") {
					wf.Ui.Switches[k] = &reliantv1.SwitchMetadata{SourceNode: "src-" + k}
				}
			},
		},
		{
			name: "daemon selector labels",
			mut: func(wf *reliantv1.Workflow) {
				labels := map[string]string{}
				for _, k := range keys("label") {
					labels[k] = "v-" + k
				}
				wf.Daemon = &reliantv1.CelDaemonSelector{
					Value: &reliantv1.CelDaemonSelector_Literal{
						Literal: &reliantv1.DaemonSelectorProto{Name: "d", Labels: labels},
					},
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wf := &reliantv1.Workflow{Name: "det-probe"}
			tc.mut(wf)

			first, err := MarshalWorkflow(wf)
			if err != nil {
				t.Fatalf("MarshalWorkflow: %v", err)
			}
			for i := 1; i < repeats; i++ {
				got, err := MarshalWorkflow(wf)
				if err != nil {
					t.Fatalf("MarshalWorkflow (repeat %d): %v", i, err)
				}
				if string(got) != string(first) {
					t.Fatalf("MarshalWorkflow is not deterministic — repeat %d differs.\nfirst:\n%s\ngot:\n%s", i, first, got)
				}
			}
		})
	}
}

// The builtin workflows are what production actually re-marshals, so they are
// the regression this protects, not just a synthetic map. Read from disk
// rather than importing the builtin package, which would make the parser
// depend on its own consumer.
func TestMarshalBuiltinWorkflowsAreDeterministic(t *testing.T) {
	for _, name := range []string{"agent.yaml", "gsd.yaml", "build-workflow.yaml"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "builtin", name))
			if err != nil {
				t.Skipf("builtin %s unavailable: %v", name, err)
			}
			wf, err := ParseWorkflow(raw)
			if err != nil {
				t.Fatalf("ParseWorkflow(%s): %v", name, err)
			}
			first, err := MarshalWorkflow(wf)
			if err != nil {
				t.Fatalf("MarshalWorkflow(%s): %v", name, err)
			}
			for i := 1; i < 50; i++ {
				got, err := MarshalWorkflow(wf)
				if err != nil {
					t.Fatalf("MarshalWorkflow(%s) repeat %d: %v", name, i, err)
				}
				if string(got) != string(first) {
					t.Fatalf("MarshalWorkflow(%s) is not deterministic at repeat %d", name, i)
				}
			}
		})
	}
}

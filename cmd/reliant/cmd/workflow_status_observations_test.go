// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// `reliant workflow status` prints what each node recorded with observe:
// under its node thread: a decision as one line, the attributes and other
// events as key=value, and an expression that failed as such. A graph of
// compute nodes records no step row and still shows its decisions.
func TestWorkflowStatusPrintsObservationsUnderTheirNode(t *testing.T) {
	attrs := func(m map[string]any) *structpb.Struct {
		s, err := structpb.NewStruct(m)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	groups := summarizeSteps(&reliantv1.WorkflowExecution{
		Id: "root", WorkflowName: "rc-train",
		Observations: []*reliantv1.NodeObservation{{
			NodeId: "plan", NodePath: "plan", Attempt: 1,
			Attributes: attrs(map[string]any{"trigger": "schedule", "behind": 3}),
			Events: []*reliantv1.ObservedEvent{
				{Name: "decision", Attributes: attrs(map[string]any{"name": "cut", "value": "no_cut", "reason": "main_unchanged"})},
				{Name: "tick", Attributes: attrs(map[string]any{"lane": "fast"})},
			},
			Errors: []string{"events[2].attributes.x: no such key: x"},
		}},
	})

	var out bytes.Buffer
	printStatusReport(&out, statusReport{ExecutionID: "e1", Workflow: "rc-train", Status: "completed", Steps: groups})
	text := out.String()
	for _, want := range []string{
		"(root)",
		"OBSERVED",
		"plan                     behind=3 trigger=schedule",
		"plan                     decision cut: no_cut (main_unchanged)",
		"plan                     tick lane=fast",
		"plan                     observe error: events[2].attributes.x: no such key: x",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("status report lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "ACTIVITY") {
		t.Fatalf("a group with no steps prints no step table:\n%s", text)
	}

	raw, err := json.Marshal(statusReport{ExecutionID: "e1", Steps: groups})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"observations":[{"node":"plan","attempt":1,"attributes":{"behind":3,"trigger":"schedule"}`) {
		t.Fatalf("--json carries the observations: %s", raw)
	}
}

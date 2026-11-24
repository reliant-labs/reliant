// Copyright (c) 2026 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
)

func mustStruct(t *testing.T, m map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(m)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// What a node recorded with observe: is reported under the node thread whose
// workflow it ran in, like that thread's steps. A thread that recorded
// observations and no step row (a graph of compute nodes) is still listed.
func TestSummarizeStepsGroupsObservationsByNodeThread(t *testing.T) {
	root := &reliantv1.WorkflowExecution{
		Id: "root", WorkflowName: "rc-train",
		Observations: []*reliantv1.NodeObservation{{
			NodeId: "plan", NodePath: "plan", Attempt: 1,
			Attributes: mustStruct(t, map[string]any{"trigger": "schedule"}),
			Events: []*reliantv1.ObservedEvent{
				{Name: "decision", Attributes: mustStruct(t, map[string]any{"name": "cut", "value": "no_cut", "reason": "main_unchanged"})},
				{Name: "tick"},
			},
			Errors: []string{"events[2].attributes.x: no such key"},
		}},
		Children: []*reliantv1.WorkflowExecution{{
			Id: "wf-review", WorkflowName: "review", SpawnedByNodeId: sp("review"),
			Steps: []*reliantv1.StepExecution{{StepId: "lint", ActivityName: "ExecuteRunStep", Success: boolPtr(true)}},
			Observations: []*reliantv1.NodeObservation{{
				NodeId: "verdict", LoopIteration: int32Ptr(1), Attempt: 1,
				Events: []*reliantv1.ObservedEvent{{Name: "decision", Attributes: mustStruct(t, map[string]any{"value": "pass"})}},
			}},
		}},
	}

	groups := SummarizeSteps(root)
	byNode := map[string]StatusStepGroup{}
	for _, g := range groups {
		byNode[g.Node] = g
	}

	top, ok := byNode[""]
	if !ok {
		t.Fatalf("the root graph recorded only observations and is still listed: %+v", groups)
	}
	if len(top.Observations) != 1 {
		t.Fatalf("root observations = %+v", top.Observations)
	}
	plan := top.Observations[0]
	if plan.NodeID != "plan" || !reflect.DeepEqual(plan.Attributes, map[string]any{"trigger": "schedule"}) {
		t.Fatalf("plan = %+v", plan)
	}
	if got := plan.Events[0].Decision(); got != "cut: no_cut (main_unchanged)" {
		t.Fatalf("decision = %q", got)
	}
	if got := plan.Events[1].Decision(); got != "" || plan.Events[1].Attributes != nil {
		t.Fatalf("a non-decision event = %q, %v", got, plan.Events[1].Attributes)
	}
	if len(plan.Errors) != 1 {
		t.Fatalf("errors = %v", plan.Errors)
	}

	review := byNode["review"]
	if len(review.Steps) != 1 || len(review.Observations) != 1 {
		t.Fatalf("review = %+v", review)
	}
	verdict := review.Observations[0]
	if verdict.LoopIteration == nil || *verdict.LoopIteration != 1 || verdict.Attributes != nil {
		t.Fatalf("verdict = %+v", verdict)
	}
	if got := verdict.Events[0].Decision(); got != "pass" {
		t.Fatalf("an unnamed decision = %q", got)
	}
}

// A server older than the field sends none: groups carry no observations.
func TestSummarizeStepsWithoutObservations(t *testing.T) {
	for _, g := range SummarizeSteps(twoPhaseRun()) {
		if g.Observations != nil {
			t.Fatalf("group %q has observations %+v", g.Node, g.Observations)
		}
	}
}

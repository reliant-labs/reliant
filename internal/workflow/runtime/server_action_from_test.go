// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

func serverActionNode(uses, from string) *reliantv1.Node {
	with := map[string]*structpb.Value{}
	if from != "" {
		with["from"] = structpb.NewStringValue(from)
	}
	return &reliantv1.Node{Id: "verdict", Type: model.NodeTypeAction, Args: &reliantv1.Node_Action{Action: &reliantv1.ActionArgs{
		Uses: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: uses}}, With: with,
	}}}
}

// A server action's `from` gets the named node as the engine holds it: the
// definition's role and review shape, and the recorded output. The author
// picks the node, never what it is or what it produced.
func TestServerActionFromIsTheEnginesRecord(t *testing.T) {
	review, err := model.LowerReviewNode(&reliantv1.Node{Id: "review", Type: model.NodeTypeReview, Role: model.RoleReviewer,
		Args: &reliantv1.Node_Review{Review: &reliantv1.ReviewArgs{Prompt: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "look"}}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := &reliantv1.Node{Id: "check", Type: model.NodeTypeRun}
	e := &StepExecutor{
		workflow:    &reliantv1.Workflow{Nodes: []*reliantv1.Node{review, check}},
		nodeOutputs: map[string]interface{}{"review": map[string]interface{}{"verdict": "pass", "session_id": "s1", "role": "reviewer"}},
	}

	got := e.serverActionFrom(serverActionNode("reliant/pr.verdict@1", "review"))
	if got == nil || !got.Review || got.Role != model.RoleReviewer || !got.Ran || got.Output["session_id"] != "s1" ||
		got.Type != model.NodeTypeWorkflow {
		t.Fatalf("review node: %+v", got)
	}
	if got := e.serverActionFrom(serverActionNode("reliant/pr.checked@1", "check")); got == nil || got.Review || got.Ran || got.Type != model.NodeTypeRun {
		t.Fatalf("a run node that has not run: %+v", got)
	}
	for _, n := range []*reliantv1.Node{
		serverActionNode("reliant/pr.verdict@1", "missing"),
		serverActionNode("reliant/pr.verdict@1", ""),
		serverActionNode("github/issue.comment@1", "review"),
	} {
		if got := e.serverActionFrom(n); got != nil {
			t.Errorf("%v: want none, got %+v", n.GetAction().GetWith(), got)
		}
	}
}

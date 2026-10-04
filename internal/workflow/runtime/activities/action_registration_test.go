// Copyright (c) 2025 Reliant Labs
package activities

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"google.golang.org/protobuf/types/known/structpb"
)

func actionNode(uses string) *reliantv1.Node {
	return &reliantv1.Node{Id: "n", Type: model.NodeTypeAction, Args: &reliantv1.Node_Action{Action: &reliantv1.ActionArgs{
		Uses: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: uses}},
		With: map[string]*structpb.Value{"url": structpb.NewStringValue("https://example.com")},
	}}}
}

func TestActionIsARegisteredNodeType(t *testing.T) {
	if !model.IsKnownNodeType(model.NodeTypeAction) || !model.IsActivityNode(model.NodeTypeAction) {
		t.Fatal("action must be a known activity node type")
	}
	meta, ok := schema.GetActivityMetadata("Action")
	if !ok || meta.DisplayName == "" {
		t.Fatal("Action has no palette metadata, so the builder cannot list it")
	}
	found := false
	for _, a := range schema.ListVisibleActivities() {
		if a.DisplayName == meta.DisplayName {
			found = true
		}
	}
	if !found {
		t.Fatal("Action is not in the visible palette (ListNodes source)")
	}
	if def, ok := nodeTypeActivities[model.NodeTypeAction]; !ok || def.activityName != "Action" {
		t.Fatalf("nodeTypeActivities entry wrong: %+v", def)
	}
}

func TestPreflightHTTPActionNeedsNoDaemon(t *testing.T) {
	cfg := newPreflightConfig()
	wf := &reliantv1.Workflow{Nodes: []*reliantv1.Node{actionNode("http/request@1")}}
	if v2.RequiresDaemon(wf, cfg) {
		t.Fatal("a workflow using only the http action must not require a daemon")
	}
	// Still daemon-bound things still count alongside it.
	wf.Nodes = append(wf.Nodes, &reliantv1.Node{Id: "r", Type: model.NodeTypeRun})
	if !v2.RequiresDaemon(wf, cfg) {
		t.Fatal("a run node alongside must still require a daemon")
	}
	// An action we cannot resolve statically is conservative.
	unknown := &reliantv1.Workflow{Nodes: []*reliantv1.Node{actionNode("nope/x@1")}}
	if !v2.RequiresDaemon(unknown, cfg) {
		t.Fatal("an unresolvable action must be treated as possibly needing a daemon")
	}
}

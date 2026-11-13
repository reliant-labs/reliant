// Copyright (c) 2026 Reliant Labs
package model

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"google.golang.org/protobuf/proto"
)

func TestWorkflowAttentionAdditiveWireContract(t *testing.T) {
	legacy := &reliantv1.Workflow{Name: "observer"}
	wire, err := proto.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	got := &reliantv1.Workflow{}
	if err := proto.Unmarshal(wire, got); err != nil {
		t.Fatal(err)
	}
	if got.GetAttention() != nil {
		t.Fatalf("legacy workflow acquired attention: %v", got.GetAttention())
	}

	configured := &reliantv1.Workflow{Name: "repair", Attention: &reliantv1.WorkflowAttention{
		Key: "repo:{{ inputs.repo }}:pr:{{ inputs.pr }}", OnStart: "recovering",
		RecoveryTimeout: "30m", OnSuccess: "resolved", OnFailure: "action_required",
	}}
	wire, err = proto.Marshal(configured)
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(wire, got); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(configured, got) {
		t.Fatalf("workflow attention lost fields: %v", got)
	}
	field := configured.ProtoReflect().Descriptor().Fields().ByNumber(23)
	if field == nil || field.Name() != "attention" || field.Message().Name() != "WorkflowAttention" {
		t.Fatal("Workflow.attention must retain field 23 and WorkflowAttention message")
	}
}

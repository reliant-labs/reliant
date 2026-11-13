// Copyright (c) 2026 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestInboxAttentionAdditiveWireContract(t *testing.T) {
	// An old producer has none of the additive fields. Their defaults must
	// remain empty rather than inventing an incident or changing its health.
	legacy := &reliantv1.InboxAutomationFailing{}
	wire, err := proto.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	got := &reliantv1.InboxAutomationFailing{}
	if err := proto.Unmarshal(wire, got); err != nil {
		t.Fatal(err)
	}
	if got.GetIncidentKey() != "" || got.GetAttentionState() != "" || got.GetFailureClass() != "" || got.GetDetail() != "" {
		t.Fatalf("legacy payload acquired attention: %v", got)
	}

	incident := &reliantv1.InboxAutomationFailing{
		IncidentKey: "repo:app:pr:42", AttentionState: "action_required",
		FailureClass: "recovery_expired", Detail: "Recovery timed out",
	}
	wire, err = proto.Marshal(incident)
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(wire, got); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(incident, got) {
		t.Fatalf("attention payload lost fields: %v", got)
	}

	fields := incident.ProtoReflect().Descriptor().Fields()
	for name, number := range map[string]int{"incident_key": 3, "attention_state": 4, "failure_class": 5, "detail": 6} {
		field := fields.ByNumber(protoreflect.FieldNumber(number))
		if field == nil || string(field.Name()) != name {
			t.Fatalf("field %s must retain wire number %d", name, number)
		}
	}
}

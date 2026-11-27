// Copyright (c) 2025 Reliant Labs
package buildmode_test

import (
	"testing"

	billingv1 "github.com/reliant-labs/reliant/gen/controlplane/services/billing/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Pin the vendored money fields, not just the fact that the generated client
// compiles: an old generated contract must not silently drop these amounts.
func TestBillingUsageCreditWireContract(t *testing.T) {
	t.Parallel()

	response := &billingv1.GetCurrentUserUsageBillingResponse{
		Enabled:             true,
		MinimumMonthlyCents: 500,
		IncludedUsageCents:  500,
		LaunchCredit: &billingv1.HostingLaunchCredit{
			OfferCents:     1000,
			Granted:        true,
			GrantedCents:   900,
			RemainingCents: 650,
			Applied:        true,
			AppliedCents:   250,
		},
	}

	for _, tc := range []struct {
		name  string
		msg   proto.Message
		field protoreflect.Name
		tag   protoreflect.FieldNumber
		json  string
		kind  protoreflect.Kind
	}{
		{"included usage", response, "included_usage_cents", 7, "includedUsageCents", protoreflect.Int64Kind},
		{"launch credit", response, "launch_credit", 8, "launchCredit", protoreflect.MessageKind},
		{"offer", response.LaunchCredit, "offer_cents", 1, "offerCents", protoreflect.Int64Kind},
		{"granted", response.LaunchCredit, "granted_cents", 3, "grantedCents", protoreflect.Int64Kind},
		{"remaining", response.LaunchCredit, "remaining_cents", 4, "remainingCents", protoreflect.Int64Kind},
		{"applied", response.LaunchCredit, "applied_cents", 8, "appliedCents", protoreflect.Int64Kind},
	} {
		t.Run(tc.name, func(t *testing.T) {
			field := tc.msg.ProtoReflect().Descriptor().Fields().ByName(tc.field)
			if field == nil {
				t.Fatalf("missing field %s", tc.field)
			}
			if field.Number() != tc.tag || field.JSONName() != tc.json || field.Kind() != tc.kind {
				t.Fatalf("%s: got tag=%d JSON=%s kind=%s; want tag=%d JSON=%s kind=%s",
					tc.field, field.Number(), field.JSONName(), field.Kind(), tc.tag, tc.json, tc.kind)
			}
		})
	}
	creditField := response.ProtoReflect().Descriptor().Fields().ByName("launch_credit")
	if creditField == nil || creditField.Message() == nil || creditField.Message().FullName() != "controlplane.v1.HostingLaunchCredit" {
		t.Fatal("launch_credit must reference controlplane.v1.HostingLaunchCredit")
	}

	for _, tc := range []struct {
		name      string
		marshal   func(proto.Message) ([]byte, error)
		unmarshal func([]byte, proto.Message) error
	}{
		{"protobuf", proto.Marshal, proto.Unmarshal},
		{"JSON", protojson.Marshal, protojson.Unmarshal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := tc.marshal(response)
			if err != nil {
				t.Fatalf("marshal billing response: %v", err)
			}
			var decoded billingv1.GetCurrentUserUsageBillingResponse
			if err := tc.unmarshal(wire, &decoded); err != nil {
				t.Fatalf("unmarshal billing response: %v", err)
			}
			if !proto.Equal(response, &decoded) {
				t.Fatalf("billing money fields lost in round trip: got %v, want %v", &decoded, response)
			}
		})
	}
}

func TestBillingUsageCreditOldServerResponse(t *testing.T) {
	t.Parallel()

	want := &billingv1.GetCurrentUserUsageBillingResponse{
		Enabled:             true,
		MinimumMonthlyCents: 500,
		PlanDisplayName:     "Hobby",
	}
	for _, tc := range []struct {
		name      string
		wire      []byte
		unmarshal func([]byte, proto.Message) error
	}{
		// Frozen pre-refresh wire response: enabled (1), minimum (5), name (6).
		// In particular, tags 7 and 8 are absent, not explicitly set to zero.
		{"protobuf", []byte{0x08, 0x01, 0x28, 0xf4, 0x03, 0x32, 0x05, 'H', 'o', 'b', 'b', 'y'}, proto.Unmarshal},
		{"JSON", []byte(`{"enabled":true,"minimumMonthlyCents":"500","planDisplayName":"Hobby"}`), protojson.Unmarshal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var decoded billingv1.GetCurrentUserUsageBillingResponse
			if err := tc.unmarshal(tc.wire, &decoded); err != nil {
				t.Fatalf("decode old-server response: %v", err)
			}
			if !proto.Equal(want, &decoded) {
				t.Fatalf("old-server response changed: got %v, want %v", &decoded, want)
			}
			if decoded.GetIncludedUsageCents() != 0 || decoded.GetLaunchCredit() != nil {
				t.Fatal("absent new fields must default to zero included usage and no launch credit")
			}
		})
	}
}

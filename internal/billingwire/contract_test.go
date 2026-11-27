// Copyright (c) 2026 Reliant Labs

package billingwire_test

import (
	"testing"

	billing "github.com/reliant-labs/reliant/gen/controlplane/services/billing/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestHostingBillingWireContract(t *testing.T) {
	for _, state := range []struct {
		name    string
		granted bool
		expired bool
		refused string
	}{
		{name: "granted", granted: true},
		{name: "expired", granted: true, expired: true},
		{name: "refused", refused: "card_already_credited"},
	} {
		t.Run(state.name, func(t *testing.T) {
			want := &billing.GetCurrentUserUsageBillingResponse{
				Enabled:             true,
				PolicyVersion:       "hosting-v1",
				Agreement:           &billing.UsageBillingAgreement{},
				Rates:               []*billing.HostedUsageRate{{ResourceKind: "egress", DisplayName: "Transfer", Unit: "GiB", PriceCents: "12.5", IncludedMonthly: "10"}},
				MinimumMonthlyCents: 500,
				PlanDisplayName:     "Hobby",
				IncludedUsageCents:  500,
				LaunchCredit: &billing.HostingLaunchCredit{
					OfferCents: 2500, Granted: state.granted, GrantedCents: 2500,
					RemainingCents: 1701, Applied: state.granted, AppliedCents: 799,
					GrantedAt: &timestamppb.Timestamp{Seconds: 1800000000, Nanos: 123000000},
					ExpiresAt: &timestamppb.Timestamp{Seconds: 1802592000, Nanos: 456000000},
					Expired:   state.expired, RefusedReason: state.refused,
				},
			}
			wire, err := proto.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			got := new(billing.GetCurrentUserUsageBillingResponse)
			if err := proto.Unmarshal(wire, got); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(want, got) {
				t.Fatalf("binary round trip lost billing fields: got %v, want %v", got, want)
			}
			jsonWire, err := protojson.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			jsonGot := new(billing.GetCurrentUserUsageBillingResponse)
			if err := protojson.Unmarshal(jsonWire, jsonGot); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(want, jsonGot) {
				t.Fatalf("JSON round trip lost billing fields: got %v, want %v", jsonGot, want)
			}
		})
	}
}

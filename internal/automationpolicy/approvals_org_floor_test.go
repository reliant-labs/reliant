// Copyright (c) 2025 Reliant Labs
package automationpolicy

import (
	"context"
	"testing"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// floorSource answers one approvals section for every org.
type floorSource struct {
	approvals *controlplanev1.ApprovalsPolicy
}

func (s floorSource) ResolveOrgPolicy(context.Context, string) (accesstokenclient.OrgPolicy, error) {
	return accesstokenclient.OrgPolicy{Version: 5, Policy: &controlplanev1.OrgPolicy{Approvals: s.approvals}}, nil
}

func floorCtx(mode controlplanev1.OrgPolicyMode, floor controlplanev1.AutomationApprovalFloor) context.Context {
	return orgpolicy.WithResolver(context.Background(), orgpolicy.New(floorSource{&controlplanev1.ApprovalsPolicy{Mode: mode, AutomationFloor: floor}}))
}

func enforceApprovals(t *testing.T, level orgpolicy.Enforcement) {
	t.Helper()
	prev := orgpolicy.EnforcementOf(orgpolicy.PointApprovals)
	orgpolicy.SetProcessEnforcement(orgpolicy.PointApprovals, level)
	t.Cleanup(func() { orgpolicy.SetProcessEnforcement(orgpolicy.PointApprovals, prev) })
}

// The org floor is one more input to the Stricter chain: it raises a lax
// setting to the floor and never lowers a stricter one.
func TestWithOrgFloorOnlyRaisesStrictness(t *testing.T) {
	enforceApprovals(t, orgpolicy.EnforcementEnforce)
	enforce := controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE
	always := controlplanev1.AutomationApprovalFloor_AUTOMATION_APPROVAL_FLOOR_ALWAYS
	onChange := controlplanev1.AutomationApprovalFloor_AUTOMATION_APPROVAL_FLOOR_ON_CAPABILITY_CHANGE
	none := controlplanev1.AutomationApprovalFloor_AUTOMATION_APPROVAL_FLOOR_UNSPECIFIED

	cases := []struct {
		member ApprovalMode
		floor  controlplanev1.AutomationApprovalFloor
		want   ApprovalMode
	}{
		{ApprovalNever, onChange, ApprovalOnCapabilityChange},
		{ApprovalNever, always, ApprovalAlways},
		{ApprovalOnCapabilityChange, always, ApprovalAlways},
		{ApprovalOnCapabilityChange, onChange, ApprovalOnCapabilityChange},
		// Never lowered.
		{ApprovalAlways, onChange, ApprovalAlways},
		{ApprovalAlways, none, ApprovalAlways},
		{ApprovalNever, none, ApprovalNever},
		{ApprovalOnCapabilityChange, none, ApprovalOnCapabilityChange},
	}
	for _, tc := range cases {
		got := WithOrgFloor(floorCtx(enforce, tc.floor), tc.member, "org-a")
		if got != tc.want {
			t.Errorf("member %s, floor %v: got %s, want %s", tc.member, tc.floor, got, tc.want)
		}
		if got.strictness() < tc.member.strictness() {
			t.Errorf("member %s, floor %v: the floor LOWERED strictness to %s", tc.member, tc.floor, got)
		}
	}
}

// In audit (the org's section, or the platform switch) the floor changes
// nothing; with no org there is nothing to floor.
func TestWithOrgFloorAuditAndNoOrgChangeNothing(t *testing.T) {
	always := controlplanev1.AutomationApprovalFloor_AUTOMATION_APPROVAL_FLOOR_ALWAYS
	enforceApprovals(t, orgpolicy.EnforcementEnforce)
	if got := WithOrgFloor(floorCtx(controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_AUDIT, always), ApprovalNever, "org-a"); got != ApprovalNever {
		t.Errorf("section in audit: got %s, want never", got)
	}
	if got := WithOrgFloor(floorCtx(controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, always), ApprovalNever); got != ApprovalNever {
		t.Errorf("no org: got %s, want never", got)
	}
	orgpolicy.SetProcessEnforcement(orgpolicy.PointApprovals, orgpolicy.EnforcementAudit)
	if got := WithOrgFloor(floorCtx(controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, always), ApprovalNever, "org-a"); got != ApprovalNever {
		t.Errorf("platform switch in audit: got %s, want never", got)
	}
}

func TestOrgFloorMode(t *testing.T) {
	if OrgFloorMode(controlplanev1.AutomationApprovalFloor_AUTOMATION_APPROVAL_FLOOR_UNSPECIFIED) != "" {
		t.Error("no floor must be \"\", never a mode Stricter would read as always")
	}
}

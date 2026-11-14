// Copyright (c) 2025 Reliant Labs

package orgpolicy

import (
	"context"
	"errors"
	"testing"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
)

// withRuleSwitch sets point's platform switch for one test.
func withRuleSwitch(t *testing.T, point Point, e Enforcement) {
	t.Helper()
	prev := EnforcementOf(point)
	SetProcessEnforcement(point, e)
	t.Cleanup(func() { SetProcessEnforcement(point, prev) })
}

// ruleCtx is a context whose resolver answers doc for every org.
func ruleCtx(doc *controlplanev1.OrgPolicy) context.Context {
	r, _ := newResolver(&scripted{replies: []reply{{version: 3, doc: doc}}})
	return WithResolver(context.Background(), r)
}

// ruleCtxDown is a context whose resolver has never had an answer.
func ruleCtxDown() context.Context {
	r, _ := newResolver(&scripted{replies: []reply{{err: errors.New("control-plane down")}}})
	return WithResolver(context.Background(), r)
}

func machinesDoc(mode controlplanev1.OrgPolicyMode, force bool) *controlplanev1.OrgPolicy {
	return &controlplanev1.OrgPolicy{Machines: &controlplanev1.MachinesPolicy{Mode: mode, ForceCredentialsNone: force}}
}

const (
	modeEnforce = controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE
	modeAudit   = controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_AUDIT
	modeUnset   = controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_UNSPECIFIED
)

// An unset switch is audit: nothing a deployment did not turn on changes
// anything.
func TestRules_UnsetSwitchIsAudit(t *testing.T) {
	if got := EnforcementOf(PointMachines); got != EnforcementAudit {
		t.Fatalf("an unset switch is %v, want audit", got)
	}
	c := ForceCredentialsNone(ruleCtx(machinesDoc(modeEnforce, true)), "org-a")
	if !c.Applies || c.Enforced {
		t.Fatalf("change = %+v, want an audited one", c)
	}
}

// A rule changes anything only when both the platform switch and the org's
// section enforce it.
func TestRules_ChangeNeedsSwitchAndSectionToEnforce(t *testing.T) {
	cases := []struct {
		name         string
		sw           Enforcement
		mode         controlplanev1.OrgPolicyMode
		force        bool
		wantApplies  bool
		wantEnforced bool
	}{
		{"both enforce", EnforcementEnforce, modeEnforce, true, true, true},
		{"section audit", EnforcementEnforce, modeAudit, true, true, false},
		{"section unspecified reads as audit", EnforcementEnforce, modeUnset, true, true, false},
		{"switch audit", EnforcementAudit, modeEnforce, true, true, false},
		{"switch off", EnforcementOff, modeEnforce, true, false, false},
		{"rule not set", EnforcementEnforce, modeEnforce, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withRuleSwitch(t, PointMachines, tc.sw)
			c := ForceCredentialsNone(ruleCtx(machinesDoc(tc.mode, tc.force)), "org-a")
			if c.Applies != tc.wantApplies || c.Enforced != tc.wantEnforced {
				t.Fatalf("change = %+v, want applies=%v enforced=%v", c, tc.wantApplies, tc.wantEnforced)
			}
			if c.Applies && c.Version != 3 {
				t.Fatalf("change carries version %d, want the policy's 3", c.Version)
			}
			c.Record(context.Background()) // counts and logs; never panics
		})
	}
}

// Work outside any org, and an org with no policy, are never affected.
func TestRules_NoOrgAndNoPolicyChangeNothing(t *testing.T) {
	withRuleSwitch(t, PointMachines, EnforcementEnforce)
	withRuleSwitch(t, PointApprovals, EnforcementEnforce)
	ctx := ruleCtx(&controlplanev1.OrgPolicy{})
	if c := ForceCredentialsNone(ctx, ""); c.Applies {
		t.Errorf("work outside an org: %+v", c)
	}
	if c := ForceCredentialsNone(ctx, "org-a"); c.Applies {
		t.Errorf("an org with no policy: %+v", c)
	}
	if c := ChatNoAuto(ctx, "org-a"); c.Applies {
		t.Errorf("an org with no policy: %+v", c)
	}
	if floor, c := AutomationApprovalFloor(ctx, "org-a"); c.Applies || floor != controlplanev1.AutomationApprovalFloor_AUTOMATION_APPROVAL_FLOOR_UNSPECIFIED {
		t.Errorf("an org with no policy: floor %v, %+v", floor, c)
	}
}

// A policy that cannot be read makes the stricter choice under enforce,
// and only counts under audit.
func TestRules_UnavailablePolicyIsStricterOnlyUnderEnforce(t *testing.T) {
	withRuleSwitch(t, PointApprovals, EnforcementEnforce)
	floor, c := AutomationApprovalFloor(ruleCtxDown(), "org-a")
	if !c.Unavailable || !c.Enforced || floor != controlplanev1.AutomationApprovalFloor_AUTOMATION_APPROVAL_FLOOR_ALWAYS {
		t.Fatalf("unavailable under enforce: floor %v, %+v; want ALWAYS, enforced", floor, c)
	}
	if c := ChatNoAuto(ruleCtxDown(), "org-a"); !c.Enforced {
		t.Fatalf("unavailable under enforce: %+v; want the mode coerced", c)
	}

	SetProcessEnforcement(PointApprovals, EnforcementAudit)
	if c := ChatNoAuto(ruleCtxDown(), "org-a"); !c.Applies || c.Enforced {
		t.Fatalf("unavailable under audit: %+v; want audited", c)
	}
}

func TestRules_ApprovalRulesReadTheirSection(t *testing.T) {
	withRuleSwitch(t, PointApprovals, EnforcementEnforce)
	ctx := ruleCtx(&controlplanev1.OrgPolicy{Approvals: &controlplanev1.ApprovalsPolicy{
		Mode:            modeEnforce,
		AutomationFloor: controlplanev1.AutomationApprovalFloor_AUTOMATION_APPROVAL_FLOOR_ON_CAPABILITY_CHANGE,
		ChatModeFloor:   controlplanev1.ChatModeFloor_CHAT_MODE_FLOOR_NO_AUTO,
	}})
	floor, c := AutomationApprovalFloor(ctx, "org-a")
	if !c.Enforced || floor != controlplanev1.AutomationApprovalFloor_AUTOMATION_APPROVAL_FLOOR_ON_CAPABILITY_CHANGE {
		t.Fatalf("automation floor = %v, %+v", floor, c)
	}
	if c := ChatNoAuto(ctx, "org-a"); !c.Enforced {
		t.Fatalf("chat mode floor NO_AUTO: %+v", c)
	}
	anyMode := ruleCtx(&controlplanev1.OrgPolicy{Approvals: &controlplanev1.ApprovalsPolicy{
		Mode: modeEnforce, ChatModeFloor: controlplanev1.ChatModeFloor_CHAT_MODE_FLOOR_ANY,
	}})
	if c := ChatNoAuto(anyMode, "org-a"); c.Applies {
		t.Fatalf("chat mode floor ANY: %+v", c)
	}
}

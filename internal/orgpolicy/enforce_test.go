// Copyright (c) 2025 Reliant Labs

package orgpolicy

import (
	"context"
	"errors"
	"testing"
	"time"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
)

// policyTable answers each org's policy from a fixed table; an org absent from it
// has no policy. down makes every call fail, as an outage does.
type policyTable struct {
	docs map[string]*controlplanev1.OrgPolicy
	down bool
}

func (b policyTable) ResolveOrgPolicy(_ context.Context, orgID string) (accesstokenclient.OrgPolicy, error) {
	if b.down {
		return accesstokenclient.OrgPolicy{}, errors.New("control plane unreachable")
	}
	doc, ok := b.docs[orgID]
	if !ok {
		return accesstokenclient.OrgPolicy{}, nil
	}
	return accesstokenclient.OrgPolicy{Version: 7, Policy: doc}, nil
}

// withSwitch sets the platform switch for point for one test.
func withSwitch(t *testing.T, point Point, e Enforcement) {
	t.Helper()
	prev := EnforcementOf(point)
	SetProcessEnforcement(point, e)
	t.Cleanup(func() { SetProcessEnforcement(point, prev) })
}

func integrationsOnly(mode controlplanev1.OrgPolicyMode, allowed ...string) *controlplanev1.OrgPolicy {
	return &controlplanev1.OrgPolicy{Integrations: &controlplanev1.IntegrationsPolicy{Mode: mode, Allowed: allowed}}
}

func TestParseEnforcement(t *testing.T) {
	for in, want := range map[string]Enforcement{
		"off": EnforcementOff, "enforce": EnforcementEnforce, " ENFORCE ": EnforcementEnforce,
		"audit": EnforcementAudit, "": EnforcementAudit, "nonsense": EnforcementAudit,
	} {
		if got := ParseEnforcement(in); got != want {
			t.Errorf("ParseEnforcement(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnforcementOf_DefaultsToAudit(t *testing.T) {
	if got := EnforcementOf(Point("never-set")); got != EnforcementAudit {
		t.Fatalf("an unset point is %q, want audit: a new point ships in audit", got)
	}
}

// A use is refused only when the platform switch AND the section's own mode
// both say enforce; audit anywhere counts and lets it through; off never asks.
func TestJudgeIntegration_SwitchTimesSectionMode(t *testing.T) {
	enforce, audit := controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_AUDIT
	unspecified := controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_UNSPECIFIED
	for name, tc := range map[string]struct {
		sw          Enforcement
		mode        controlplanev1.OrgPolicyMode
		subject     string
		denied, ref bool
	}{
		"enforce x ENFORCE, disallowed: refused":        {EnforcementEnforce, enforce, "slack", true, true},
		"enforce x ENFORCE, allowed: admitted":          {EnforcementEnforce, enforce, "github", false, false},
		"enforce x AUDIT: counted, not refused":         {EnforcementEnforce, audit, "slack", true, false},
		"enforce x UNSPECIFIED reads as AUDIT":          {EnforcementEnforce, unspecified, "slack", true, false},
		"audit x ENFORCE: counted, never refused":       {EnforcementAudit, enforce, "slack", true, false},
		"off x ENFORCE: never consulted, never counted": {EnforcementOff, enforce, "slack", false, false},
	} {
		t.Run(name, func(t *testing.T) {
			withSwitch(t, PointIntegrations, tc.sw)
			ctx := WithResolver(context.Background(), New(policyTable{docs: map[string]*controlplanev1.OrgPolicy{
				"org-a": integrationsOnly(tc.mode, "github"),
			}}))
			d, err := JudgeIntegration(ctx, "org-a", tc.subject, false)
			if err != nil {
				t.Fatal(err)
			}
			if d.Denied != tc.denied || d.Refused != tc.ref {
				t.Fatalf("decision = %+v, want denied=%v refused=%v", d, tc.denied, tc.ref)
			}
			if tc.sw != EnforcementOff && d.Version != 7 {
				t.Fatalf("the decision must name the policy version it used, got %d", d.Version)
			}
			if d.Denied && d.Reason != ReasonIntegrationNotAllowed {
				t.Fatalf("reason = %q", d.Reason)
			}
		})
	}
}

func TestJudgeIntegration_NoPolicyNoSectionNoOrgAdmitEverything(t *testing.T) {
	withSwitch(t, PointIntegrations, EnforcementEnforce)
	ctx := WithResolver(context.Background(), New(policyTable{docs: map[string]*controlplanev1.OrgPolicy{
		"org-models-only": {Models: &controlplanev1.ModelsPolicy{Mode: controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, Allowed: []string{"m"}}},
		"org-allow-all":   {Integrations: &controlplanev1.IntegrationsPolicy{Mode: controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, AllowAll: true}},
	}}))
	for _, org := range []string{"org-no-policy", "org-models-only", "org-allow-all", ""} {
		d, err := JudgeIntegration(ctx, org, "slack", false)
		if err != nil || d.Denied || d.Refused {
			t.Fatalf("org %q: decision %+v, err %v; want admitted", org, d, err)
		}
	}
	// A deployment with no policies at all (no resolver anywhere).
	d, err := JudgeIntegration(context.Background(), "org-a", "slack", false)
	if err != nil || d.Refused {
		t.Fatalf("no resolver: %+v, %v", d, err)
	}
}

// An enforced section with an empty list admits nothing.
func TestIntegrationAdmission_EmptyListAdmitsNone(t *testing.T) {
	adm := IntegrationAdmission(integrationsOnly(controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE), "github")
	if !adm.Governed || adm.Admitted {
		t.Fatalf("admission = %+v", adm)
	}
	if adm := IntegrationAdmission(&controlplanev1.OrgPolicy{}, "github"); adm.Governed {
		t.Fatalf("an empty document governs nothing: %+v", adm)
	}
}

// Past Grace the policy is unknown: an enforcing point refuses retryably
// (fail closed, D10), an auditing one lets the use through (audit refuses
// nothing).
func TestJudgeIntegration_UnreadablePolicy(t *testing.T) {
	r := New(policyTable{down: true})
	r.now = func() time.Time { return time.Unix(0, 0) }
	ctx := WithResolver(context.Background(), r)

	withSwitch(t, PointIntegrations, EnforcementEnforce)
	if _, err := JudgeIntegration(ctx, "org-a", "slack", false); !IsUnavailable(err) {
		t.Fatalf("enforce: err = %v, want ErrUnavailable", err)
	}
	SetProcessEnforcement(PointIntegrations, EnforcementAudit)
	if d, err := JudgeIntegration(ctx, "org-a", "slack", false); err != nil || d.Refused {
		t.Fatalf("audit: %+v, %v; want admitted", d, err)
	}
}

func TestEnforced(t *testing.T) {
	withSwitch(t, PointIntegrations, EnforcementAudit)
	if Enforced(PointIntegrations, controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE) {
		t.Fatal("the platform switch in audit enforces nothing")
	}
	SetProcessEnforcement(PointIntegrations, EnforcementEnforce)
	if !Enforced(PointIntegrations, controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE) ||
		Enforced(PointIntegrations, controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_AUDIT) {
		t.Fatal("enforce x mode")
	}
}

// The generic HTTP integration (http/request@1) is raw egress, outside the
// integrations policy (D14), whether or not a saved connection authenticates
// it.
func TestIntegrationAdmission_GenericHTTPIsNeverGoverned(t *testing.T) {
	withSwitch(t, PointIntegrations, EnforcementEnforce)
	doc := integrationsOnly(controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, "github")
	if adm := IntegrationAdmission(doc, GenericHTTPIntegrationID); adm.Governed {
		t.Fatalf("http is governed: %+v", adm)
	}
	ctx := WithResolver(context.Background(), New(policyTable{docs: map[string]*controlplanev1.OrgPolicy{"org-a": doc}}))
	if d, err := JudgeIntegration(ctx, "org-a", "http", false); err != nil || d.Denied {
		t.Fatalf("http: %+v, %v", d, err)
	}
}

// A use judged upstream (WithJudged) is decided alike below, and counted once.
func TestJudge_AUseJudgedUpstreamIsNotCountedAgain(t *testing.T) {
	withSwitch(t, PointIntegrations, EnforcementEnforce)
	ctx := WithResolver(context.Background(), New(policyTable{docs: map[string]*controlplanev1.OrgPolicy{
		"org-a": integrationsOnly(controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, "github"),
	}}))
	d, err := JudgeIntegration(ctx, "org-a", "slack", false)
	if err != nil || !d.Refused {
		t.Fatalf("first: %+v %v", d, err)
	}
	judged := WithJudged(ctx, d)
	if !judgedOn(judged, d) {
		t.Fatal("the use is not marked judged")
	}
	if judgedOn(judged, Decision{Point: d.Point, OrgID: d.OrgID, Subject: "github"}) {
		t.Fatal("another subject is not the same use")
	}
	again, err := JudgeIntegration(judged, "org-a", "slack", false)
	if err != nil || !again.Refused {
		t.Fatalf("below: %+v %v; a judged use is still refused", again, err)
	}
}

// Raw HTTP stays outside the policy even while the policy cannot be read
// under enforce: it is admitted without asking control-plane at all.
func TestJudgeIntegration_GenericHTTPDuringAPolicyOutage(t *testing.T) {
	withSwitch(t, PointIntegrations, EnforcementEnforce)
	src := &countingOutage{}
	ctx := WithResolver(context.Background(), New(src))
	d, err := JudgeIntegration(ctx, "org-a", GenericHTTPIntegrationID, false)
	if err != nil || d.Denied || d.Refused {
		t.Fatalf("http during an outage: %+v, %v", d, err)
	}
	if src.calls != 0 {
		t.Fatalf("the policy source was asked %d times for raw HTTP", src.calls)
	}
	if _, err := JudgeIntegration(ctx, "org-a", "github", false); !IsUnavailable(err) {
		t.Fatalf("a governed integration under the same outage: %v, want ErrUnavailable", err)
	}
}

// countingOutage is a control plane that cannot be asked, counting the asks.
type countingOutage struct{ calls int }

func (c *countingOutage) ResolveOrgPolicy(context.Context, string) (accesstokenclient.OrgPolicy, error) {
	c.calls++
	return accesstokenclient.OrgPolicy{}, errors.New("control plane unreachable")
}

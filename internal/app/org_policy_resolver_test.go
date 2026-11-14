package app

import (
	"context"
	"testing"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// hostedAuthority is control-plane's token authority: it answers org
// policies.
type hostedAuthority struct{ tokenauthority.Authority }

func (hostedAuthority) ResolveOrgPolicy(context.Context, string) (accesstokenclient.OrgPolicy, error) {
	return accesstokenclient.OrgPolicy{Version: 2}, nil
}

// A hosted deployment resolves org policies through control-plane and makes
// that resolver the process's too; a self-hosted one has none, and every org
// then has no policy.
func TestNewOrgPolicyResolver(t *testing.T) {
	t.Cleanup(func() { orgpolicy.SetProcessResolver(nil) })

	r, err := NewOrgPolicyResolver(OrgPolicyResolverDeps{Authority: hostedAuthority{}})
	if err != nil || r == nil {
		t.Fatalf("hosted: %v, %v", r, err)
	}
	if p, err := orgpolicy.For(context.Background(), "org-a"); err != nil || p.Version != 2 {
		t.Fatalf("the process resolver answers %+v, %v; want control-plane's v2", p, err)
	}

	r, err = NewOrgPolicyResolver(OrgPolicyResolverDeps{Authority: selfHostedAuthority{}})
	if err != nil || r != nil {
		t.Fatalf("self-hosted: %v, %v; want no resolver", r, err)
	}
	if p, err := orgpolicy.For(context.Background(), "org-a"); err != nil || !p.None() {
		t.Fatalf("self-hosted process answers %+v, %v; want no policy", p, err)
	}
}

// The platform switch of each R5 point reaches the process from
// ServerProcessConfig org_policy_enforcement; unset is audit, the level a
// point ships in.
func TestNewOrgPolicyResolver_InstallsTheApprovalAndMachineSwitches(t *testing.T) {
	t.Cleanup(func() {
		orgpolicy.SetProcessResolver(nil)
		orgpolicy.SetProcessEnforcement(orgpolicy.PointApprovals, orgpolicy.EnforcementAudit)
		orgpolicy.SetProcessEnforcement(orgpolicy.PointMachines, orgpolicy.EnforcementAudit)
	})
	_, err := NewOrgPolicyResolver(OrgPolicyResolverDeps{Authority: selfHostedAuthority{},
		Enforcement: &configv1.OrgPolicyEnforcementConfig{Approvals: "enforce", Machines: "off"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := orgpolicy.EnforcementOf(orgpolicy.PointApprovals); got != orgpolicy.EnforcementEnforce {
		t.Errorf("approvals = %s, want enforce", got)
	}
	if got := orgpolicy.EnforcementOf(orgpolicy.PointMachines); got != orgpolicy.EnforcementOff {
		t.Errorf("machines = %s, want off", got)
	}
	if _, err := NewOrgPolicyResolver(OrgPolicyResolverDeps{Authority: selfHostedAuthority{}}); err != nil {
		t.Fatal(err)
	}
	if got := orgpolicy.EnforcementOf(orgpolicy.PointApprovals); got != orgpolicy.EnforcementAudit {
		t.Errorf("unset approvals = %s, want audit", got)
	}
	if got := orgpolicy.EnforcementOf(orgpolicy.PointMachines); got != orgpolicy.EnforcementAudit {
		t.Errorf("unset machines = %s, want audit", got)
	}
}

// selfHostedAuthority is a token store with no control plane behind it.
type selfHostedAuthority struct{ tokenauthority.Authority }

// With no policies (self-hosted) the worker has no watcher, and a refusal
// registers with none.
func TestNewOrgPolicyWatcher_NoPolicies(t *testing.T) {
	t.Cleanup(func() { orgpolicy.SetProcessWatcher(nil) })
	w, err := NewOrgPolicyWatcher(OrgPolicyWatcherDeps{})
	if err != nil || w != nil {
		t.Fatalf("no policies: %v, %v; want no watcher", w, err)
	}
	orgpolicy.WatchRefused(context.Background(), "org-a") // a no-op, not a panic
}

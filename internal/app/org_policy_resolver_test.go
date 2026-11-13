package app

import (
	"context"
	"testing"

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

// selfHostedAuthority is a token store with no control plane behind it.
type selfHostedAuthority struct{ tokenauthority.Authority }

// Copyright (c) 2025 Reliant Labs

package authz

import (
	"context"
	"errors"
	"testing"
)

// grants: alice is a member of acme holding billing:read; bob a member
// holding nothing. It counts the asks.
type grants struct {
	err  error
	asks int
}

func (g *grants) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	return orgID == "acme" && (userID == "alice" || userID == "bob"), true, g.err
}

func (g *grants) ScopesFresh(_ context.Context, userID, orgID string) ([]string, bool, error) {
	g.asks++
	if g.err != nil {
		return nil, false, g.err
	}
	switch {
	case orgID == "acme" && userID == "alice":
		return []string{"deploy:read", "billing:read"}, true, nil
	case orgID == "acme" && userID == "bob":
		return nil, true, nil
	}
	return nil, false, nil
}

func TestRequireOrgScope(t *testing.T) {
	live := &grants{}
	for name, tc := range map[string]struct {
		ctx              context.Context
		user, org, scope string
		want             error
	}{
		"a member with the grant":          {WithMembership(context.Background(), live), "alice", "acme", "billing:read", nil},
		"a member without it":              {WithMembership(context.Background(), live), "bob", "acme", "billing:read", ErrMissingOrgScope},
		"a member with another grant only": {WithMembership(context.Background(), live), "alice", "acme", "secret:read", ErrMissingOrgScope},
		"another org's admin":              {WithMembership(context.Background(), live), "alice", "globex", "billing:read", ErrNotOrgMemberRead},
		"a credential bound to another org": {WithCredentialOrg(WithMembership(context.Background(), live), "globex"),
			"alice", "acme", "billing:read", ErrDenied},
		"no membership wired":            {context.Background(), "alice", "acme", "billing:read", ErrMembershipUnavailable},
		"a membership naming no grants":  {WithMembership(context.Background(), members{}), "alice", "acme", "billing:read", ErrMembershipUnavailable},
		"an outage":                      {WithMembership(context.Background(), &grants{err: errors.New("down")}), "alice", "acme", "billing:read", ErrMembershipUnavailable},
		"no org":                         {WithMembership(context.Background(), live), "alice", "", "billing:read", ErrDenied},
		"no deployment orgs (no answer)": {WithMembership(context.Background(), &grants{}), "carol", "acme", "billing:read", ErrNotOrgMemberRead},
	} {
		t.Run(name, func(t *testing.T) {
			if err := RequireOrgScope(tc.ctx, tc.user, tc.org, tc.scope); !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("RequireOrgScope = %v, want %v", err, tc.want)
			}
		})
	}
	before := live.asks
	_ = RequireOrgScope(WithMembership(context.Background(), live), "alice", "acme", "billing:read")
	_ = RequireOrgScope(WithMembership(context.Background(), live), "alice", "acme", "billing:read")
	if live.asks-before != 2 {
		t.Fatalf("asked %d times for two decisions; every decision must ask fresh", live.asks-before)
	}
}

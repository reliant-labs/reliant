// Copyright (c) 2025 Reliant Labs

package authz

import (
	"context"
	"errors"
	"testing"
)

// roleMembers answers roles[user][org], fresh, and counts the asks. A user
// in unknownRole is a member whose control plane sent no role.
type roleMembers struct {
	members
	roles   map[string]map[string]string
	unknown map[string]bool
	err     error
	asks    *int
}

func (r roleMembers) RoleFresh(_ context.Context, userID, orgID string) (string, bool, error) {
	if r.asks != nil {
		*r.asks++
	}
	if r.err != nil {
		return "", false, r.err
	}
	if r.unknown[userID] {
		return "", false, nil
	}
	return r.roles[userID][orgID], true, nil
}

func TestRequireOrgAdmin(t *testing.T) {
	asks := 0
	m := roleMembers{
		roles:   map[string]map[string]string{"olive": {"acme": "owner"}, "adam": {"acme": "admin"}, "mia": {"acme": "member"}},
		unknown: map[string]bool{"uma": true},
		asks:    &asks,
	}
	ctx := person(WithMembership(context.Background(), m))
	cases := []struct {
		name      string
		ctx       context.Context
		user, org string
		want      error
	}{
		{"owner", ctx, "olive", "acme", nil},
		{"admin", ctx, "adam", "acme", nil},
		{"member", ctx, "mia", "acme", ErrNotOrgAdmin},
		{"non-member", ctx, "nora", "acme", ErrNotOrgAdmin},
		{"admin of another org", ctx, "adam", "globex", ErrNotOrgAdmin},
		{"role unknown (older control plane)", ctx, "uma", "acme", ErrNotOrgAdmin},
		{"no caller", ctx, "", "acme", ErrDenied},
		{"no org", ctx, "adam", "", ErrDenied},
		{"credential bound to another org", WithCredentialOrg(ctx, "globex"), "adam", "acme", ErrDenied},
		{"credential bound to this org", WithCredentialOrg(ctx, "acme"), "adam", "acme", nil},
		{"outage", person(WithMembership(context.Background(), roleMembers{err: errors.New("down")})), "adam", "acme", ErrMembershipUnavailable},
		{"membership that cannot name roles", person(WithMembership(context.Background(), members{})), "adam", "acme", ErrMembershipUnavailable},
		{"no orgs in this deployment", person(WithMembership(context.Background(), NoOrgs{})), "adam", "acme", ErrMembershipUnavailable},
		{"no membership wired", person(context.Background()), "adam", "acme", ErrMembershipUnavailable},
		{"an admin through a software credential (an agent's CLI login)", WithCredentialKind(WithMembership(context.Background(), m), CredentialKindSoftware), "adam", "acme", ErrPersonRequired},
		{"an admin through an unclassified credential", WithMembership(context.Background(), m), "adam", "acme", ErrPersonRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RequireOrgAdmin(tc.ctx, tc.user, tc.org); !errors.Is(got, tc.want) || (tc.want == nil && got != nil) {
				t.Fatalf("RequireOrgAdmin(%q, %q) = %v, want %v", tc.user, tc.org, got, tc.want)
			}
		})
	}

	// Every decision asks afresh: two decisions, two asks.
	asks = 0
	_ = RequireOrgAdmin(ctx, "adam", "acme")
	_ = RequireOrgAdmin(ctx, "adam", "acme")
	if asks != 2 {
		t.Fatalf("RequireOrgAdmin asked %d times for two decisions, want 2 (never cached)", asks)
	}
}

// The credential refused on another org is reported, as for any other
// refusal of a bound credential.
func TestRequireOrgAdminRecordsCredentialRefusal(t *testing.T) {
	ctx := WithCredentialOrg(person(WithMembership(context.Background(), roleMembers{})), "globex")
	_ = RequireOrgAdmin(ctx, "adam", "acme")
	if bound, refused, ok := CredentialRefusal(ctx); !ok || bound != "globex" || refused != "acme" {
		t.Fatalf("CredentialRefusal = %q, %q, %v; want globex, acme, true", bound, refused, ok)
	}
}

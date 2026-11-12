// Copyright (c) 2025 Reliant Labs

package orgs

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/authz"
)

func newRoleFake() *fakeSource {
	f := newFake()
	f.members["alice"]["old"] = true
	f.roles = map[string]map[string]string{"alice": {"acme": RoleAdmin, "home": RoleOwner}}
	return f
}

// Role answers control-plane's role with ok, "" with ok for a non-member,
// and "" without ok whenever the role is unknown: no orgs, no rpc, or a
// member whose control plane sent no role. Every one but an owner or admin
// is refused an admin action by its caller.
func TestRole_Answers(t *testing.T) {
	ctx := context.Background()
	r, _ := newResolver(newRoleFake())
	cases := []struct {
		user, org string
		role      string
		ok        bool
	}{
		{"alice", "acme", RoleAdmin, true},
		{"alice", "home", RoleOwner, true},
		{"alice", "globex", "", true},
		{"mallory", "acme", "", true},
		{"alice", "old", "", false}, // a member, but no role sent: unknown
		{"alice", "", "", false},
		{"", "acme", "", false},
	}
	for _, tc := range cases {
		for name, ask := range map[string]func(context.Context, string, string) (string, bool, error){"Role": r.Role, "RoleFresh": r.RoleFresh} {
			role, ok, err := ask(ctx, tc.user, tc.org)
			if err != nil || role != tc.role || ok != tc.ok {
				t.Errorf("%s(%q, %q) = %q, %v, %v; want %q, %v", name, tc.user, tc.org, role, ok, err, tc.role, tc.ok)
			}
		}
	}

	var none *Resolver
	if role, ok, err := none.RoleFresh(ctx, "alice", "acme"); role != "" || ok || err != nil {
		t.Errorf("nil resolver: %q, %v, %v; want no answer", role, ok, err)
	}
	unsupported, _ := newResolver(&fakeSource{err: fmt.Errorf("%w: ResolveOrgMembership", accesstokenclient.ErrUnsupported)})
	if role, ok, err := unsupported.Role(ctx, "alice", "acme"); role != "" || ok || err != nil {
		t.Errorf("control plane without the rpc: %q, %v, %v; want no answer", role, ok, err)
	}
}

// A role this build does not know is unknown, never guessed upward.
func TestRole_UnknownRoleIsUnknown(t *testing.T) {
	f := newRoleFake()
	f.roles["alice"]["acme"] = "superuser"
	r, _ := newResolver(f)
	if role, ok, err := r.Role(context.Background(), "alice", "acme"); role != "" || ok || err != nil {
		t.Fatalf("got %q, %v, %v; want unknown", role, ok, err)
	}
}

// An outage is an error from both, never a role and never "not a member";
// it is not cached.
func TestRole_OutageIsAnError(t *testing.T) {
	f := newRoleFake()
	f.err = errors.New("dial admin-server: connection refused")
	r, _ := newResolver(f)
	for name, ask := range map[string]func(context.Context, string, string) (string, bool, error){"Role": r.Role, "RoleFresh": r.RoleFresh} {
		if role, _, err := ask(context.Background(), "alice", "acme"); err == nil || role != "" {
			t.Errorf("%s: %q, %v; want an error", name, role, err)
		}
	}
	f.err = nil
	if role, _, err := r.Role(context.Background(), "alice", "acme"); err != nil || role != RoleAdmin {
		t.Fatalf("after the outage: %q, %v; want admin (the error was not cached)", role, err)
	}
}

// Role honours the cache TTL; RoleFresh always asks and refreshes the cache,
// so a demotion is seen by the next admin write at once and by every
// cached reader from then on.
func TestRole_CacheAndFresh(t *testing.T) {
	ctx := context.Background()
	f := newRoleFake()
	r, c := newResolver(f)

	if role, _, _ := r.Role(ctx, "alice", "acme"); role != RoleAdmin || f.calls != 1 {
		t.Fatalf("first ask: %q after %d calls", role, f.calls)
	}
	f.roles["alice"]["acme"] = RoleMember // demoted at control-plane
	if role, _, _ := r.Role(ctx, "alice", "acme"); role != RoleAdmin || f.calls != 1 {
		t.Fatalf("within the TTL Role is cached: %q after %d calls", role, f.calls)
	}
	if role, _, _ := r.RoleFresh(ctx, "alice", "acme"); role != RoleMember || f.calls != 2 {
		t.Fatalf("RoleFresh bypasses the cache: %q after %d calls", role, f.calls)
	}
	if role, _, _ := r.Role(ctx, "alice", "acme"); role != RoleMember || f.calls != 2 {
		t.Fatalf("RoleFresh refreshed the cache: %q after %d calls", role, f.calls)
	}
	if role, _, _ := r.RoleFresh(ctx, "alice", "acme"); role != RoleMember || f.calls != 3 {
		t.Fatalf("RoleFresh asks every time: %q after %d calls", role, f.calls)
	}

	f.roles["alice"]["acme"] = RoleOwner
	c.t = c.t.Add(CacheTTL)
	if role, _, _ := r.Role(ctx, "alice", "acme"); role != RoleOwner || f.calls != 4 {
		t.Fatalf("after the TTL Role asks again: %q after %d calls", role, f.calls)
	}
	// Membership shares the entry: a removal RoleFresh saw is what IsMember
	// answers from the cache too.
	delete(f.members["alice"], "acme")
	if _, _, err := r.RoleFresh(ctx, "alice", "acme"); err != nil {
		t.Fatal(err)
	}
	if member, _, _ := r.IsMember(ctx, "alice", "acme"); member || f.calls != 5 {
		t.Fatalf("IsMember after a fresh removal: member=%v after %d calls", member, f.calls)
	}
}

// *Resolver is the role source authz's admin check asks for: installed as
// the membership, it lights up authz.RequireOrgAdmin (an optional interface,
// so a signature drift would silently refuse every admin).
func TestResolverAnswersAuthzAdminCheck(t *testing.T) {
	r, _ := newResolver(newRoleFake())
	ctx := authz.WithMembership(context.Background(), r)
	if err := authz.RequireOrgAdmin(ctx, "alice", "acme"); err != nil {
		t.Fatalf("an admin: %v", err)
	}
	if err := authz.RequireOrgAdmin(ctx, "alice", "old"); !errors.Is(err, authz.ErrNotOrgAdmin) {
		t.Fatalf("a member with an unknown role: %v, want ErrNotOrgAdmin", err)
	}
}

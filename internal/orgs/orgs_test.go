// Copyright (c) 2025 Reliant Labs

package orgs

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
)

// fakeSource: members[user] is the set of orgs a user is in; defaults[user]
// their default org. err, when set, fails every call.
type fakeSource struct {
	members  map[string]map[string]bool
	defaults map[string]string
	err      error
	calls    int
}

func (f *fakeSource) ResolveOrgMembership(_ context.Context, userID, orgID string) (accesstokenclient.OrgMembership, error) {
	f.calls++
	if f.err != nil {
		return accesstokenclient.OrgMembership{}, f.err
	}
	if orgID == "" {
		def := f.defaults[userID]
		return accesstokenclient.OrgMembership{OrgID: def, Member: def != ""}, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: f.members[userID][orgID]}, nil
}

func newFake() *fakeSource {
	return &fakeSource{
		members:  map[string]map[string]bool{"alice": {"acme": true, "home": true}},
		defaults: map[string]string{"alice": "home"},
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newResolver(src *fakeSource) (*Resolver, *clock) {
	r := New(src)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	r.now = c.now
	return r, c
}

func TestNilResolverIsADeploymentWithNoOrgs(t *testing.T) {
	ctx := context.Background()
	var r *Resolver
	if New(nil) != nil {
		t.Fatal("New(nil) must be the no-orgs resolver, nil")
	}
	if org, err := r.ActingOrg(ctx, "alice", "acme"); org != "" || err != nil {
		t.Fatalf("ActingOrg = %q, %v; want no org", org, err)
	}
	if scope, err := r.ListScope(ctx, "alice", "acme", nil); scope != nil || err != nil {
		t.Fatalf("ListScope = %v, %v; want no filter", scope, err)
	}
	if _, ok, err := r.IsMember(ctx, "alice", "acme"); ok || err != nil {
		t.Fatalf("IsMember ok=%v err=%v; want no answer", ok, err)
	}
}

func TestNamed(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		src       *fakeSource
		requested string
		want      string
		code      connect.Code
		reason    string
	}{
		"a member's org":              {src: newFake(), requested: "acme", want: "acme"},
		"nothing named":               {src: newFake(), requested: " ", want: ""},
		"not a member":                {src: newFake(), requested: "globex", code: connect.CodePermissionDenied, reason: ReasonNotOrgMember},
		"an outage fails closed":      {src: &fakeSource{err: errors.New("down")}, requested: "acme", code: connect.CodeUnavailable},
		"no rpc on the control plane": {src: &fakeSource{err: fmt.Errorf("%w: x", accesstokenclient.ErrUnsupported)}, requested: "acme", want: ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r, _ := newResolver(tc.src)
			got, err := r.Named(ctx, "alice", tc.requested)
			if tc.code != 0 {
				if svcerr.Code(err) != tc.code {
					t.Fatalf("err %v (code %v), want %v", err, svcerr.Code(err), tc.code)
				}
				if tc.reason != "" && reasonOf(err) != tc.reason {
					t.Fatalf("reason %q, want %q", reasonOf(err), tc.reason)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// A machine credential acts in exactly one org: its own, named or not, and
// naming another is refused without asking control-plane.
func TestNamedMachineCredentialActsInItsOrg(t *testing.T) {
	src := newFake()
	r, _ := newResolver(src)
	ctx := auth.WithMachineToken(context.Background(), &fat.Principal{OrgID: "acme", ActingUserID: "alice"})
	if got, err := r.Named(ctx, "alice", ""); got != "acme" || err != nil {
		t.Fatalf("unnamed: %q, %v; want the credential's org", got, err)
	}
	if got, err := r.Named(ctx, "alice", "acme"); got != "acme" || err != nil {
		t.Fatalf("own org: %q, %v", got, err)
	}
	_, err := r.Named(ctx, "alice", "home")
	if svcerr.Code(err) != connect.CodePermissionDenied || reasonOf(err) != ReasonOrgMismatch {
		t.Fatalf("another org: %v, want PermissionDenied %s", err, ReasonOrgMismatch)
	}
	if src.calls != 0 {
		t.Fatalf("asked control-plane %d times; the credential already says", src.calls)
	}
}

// A create that names no org never fails over the default: an outage stores
// no org, which is today's owner-only semantics.
func TestActingOrgDefaultsAndNeverFailsOnTheDefault(t *testing.T) {
	ctx := context.Background()
	r, _ := newResolver(newFake())
	if got, err := r.ActingOrg(ctx, "alice", ""); got != "home" || err != nil {
		t.Fatalf("got %q, %v; want the default org", got, err)
	}
	down, _ := newResolver(&fakeSource{err: errors.New("down")})
	if got, err := down.ActingOrg(ctx, "alice", ""); got != "" || err != nil {
		t.Fatalf("outage: %q, %v; want no org and no error", got, err)
	}
	if _, err := down.ActingOrg(ctx, "alice", "acme"); svcerr.Code(err) != connect.CodeUnavailable {
		t.Fatalf("outage with a named org: %v, want Unavailable", err)
	}
}

func TestAnswersAreCachedForTheTTLAndErrorsNever(t *testing.T) {
	ctx := context.Background()
	src := newFake()
	r, c := newResolver(src)
	for range 3 {
		if m, _, _ := r.IsMember(ctx, "alice", "acme"); !m {
			t.Fatal("alice is in acme")
		}
	}
	if src.calls != 1 {
		t.Fatalf("calls = %d, want 1 within the TTL", src.calls)
	}
	// Removed at control-plane: seen once the cached answer expires.
	delete(src.members["alice"], "acme")
	c.t = c.t.Add(CacheTTL - time.Second)
	if m, _, _ := r.IsMember(ctx, "alice", "acme"); !m {
		t.Fatal("within the TTL the cached answer stands")
	}
	c.t = c.t.Add(2 * time.Second)
	if m, _, _ := r.IsMember(ctx, "alice", "acme"); m {
		t.Fatal("after the TTL a removal is seen")
	}

	src.err = errors.New("down")
	c.t = c.t.Add(CacheTTL + time.Second)
	if _, _, err := r.IsMember(ctx, "alice", "acme"); err == nil {
		t.Fatal("an outage is an error")
	}
	src.err = nil
	src.members["alice"]["acme"] = true
	if m, _, err := r.IsMember(ctx, "alice", "acme"); !m || err != nil {
		t.Fatalf("after the outage: member=%v err=%v; the error was cached", m, err)
	}
}

func TestListScope(t *testing.T) {
	ctx := context.Background()
	var stamped []string
	stamp := func(_ context.Context, userID, orgID string) error {
		stamped = append(stamped, userID+"→"+orgID)
		return nil
	}

	r, c := newResolver(newFake())
	if scope, err := r.ListScope(ctx, "alice", "", stamp); scope != nil || err != nil {
		t.Fatalf("no org named: %v, %v; want no filter", scope, err)
	}
	scope, err := r.ListScope(ctx, "alice", "acme", stamp)
	if err != nil || scope == nil || scope.OrgID != "acme" || scope.IncludeUnassigned {
		t.Fatalf("another member org: %+v, %v; want acme without unassigned rows", scope, err)
	}
	if len(stamped) != 0 {
		t.Fatalf("stamped %v for a non-default org", stamped)
	}
	scope, err = r.ListScope(ctx, "alice", "home", stamp)
	if err != nil || scope == nil || !scope.IncludeUnassigned {
		t.Fatalf("the default org: %+v, %v; want unassigned rows included", scope, err)
	}
	if _, err := r.ListScope(ctx, "alice", "home", stamp); err != nil {
		t.Fatal(err)
	}
	if len(stamped) != 1 || stamped[0] != "alice→home" {
		t.Fatalf("stamped %v, want once within BackfillInterval", stamped)
	}
	c.t = c.t.Add(BackfillInterval)
	if _, err := r.ListScope(ctx, "alice", "home", stamp); err != nil {
		t.Fatal(err)
	}
	if len(stamped) != 2 {
		t.Fatalf("stamped %v, want again after BackfillInterval", stamped)
	}

	_, err = r.ListScope(ctx, "alice", "globex", stamp)
	if svcerr.Code(err) != connect.CodePermissionDenied {
		t.Fatalf("not a member: %v, want PermissionDenied", err)
	}
	down, _ := newResolver(&fakeSource{err: errors.New("down")})
	if _, err := down.ListScope(ctx, "alice", "home", stamp); svcerr.Code(err) != connect.CodeUnavailable {
		t.Fatalf("outage: %v, want Unavailable (fail closed)", err)
	}
}

// A failed stamp is retried by the next list, not remembered as done.
func TestListScopeRetriesAFailedStamp(t *testing.T) {
	ctx := context.Background()
	r, _ := newResolver(newFake())
	fails := 1
	calls := 0
	stamp := func(context.Context, string, string) error {
		calls++
		if fails > 0 {
			fails--
			return errors.New("db down")
		}
		return nil
	}
	for range 2 {
		if scope, err := r.ListScope(ctx, "alice", "home", stamp); err != nil || !scope.IncludeUnassigned {
			t.Fatalf("a failed stamp must not fail the list: %+v, %v", scope, err)
		}
	}
	if calls != 2 {
		t.Fatalf("stamp calls = %d, want the failed one retried", calls)
	}
}

// reasonOf is the x-forge-error-reason err carries on the wire.
func reasonOf(err error) string {
	var ce *connect.Error
	if !errors.As(svcerr.Wrap(err), &ce) {
		return ""
	}
	return ce.Meta().Get(svcerr.ReasonHeader)
}

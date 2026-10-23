// Copyright (c) 2025 Reliant Labs
package token

import (
	"context"
	"errors"
	"slices"
	"testing"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// orgScopes mirrors control-plane's orggrants.OrgScopes: the permissions that
// need a per-member grant. obs:read/obs:triage are deliberately absent (every
// member holds them) and so are the session scopes.
var orgScopes = []fat.Scope{
	fat.ScopeDeployRead, fat.ScopeDeployWrite, fat.ScopeSecretRead, fat.ScopeSecretWrite,
	fat.ScopeDomainRead, fat.ScopeDomainWrite, fat.ScopeClusterManage, fat.ScopeTokenRead,
	fat.ScopeTokenWrite, fat.ScopeIdentityRead, fat.ScopeIdentityManage, fat.ScopeDBQuery,
}

// cpAuthority is tokenauthority.Memory behind control-plane's REAL semantics
// (access_token_internal), which the first version of these tests got wrong:
//
//   - MintForUser acts in the user's primary org, which exists only while they
//     are a member (members), and CLIPS every org scope the user's grants do
//     not include, reporting what it minted. Nothing surviving is refused.
//   - UpdateForUser does NOT clip: it REFUSES the whole request with
//     permission_denied if ANY org scope in it is not held.
//
// Both maps are read at call time, so a test can revoke a grant or remove a
// member between calls.
type cpAuthority struct {
	*tokenauthority.Memory
	members map[string]bool
	grants  map[string]fat.Set
	// asParent, when set, rewrites what Introspect says about the PARENT token
	// (not minted children), to model a caller shape Memory cannot mint.
	asParent func(*fat.Principal)
	parent   string
	// mintedByCP counts MintForUser calls that reached control-plane.
	mintedByCP int
}

func (a *cpAuthority) MintForUser(ctx context.Context, req tokenauthority.MintRequest) (tokenauthority.Minted, error) {
	a.mintedByCP++
	if !a.members[req.UserID] {
		return tokenauthority.Minted{}, &accesstokenclient.RPCError{Code: "failed_precondition", Message: "user " + req.UserID + " has no organization"}
	}
	kept := make([]fat.Scope, 0, len(req.Scopes))
	var clipped []fat.Scope
	for _, sc := range req.Scopes {
		if slices.Contains(orgScopes, sc) && !a.grants[req.UserID].Has(sc) {
			clipped = append(clipped, sc)
			continue
		}
		kept = append(kept, sc)
	}
	if len(kept) == 0 {
		return tokenauthority.Minted{}, &accesstokenclient.RPCError{Code: "permission_denied",
			Message: "your organization permissions do not include " + fat.JoinScopes(clipped)}
	}
	req.Scopes = kept
	return a.Memory.MintForUser(ctx, req)
}

func (a *cpAuthority) UpdateForUser(ctx context.Context, userID, tokenID string, name *string, scopes fat.Set) (tokenauthority.TokenInfo, error) {
	var missing []fat.Scope
	for _, sc := range orgScopes {
		if scopes.Has(sc) && !a.grants[userID].Has(sc) {
			missing = append(missing, sc)
		}
	}
	if len(missing) > 0 {
		return tokenauthority.TokenInfo{}, &accesstokenclient.RPCError{Code: "permission_denied",
			Message: "your organization permissions do not include " + fat.JoinScopes(missing)}
	}
	return a.Memory.UpdateForUser(ctx, userID, tokenID, name, scopes)
}

func (a *cpAuthority) Introspect(ctx context.Context, token string) (*fat.Principal, error) {
	p, err := a.Memory.Introspect(ctx, token)
	if err == nil && a.asParent != nil && token == a.parent {
		c := *p
		a.asParent(&c)
		return &c, nil
	}
	return p, err
}

func newCP(members []string, grants map[string]fat.Set) *cpAuthority {
	a := &cpAuthority{Memory: tokenauthority.NewMemory(), members: map[string]bool{}, grants: grants}
	for _, m := range members {
		a.members[m] = true
	}
	return a
}

func exchange(t *testing.T, a tokenauthority.Authority, bearer string, scopes ...string) (*reliantv1.ExchangeTokenResponse, error) {
	t.Helper()
	svc := newTokenService(t, a, TokenControlPlane{Issuer: testControlPlane, ClipsGrants: true})
	resp, err := svc.ExchangeToken(context.Background(), exchangeReq(bearer, &reliantv1.ExchangeTokenRequest{
		Audience: testControlPlane, Scopes: scopes,
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func liveTokens(t *testing.T, a tokenauthority.Authority, userID string) []tokenauthority.TokenInfo {
	t.Helper()
	infos, err := a.ListForUser(context.Background(), userID, "")
	if err != nil {
		t.Fatal(err)
	}
	return infos
}

// A deploy-only daemon credential (what exists today) is enough to ask for the
// derived scopes: they are derived, not inherited from the caller.
func TestExchangeToken_ObsScopesForMembers(t *testing.T) {
	a := newCP([]string{"user-1"}, nil)
	bearer := mintSession(t, a, "user-1", tokenauthority.MintRequest{
		Scopes: fullDaemon, Resource: &fat.Resource{Kind: fat.ResourceDaemon, ID: "daemon-1"},
	})
	resp, err := exchange(t, a, bearer, "obs:read", "obs:triage")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.GetScopes(), []string{"obs:read", "obs:triage"}) {
		t.Fatalf("granted %v", resp.GetScopes())
	}
	p, err := a.Introspect(context.Background(), resp.GetToken())
	if err != nil {
		t.Fatal(err)
	}
	if !p.BoundTo(fat.ResourceDaemon, "daemon-1") || p.ActingUserID != "user-1" {
		t.Errorf("binding/actor not inherited: %+v", p)
	}
	if p.Scopes.Has(fat.ScopeDBQuery) || p.Scopes.Has(fat.ScopeDeployWrite) {
		t.Errorf("only the requested scopes: %s", p.Scopes)
	}
}

func TestExchangeToken_DBQueryNeedsTheGrant(t *testing.T) {
	for _, tc := range []struct {
		name   string
		grants map[string]fat.Set
		want   []string
	}{
		{"held", map[string]fat.Set{"user-1": fat.SetOf(fat.ScopeDBQuery)}, []string{"obs:read", "db:query"}},
		{"member without the grant: omitted, rest proceeds", map[string]fat.Set{"user-1": fat.SetOf(fat.ScopeDeployWrite)}, []string{"obs:read"}},
		{"no grant rows at all", nil, []string{"obs:read"}},
		{"another user's grant is no use", map[string]fat.Set{"user-2": fat.SetOf(fat.ScopeDBQuery)}, []string{"obs:read"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newCP([]string{"user-1", "user-2"}, tc.grants)
			// The calling token claims db:query itself: it must not matter.
			bearer := mintSession(t, a, "user-1", tokenauthority.MintRequest{
				Scopes: append([]fat.Scope{fat.ScopeDBQuery}, fullDaemon...),
			})
			resp, err := exchange(t, a, bearer, "obs:read", "db:query")
			if err != nil {
				t.Fatal(err)
			}
			if got := resp.GetScopes(); !slices.Equal(got, tc.want) {
				t.Fatalf("granted %v, want %v", got, tc.want)
			}
			p, err := a.Introspect(context.Background(), resp.GetToken())
			if err != nil {
				t.Fatal(err)
			}
			if p.Scopes.Has(fat.ScopeDBQuery) != slices.Contains(tc.want, "db:query") {
				t.Errorf("stored scopes %s disagree with the response %v", p.Scopes, tc.want)
			}
			// Exactly one exchanged token survives; any superseded one is revoked.
			if n := len(liveTokens(t, a, "user-1")); n != 2 {
				t.Errorf("live tokens = %d, want the parent plus one exchanged", n)
			}
		})
	}
}

func TestExchangeToken_DBQueryAloneWithoutGrantIsRefusedAndMintsNothing(t *testing.T) {
	a := newCP([]string{"user-1"}, map[string]fat.Set{"user-1": fat.SetOf(fat.ScopeDeployWrite)})
	bearer := mintSession(t, a, "user-1", tokenauthority.MintRequest{Scopes: fullDaemon})
	_, err := exchange(t, a, bearer, "db:query")
	if svcerr.Code(err) != connect.CodePermissionDenied {
		t.Fatalf("err = %v, want PermissionDenied", err)
	}
	if n := len(liveTokens(t, a, "user-1")); n != 1 {
		t.Fatalf("a refused exchange left %d live tokens, want the parent alone", n)
	}
}

func TestExchangeToken_RevokedGrantIsHonouredAtTheNextExchange(t *testing.T) {
	a := newCP([]string{"user-1"}, map[string]fat.Set{"user-1": fat.SetOf(fat.ScopeDBQuery)})
	bearer := mintSession(t, a, "user-1", tokenauthority.MintRequest{Scopes: fullDaemon})
	resp, err := exchange(t, a, bearer, "db:query")
	if err != nil || !slices.Equal(resp.GetScopes(), []string{"db:query"}) {
		t.Fatalf("with the grant: %v %v", resp.GetScopes(), err)
	}
	delete(a.grants, "user-1") // an admin revokes the grant; the session token is unchanged
	if _, err := exchange(t, a, bearer, "db:query"); svcerr.Code(err) != connect.CodePermissionDenied {
		t.Fatalf("after revocation: err = %v, want PermissionDenied", err)
	}
}

func TestExchangeToken_NonMemberGetsNoDerivedScope(t *testing.T) {
	// user-9 holds a (stale) session credential but is no longer in any org.
	a := newCP(nil, map[string]fat.Set{"user-9": fat.SetOf(fat.ScopeDBQuery)})
	bearer := mintSession(t, a.Memory, "user-9", tokenauthority.MintRequest{Scopes: fullDaemon})
	for _, scopes := range [][]string{{"obs:read"}, {"obs:triage"}, {"db:query"}, {"obs:read", "db:query"}} {
		if resp, err := exchange(t, a, bearer, scopes...); err == nil {
			t.Fatalf("%v: a non-member was granted %v", scopes, resp.GetScopes())
		}
	}
	if n := len(liveTokens(t, a, "user-9")); n != 1 {
		t.Fatalf("live tokens = %d, want the parent alone", n)
	}
}

func TestExchangeToken_MemberOfAnotherOrgIsRefused(t *testing.T) {
	// The caller's token is for org A; the user's primary org is now B.
	mem := tokenauthority.NewMemory()
	bearer := mintSession(t, mem, "user-1", tokenauthority.MintRequest{Scopes: fullDaemon})
	a := orgShiftAuthority{Memory: mem, parentToken: bearer}
	svc := newTokenService(t, a, TokenControlPlane{Issuer: testControlPlane, ClipsGrants: true})
	_, err := svc.ExchangeToken(context.Background(), exchangeReq(bearer, &reliantv1.ExchangeTokenRequest{
		Audience: testControlPlane, Scopes: []string{"obs:read", "obs:triage"},
	}))
	if svcerr.Code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("err = %v, want FailedPrecondition", err)
	}
	if n := len(liveTokens(t, mem, "user-1")); n != 1 {
		t.Fatalf("live tokens = %d, want the parent alone", n)
	}
}

func TestExchangeToken_NonDaemonResourceBoundCallerStillRefused(t *testing.T) {
	for _, kind := range []fat.ResourceKind{fat.ResourcePort, fat.ResourceConnector} {
		a := newCP([]string{"user-1"}, map[string]fat.Set{"user-1": fat.SetOf(fat.ScopeDBQuery)})
		bearer := mintSession(t, a, "user-1", tokenauthority.MintRequest{Scopes: fullDaemon})
		a.parent = bearer
		a.asParent = func(p *fat.Principal) { p.Resource = &fat.Resource{Kind: kind, ID: "x"} }
		for _, scopes := range [][]string{{"obs:read"}, {"db:query"}, {"deploy:read"}} {
			if _, err := exchange(t, a, bearer, scopes...); svcerr.Code(err) != connect.CodePermissionDenied {
				t.Errorf("%s-bound, %v: err = %v, want PermissionDenied", kind, scopes, err)
			}
		}
		if n := len(liveTokens(t, a, "user-1")); n != 1 {
			t.Errorf("%s-bound: refused exchanges left %d live tokens", kind, n)
		}
	}
}

func TestExchangeToken_DerivedScopesOnlyWhereControlPlaneClips(t *testing.T) {
	a := newCP([]string{"user-1"}, map[string]fat.Set{"user-1": fat.SetOf(fat.ScopeDBQuery)})
	bearer := mintSession(t, a, "user-1", tokenauthority.MintRequest{Scopes: fullDaemon})
	svc := newTokenService(t, a, TokenControlPlane{Issuer: testControlPlane}) // ClipsGrants false
	_, err := svc.ExchangeToken(context.Background(), exchangeReq(bearer, &reliantv1.ExchangeTokenRequest{
		Audience: testControlPlane, Scopes: []string{"obs:read", "db:query"},
	}))
	if svcerr.Code(err) != connect.CodePermissionDenied {
		t.Fatalf("err = %v, want PermissionDenied", err)
	}
}

func TestExchangeToken_UnavailableGrantStoreNeverGrantsDBQuery(t *testing.T) {
	a := &grantsDown{cpAuthority: newCP([]string{"user-1"}, map[string]fat.Set{"user-1": fat.SetOf(fat.ScopeDBQuery)})}
	bearer := mintSession(t, a.cpAuthority, "user-1", tokenauthority.MintRequest{Scopes: fullDaemon})
	if resp, err := exchange(t, a, bearer, "obs:read", "db:query"); err == nil {
		t.Fatalf("granted %v although control-plane could not read the grants", resp.GetScopes())
	}
	if n := len(liveTokens(t, a, "user-1")); n != 1 {
		t.Fatalf("live tokens = %d, want the parent alone", n)
	}
}

type grantsDown struct{ *cpAuthority }

func (g *grantsDown) MintForUser(context.Context, tokenauthority.MintRequest) (tokenauthority.Minted, error) {
	return tokenauthority.Minted{}, &accesstokenclient.RPCError{Code: "internal", Message: "reading organization permissions"}
}

func TestExchangeToken_MintsOnceAndNeverRevokes(t *testing.T) {
	a := &revokeCounter{cpAuthority: newCP([]string{"user-1"}, map[string]fat.Set{"user-1": fat.SetOf(fat.ScopeDeployRead)})}
	bearer := mintSession(t, a.cpAuthority, "user-1", tokenauthority.MintRequest{Scopes: fullDaemon})
	a.mintedByCP = 0
	resp, err := exchange(t, a, bearer, "obs:read", "db:query")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.GetScopes(), []string{"obs:read"}) {
		t.Fatalf("granted %v", resp.GetScopes())
	}
	if a.mintedByCP != 1 || a.revokes != 0 || a.updates != 0 {
		t.Fatalf("mints=%d revokes=%d updates=%d: the clip belongs to the mint, so one call and no cleanup", a.mintedByCP, a.revokes, a.updates)
	}
}

type revokeCounter struct {
	*cpAuthority
	revokes, updates int
}

func (r *revokeCounter) RevokeForUser(ctx context.Context, u, id string) error {
	r.revokes++
	return r.cpAuthority.RevokeForUser(ctx, u, id)
}

func (r *revokeCounter) UpdateForUser(ctx context.Context, u, id string, n *string, s fat.Set) (tokenauthority.TokenInfo, error) {
	r.updates++
	return r.cpAuthority.UpdateForUser(ctx, u, id, n, s)
}

// An older control plane does not clip a mint and does not report what it
// minted. It must never leave a db:query token behind for a user without the
// grant: the exchange is redone without it, and fails if that cannot be done.
type oldCP struct {
	*cpAuthority
	revokeErr error
}

func (o *oldCP) MintForUser(ctx context.Context, req tokenauthority.MintRequest) (tokenauthority.Minted, error) {
	m, err := o.Memory.MintForUser(ctx, req) // no membership check, no clip
	m.Scopes = nil                           // and it does not say what it minted
	return m, err
}

func (o *oldCP) RevokeForUser(ctx context.Context, u, id string) error {
	if o.revokeErr != nil {
		return o.revokeErr
	}
	return o.cpAuthority.RevokeForUser(ctx, u, id)
}

func TestExchangeToken_OlderControlPlaneFailsClosedOnDBQuery(t *testing.T) {
	newOld := func() (*oldCP, string) {
		o := &oldCP{cpAuthority: newCP([]string{"user-1"}, nil)}
		return o, mintSession(t, o.Memory, "user-1", tokenauthority.MintRequest{Scopes: fullDaemon})
	}

	o, bearer := newOld()
	resp, err := exchange(t, o, bearer, "obs:read", "db:query")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.GetScopes(), []string{"obs:read"}) {
		t.Fatalf("granted %v, want obs:read only", resp.GetScopes())
	}
	for _, info := range liveTokens(t, o, "user-1") {
		if slices.Contains(info.Scopes, "db:query") {
			t.Fatalf("a live db:query token %q survived against an older control plane", info.Name)
		}
	}

	// db:query alone: nothing to fall back to, and nothing left behind.
	o, bearer = newOld()
	if _, err := exchange(t, o, bearer, "db:query"); svcerr.Code(err) != connect.CodePermissionDenied {
		t.Fatalf("err = %v, want PermissionDenied", err)
	}
	if n := len(liveTokens(t, o, "user-1")); n != 1 {
		t.Fatalf("live tokens = %d, want the parent alone", n)
	}

	// The unverifiable token cannot be revoked: fail, return nothing.
	o, bearer = newOld()
	o.revokeErr = errors.New("revoke failed")
	if resp, err := exchange(t, o, bearer, "obs:read", "db:query"); err == nil {
		t.Fatalf("returned %v although the unverified token could not be revoked", resp.GetScopes())
	}

	// Without db:query an older control plane is fine: nothing to verify.
	o, bearer = newOld()
	if resp, err := exchange(t, o, bearer, "obs:read", "obs:triage"); err != nil || len(resp.GetScopes()) != 2 {
		t.Fatalf("obs only: %v %v", resp.GetScopes(), err)
	}
}

// The permanent daemon credential is widened with deploy/secret/domain ONLY.
// control-plane refuses an UpdateForUser naming a permission the user lacks, so
// a ceiling with db:query would also cost a member without that grant the
// deploy authority, and would put db:query on a long-lived credential that
// lives on a remote machine.
func TestWidenDaemonCredential_DeployFamilyOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		grants fat.Set
	}{
		{"member: deploy family, no db:query", fat.SetOf(fullDaemon[1:]...)},
		{"admin: everything including db:query", fat.SetOf(append([]fat.Scope{fat.ScopeDBQuery}, fullDaemon[1:]...)...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newCP([]string{"user-1"}, map[string]fat.Set{"user-1": tc.grants})
			svc := newTokenService(t, a, TokenControlPlane{Issuer: testControlPlane, ClipsGrants: true})
			resp, err := svc.CreateToken(tokenAuthCtx("user-1"), req(&reliantv1.CreateTokenRequest{
				Name: "laptop", Kind: reliantv1.TokenKind_TOKEN_KIND_DAEMON,
			}))
			if err != nil {
				t.Fatal(err)
			}
			p, err := a.Introspect(context.Background(), resp.Msg.GetToken())
			if err != nil {
				t.Fatal(err)
			}
			for _, derived := range derivedScopes {
				if p.Scopes.Has(derived) {
					t.Errorf("the daemon credential carries %s: %s", derived, p.Scopes)
				}
			}
			for _, want := range fullDaemon {
				if !p.Scopes.Has(want) {
					t.Errorf("the daemon credential lacks %s: %s", want, p.Scopes)
				}
			}
		})
	}
}

func TestExchangeToken_DefaultRequestStaysDeployOnly(t *testing.T) {
	a := newCP([]string{"user-1"}, map[string]fat.Set{"user-1": fat.SetOf(append([]fat.Scope{fat.ScopeDBQuery}, fullDaemon[1:]...)...)})
	bearer := mintSession(t, a, "user-1", tokenauthority.MintRequest{Scopes: fullDaemon})
	resp, err := exchange(t, a, bearer)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range resp.GetScopes() {
		if s == "obs:read" || s == "obs:triage" || s == "db:query" {
			t.Errorf("an unqualified exchange granted %s", s)
		}
	}
}

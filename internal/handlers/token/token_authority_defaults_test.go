package token

import (
	"context"
	"reflect"
	"testing"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

func TestExchangeToken_AllUserOrgCapabilities(t *testing.T) {
	// Explicit expectations: adding an unsafe registry capability must not opt it in.
	want := fat.SetOf(fat.ScopeDeployRead, fat.ScopeDeployWrite, fat.ScopeClusterManage,
		fat.ScopeSecretRead, fat.ScopeSecretWrite,
		fat.ScopeDomainRead, fat.ScopeDomainWrite, fat.ScopeIdentityRead, fat.ScopeIdentityManage,
		fat.ScopeObsRead, fat.ScopeObsTriage, fat.ScopeObsWrite, fat.ScopeDBQuery, fat.ScopeBillingRead)
	if got := fat.SetOf(exchangeableScopes...); !reflect.DeepEqual(got, want) {
		t.Fatalf("exchangeable = %s, want %s", got, want)
	}
	authority := tokenauthority.NewMemory()
	svc := newTokenService(t, authority, TokenControlPlane{Issuer: testControlPlane})
	bearer := mintSession(t, authority, "user-1", tokenauthority.MintRequest{Scopes: append([]fat.Scope{fat.ScopeDaemonConnect}, exchangeableScopes...)})
	resp, err := svc.ExchangeToken(context.Background(), exchangeReq(bearer, &reliantv1.ExchangeTokenRequest{Audience: testControlPlane}))
	if err != nil {
		t.Fatal(err)
	}
	p, err := authority.Introspect(context.Background(), resp.Msg.GetToken())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Scopes, want) {
		t.Fatalf("child = %s, want %s", p.Scopes, want)
	}
}

func TestExchangeToken_BillingReadRequiresHeldExplicitScope(t *testing.T) {
	authority := tokenauthority.NewMemory()
	svc := newTokenService(t, authority, TokenControlPlane{Issuer: testControlPlane})
	bearer := mintSession(t, authority, "restricted-user", tokenauthority.MintRequest{Scopes: []fat.Scope{fat.ScopeDaemonConnect, fat.ScopeObsRead}})
	_, err := svc.ExchangeToken(context.Background(), exchangeReq(bearer, &reliantv1.ExchangeTokenRequest{
		Audience: testControlPlane, Scopes: []string{string(fat.ScopeBillingRead)},
	}))
	if svcerr.Code(err) != connect.CodePermissionDenied {
		t.Fatalf("unheld billing:read was delegated: %v", err)
	}
	resp, err := svc.ExchangeToken(context.Background(), exchangeReq(bearer, &reliantv1.ExchangeTokenRequest{Audience: testControlPlane}))
	if err != nil {
		t.Fatal(err)
	}
	child, err := authority.Introspect(context.Background(), resp.Msg.GetToken())
	if err != nil {
		t.Fatal(err)
	}
	if child.Scopes.Has(fat.ScopeBillingRead) {
		t.Fatal("default exchange widened an existing restricted token")
	}
}

func TestExchangeToken_BoundUserCapabilitiesKeepLifecycle(t *testing.T) {
	authority := tokenauthority.NewMemory()
	svc := newTokenService(t, authority, TokenControlPlane{Issuer: testControlPlane})
	resource := fat.Resource{Kind: fat.ResourceDaemon, ID: "daemon-1"}
	bearer := mintSession(t, authority, "user-1", tokenauthority.MintRequest{Scopes: fullDaemon, Resource: &resource})
	resp, err := svc.ExchangeToken(context.Background(), exchangeReq(bearer, &reliantv1.ExchangeTokenRequest{Audience: testControlPlane}))
	if err != nil {
		t.Fatal(err)
	}
	p, err := authority.Introspect(context.Background(), resp.Msg.GetToken())
	if err != nil {
		t.Fatal(err)
	}
	want := fat.SetOf(fullDaemon[1:]...)
	if !reflect.DeepEqual(p.Scopes, want) || !p.BoundTo(resource.Kind, resource.ID) {
		t.Fatalf("child = %+v", p)
	}
	if _, err := authority.RevokeResource(context.Background(), resource); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Introspect(context.Background(), resp.Msg.GetToken()); err == nil {
		t.Fatal("child survived daemon teardown")
	}
}

// A credential-helper cache miss/refresh is another exchange of the SAME
// parent. It must reread opt-in restrictions, not restore the default ceiling.
func TestExchangeToken_RefreshPreservesOptInRestrictions(t *testing.T) {
	authority := tokenauthority.NewMemory()
	svc := newTokenService(t, authority, TokenControlPlane{Issuer: testControlPlane})
	expiry := time.Now().Add(20 * time.Minute)
	resource := fat.Resource{Kind: fat.ResourceDaemon, ID: "daemon-restricted"}
	bearer := mintSession(t, authority, "user-1", tokenauthority.MintRequest{Scopes: fullDaemon, Resource: &resource, Ephemeral: true, ExpiresAt: &expiry})
	parent, err := authority.Introspect(context.Background(), bearer)
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []fat.Scope{fat.ScopeObsRead, fat.ScopeDBQuery, fat.ScopeBillingRead} {
		_, err := authority.UpdateForUser(context.Background(), "user-1", parent.TokenID, nil, fat.SetOf(fat.ScopeDaemonConnect, keep))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := svc.ExchangeToken(context.Background(), exchangeReq(bearer, &reliantv1.ExchangeTokenRequest{Audience: testControlPlane}))
		if err != nil {
			t.Fatal(err)
		}
		child, err := authority.Introspect(context.Background(), resp.Msg.GetToken())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(child.Scopes, fat.SetOf(keep)) || !child.Ephemeral || !child.BoundTo(resource.Kind, resource.ID) || child.ExpiresAt == nil || child.ExpiresAt.After(expiry) {
			t.Fatalf("refresh reset restrictions: %+v", child)
		}
	}
}

func TestExchangeToken_WriteDoesNotDelegateAnUnheldReadScope(t *testing.T) {
	authority := tokenauthority.NewMemory()
	svc := newTokenService(t, authority, TokenControlPlane{Issuer: testControlPlane})
	for _, pair := range [][2]fat.Scope{{fat.ScopeDeployWrite, fat.ScopeDeployRead}, {fat.ScopeIdentityManage, fat.ScopeIdentityRead}, {fat.ScopeObsTriage, fat.ScopeObsRead}, {fat.ScopeObsWrite, fat.ScopeObsRead}} {
		bearer := mintSession(t, authority, "user-1", tokenauthority.MintRequest{Scopes: []fat.Scope{fat.ScopeDaemonConnect, pair[0]}})
		_, err := svc.ExchangeToken(context.Background(), exchangeReq(bearer, &reliantv1.ExchangeTokenRequest{Audience: testControlPlane, Scopes: []string{string(pair[1])}}))
		if svcerr.Code(err) != connect.CodePermissionDenied {
			t.Fatalf("%s delegated unheld %s: %v", pair[0], pair[1], err)
		}
	}
}

// This fake models CP's ATOMIC mint-time grant clipping, not a mint followed
// by an update that might fail and leave an over-privileged token behind, and
// its wildcard: a daemon credential (daemon:connect) holds every scope its
// person is granted.
type clippingAuthority struct {
	*tokenauthority.Memory
	grants fat.Set
}

func (a *clippingAuthority) MintForUser(ctx context.Context, req tokenauthority.MintRequest) (tokenauthority.Minted, error) {
	var scopes []fat.Scope
	daemon := false
	for _, s := range req.Scopes {
		if s == fat.ScopeDaemonConnect {
			daemon = true
		}
		if s == fat.ScopeDaemonConnect || a.grants.Has(s) {
			scopes = append(scopes, s)
		}
	}
	if daemon {
		for _, s := range a.grants.Strings() {
			scopes = append(scopes, fat.Scope(s))
		}
	}
	req.Scopes = scopes
	return a.Memory.MintForUser(ctx, req)
}
func (*clippingAuthority) UpdateForUser(context.Context, string, string, *string, fat.Set) (tokenauthority.TokenInfo, error) {
	panic("default mint and exchange must not widen an existing token")
}

func TestCreateAndExchangeToken_ReportCurrentOrgGrants(t *testing.T) {
	a := &clippingAuthority{Memory: tokenauthority.NewMemory(), grants: fat.SetOf(fat.ScopeObsRead, fat.ScopeObsTriage)}
	svc := newTokenService(t, a, TokenControlPlane{Issuer: testControlPlane})
	created, err := svc.CreateToken(tokenAuthCtx("user-1"), connect.NewRequest(&reliantv1.CreateTokenRequest{Name: "laptop", Kind: reliantv1.TokenKind_TOKEN_KIND_DAEMON}))
	if err != nil {
		t.Fatal(err)
	}
	parent, err := a.Introspect(context.Background(), created.Msg.GetToken())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parent.Scopes, fat.SetOf(fat.ScopeDaemonConnect, fat.ScopeObsRead, fat.ScopeObsTriage)) || !reflect.DeepEqual(created.Msg.GetInfo().GetScopes(), parent.Scopes.Strings()) {
		t.Fatalf("default mint did not report clipped authority: %+v", created.Msg.GetInfo())
	}
	// The parent still holds triage, but the owner has since lost that grant.
	a.grants = fat.SetOf(fat.ScopeObsRead)
	resp, err := svc.ExchangeToken(context.Background(), exchangeReq(created.Msg.GetToken(), &reliantv1.ExchangeTokenRequest{Audience: testControlPlane}))
	if err != nil {
		t.Fatal(err)
	}
	child, err := a.Introspect(context.Background(), resp.Msg.GetToken())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(child.Scopes, a.grants) || !reflect.DeepEqual(resp.Msg.GetScopes(), a.grants.Strings()) {
		t.Fatalf("exchange did not report current org grant clipping: child=%s response=%v", child.Scopes, resp.Msg.GetScopes())
	}
}

// TestExplicitListsNeverRequestTokenScopes: token:read/token:write administer
// member permissions and tokens, which the control plane reserves to a
// signed-in person, so the EXPLICIT lists — a CLI sign-in's ceiling and what a
// helper may be exchanged for — leave them out. A daemon's wildcard is not a
// list here at all (TestCreateToken_DaemonAsksForDaemonConnectAlone).
func TestExplicitListsNeverRequestTokenScopes(t *testing.T) {
	for _, s := range cliauth.ForgeScopes() {
		if fat.Scope(s) == fat.ScopeTokenRead || fat.Scope(s) == fat.ScopeTokenWrite {
			t.Errorf("cliauth.ForgeScopes() requests %s", s)
		}
	}
	for _, s := range exchangeableScopes {
		if s == fat.ScopeTokenRead || s == fat.ScopeTokenWrite {
			t.Errorf("exchangeableScopes includes %s", s)
		}
	}
}

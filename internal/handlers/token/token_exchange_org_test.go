package token

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// orgAuthority is the control plane's org rule over the in-memory authority:
// a mint lands in the org it names, and only if the user belongs to it; with
// none named it lands in the user's default. Introspect reports that org.
type orgAuthority struct {
	*tokenauthority.Memory
	member map[string]bool // "user|org"
	def    map[string]string
	orgOf  map[string]string // token plaintext -> org
	minted []tokenauthority.MintRequest
}

func (a *orgAuthority) MintForUser(ctx context.Context, req tokenauthority.MintRequest) (tokenauthority.Minted, error) {
	org := req.OrgID
	if org == "" {
		org = a.def[req.UserID]
	} else if !a.member[req.UserID+"|"+org] {
		return tokenauthority.Minted{}, &accesstokenclient.RPCError{Code: "permission_denied", Message: "not a member", Reason: "not_org_member"}
	}
	req.OrgID = ""
	a.minted = append(a.minted, req)
	m, err := a.Memory.MintForUser(ctx, req)
	if err == nil {
		a.orgOf[m.Plaintext] = org
	}
	return m, err
}

func (a *orgAuthority) Introspect(ctx context.Context, token string) (*fat.Principal, error) {
	p, err := a.Memory.Introspect(ctx, token)
	if err == nil {
		p.OrgID = a.orgOf[token]
	}
	return p, err
}

func newOrgAuthority() *orgAuthority {
	return &orgAuthority{
		Memory: tokenauthority.NewMemory(),
		member: map[string]bool{"user-1|org-a": true, "user-1|org-b": true},
		def:    map[string]string{"user-1": "org-a"},
		orgOf:  map[string]string{},
	}
}

func exchangeIn(t *testing.T, a *orgAuthority, org *string) (*connect.Response[reliantv1.ExchangeTokenResponse], error) {
	t.Helper()
	svc := newTokenService(t, a, TokenControlPlane{Issuer: testControlPlane})
	bearer := mintSession(t, a, "user-1", tokenauthority.MintRequest{Scopes: fullDaemon})
	return svc.ExchangeToken(context.Background(), exchangeReq(bearer, &reliantv1.ExchangeTokenRequest{
		Audience: testControlPlane, Scopes: []string{"deploy:write"}, OrgId: org,
	}))
}

func ptr(s string) *string { return &s }

func TestExchangeToken_MintsForTheNamedOrgOfTheSamePerson(t *testing.T) {
	a := newOrgAuthority()
	resp, err := exchangeIn(t, a, ptr("org-b"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetOrgId() != "org-b" {
		t.Fatalf("response org = %q, want org-b", resp.Msg.GetOrgId())
	}
	if got := a.orgOf[resp.Msg.GetToken()]; got != "org-b" {
		t.Fatalf("token minted in %q, want org-b even though the session's default is org-a", got)
	}
}

func TestExchangeToken_NoOrgNamedStaysInTheSessionsOwn(t *testing.T) {
	a := newOrgAuthority()
	resp, err := exchangeIn(t, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetOrgId() != "org-a" || a.orgOf[resp.Msg.GetToken()] != "org-a" {
		t.Fatalf("org = %q / %q, want the session's own org-a", resp.Msg.GetOrgId(), a.orgOf[resp.Msg.GetToken()])
	}
}

func TestExchangeToken_AnOrgThePersonIsNotInMintsNothing(t *testing.T) {
	a := newOrgAuthority()
	before := len(a.minted)
	_, err := exchangeIn(t, a, ptr("org-c"))
	if svcerr.Code(err) != connect.CodePermissionDenied {
		t.Fatalf("err = %v, want PermissionDenied from the control plane's membership check", err)
	}
	// Only the session itself was minted (by the test), never an exchanged token.
	if len(a.minted) != before+1 {
		t.Fatalf("a refused exchange minted %d tokens", len(a.minted)-before-1)
	}
}

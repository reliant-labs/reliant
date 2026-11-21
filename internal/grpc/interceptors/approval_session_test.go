package interceptors

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	forgeauth "github.com/reliant-labs/forge/pkg/auth"
	"github.com/reliant-labs/forge/pkg/authn"
	"github.com/reliant-labs/forge/pkg/session"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/auth/authtest"
)

// approvalHarness is the API's user authentication with server-side sessions:
// the managed identity and Supabase in the chain, a real forge/pkg/session
// Manager over a memory store, and an rlat_ authority that grants
// approvals:decide.
type approvalHarness struct {
	interceptor *AuthInterceptor
	sessions    *session.Manager
	id          *authtest.Identity
	supa        *authtest.Supabase
}

func newApprovalHarness(t *testing.T, devCookie bool) *approvalHarness {
	t.Helper()
	supa, id := authtest.NewSupabase(t), authtest.NewIdentity(t)
	sessions, err := session.NewManager(session.Config{
		Store: session.NewMemoryStore(),
		Refresher: session.RefresherFunc(func(context.Context, string) (session.Tokens, error) {
			return session.Tokens{}, session.ErrRefreshRejected
		}),
	})
	require.NoError(t, err)
	interceptor, err := NewAuthInterceptor(auth.Settings{
		PublicKeyPEM:     supa.PublicKeyPEM,
		SupabaseIssuer:   supa.Issuer,
		SupabaseAudience: "authenticated",
		Identity:         auth.IdentitySettings{Issuer: id.Issuer, Audience: id.Audience, JWKSURL: id.JWKSURL},
		Sessions:         sessions,
		DevSessionCookie: devCookie,
	}, nil)
	require.NoError(t, err)
	interceptor.SetAccessTokenIntrospector(decideAuthority{})
	return &approvalHarness{interceptor: interceptor, sessions: sessions, id: id, supa: supa}
}

// signIn is what the login broker does: a server-side session holding the
// identity's tokens. It returns the session id the cookie carries.
func (h *approvalHarness) signIn(t *testing.T, sub string) string {
	t.Helper()
	issued, err := h.sessions.Create(context.Background(), session.Tokens{
		AccessToken: h.id.Sign(t, sub, nil),
		Expiry:      time.Now().Add(time.Hour),
	}, session.Meta{Subject: sub})
	require.NoError(t, err)
	return issued.ID
}

// decideAuthority resolves every rlat_ token to an unbound principal that
// holds approvals:decide (and reliant:api), the strongest token a person can
// mint for themselves.
type decideAuthority struct{}

func (decideAuthority) Introspect(_ context.Context, _ string) (*fat.Principal, error) {
	return &fat.Principal{TokenID: "tok-1", ActingUserID: "user-1", Scopes: fat.SetOf(fat.ScopeApprovalsDecide, fat.ScopeReliantAPI)}, nil
}

func cookie(name, value string) string { return name + "=" + value }

// TestOnlyABrowserSessionDecidesAnApproval is the owner's rule (2026-11-19:
// approvals need a cookie session; bearer and rlat_ tokens never approve) at
// the edge: of every way a request can present the same person's credential,
// only the server-side session id read from the sign-in cookie decides an
// approval. Supabase sessions keep deciding until the cutover (the bridge).
func TestOnlyABrowserSessionDecidesAnApproval(t *testing.T) {
	h := newApprovalHarness(t, false)
	sid := h.signIn(t, "user-1")
	identityToken := h.id.Sign(t, "user-1", nil)
	rlat := fat.Prefix + strings.Repeat("A1b2", 8)

	for _, tc := range []struct {
		name   string
		header http.Header
		// authErr: the request does not authenticate at all.
		authErr bool
		decides bool
		cred    auth.Credential
	}{
		{name: "the session id in the __Host- cookie", header: http.Header{"Cookie": {cookie(auth.SecureSessionCookieName, sid)}}, decides: true, cred: auth.CredentialBrowserSession},
		{name: "the session id among other cookies", header: http.Header{"Cookie": {"theme=dark; " + cookie(auth.SecureSessionCookieName, sid) + "; x=y"}}, decides: true, cred: auth.CredentialBrowserSession},
		{name: "a Supabase session bearer (until cutover)", header: http.Header{"Authorization": {"Bearer " + h.supa.Sign(t, "user-1", nil)}}, decides: true, cred: auth.CredentialSession},

		{name: "the identity's access token as a bearer", header: http.Header{"Authorization": {"Bearer " + identityToken}}, cred: auth.CredentialIdentityToken},
		{name: "the session id as a bearer", header: http.Header{"Authorization": {"Bearer " + sid}}, cred: auth.CredentialIdentityToken},
		{name: "an rlat_ token holding approvals:decide", header: http.Header{"Authorization": {"Bearer " + rlat}}, cred: auth.CredentialAccessToken},
		{name: "a bearer beside the session cookie: the bearer counts", header: http.Header{"Authorization": {"Bearer " + identityToken}, "Cookie": {cookie(auth.SecureSessionCookieName, sid)}}, cred: auth.CredentialIdentityToken},

		{name: "the identity's access token as the cookie", header: http.Header{"Cookie": {cookie(auth.SecureSessionCookieName, identityToken)}}, authErr: true},
		{name: "a Supabase session as the cookie", header: http.Header{"Cookie": {cookie(auth.SecureSessionCookieName, h.supa.Sign(t, "user-1", nil))}}, authErr: true},
		{name: "an rlat_ token as the cookie", header: http.Header{"Cookie": {cookie(auth.SecureSessionCookieName, rlat)}}, authErr: true},
		{name: "a session id no one issued", header: http.Header{"Cookie": {cookie(auth.SecureSessionCookieName, mustNewSessionID(t))}}, authErr: true},
		{name: "the plain-named cookie outside development", header: http.Header{"Cookie": {cookie(auth.SessionCookieName, sid)}}, authErr: true},
		{name: "a malformed Authorization header never falls through to the cookie", header: http.Header{"Authorization": {"Basic abc"}, "Cookie": {cookie(auth.SecureSessionCookieName, sid)}}, authErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, claims, err := h.interceptor.authenticateRequest(context.Background(), "/reliant.v1.ApprovalService/Approve", tc.header)
			if tc.authErr {
				require.Error(t, err, "it authenticated as %v", claims)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "user-1", claims.Sub)
			require.Equal(t, tc.cred, auth.CredentialFromContext(ctx))
			err = auth.RequireApprovalDecider(ctx, "decide an approval")
			if tc.decides {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Equal(t, auth.ReasonPersonRequired, svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader))
			if m, ok := authn.AuthMethod(ctx); ok {
				require.False(t, m.IsBrowserSession(), "a credential that may not decide passed as a browser session")
			}
		})
	}
}

// The browser session is exactly forge's notion of one: the cookie channel
// and a server-side session the store resolved.
func TestBrowserSessionCarriesForgesMethod(t *testing.T) {
	h := newApprovalHarness(t, false)
	sid := h.signIn(t, "user-1")
	ctx, _, err := h.interceptor.authenticateRequest(context.Background(), "/reliant.v1.ApprovalService/Approve",
		http.Header{"Cookie": {cookie(auth.SecureSessionCookieName, sid)}})
	require.NoError(t, err)
	m, ok := authn.AuthMethod(ctx)
	require.True(t, ok)
	require.Equal(t, authn.Method{Channel: authn.ChannelCookie, Credential: forgeauth.CredentialSession}, m)
	require.True(t, m.IsBrowserSession())
	require.NoError(t, auth.RequirePerson(ctx, "administer an organization"))
}

// Sign-out deletes the session: its cookie stops authenticating at once.
func TestASignedOutSessionNoLongerAuthenticates(t *testing.T) {
	h := newApprovalHarness(t, false)
	sid := h.signIn(t, "user-1")
	header := http.Header{"Cookie": {cookie(auth.SecureSessionCookieName, sid)}}
	_, _, err := h.interceptor.authenticateRequest(context.Background(), "/reliant.v1.ApprovalService/Approve", header)
	require.NoError(t, err)
	_, _, err = h.sessions.Revoke(context.Background(), sid)
	require.NoError(t, err)
	_, _, err = h.interceptor.authenticateRequest(context.Background(), "/reliant.v1.ApprovalService/Approve", header)
	require.Error(t, err)
}

// In local development (plain HTTP, where a browser refuses a __Host-
// cookie) the plain-named cookie is the sign-in session.
func TestDevReadsThePlainNamedCookie(t *testing.T) {
	h := newApprovalHarness(t, true)
	sid := h.signIn(t, "user-1")
	ctx, _, err := h.interceptor.authenticateRequest(context.Background(), "/reliant.v1.ApprovalService/Approve",
		http.Header{"Cookie": {cookie(auth.SessionCookieName, sid)}})
	require.NoError(t, err)
	require.NoError(t, auth.RequireApprovalDecider(ctx, "decide an approval"))
}

// Without a session store the cookie is refused outright: nothing can
// resolve it, and a token in it is never checked as a bearer.
func TestCookieRefusedWithoutSessions(t *testing.T) {
	id := authtest.NewIdentity(t)
	interceptor, err := NewAuthInterceptor(auth.Settings{
		Identity: auth.IdentitySettings{Issuer: id.Issuer, Audience: id.Audience, JWKSURL: id.JWKSURL},
	}, nil)
	require.NoError(t, err)
	_, _, err = interceptor.authenticateRequest(context.Background(), "/reliant.v1.ApprovalService/Approve",
		http.Header{"Cookie": {cookie(auth.SecureSessionCookieName, id.Sign(t, "user-1", nil))}})
	require.Error(t, err)
}

func mustNewSessionID(t *testing.T) string {
	t.Helper()
	id, err := session.NewID()
	require.NoError(t, err)
	return id
}

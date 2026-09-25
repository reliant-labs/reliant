package interceptors

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// mintDaemonToken mints an `rlat_` in authority acting as userID with scopes,
// optionally bound to a resource.
func mintDaemonToken(t *testing.T, authority *tokenauthority.Memory, userID string, res *fat.Resource, scopes ...fat.Scope) string {
	t.Helper()
	m, err := authority.MintForUser(context.Background(), tokenauthority.MintRequest{
		UserID: userID, Name: "daemon", Scopes: scopes, Resource: res,
	})
	require.NoError(t, err)
	return m.Plaintext
}

func bearer(token string) func(string) string {
	return func(key string) string {
		if key == "Authorization" {
			return "Bearer " + token
		}
		return ""
	}
}

func TestNewDaemonAuthInterceptorValidation(t *testing.T) {
	_, err := NewDaemonAuthInterceptor(nil)
	require.Error(t, err)

	interceptor, err := NewDaemonAuthInterceptor(tokenauthority.NewMemory())
	require.NoError(t, err)
	require.NotNil(t, interceptor)
}

func TestDaemonAuthInterceptorBoundToken(t *testing.T) {
	authority := tokenauthority.NewMemory()
	token := mintDaemonToken(t, authority, "user-123",
		&fat.Resource{Kind: fat.ResourceDaemon, ID: "daemon-42"}, fat.ScopeDaemonConnect)
	interceptor, err := NewDaemonAuthInterceptor(authority)
	require.NoError(t, err)

	ctx, err := interceptor.authenticate(context.Background(), bearer(token))
	require.NoError(t, err)
	userID, ok := auth.GetUserIDFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "user-123", userID)
	require.Equal(t, "daemon-42", auth.GetDaemonIDFromContext(ctx))
}

func TestDaemonAuthInterceptorUnboundToken(t *testing.T) {
	authority := tokenauthority.NewMemory()
	token := mintDaemonToken(t, authority, "user-123", nil, fat.ScopeDaemonConnect)
	interceptor, err := NewDaemonAuthInterceptor(authority)
	require.NoError(t, err)

	ctx, err := interceptor.authenticate(context.Background(), bearer(token))
	require.NoError(t, err)
	userID, _ := auth.GetUserIDFromContext(ctx)
	require.Equal(t, "user-123", userID)
	// An unbound token names no daemon.
	require.Equal(t, "", auth.GetDaemonIDFromContext(ctx))
}

func TestDaemonAuthInterceptorRejectsWrongScope(t *testing.T) {
	authority := tokenauthority.NewMemory()
	apiToken := mintDaemonToken(t, authority, "user-123", nil, fat.ScopeReliantAPI)
	interceptor, err := NewDaemonAuthInterceptor(authority)
	require.NoError(t, err)

	_, err = interceptor.authenticate(context.Background(), bearer(apiToken))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// principalIntrospector returns a fixed principal for any token — for shapes
// the grant rules refuse to mint, which the interceptor must still refuse.
type principalIntrospector struct{ p *fat.Principal }

func (s principalIntrospector) Introspect(context.Context, string) (*fat.Principal, error) {
	return s.p, nil
}

// forge/pkg/accesstoken refuses to mint daemon:connect bound to a non-daemon
// resource, so this principal cannot come from either store today. The
// interceptor still refuses it (defense in depth against a future authority).
func TestDaemonAuthInterceptorRejectsNonDaemonBinding(t *testing.T) {
	interceptor, err := NewDaemonAuthInterceptor(principalIntrospector{&fat.Principal{
		TokenID: "t", ActingUserID: "user-123", Scopes: fat.SetOf(fat.ScopeDaemonConnect),
		Resource: &fat.Resource{Kind: fat.ResourceConnector, ID: "c1"},
	}})
	require.NoError(t, err)
	minted, err := fat.Mint()
	require.NoError(t, err)

	_, err = interceptor.authenticate(context.Background(), bearer(minted.Plaintext))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestDaemonAuthInterceptorRejectsTokenWithoutActingUser(t *testing.T) {
	interceptor, err := NewDaemonAuthInterceptor(principalIntrospector{&fat.Principal{
		TokenID: "t", OrgID: "org-1", Scopes: fat.SetOf(fat.ScopeDaemonConnect),
	}})
	require.NoError(t, err)
	minted, err := fat.Mint()
	require.NoError(t, err)

	_, err = interceptor.authenticate(context.Background(), bearer(minted.Plaintext))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestDaemonAuthInterceptorRevokedIsUnauthenticated(t *testing.T) {
	authority := tokenauthority.NewMemory()
	m, err := authority.MintForUser(context.Background(), tokenauthority.MintRequest{
		UserID: "user-123", Name: "daemon", Scopes: []fat.Scope{fat.ScopeDaemonConnect},
	})
	require.NoError(t, err)
	interceptor, err := NewDaemonAuthInterceptor(authority)
	require.NoError(t, err)

	_, err = interceptor.authenticate(context.Background(), bearer(m.Plaintext))
	require.NoError(t, err)
	require.NoError(t, authority.RevokeForUser(context.Background(), "user-123", m.TokenID))

	// Uncached: the very next connect is refused.
	_, err = interceptor.authenticate(context.Background(), bearer(m.Plaintext))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestDaemonAuthInterceptorUnknownTokenIsUnauthenticated(t *testing.T) {
	interceptor, err := NewDaemonAuthInterceptor(tokenauthority.NewMemory())
	require.NoError(t, err)
	minted, err := fat.Mint()
	require.NoError(t, err)

	_, err = interceptor.authenticate(context.Background(), bearer(minted.Plaintext))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// An unreachable authority is our outage, not a bad credential: the daemon
// must retry rather than discard its token.
func TestDaemonAuthInterceptorOutageIsUnavailable(t *testing.T) {
	authority := tokenauthority.NewMemory()
	token := mintDaemonToken(t, authority, "user-123", nil, fat.ScopeDaemonConnect)
	tokenauthority.SetMemoryUnavailable(authority, true)
	interceptor, err := NewDaemonAuthInterceptor(authority)
	require.NoError(t, err)

	_, err = interceptor.authenticate(context.Background(), bearer(token))
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

func TestDaemonAuthInterceptorRejectsRetiredPAT(t *testing.T) {
	interceptor, err := NewDaemonAuthInterceptor(tokenauthority.NewMemory())
	require.NoError(t, err)
	_, err = interceptor.authenticate(context.Background(), bearer("rlnt_pat_AbCdEfGhIjKlMnOpQrStUvWxYz123456"))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestDaemonAuthInterceptorRejectsMissingHeader(t *testing.T) {
	interceptor, err := NewDaemonAuthInterceptor(tokenauthority.NewMemory())
	require.NoError(t, err)
	_, err = interceptor.authenticate(context.Background(), func(string) string { return "" })
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestDaemonAuthInterceptorRejectsInvalidHeader(t *testing.T) {
	interceptor, err := NewDaemonAuthInterceptor(tokenauthority.NewMemory())
	require.NoError(t, err)
	_, err = interceptor.authenticate(context.Background(), func(key string) string {
		if key == "Authorization" {
			return "some-token-without-bearer"
		}
		return ""
	})
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

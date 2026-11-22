package interceptors

import (
	"context"
	"testing"

	forgeauth "github.com/reliant-labs/forge/pkg/auth"
	"github.com/reliant-labs/forge/pkg/authn"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/auth/authtest"
)

// TestAuthInterceptorAcceptsTheManagedIdentityAsSoftware: with JWT_ISSUER
// set, the interceptor accepts the managed identity's tokens beside
// Supabase's, records how each authenticated (forge's authn.Method), and
// treats an identity token held as a bearer as software's: it is no person,
// and it never decides an approval.
func TestAuthInterceptorAcceptsTheManagedIdentityAsSoftware(t *testing.T) {
	supa, id := authtest.NewSupabase(t), authtest.NewIdentity(t)
	interceptor, err := NewAuthInterceptor(auth.Settings{
		PublicKeyPEM:     supa.PublicKeyPEM,
		SupabaseIssuer:   supa.Issuer,
		SupabaseAudience: "authenticated",
		Identity:         auth.IdentitySettings{Issuer: id.Issuer, Audience: id.Audience, JWKSURL: id.JWKSURL},
	}, nil)
	require.NoError(t, err)

	authenticate := func(t *testing.T, token string) (context.Context, *auth.JWTClaims, error) {
		return interceptor.authenticateRequest(context.Background(), "/reliant.v1.ChatService/ListChats",
			func(string) string { return "Bearer " + token })
	}

	t.Run("identity bearer", func(t *testing.T) {
		ctx, claims, err := authenticate(t, id.Sign(t, "user-1", nil))
		require.NoError(t, err)
		require.Equal(t, "user-1", claims.Sub)
		require.Equal(t, "user-1@example.com", claims.Email)
		require.Equal(t, auth.CredentialIdentityToken, auth.CredentialFromContext(ctx))
		m, ok := authn.AuthMethod(ctx)
		require.True(t, ok, "the interceptor records forge's authn.Method")
		require.Equal(t, authn.Method{Channel: authn.ChannelAuthorization, Credential: forgeauth.CredentialToken}, m)
		require.Error(t, auth.RequirePerson(ctx, "decide an approval"))
		require.Error(t, auth.RequireApprovalDecider(ctx, "decide an approval"))
	})

	t.Run("an unverified identity email is dropped", func(t *testing.T) {
		_, claims, err := authenticate(t, id.Sign(t, "user-1", map[string]any{"email_verified": false}))
		require.NoError(t, err)
		require.Empty(t, claims.Email)
	})

	t.Run("Supabase session", func(t *testing.T) {
		ctx, claims, err := authenticate(t, supa.Sign(t, "user-2", nil))
		require.NoError(t, err)
		require.Equal(t, "user-2", claims.Sub)
		require.Equal(t, auth.CredentialSession, auth.CredentialFromContext(ctx))
		m, ok := authn.AuthMethod(ctx)
		require.True(t, ok)
		require.False(t, m.IsBrowserSession())
		require.NoError(t, auth.RequirePerson(ctx, "decide an approval"))
	})

	t.Run("Supabase token with another issuer", func(t *testing.T) {
		_, _, err := authenticate(t, supa.Sign(t, "user-2", map[string]any{"iss": "https://elsewhere.supabase.co/auth/v1"}))
		require.Error(t, err)
	})

	t.Run("identity token for another project", func(t *testing.T) {
		_, _, err := authenticate(t, id.Sign(t, "user-1", map[string]any{"aud": []string{"another-project"}}))
		require.Error(t, err)
	})
}

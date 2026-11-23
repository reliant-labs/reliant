package terminal

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/stretchr/testify/require"
)

type loginCredentialValidator struct{ credential auth.Credential }

func (v loginCredentialValidator) ValidateToken(string) (*auth.JWTClaims, error) {
	return &auth.JWTClaims{Sub: terminalTestUserID, Credential: v.credential}, nil
}

func TestCLILoginCredentialAllowlist(t *testing.T) {
	for _, credential := range []auth.Credential{auth.CredentialUnknown, auth.CredentialSession, auth.CredentialSharedKey, auth.CredentialOAuthClient, auth.CredentialIdentityToken, auth.CredentialAccessToken} {
		t.Run(credential.String(), func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", "Bearer session-token")
			_, _, status, _ := authenticateCLILogin(req, loginCredentialValidator{credential}, nil)
			if credential == auth.CredentialSession || credential == auth.CredentialSharedKey {
				require.Zero(t, status)
			} else {
				require.Equal(t, http.StatusForbidden, status)
			}
		})
	}
}

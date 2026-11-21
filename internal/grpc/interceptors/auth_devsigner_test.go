package interceptors

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/runenv"
)

const devSignerSecret = "dev-local-hmac-shared-secret-do-not-use-in-prod"

func sessionPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// In the dev tier, a token minted with AUTH_DEV_JWT_SECRET (control-plane's
// `auth mint-dev-token`) authenticates an rpc the way a browser session does.
func TestAuthInterceptorAcceptsDevSignerInDev(t *testing.T) {
	runenv.ConfigureForTest(t, "dev")
	interceptor, err := NewAuthInterceptor(auth.Settings{PublicKeyPEM: sessionPEM(t), DevJWTSecret: devSignerSecret}, nil)
	require.NoError(t, err)

	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "dev-smoke-user", "email": "dev-smoke-user@dev.test", "email_verified": true,
		// A fixed exp no wall clock reaches: nothing here depends on today.
		"exp": time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC).Unix(),
	}).SignedString([]byte(devSignerSecret))
	require.NoError(t, err)

	_, claims, err := interceptor.authenticateRequest(context.Background(), "/reliant.v1.ChatService/ListChats",
		func(string) string { return "Bearer " + token })
	require.NoError(t, err)
	require.Equal(t, "dev-smoke-user", claims.Sub)
	require.Equal(t, auth.CredentialSession, claims.Credential)
}

// Outside the dev tier the interceptor is never built with a dev signer, so
// the api-server cannot start with one: there is no runtime switch for it.
func TestAuthInterceptorRefusesDevSignerOutsideDev(t *testing.T) {
	runenv.ConfigureForTest(t, "prod")
	_, err := NewAuthInterceptor(auth.Settings{PublicKeyPEM: sessionPEM(t), DevJWTSecret: devSignerSecret}, nil)
	if !errors.Is(err, auth.ErrDevSignerOutsideDev) {
		t.Fatalf("NewAuthInterceptor with AUTH_DEV_JWT_SECRET in prod: err = %v, want ErrDevSignerOutsideDev", err)
	}
}

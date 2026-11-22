// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/mcpserver"
)

const (
	mcpRoutesPublicURL = "https://api.example.com"
	mcpRoutesCP        = "https://cp.example.com"
)

// mcpOAuthAuthority introspects the one mcp:oauth token these tests present.
type mcpOAuthAuthority struct {
	token string
	p     *fat.Principal
}

func (a mcpOAuthAuthority) Introspect(_ context.Context, token string) (*fat.Principal, error) {
	if token == a.token {
		return a.p, nil
	}
	return nil, errors.New("inactive")
}

// noGrants is a user with no connectors and no consent recorded: an
// authenticated OAuth client is then sent to consent (403), never 401.
type noGrants struct{ connectorgrant.Store }

func (noGrants) ListBindingsByUser(context.Context, string) ([]*connectorgrant.ClientBinding, error) {
	return nil, nil
}

func (noGrants) ListGrantsByUser(context.Context, string) ([]*connectorgrant.Grant, error) {
	return nil, nil
}

// mcpOAuthRoutes is the API's plain-HTTP routes as internal/app wires them,
// with the given MCP_OAUTH_ISSUERS and MCP_OAUTH_SCOPES and the control plane
// as RELIANT_AUTHORIZATION_SERVER.
func mcpOAuthRoutes(t *testing.T, issuers, scopes []string, authority mcpOAuthAuthority) *httptest.Server {
	t.Helper()
	routes, err := NewAPIHTTPRoutes(APIHTTPRoutesDeps{
		Auth:                testUserAuth(t),
		Repo:                drainTestRoutesStore{},
		Router:              testRouter{},
		OAuth:               connections.NewOAuthHTTP(nil),
		Inbound:             webhook.NewInbound(nil, nil, nil, nil, ""),
		Grants:              noGrants{},
		Tokens:              authority,
		PublicURL:           mcpRoutesPublicURL,
		OAuthIssuers:        issuers,
		OAuthScopes:         scopes,
		AuthorizationServer: mcpRoutesCP + "/",
	})
	require.NoError(t, err)
	mux := http.NewServeMux()
	routes.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func mcpRoutesToken() string {
	return fat.Prefix + strings.Repeat("7", fat.TokenLen-len(fat.Prefix))
}

func mcpRoutesPrincipal(audience string) *fat.Principal {
	return &fat.Principal{
		ActingUserID: "user-1", Scopes: fat.SetOf(fat.ScopeMCPOAuth),
		ClientID: "mcp_abc", Audience: audience,
	}
}

func postMCP(t *testing.T, base, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+mcpserver.MountPath, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// RELIANT_AUTHORIZATION_SERVER listed in MCP_OAUTH_ISSUERS is the switch: the
// workspace connector then accepts an mcp:oauth token issued for its own
// resource URI (403: authenticated, consent needed), and nothing else.
func TestAPIRoutesAcceptMCPOAuthTokensOnlyWhenTheControlPlaneIsListed(t *testing.T) {
	token := mcpRoutesToken()
	resource := mcpRoutesPublicURL + mcpserver.MountPath
	for name, tc := range map[string]struct {
		issuers  []string
		audience string
		want     int
	}{
		"listed second":  {issuers: []string{"https://project.supabase.co", mcpRoutesCP}, audience: resource, want: http.StatusForbidden},
		"not listed":     {issuers: []string{"https://project.supabase.co"}, audience: resource, want: http.StatusUnauthorized},
		"none":           {audience: resource, want: http.StatusUnauthorized},
		"forge audience": {issuers: []string{mcpRoutesCP}, audience: mcpRoutesPublicURL + "/forge/mcp", want: http.StatusUnauthorized},
		"another host":   {issuers: []string{mcpRoutesCP}, audience: "https://other.example.com/mcp", want: http.StatusUnauthorized},
		"no audience":    {issuers: []string{mcpRoutesCP}, audience: "", want: http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			srv := mcpOAuthRoutes(t, tc.issuers, nil, mcpOAuthAuthority{token: token, p: mcpRoutesPrincipal(tc.audience)})
			require.Equal(t, tc.want, postMCP(t, srv.URL, token))
		})
	}
}

// The discovery document advertises MCP_OAUTH_ISSUERS in its order and
// MCP_OAUTH_SCOPES, and "openid" when no scopes are configured.
func TestAPIRoutesAdvertiseTheConfiguredIssuersAndScopes(t *testing.T) {
	issuers := []string{"https://project.supabase.co", mcpRoutesCP}
	for name, tc := range map[string]struct{ scopes, want []string }{
		"configured": {scopes: []string{"openid", "mcp:oauth"}, want: []string{"openid", "mcp:oauth"}},
		"default":    {want: []string{"openid"}},
	} {
		t.Run(name, func(t *testing.T) {
			srv := mcpOAuthRoutes(t, issuers, tc.scopes, mcpOAuthAuthority{})
			resp, err := http.Get(srv.URL + mcpserver.WellKnownProtectedResourcePath)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			var doc struct {
				Resource             string   `json:"resource"`
				AuthorizationServers []string `json:"authorization_servers"`
				ScopesSupported      []string `json:"scopes_supported"`
			}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
			require.Equal(t, mcpRoutesPublicURL+mcpserver.MountPath, doc.Resource)
			require.Equal(t, issuers, doc.AuthorizationServers)
			require.Equal(t, tc.want, doc.ScopesSupported)
		})
	}
}

// The connector's JWT validator is the deployment's Supabase validator alone:
// a managed-identity (Zitadel) operator token is refused even when that
// identity is configured for the rest of the API, so an operator JWT can
// never act at /mcp. A Supabase token from the same settings is accepted, so
// the refusal is not vacuous.
func TestConnectorValidatorRefusesAManagedIdentityOperatorJWT(t *testing.T) {
	supabase, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(&supabase.PublicKey)
	require.NoError(t, err)
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))

	operator, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	const zitadel = "https://identity.example.com"

	validator := connectorTokenValidator(auth.Settings{
		PublicKeyPEM: pubPEM,
		Identity:     auth.IdentitySettings{Issuer: zitadel, Audience: "project-1", JWKSURL: "https://identity.example.com/oauth/v2/keys"},
	})
	require.NotNil(t, validator)
	clientValidator, ok := validator.(mcpserver.OAuthClientValidator)
	require.True(t, ok)

	now := time.Now()
	opTok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": zitadel, "aud": "project-1", "sub": "operator-1", "client_id": "mcp_abc",
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
		"urn:zitadel:iam:org:project:roles": map[string]any{"operator": map[string]any{}},
	})
	opTok.Header["kid"] = "identity-1"
	signedOp, err := opTok.SignedString(operator)
	require.NoError(t, err)
	_, _, err = clientValidator.ValidateTokenClient(signedOp)
	require.Error(t, err, "an operator JWT must never authenticate at the connector")

	userTok, err := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"sub": "user-1", "client_id": "chatgpt", "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
	}).SignedString(supabase)
	require.NoError(t, err)
	user, client, err := clientValidator.ValidateTokenClient(userTok)
	require.NoError(t, err)
	require.Equal(t, "user-1", user)
	require.Equal(t, "chatgpt", client)
}

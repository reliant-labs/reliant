package gitcredentialclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
)

const secret = "test-internal-service-secret"

// fakeCP is control-plane's GitCredentialInternalService as the wire shows
// it: Connect unary over HTTP+JSON, an internal-service bearer required, and
// FailedPrecondition told apart by the X-Forge-Error-Reason header. The shapes
// here are pinned on control-plane's side by
// internal/handlers/git_credential_internal/wire_json_test.go.
type fakeCP struct {
	t        *testing.T
	gotUser  string
	gotProv  string
	gotPath  string
	respond  func(w http.ResponseWriter)
	requests int
}

func (f *fakeCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests++
	f.gotPath = r.URL.Path
	// The bearer must be a valid internal-service JWT, or control-plane
	// answers 403 — reproduce that so a client sending none fails here.
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	tok, err := jwt.Parse(bearer, func(*jwt.Token) (any, error) { return []byte(secret), nil })
	if err != nil || !tok.Valid {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"code":"permission_denied","message":"GetUserAccessToken is internal-service only"}`)
		return
	}
	claims := tok.Claims.(jwt.MapClaims)
	if claims["sub"] != auth.InternalServiceSubject || claims["role"] != auth.InternalServiceRole {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Connect-Protocol-Version") != "1" {
		f.t.Errorf("not a Connect unary JSON request: %v", r.Header)
	}
	var in struct {
		UserID   string `json:"userId"`
		Provider string `json:"provider"`
	}
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&in))
	f.gotUser, f.gotProv = in.UserID, in.Provider
	f.respond(w)
}

func connectError(status int, code, reason, msg string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		if reason != "" {
			w.Header().Set("X-Forge-Error-Reason", reason)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"code":%q,"message":%q}`, code, msg)
	}
}

func newClient(t *testing.T, cp *fakeCP) gitcredentialclient.Service {
	t.Helper()
	cp.t = t
	srv := httptest.NewServer(cp)
	t.Cleanup(srv.Close)
	return gitcredentialclient.New(gitcredentialclient.Deps{
		ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL + "/"},
		Sign:         func() (string, error) { return auth.SignInternalServiceToken(secret) },
	})
}

func TestUserAccessToken_ReturnsTokenAndExpiry(t *testing.T) {
	expires := time.Now().Add(8 * time.Hour).UTC().Truncate(time.Second)
	cp := &fakeCP{respond: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"accessToken":"ghu_fresh_from_cp","expiresAt":%q}`, expires.Format(time.RFC3339Nano))
	}}
	c := newClient(t, cp)

	tok, err := c.UserAccessToken(context.Background(), "idp|alice", "github")
	require.NoError(t, err)
	assert.Equal(t, "/controlplane.v1.GitCredentialInternalService/GetUserAccessToken", cp.gotPath)
	assert.Equal(t, "idp|alice", cp.gotUser, "the EXTERNAL id is sent as-is")
	assert.Equal(t, "github", cp.gotProv)
	var plain string
	require.NoError(t, tok.Use(func(s string) error { plain = s; return nil }))
	assert.Equal(t, "ghu_fresh_from_cp", plain)
	require.NotNil(t, tok.ExpiresAt)
	assert.True(t, tok.ExpiresAt.Equal(expires))

	// A Token never prints its plaintext.
	for _, rendered := range []string{fmt.Sprint(tok), fmt.Sprintf("%v %+v %#v %s", tok, tok, tok, tok)} {
		assert.NotContains(t, rendered, "ghu_fresh_from_cp")
	}
}

func TestUserAccessToken_NonExpiringTokenHasNoExpiry(t *testing.T) {
	c := newClient(t, &fakeCP{respond: func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, `{"accessToken":"ghp_pat"}`)
	}})
	tok, err := c.UserAccessToken(context.Background(), "idp|alice", "github")
	require.NoError(t, err)
	assert.Nil(t, tok.ExpiresAt)
}

func TestUserAccessToken_TypedPreconditions(t *testing.T) {
	cases := map[string]struct {
		respond func(w http.ResponseWriter)
		want    error
	}{
		"not connected":   {connectError(http.StatusBadRequest, "failed_precondition", "git_credential_not_connected", "no github credential is connected"), gitcredentialclient.ErrNotConnected},
		"needs reconnect": {connectError(http.StatusBadRequest, "failed_precondition", "git_credential_needs_reconnect", "github connection needs to be reconnected"), gitcredentialclient.ErrNeedsReconnect},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newClient(t, &fakeCP{respond: tc.respond})
			_, err := c.UserAccessToken(context.Background(), "idp|alice", "github")
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// Anything that is not one of the two typed outcomes is an RPCError, never
// mistaken for "not connected" — an outage must not tell the user to connect.
func TestUserAccessToken_OtherErrorsAreNotPreconditions(t *testing.T) {
	c := newClient(t, &fakeCP{respond: connectError(http.StatusServiceUnavailable, "unavailable", "", "could not refresh github token")})
	_, err := c.UserAccessToken(context.Background(), "idp|alice", "github")
	var rpc *gitcredentialclient.RPCError
	require.True(t, errors.As(err, &rpc), "got %v", err)
	assert.Equal(t, "unavailable", rpc.Code)
	assert.NotErrorIs(t, err, gitcredentialclient.ErrNotConnected)
	assert.NotErrorIs(t, err, gitcredentialclient.ErrNeedsReconnect)
}

func TestUserAccessToken_SendsTheInternalServiceBearer(t *testing.T) {
	cp := &fakeCP{respond: func(w http.ResponseWriter) { _, _ = io.WriteString(w, `{"accessToken":"ghu_x"}`) }}
	cp.t = t
	srv := httptest.NewServer(cp)
	defer srv.Close()
	unsigned := gitcredentialclient.New(gitcredentialclient.Deps{
		ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL},
		Sign:         func() (string, error) { return auth.SignInternalServiceToken("the-wrong-secret") },
	})
	_, err := unsigned.UserAccessToken(context.Background(), "idp|alice", "github")
	var rpc *gitcredentialclient.RPCError
	require.True(t, errors.As(err, &rpc), "a wrongly-signed call must be refused, got %v", err)
	assert.Equal(t, "permission_denied", rpc.Code)
}

// A decode failure must not echo the body, which carries a token.
func TestUserAccessToken_MalformedBodyIsNotEchoed(t *testing.T) {
	c := newClient(t, &fakeCP{respond: func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, `{"accessToken":"ghu_secret_in_a_broken_body",`)
	}})
	_, err := c.UserAccessToken(context.Background(), "idp|alice", "github")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "ghu_secret")
}

func TestUserAccessToken_RefusesLocally(t *testing.T) {
	cp := &fakeCP{respond: func(http.ResponseWriter) {}}
	c := newClient(t, cp)
	_, err := c.UserAccessToken(context.Background(), " ", "github")
	require.Error(t, err)

	none := gitcredentialclient.New(gitcredentialclient.Deps{Sign: func() (string, error) { return "x", nil }})
	_, err = none.UserAccessToken(context.Background(), "idp|alice", "github")
	require.Error(t, err)
	assert.Zero(t, cp.requests, "nothing reached control-plane")
}

// fakeMint is control-plane's MintInstallationToken as the wire shows it. The
// JSON names and reasons are pinned on control-plane's side by
// internal/handlers/git_credential_internal/mint_installation_token_test.go.
type fakeMint struct {
	t       *testing.T
	gotPath string
	got     map[string]any
	respond func(w http.ResponseWriter)
}

func (f *fakeMint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.gotPath = r.URL.Path
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok, err := jwt.Parse(bearer, func(*jwt.Token) (any, error) { return []byte(secret), nil }); err != nil || !tok.Valid {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&f.got))
	f.respond(w)
}

func newMintClient(t *testing.T, f *fakeMint) gitcredentialclient.Service {
	t.Helper()
	f.t = t
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return gitcredentialclient.New(gitcredentialclient.Deps{
		ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL},
		Sign:         func() (string, error) { return auth.SignInternalServiceToken(secret) },
	})
}

func TestMintInstallationToken_SendsTheRequestAndReturnsTheToken(t *testing.T) {
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	f := &fakeMint{respond: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"token":"ghs_install","expiresAt":%q,"botLogin":"reliant-labs[bot]","actingForLogin":"alice","repository":"acme/widgets","permissions":{"issues":"write"}}`,
			expires.Format(time.RFC3339Nano))
	}}
	c := newMintClient(t, f)

	got, err := c.MintInstallationToken(context.Background(), gitcredentialclient.InstallationRequest{
		ExternalUserID: "idp|alice", Owner: "acme", Repo: "widgets", Permissions: map[string]string{"issues": "write"},
	})
	require.NoError(t, err)
	assert.Equal(t, "/controlplane.v1.GitCredentialInternalService/MintInstallationToken", f.gotPath)
	assert.Equal(t, "idp|alice", f.got["userId"], "the EXTERNAL id is sent as-is")
	assert.Equal(t, "acme", f.got["owner"])
	assert.Equal(t, "widgets", f.got["repo"])
	assert.Equal(t, map[string]any{"issues": "write"}, f.got["permissions"])

	var plain string
	require.NoError(t, got.Token.Use(func(s string) error { plain = s; return nil }))
	assert.Equal(t, "ghs_install", plain)
	assert.Equal(t, "reliant-labs[bot]", got.BotLogin)
	assert.Equal(t, "alice", got.ActingForLogin)
	assert.Equal(t, "acme/widgets", got.Repository)
	assert.True(t, got.ExpiresAt.Equal(expires))
	assert.NotContains(t, fmt.Sprintf("%v %+v %#v", got, got, got), "ghs_install", "an InstallationToken never prints its token")
}

func TestMintInstallationToken_OmitsPermissionsWhenNoneAsked(t *testing.T) {
	f := &fakeMint{respond: func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, `{"token":"ghs_x","expiresAt":"2099-01-01T00:00:00Z"}`)
	}}
	c := newMintClient(t, f)
	_, err := c.MintInstallationToken(context.Background(), gitcredentialclient.InstallationRequest{ExternalUserID: "u", Owner: "o", Repo: "r"})
	require.NoError(t, err)
	_, present := f.got["permissions"]
	assert.False(t, present, "an empty set means control-plane's default, not an empty grant")
}

func TestMintInstallationToken_TypedOutcomes(t *testing.T) {
	cases := map[string]struct {
		status int
		code   string
		reason string
		want   error
	}{
		"app not configured": {http.StatusBadRequest, "failed_precondition", "github_app_not_configured", gitcredentialclient.ErrAppNotConfigured},
		"app not installed":  {http.StatusBadRequest, "failed_precondition", "github_app_not_installed", gitcredentialclient.ErrAppNotInstalled},
		"app permissions":    {http.StatusBadRequest, "failed_precondition", "github_app_permissions", gitcredentialclient.ErrAppPermissions},
		"owner cannot push":  {http.StatusForbidden, "permission_denied", "github_owner_cannot_push", gitcredentialclient.ErrOwnerCannotPush},
		"not connected":      {http.StatusBadRequest, "failed_precondition", "git_credential_not_connected", gitcredentialclient.ErrNotConnected},
		"needs reconnect":    {http.StatusBadRequest, "failed_precondition", "git_credential_needs_reconnect", gitcredentialclient.ErrNeedsReconnect},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newMintClient(t, &fakeMint{respond: connectError(tc.status, tc.code, tc.reason, "msg")})
			_, err := c.MintInstallationToken(context.Background(), gitcredentialclient.InstallationRequest{ExternalUserID: "u", Owner: "o", Repo: "r"})
			assert.ErrorIs(t, err, tc.want)
			var rpc *gitcredentialclient.RPCError
			assert.False(t, errors.As(err, &rpc), "a typed outcome is not a generic RPCError")
		})
	}
}

func TestMintInstallationToken_OutageIsTransientNotAPrecondition(t *testing.T) {
	c := newMintClient(t, &fakeMint{respond: connectError(http.StatusServiceUnavailable, "unavailable", "", "try again")})
	_, err := c.MintInstallationToken(context.Background(), gitcredentialclient.InstallationRequest{ExternalUserID: "u", Owner: "o", Repo: "r"})
	var rpc *gitcredentialclient.RPCError
	require.True(t, errors.As(err, &rpc), "got %v", err)
	assert.Equal(t, "unavailable", rpc.Code)
	for _, typed := range []error{
		gitcredentialclient.ErrAppNotConfigured, gitcredentialclient.ErrAppNotInstalled, gitcredentialclient.ErrAppPermissions,
		gitcredentialclient.ErrOwnerCannotPush, gitcredentialclient.ErrNotConnected, gitcredentialclient.ErrNeedsReconnect,
	} {
		assert.NotErrorIs(t, err, typed)
	}
}

func TestMintInstallationToken_RefusesLocallyAndNeverCalls(t *testing.T) {
	calls := 0
	f := &fakeMint{respond: func(http.ResponseWriter) { calls++ }}
	c := newMintClient(t, f)
	for _, req := range []gitcredentialclient.InstallationRequest{
		{Owner: "o", Repo: "r"}, {ExternalUserID: "u", Repo: "r"}, {ExternalUserID: "u", Owner: "o"}, {ExternalUserID: " ", Owner: "o", Repo: "r"},
	} {
		_, err := c.MintInstallationToken(context.Background(), req)
		require.Error(t, err)
	}
	assert.Zero(t, calls)
}

func TestMintInstallationToken_MalformedBodyIsNotEchoed(t *testing.T) {
	c := newMintClient(t, &fakeMint{respond: func(w http.ResponseWriter) { _, _ = io.WriteString(w, `{"token":"ghs_secret_in_a_broken_body",`) }})
	_, err := c.MintInstallationToken(context.Background(), gitcredentialclient.InstallationRequest{ExternalUserID: "u", Owner: "o", Repo: "r"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "ghs_secret")
}

func TestMintInstallationToken_EmptyTokenIsAnError(t *testing.T) {
	c := newMintClient(t, &fakeMint{respond: func(w http.ResponseWriter) { _, _ = io.WriteString(w, `{"token":""}`) }})
	_, err := c.MintInstallationToken(context.Background(), gitcredentialclient.InstallationRequest{ExternalUserID: "u", Owner: "o", Repo: "r"})
	require.Error(t, err)
}

func TestMintInstallationToken_SendsTheInternalServiceBearer(t *testing.T) {
	f := &fakeMint{respond: func(w http.ResponseWriter) { _, _ = io.WriteString(w, `{"token":"ghs_x"}`) }}
	f.t = t
	srv := httptest.NewServer(f)
	defer srv.Close()
	wrong := gitcredentialclient.New(gitcredentialclient.Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL}, Sign: func() (string, error) { return auth.SignInternalServiceToken("the-wrong-secret") }})
	_, err := wrong.MintInstallationToken(context.Background(), gitcredentialclient.InstallationRequest{ExternalUserID: "u", Owner: "o", Repo: "r"})
	var rpc *gitcredentialclient.RPCError
	require.True(t, errors.As(err, &rpc), "a wrongly-signed call must be refused, got %v", err)
}

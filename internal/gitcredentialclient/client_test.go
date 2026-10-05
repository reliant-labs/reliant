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

func newClient(t *testing.T, cp *fakeCP) *gitcredentialclient.Client {
	t.Helper()
	cp.t = t
	srv := httptest.NewServer(cp)
	t.Cleanup(srv.Close)
	return gitcredentialclient.New(gitcredentialclient.Deps{
		BaseURL: srv.URL + "/",
		Sign:    func() (string, error) { return auth.SignInternalServiceToken(secret) },
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
		BaseURL: srv.URL,
		Sign:    func() (string, error) { return auth.SignInternalServiceToken("the-wrong-secret") },
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

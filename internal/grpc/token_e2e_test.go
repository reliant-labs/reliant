// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"

	"github.com/reliant-labs/reliant/internal/auth"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v5"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/handlers/token"
	"github.com/reliant-labs/reliant/internal/handlers/tools_daemon"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/pkg/config"
	"github.com/reliant-labs/reliant/pkg/middleware"
)

// ── End-to-end: ONE credential, minted by reliant, owned by control-plane ──
//
// TestDaemonCredential_HostedEndToEnd proves the hosted chain with nothing
// between the pieces faked except control-plane's STORAGE:
//
//	user session ─► reliant TokenService (behind the real API AuthInterceptor)
//	             ─► tokenauthority (selected from the environment, as in prod)
//	             ─► accesstokenclient over HTTP ─► control-plane
//	             ◄─ `rlat_`
//	daemon ─► real daemon listener (NewDaemonListener, h2c, DaemonAuthInterceptor)
//	       ─► Introspect over HTTP ─► control-plane ─► registered
//	user revokes through TokenService ─► next connect is refused
//
// "control-plane" is fakeControlPlane: an HTTP server speaking
// AccessTokenInternalService's Connect-JSON wire contract (the protojson key
// names control-plane pins in internal/handlers/access_token_internal's
// wire_json_test), verifying the HS256 internal-service bearer with the shared
// secret exactly as control-plane does, and backed by tokenauthority.Memory —
// which applies forge/pkg/accesstoken's real grant rules and is held to
// LocalStore's behaviour by tokenauthority's conformance suite.

// fakeControlPlane serves AccessTokenInternalService over Connect JSON.
type fakeControlPlane struct {
	t          *testing.T
	secret     string
	store      *tokenauthority.Memory
	introspect atomic.Int64
	// grants is each user's org permissions: MintForUser clips to them;
	// UpdateForUser refuses any excess (control-plane's org_member_grants).
	grants map[string]fat.Set
}

type wireResource struct {
	Kind string `json:"kind,omitempty"`
	ID   string `json:"id,omitempty"`
}

func (w *wireResource) resource() *fat.Resource {
	if w == nil || w.Kind == "" {
		return nil
	}
	return &fat.Resource{Kind: fat.ResourceKind(w.Kind), ID: w.ID}
}

func wireOf(r *fat.Resource) *wireResource {
	if r == nil {
		return nil
	}
	return &wireResource{Kind: string(r.Kind), ID: r.ID}
}

func (cp *fakeControlPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method, ok := strings.CutPrefix(r.URL.Path, accesstokenclient.ServicePath)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !cp.authorized(r.Header.Get("Authorization")) {
		writeConnectError(w, http.StatusUnauthorized, "unauthenticated", "internal-service token required")
		return
	}
	raw, _ := io.ReadAll(r.Body)
	ctx := r.Context()

	var out any
	var err error
	switch method {
	case "Introspect":
		cp.introspect.Add(1)
		var in struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal(raw, &in)
		p, ierr := cp.store.Introspect(ctx, in.Token)
		switch {
		case errors.Is(ierr, tokenauthority.ErrInactive):
			out = map[string]any{} // protojson omits active=false
		case ierr != nil:
			err = ierr
		default:
			out = map[string]any{
				"active": true, "tokenId": p.TokenID, "orgId": p.OrgID, "actingUserId": p.ActingUserID,
				"scopes": p.Scopes.Strings(), "resource": wireOf(p.Resource), "ephemeral": p.Ephemeral,
				"expiresAt": p.ExpiresAt,
			}
		}
	case "MintForUser":
		var in struct {
			UserID    string        `json:"userId"`
			Name      string        `json:"name"`
			Scopes    []string      `json:"scopes"`
			Resource  *wireResource `json:"resource"`
			Ephemeral bool          `json:"ephemeral"`
			ExpiresAt *time.Time    `json:"expiresAt"`
			Rotate    bool          `json:"rotate"`
		}
		_ = json.Unmarshal(raw, &in)
		scopes := make([]fat.Scope, 0, len(in.Scopes))
		requested, serr := fat.NewSet(in.Scopes)
		if serr != nil {
			writeConnectError(w, http.StatusBadRequest, "invalid_argument", serr.Error())
			return
		}
		for s := range cp.clipToGrants(in.UserID, requested) {
			scopes = append(scopes, s)
		}
		m, merr := cp.store.MintForUser(ctx, tokenauthority.MintRequest{UserID: in.UserID, Name: in.Name, Scopes: scopes,
			Resource: in.Resource.resource(), Ephemeral: in.Ephemeral, ExpiresAt: in.ExpiresAt, Rotate: in.Rotate})
		if merr != nil {
			writeConnectError(w, http.StatusBadRequest, "invalid_argument", merr.Error())
			return
		}
		out = map[string]any{"tokenId": m.TokenID, "secret": m.Plaintext, "displayPrefix": m.DisplayPrefix,
			"expiresAt": m.ExpiresAt, "rotated": m.Rotated, "scopes": m.Scopes}
	case "ListForUser":
		var in struct {
			UserID string `json:"userId"`
			Scope  string `json:"scope"`
		}
		_ = json.Unmarshal(raw, &in)
		infos, lerr := cp.store.ListForUser(ctx, in.UserID, fat.Scope(in.Scope))
		err = lerr
		tokens := make([]map[string]any, 0, len(infos))
		for _, i := range infos {
			tokens = append(tokens, map[string]any{"id": i.ID, "name": i.Name, "displayPrefix": i.DisplayPrefix,
				"scopes": i.Scopes, "resource": wireOf(i.Resource), "ephemeral": i.Ephemeral,
				"createdAt": i.CreatedAt, "expiresAt": i.ExpiresAt, "lastUsedAt": i.LastUsedAt})
		}
		out = map[string]any{"tokens": tokens}
	case "UpdateForUser":
		var in struct {
			UserID  string  `json:"userId"`
			TokenID string  `json:"tokenId"`
			Name    *string `json:"name"`
			Scopes  *struct {
				Scopes []string `json:"scopes"`
			} `json:"scopes"`
		}
		_ = json.Unmarshal(raw, &in)
		var scopes fat.Set
		if in.Scopes != nil {
			set, serr := fat.NewSet(in.Scopes.Scopes)
			if serr != nil {
				writeConnectError(w, http.StatusBadRequest, "invalid_argument", serr.Error())
				return
			}
			if !cp.clipToGrants(in.UserID, set).Covers(set) {
				writeConnectError(w, http.StatusForbidden, "permission_denied", "scope exceeds current org grants")
				return
			}
			scopes = set
		}
		info, uerr := cp.store.UpdateForUser(ctx, in.UserID, in.TokenID, in.Name, scopes)
		if uerr != nil {
			writeConnectError(w, http.StatusBadRequest, "invalid_argument", uerr.Error())
			return
		}
		out = map[string]any{"token": map[string]any{"id": info.ID, "name": info.Name, "displayPrefix": info.DisplayPrefix,
			"scopes": info.Scopes, "resource": wireOf(info.Resource), "ephemeral": info.Ephemeral,
			"createdAt": info.CreatedAt, "expiresAt": info.ExpiresAt, "lastUsedAt": info.LastUsedAt}}
	case "RevokeForUser":
		var in struct {
			UserID  string `json:"userId"`
			TokenID string `json:"tokenId"`
		}
		_ = json.Unmarshal(raw, &in)
		if rerr := cp.store.RevokeForUser(ctx, in.UserID, in.TokenID); errors.Is(rerr, tokenauthority.ErrNotFound) {
			writeConnectError(w, http.StatusNotFound, "not_found", "token not found")
			return
		} else {
			err = rerr
		}
		out = map[string]any{}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeConnectError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// clipToGrants is control-plane's MintForUser rule: an org-administration
// scope survives only if the user holds it as a grant; session scopes pass.
// A nil grants map grants everything (no clipping configured).
func (cp *fakeControlPlane) clipToGrants(userID string, requested fat.Set) fat.Set {
	if cp.grants == nil {
		return requested
	}
	held := cp.grants[userID]
	out := fat.Set{}
	for s := range requested {
		switch s {
		case fat.ScopeDaemonConnect, fat.ScopeReliantAPI, fat.ScopeLLMInvoke:
			out[s] = struct{}{}
		default:
			if held.Has(s) {
				out[s] = struct{}{}
			}
		}
	}
	return out
}

// authorized verifies the internal-service bearer the way control-plane's
// validator does: HS256 over the shared secret, sub internal-service.
func (cp *fakeControlPlane) authorized(header string) bool {
	raw, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return false
	}
	tok, err := jwt.Parse(raw, func(*jwt.Token) (any, error) { return []byte(cp.secret), nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithAudience("authenticated"), jwt.WithIssuer("control-plane"))
	if err != nil || !tok.Valid {
		return false
	}
	sub, _ := tok.Claims.GetSubject()
	return sub == "internal-service"
}

func writeConnectError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": msg})
}

// sessionSigner mints the ES256 session JWTs reliant's API validates.
type sessionSigner struct {
	key    *ecdsa.PrivateKey
	pubPEM string
}

func newSessionSigner(t *testing.T) *sessionSigner {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	return &sessionSigner{key: key, pubPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}
}

func (s *sessionSigner) sign(t *testing.T, userID string) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"sub": userID, "role": "authenticated", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
	}).SignedString(s.key)
	require.NoError(t, err)
	return tok
}

// serveTokenService mounts reliant's TokenService behind the REAL API auth
// interceptor, with the authority's introspector wired exactly as the api
// root's user chain wires it (cached, NewUserChain) and the same public procedures (ExchangeToken and
// GetGitToken among them).
func serveTokenService(t *testing.T, authority tokenauthority.Authority, jwtPubPEM string, cp token.TokenControlPlane) string {
	t.Helper()
	authInterceptor, err := interceptors.NewAuthInterceptor(auth.Settings{PublicKeyPEM: jwtPubPEM}, middleware.UnauthenticatedProcedures)
	require.NoError(t, err)
	authInterceptor.SetAccessTokenIntrospector(accesstokenclient.NewCachedIntrospector(authority))
	tokenService, err := token.New(token.Deps{
		Logger:       slog.New(slog.DiscardHandler),
		Config:       &config.Config{},
		Authority:    authority,
		ControlPlane: cp,
	})
	require.NoError(t, err)
	mux := http.NewServeMux()
	// svcerr.Interceptor innermost, as serverkit.NewChain installs it in
	// production: it gives the handler's svcerr errors their Connect code.
	path, handler := reliantv1connect.NewTokenServiceHandler(tokenService,
		connect.WithInterceptors(authInterceptor, svcerr.Interceptor()))
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// serveGateway starts the REAL daemon gateway listener (NewDaemonListener)
// over h2c, authenticating daemons through authority.
func serveGateway(t *testing.T, authority tokenauthority.Authority) string {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	gateway, err := tools_daemon.New(tools_daemon.Deps{Logger: slog.Default(), Config: &config.Config{}, Repo: repo})
	require.NoError(t, err)
	return serveDaemonListener(t, newTestDaemonListener(t, authority, gateway))
}

// connectDaemon opens ConnectDaemon with bearer, registers, and returns the
// first server message or the error the gateway answered with.
func connectDaemon(t *testing.T, gatewayURL, bearer string) (*reliantv1.ServerMessage, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := reliantv1connect.NewToolsDaemonServiceClient(priorKnowledgeClient(), gatewayURL, connect.WithGRPC())
	stream := client.ConnectDaemon(ctx)
	stream.RequestHeader().Set("Authorization", "Bearer "+bearer)
	defer func() {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
	}()
	if err := stream.Send(&reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_Register{
		Register: &reliantv1.DaemonRegister{Hostname: "e2e-host", Platform: "linux", DaemonType: "local"},
	}}); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return stream.Receive()
}

func TestDaemonCredential_HostedEndToEnd(t *testing.T) {
	const (
		secret = "e2e-internal-service-secret"
		userID = "user-e2e-hosted"
	)
	cp := &fakeControlPlane{t: t, secret: secret, store: tokenauthority.NewMemory()}
	cpSrv := httptest.NewServer(cp)
	t.Cleanup(cpSrv.Close)

	// Select the authority from the configured control plane, exactly as the
	// api-server and the gateway do. A configured control-plane MUST win.
	authority, mode, err := tokenauthority.New(tokenauthority.Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: cpSrv.URL, InternalServiceSecret: secret}})
	require.NoError(t, err)
	require.Equal(t, tokenauthority.ModeControlPlane, mode)

	signer := newSessionSigner(t)
	apiURL := serveTokenService(t, authority, signer.pubPEM, token.TokenControlPlane{})
	gatewayURL := serveGateway(t, authority)

	tokens := func(bearer string) reliantv1connect.TokenServiceClient {
		return reliantv1connect.NewTokenServiceClient(&http.Client{Transport: bearerTransport(bearer)}, apiURL)
	}
	session := signer.sign(t, userID)

	// 1. Mint through reliant's facade with an interactive session.
	created, err := tokens(session).CreateToken(context.Background(), connect.NewRequest(&reliantv1.CreateTokenRequest{
		Name: "laptop", Kind: reliantv1.TokenKind_TOKEN_KIND_DAEMON,
	}))
	require.NoError(t, err)
	daemonToken := created.Msg.GetToken()
	require.True(t, fat.HasFormat(daemonToken), "minted %q is not an rlat_ token", daemonToken)
	// The row lives in control-plane's store, not reliant's.
	p, err := cp.store.Introspect(context.Background(), daemonToken)
	require.NoError(t, err)
	require.Equal(t, userID, p.ActingUserID)
	require.True(t, p.Scopes.Permits(fat.ScopeDaemonConnect))

	// 2. The daemon connects to the gateway with it and is registered as the
	//    minting user. The gateway introspected at control-plane to do so.
	before := cp.introspect.Load()
	msg, err := connectDaemon(t, gatewayURL, daemonToken)
	require.NoError(t, err, "a live daemon credential must connect")
	ack := msg.GetRegistrationAck()
	require.NotNil(t, ack, "first server message must be the registration ack, got %v", msg)
	require.True(t, ack.GetAccepted())
	require.Equal(t, userID, ack.GetUserId())
	require.Equal(t, int64(1), cp.introspect.Load()-before, "the gateway introspects exactly once per connect")

	// A daemon credential is not an API credential: the API refuses it.
	_, err = tokens(daemonToken).ListTokens(context.Background(), connect.NewRequest(&reliantv1.ListTokensRequest{}))
	require.Equal(t, connect.CodeUnauthenticated, svcerr.Code(err), "daemon:connect must not authenticate the API: %v", err)

	// 3. The user revokes it through the same facade.
	_, err = tokens(session).RevokeToken(context.Background(), connect.NewRequest(&reliantv1.RevokeTokenRequest{
		Id: created.Msg.GetInfo().GetId(),
	}))
	require.NoError(t, err)

	// 4. The very next connect is refused — no cache window at the gateway.
	_, err = connectDaemon(t, gatewayURL, daemonToken)
	require.Error(t, err, "a revoked daemon credential must not connect")
	require.Equal(t, connect.CodeUnauthenticated, svcerr.Code(err), "revocation is a credential rejection: %v", err)
}

// TestDaemonCredential_HostedRefusesWhenControlPlaneIsDown pins "never fall
// back": with control-plane configured but unreachable, a daemon connect is
// Unavailable (retry later), not accepted and not a credential rejection.
func TestDaemonCredential_HostedRefusesWhenControlPlaneIsDown(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	authority, mode, err := tokenauthority.New(tokenauthority.Deps{
		ControlPlane: &configv1.ControlPlaneConfig{Url: deadURL, InternalServiceSecret: "s"}, DB: nil,
	})
	require.NoError(t, err)
	require.Equal(t, tokenauthority.ModeControlPlane, mode)

	minted, err := fat.Mint()
	require.NoError(t, err)
	_, err = connectDaemon(t, serveGateway(t, authority), minted.Plaintext)
	require.Equal(t, connect.CodeUnavailable, svcerr.Code(err), "an unreachable control-plane must refuse loudly: %v", err)
}

// TestDaemonCredential_SelfHostedEndToEnd is the self-hosted twin: no
// control-plane configured, so the authority is reliant's own access_tokens
// table (migration 20260925000000), and the same facade mints, the gateway
// validates, and revocation takes effect.
func TestDaemonCredential_SelfHostedEndToEnd(t *testing.T) {
	const userID = "user-e2e-selfhosted"

	repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	authority, mode, err := tokenauthority.New(tokenauthority.Deps{DB: repo.ORM()})
	require.NoError(t, err)
	require.Equal(t, tokenauthority.ModeLocal, mode)

	signer := newSessionSigner(t)
	apiURL := serveTokenService(t, authority, signer.pubPEM, token.TokenControlPlane{})
	gatewayURL := serveGateway(t, authority)
	tokens := reliantv1connect.NewTokenServiceClient(&http.Client{Transport: bearerTransport(signer.sign(t, userID))}, apiURL)

	created, err := tokens.CreateToken(context.Background(), connect.NewRequest(&reliantv1.CreateTokenRequest{
		Name: "server", Kind: reliantv1.TokenKind_TOKEN_KIND_DAEMON,
	}))
	require.NoError(t, err)
	daemonToken := created.Msg.GetToken()
	require.True(t, fat.HasFormat(daemonToken))

	var stored int
	require.NoError(t, rawDB.QueryRow(`SELECT count(*) FROM access_tokens WHERE id = $1 AND acting_user_id = $2`,
		created.Msg.GetInfo().GetId(), userID).Scan(&stored))
	require.Equal(t, 1, stored, "self-hosted tokens live in reliant's own access_tokens")

	msg, err := connectDaemon(t, gatewayURL, daemonToken)
	require.NoError(t, err)
	require.Equal(t, userID, msg.GetRegistrationAck().GetUserId())

	listed, err := tokens.ListTokens(context.Background(), connect.NewRequest(&reliantv1.ListTokensRequest{
		Kind: reliantv1.TokenKind_TOKEN_KIND_DAEMON.Enum(),
	}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetTokens(), 1)

	_, err = tokens.RevokeToken(context.Background(), connect.NewRequest(&reliantv1.RevokeTokenRequest{
		Id: created.Msg.GetInfo().GetId(),
	}))
	require.NoError(t, err)
	_, err = connectDaemon(t, gatewayURL, daemonToken)
	require.Equal(t, connect.CodeUnauthenticated, svcerr.Code(err), "revoked: %v", err)
}

type bearerTransport string

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}

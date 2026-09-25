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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v5"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
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
//	daemon ─► real DaemonServer (NewDaemonServer, h2c, DaemonAuthInterceptor)
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
		for _, s := range in.Scopes {
			scopes = append(scopes, fat.Scope(s))
		}
		m, merr := cp.store.MintForUser(ctx, tokenauthority.MintRequest{UserID: in.UserID, Name: in.Name, Scopes: scopes,
			Resource: in.Resource.resource(), Ephemeral: in.Ephemeral, ExpiresAt: in.ExpiresAt, Rotate: in.Rotate})
		if merr != nil {
			writeConnectError(w, http.StatusBadRequest, "invalid_argument", merr.Error())
			return
		}
		out = map[string]any{"tokenId": m.TokenID, "secret": m.Plaintext, "displayPrefix": m.DisplayPrefix,
			"expiresAt": m.ExpiresAt, "rotated": m.Rotated}
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
// interceptor, with the authority's introspector wired exactly as NewServer
// wires it (cached).
func serveTokenService(t *testing.T, authority tokenauthority.Authority, jwtPubPEM string) string {
	t.Helper()
	authInterceptor, err := interceptors.NewAuthInterceptor(jwtPubPEM, "", nil)
	require.NoError(t, err)
	authInterceptor.SetAccessTokenIntrospector(accesstokenclient.NewCachedIntrospector(authority))
	mux := http.NewServeMux()
	path, handler := reliantv1connect.NewTokenServiceHandler(services.NewTokenService(authority),
		connect.WithInterceptors(authInterceptor))
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// serveGateway starts the REAL daemon gateway server (NewDaemonServer) over
// h2c, authenticating daemons through authority.
func serveGateway(t *testing.T, authority tokenauthority.Authority) string {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ds := NewDaemonServer(&DaemonConfig{
		ToolsDaemonService: services.NewToolsDaemonService(repo),
		DaemonTokens:       authority,
	})
	return serveCleartext(t, ds.server.Handler)
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

	// Select the authority from the environment, exactly as the api-server
	// and the gateway do. A configured control-plane MUST win.
	t.Setenv("RELIANT_CONTROL_PLANE_URL", cpSrv.URL)
	t.Setenv("CONTROL_PLANE_API_URL", "")
	t.Setenv("CONTROL_PLANE_BASE_URL", "")
	t.Setenv("INTERNAL_SERVICE_SECRET", secret)
	authority, mode, err := tokenauthority.New(tokenauthority.DepsFromEnv(nil))
	require.NoError(t, err)
	require.Equal(t, tokenauthority.ModeControlPlane, mode)

	signer := newSessionSigner(t)
	apiURL := serveTokenService(t, authority, signer.pubPEM)
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
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), "daemon:connect must not authenticate the API: %v", err)

	// 3. The user revokes it through the same facade.
	_, err = tokens(session).RevokeToken(context.Background(), connect.NewRequest(&reliantv1.RevokeTokenRequest{
		Id: created.Msg.GetInfo().GetId(),
	}))
	require.NoError(t, err)

	// 4. The very next connect is refused — no cache window at the gateway.
	_, err = connectDaemon(t, gatewayURL, daemonToken)
	require.Error(t, err, "a revoked daemon credential must not connect")
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), "revocation is a credential rejection: %v", err)
}

// TestDaemonCredential_HostedRefusesWhenControlPlaneIsDown pins "never fall
// back": with control-plane configured but unreachable, a daemon connect is
// Unavailable (retry later), not accepted and not a credential rejection.
func TestDaemonCredential_HostedRefusesWhenControlPlaneIsDown(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	authority, mode, err := tokenauthority.New(tokenauthority.Deps{
		ControlPlaneURL: deadURL, InternalServiceSecret: "s", DB: nil,
	})
	require.NoError(t, err)
	require.Equal(t, tokenauthority.ModeControlPlane, mode)

	minted, err := fat.Mint()
	require.NoError(t, err)
	_, err = connectDaemon(t, serveGateway(t, authority), minted.Plaintext)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err), "an unreachable control-plane must refuse loudly: %v", err)
}

// TestDaemonCredential_SelfHostedEndToEnd is the self-hosted twin: no
// control-plane configured, so the authority is reliant's own access_tokens
// table (migration 20260925000000), and the same facade mints, the gateway
// validates, and revocation takes effect.
func TestDaemonCredential_SelfHostedEndToEnd(t *testing.T) {
	const userID = "user-e2e-selfhosted"
	t.Setenv("RELIANT_CONTROL_PLANE_URL", "")
	t.Setenv("CONTROL_PLANE_API_URL", "")
	t.Setenv("CONTROL_PLANE_BASE_URL", "")

	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	authority, mode, err := tokenauthority.New(tokenauthority.DepsFromEnv(rawDB))
	require.NoError(t, err)
	require.Equal(t, tokenauthority.ModeLocal, mode)

	signer := newSessionSigner(t)
	apiURL := serveTokenService(t, authority, signer.pubPEM)
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
		Kind: reliantv1.TokenKind_TOKEN_KIND_DAEMON,
	}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetTokens(), 1)

	_, err = tokens.RevokeToken(context.Background(), connect.NewRequest(&reliantv1.RevokeTokenRequest{
		Id: created.Msg.GetInfo().GetId(),
	}))
	require.NoError(t, err)
	_, err = connectDaemon(t, gatewayURL, daemonToken)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), "revoked: %v", err)
}

type bearerTransport string

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}

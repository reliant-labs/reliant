// Copyright (c) 2025 Reliant Labs
//
// These are Connect RPC handlers: the exported methods are the proto-defined
// service methods, and the package embeds the generated
// reliantv1connect.*ServiceHandler. The contract is the .proto service, so a
// contract.go here would duplicate the proto boundary.
//
//forge:exclude-contract: Connect interceptors (auth, recovery, observability, timeouts); shaped by connect.Interceptor, not a Service
package interceptors

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/authn"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// sessionTracker tracks active user sessions to avoid duplicate session_start events
type sessionTracker struct {
	sessions map[string]time.Time // userID -> last seen time
	mu       sync.RWMutex
}

func newSessionTracker() *sessionTracker {
	tracker := &sessionTracker{
		sessions: make(map[string]time.Time),
	}
	// Clean up old sessions every hour
	go tracker.cleanup()
	return tracker
}

func (t *sessionTracker) isNewSession(userID string) bool {
	t.mu.RLock()
	lastSeen, exists := t.sessions[userID]
	t.mu.RUnlock()

	if !exists {
		return true
	}

	// Consider it a new session if user hasn't been seen in 30 minutes
	return time.Since(lastSeen) > 30*time.Minute
}

func (t *sessionTracker) updateSession(userID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessions[userID] = time.Now()
}

func (t *sessionTracker) cleanup() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		t.mu.Lock()
		cutoff := time.Now().Add(-30 * time.Minute)
		for userID, lastSeen := range t.sessions {
			if lastSeen.Before(cutoff) {
				delete(t.sessions, userID)
			}
		}
		t.mu.Unlock()
	}
}

// AuthInterceptor provides authentication for gRPC requests. An `rlat_`
// access token goes to the token authority, must carry reliant:api or a
// runs/triggers slice of it (enforced per procedure: procedure_scopes.go), and
// resolves to the same claims/identity object its acting user's session
// would. Every other credential goes to the authenticator (auth.Authenticator):
// forge's token-validator chain of the managed identity and Supabase (or the
// self-host API key), behind forge's server-side session resolution.
type AuthInterceptor struct {
	authenticator  *auth.Authenticator
	accessTokens   auth.AccessTokenIntrospector // optional; nil disables access-token auth
	sessionTracker *sessionTracker
	// publicProcedures are the procedures this interceptor lets through with
	// no credentials: middleware.UnauthenticatedProcedures in the server.
	publicProcedures map[string]struct{}
}

// SetAccessTokenIntrospector enables `rlat_` bearer auth on this interceptor.
// In production it is the api-server's CachedIntrospector, so a revoked token
// keeps working here for at most accesstokenclient.CacheTTL. Call before
// serving.
func (i *AuthInterceptor) SetAccessTokenIntrospector(v auth.AccessTokenIntrospector) {
	i.accessTokens = v
}

// Authenticator is the credential check behind the interceptor, for the
// routes outside the chain that verify the same credentials.
func (i *AuthInterceptor) Authenticator() *auth.Authenticator { return i.authenticator }

// NewAuthInterceptor creates a new auth interceptor.
// The auth mode is settings':
//   - "apikey":   validate bearer tokens against settings.APIKey
//   - "supabase": validate Supabase JWTs using settings.PublicKeyPEM or
//     JWKSURL and, when settings.Identity names one, the managed identity's
//     tokens (default)
//
// publicProcedures skip authentication entirely; the server passes the
// generated middleware.UnauthenticatedProcedures, which every rpc declaring
// (forge.v1.method) auth_required: false projects into.
func NewAuthInterceptor(settings auth.Settings, publicProcedures map[string]struct{}) (*AuthInterceptor, error) {
	if settings.ModeName() == auth.ModeAPIKey && settings.APIKey == "" {
		return nil, fmt.Errorf("AUTH_MODE=apikey requires AUTH_API_KEY to be set")
	}
	authenticator, err := auth.NewAuthenticator(context.Background(), settings)
	if err != nil {
		return nil, err
	}
	switch {
	case settings.ModeName() == auth.ModeAPIKey:
		logging.Info("[gRPC Auth] API key auth mode enabled")
	default:
		if settings.DevJWTSecret != "" {
			logging.Warn("[gRPC Auth] dev signer enabled: HS256 tokens signed with AUTH_DEV_JWT_SECRET are accepted (dev tier only)")
		}
		logging.Info("[gRPC Auth] JWT auth mode enabled",
			"supabase", settings.PublicKeyPEM != "" || settings.JWKSURL != "",
			"managed_identity", settings.Identity.Issuer,
			"sessions", settings.Sessions != nil)
	}
	return &AuthInterceptor{
		authenticator:    authenticator,
		sessionTracker:   newSessionTracker(),
		publicProcedures: publicProcedures,
	}, nil
}

// authenticateRequest validates the auth token and returns the authenticated context and claims.
// Returns an error if authentication fails.
func (i *AuthInterceptor) authenticateRequest(ctx context.Context, procedure string, header http.Header) (context.Context, *auth.JWTClaims, error) {
	// Check if this method is public
	if _, public := i.publicProcedures[procedure]; public {
		return ctx, nil, nil
	}

	// The Authorization header when the request has one, else the sign-in
	// session cookie (auth.CredentialFromHeader).
	tokenString, channel := i.authenticator.CredentialFrom(header)
	// Rejections log at DEBUG: forge's LoggingInterceptor already records the
	// Unauthenticated RPC ("rpc failed", procedure, request_id) once at WARN.
	if tokenString == "" {
		if channel == authn.ChannelAuthorization {
			logging.DebugContext(ctx, "[gRPC Auth] Invalid authorization header format",
				"procedure", procedure)
			return nil, nil, svcerr.ToConnect(svcerr.Unauthenticated("invalid authorization header format"))
		}
		logging.DebugContext(ctx, "[gRPC Auth] Missing authorization token",
			"procedure", procedure)
		return nil, nil, svcerr.ToConnect(svcerr.Unauthenticated("missing authorization token"))
	}
	return i.authenticateCredential(ctx, procedure, tokenString, channel)
}

// authenticateCredential checks credential, which arrived on channel, and
// returns ctx carrying the caller it proves.
func (i *AuthInterceptor) authenticateCredential(ctx context.Context, procedure, tokenString string, channel authn.Channel) (context.Context, *auth.JWTClaims, error) {
	// Shape dispatch: `rlat_` bearers are access tokens (resolved by the token
	// authority, reliant:api or a runs/triggers slice of it); anything else goes through the
	// authenticator's token chain. A machine credential that reaches a token-MINTING
	// RPC is refused by that handler (TokenService.CreateToken), not here.
	isAccessToken := auth.IsAccessTokenFormat(tokenString)
	var verified *auth.Verified
	err := i.authenticator.CheckChannel(tokenString, channel)
	switch {
	case err != nil:
	case isAccessToken:
		if i.accessTokens == nil {
			logging.WarnContext(ctx, "[gRPC Auth] access token presented but access-token auth is not enabled",
				"procedure", procedure)
			return nil, nil, svcerr.ToConnect(svcerr.Unauthenticated("invalid or expired token"))
		}
		var claims *auth.JWTClaims
		var principal *fat.Principal
		claims, principal, err = auth.ClaimsForAccessToken(ctx, i.accessTokens, tokenString)
		if err == nil {
			verified = auth.VerifiedAccessToken(claims, principal, channel)
		}
	default:
		verified, err = i.authenticator.Verify(ctx, tokenString, channel)
	}
	if err != nil {
		// Only a credential rejection is Unauthenticated. Anything else — the
		// token store unreachable, a query deadline, a JWKS fetch failing, a
		// session store or issuer down — means verification never happened.
		// Reporting that as Unauthenticated makes every client tell the
		// operator their credential was rejected and to mint a new one, which
		// cannot fix an unreachable dependency and destroys the only real
		// signal.
		if !auth.IsCredentialRejection(err) {
			logging.ErrorContext(ctx, "[gRPC Auth] Token verification unavailable",
				"error", err,
				"procedure", procedure)
			// The client reads only the fixed text; the store's or JWKS's
			// own error (driver messages, URLs) stays server-side as the
			// cause, which the "rpc failed" record logs.
			return nil, nil, svcerr.ToConnect(svcerr.WithCause(
				svcerr.Unavailable("token verification unavailable"), err))
		}
		logging.DebugContext(ctx, "[gRPC Auth] Invalid token",
			"error", err,
			"procedure", procedure)
		return nil, nil, svcerr.ToConnect(svcerr.Unauthenticated("invalid or expired token"))
	}

	// A machine credential may be a narrow slice of reliant:api (runs:*,
	// triggers:*): it reaches only the procedures procedureScopes grants it.
	// A session carries no principal and is never asked.
	if principal := verified.Principal; principal != nil {
		if err := requireProcedureScope(procedure, principal); err != nil {
			logging.DebugContext(ctx, "[gRPC Auth] Access token lacks the scope for this procedure",
				"procedure", procedure,
				"token_id", principal.TokenID)
			return nil, nil, svcerr.ToConnect(err)
		}
	}

	claims := verified.Claims
	ctx = verified.Bind(ctx)

	// Store the bearer so subsystems (e.g. Reliant LLM driver) can look it up
	// by userID. Only Supabase JWTs are stored: they are what the control
	// plane accepts where these are forwarded. An access token, a managed
	// identity's token and a session id are not, and must never be forwarded
	// where one is expected.
	if verified.Source == auth.SourceSupabase || verified.Source == auth.SourceDevSigner {
		auth.SetUserJWT(claims.Sub, tokenString)
	}

	logging.DebugContext(ctx, "[gRPC Auth] Authenticated request",
		"user_id", claims.Sub,
		"role", claims.Role,
		"credential", claims.Credential.Label(),
		"source", string(verified.Source),
		"procedure", procedure)

	return ctx, claims, nil
}

// AuthenticateHTTP authenticates a plain HTTP request (not a Connect RPC) and
// returns the caller's user id. The credential is the Authorization header, or
// a `token` query parameter for clients that cannot set headers — a <video>
// element, like the terminal WebSocket.
func (i *AuthInterceptor) AuthenticateHTTP(r *http.Request) (string, error) {
	procedure := "http:" + r.URL.Path
	var ctx context.Context
	var err error
	if tok, ch := i.authenticator.CredentialFrom(r.Header); tok == "" && ch != authn.ChannelAuthorization && r.URL.Query().Get("token") != "" {
		// A query parameter is no header: where it came from is unknown.
		ctx, _, err = i.authenticateCredential(r.Context(), procedure, r.URL.Query().Get("token"), authn.ChannelUnknown)
	} else {
		ctx, _, err = i.authenticateRequest(r.Context(), procedure, r.Header)
	}
	if err != nil {
		return "", err
	}
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return "", fmt.Errorf("no authenticated user")
	}
	return userID, nil
}

// trackSession handles analytics session tracking for authenticated users
func (i *AuthInterceptor) trackSession(ctx context.Context, claims *auth.JWTClaims) {
	if claims == nil {
		return
	}

	// Track session if new (first request in 30 minutes)
	if i.sessionTracker.isNewSession(claims.Sub) {
		i.sessionTracker.updateSession(claims.Sub)
		// ForUser reads the user's consent, so only on a new session.
		analytics.ForUser(ctx, claims.Sub).TrackSessionStart()
	} else {
		// Just update the last seen time without logging event
		i.sessionTracker.updateSession(claims.Sub)
	}
}

// WrapUnary implements connect.Interceptor for unary RPCs
func (i *AuthInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		procedure := req.Spec().Procedure

		ctx, claims, err := i.authenticateRequest(ctx, procedure, req.Header())
		if err != nil {
			return nil, err
		}

		i.trackSession(ctx, claims)

		// Stash x-daemon-last-seen header in context for downstream daemon router.
		if v := req.Header().Get("x-daemon-last-seen"); v != "" {
			ctx = WithDaemonLastSeen(ctx, v)
		}

		resp, err := next(ctx, req)
		return resp, credentialMismatch(ctx, err)
	}
}

// credentialMismatch turns a refusal the handler answered as missing into one
// that says why, when authz refused this request's credential a resource of
// another org: a CLI user then knows to sign in for that org instead of
// hunting for a resource that exists. Any other error passes as it is.
func credentialMismatch(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	bound, refused, ok := authz.CredentialRefusal(ctx)
	if !ok {
		return err
	}
	if code := svcerr.Code(err); code != connect.CodeNotFound && code != connect.CodePermissionDenied {
		return err
	}
	return svcerr.ToConnect(svcerr.WithReason(svcerr.PermissionDenied(fmt.Sprintf(
		"this credential acts in organization %s only, and this belongs to organization %s; "+
			"to act there, sign in for it: `reliant auth login` (or `forge cloud login --org %s`)",
		bound, refused, refused)), orgs.ReasonOrgMismatch))
}

// WrapStreamingClient implements connect.Interceptor for client streaming (not used server-side)
func (i *AuthInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	// Client-side streaming doesn't need server authentication
	return next
}

// WrapStreamingHandler implements connect.Interceptor for server streaming RPCs
func (i *AuthInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		procedure := conn.Spec().Procedure

		ctx, claims, err := i.authenticateRequest(ctx, procedure, conn.RequestHeader())
		if err != nil {
			return err
		}

		i.trackSession(ctx, claims)

		return credentialMismatch(ctx, next(ctx, conn))
	}
}

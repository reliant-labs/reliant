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
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/telemetry"
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

// AuthInterceptor provides authentication for gRPC requests. A session JWT (or
// the self-host API key) goes to the configured validator; an `rlat_` access
// token goes to the token authority, must carry reliant:api, and resolves to
// the same claims/identity object its acting user's session would.
type AuthInterceptor struct {
	validator      auth.TokenValidator
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

// NewAuthInterceptor creates a new auth interceptor.
// The auth mode is settings':
//   - "apikey":   validate bearer tokens against settings.APIKey
//   - "supabase": validate JWTs using settings.PublicKeyPEM or JWKSURL (default)
//
// publicProcedures skip authentication entirely; the server passes the
// generated middleware.UnauthenticatedProcedures, which every rpc declaring
// (forge.v1.method) auth_required: false projects into.
func NewAuthInterceptor(settings auth.Settings, publicProcedures map[string]struct{}) (*AuthInterceptor, error) {
	publicKeyPEM, jwksURL := settings.PublicKeyPEM, settings.JWKSURL
	switch settings.ModeName() {
	case auth.ModeAPIKey:
		apiKey := settings.APIKey
		if apiKey == "" {
			return nil, fmt.Errorf("AUTH_MODE=apikey requires AUTH_API_KEY to be set")
		}
		validator, err := auth.NewAPIKeyValidator(apiKey)
		if err != nil {
			return nil, fmt.Errorf("failed to create API key validator: %w", err)
		}
		logging.Info("[gRPC Auth] API key auth mode enabled")
		return &AuthInterceptor{
			validator:        validator,
			sessionTracker:   newSessionTracker(),
			publicProcedures: publicProcedures,
		}, nil

	default: // "supabase"
		var validator auth.TokenValidator
		switch {
		case publicKeyPEM != "":
			var err error
			validator, err = auth.NewJWTValidator(publicKeyPEM, settings.JWTOptions()...)
			if err != nil {
				return nil, err
			}
		case jwksURL != "":
			var err error
			validator, err = auth.LoadJWKS(context.Background(), jwksURL, settings.JWTOptions()...)
			if err != nil {
				return nil, fmt.Errorf("failed to load JWKS from %s: %w", jwksURL, err)
			}
		default:
			return nil, auth.ErrInvalidPublicKey
		}
		logging.Info("[gRPC Auth] Supabase JWT auth mode enabled")
		return &AuthInterceptor{
			validator:        validator,
			sessionTracker:   newSessionTracker(),
			publicProcedures: publicProcedures,
		}, nil
	}
}

// authenticateRequest validates the auth token and returns the authenticated context and claims.
// Returns an error if authentication fails.
func (i *AuthInterceptor) authenticateRequest(ctx context.Context, procedure string, header func(string) string) (context.Context, *auth.JWTClaims, error) {
	// Check if this method is public
	if _, public := i.publicProcedures[procedure]; public {
		return ctx, nil, nil
	}

	// Extract token from Authorization header
	authHeader := header("Authorization")
	// Rejections log at DEBUG: forge's LoggingInterceptor already records the
	// Unauthenticated RPC ("rpc failed", procedure, request_id) once at WARN.
	if authHeader == "" {
		logging.DebugContext(ctx, "[gRPC Auth] Missing authorization token",
			"procedure", procedure)
		return nil, nil, svcerr.ToConnect(svcerr.Unauthenticated("missing authorization token"))
	}

	tokenString := strings.TrimPrefix(authHeader, "Bearer ")
	if tokenString == authHeader {
		logging.DebugContext(ctx, "[gRPC Auth] Invalid authorization header format",
			"procedure", procedure)
		return nil, nil, svcerr.ToConnect(svcerr.Unauthenticated("invalid authorization header format"))
	}

	// Shape dispatch: `rlat_` bearers are access tokens (resolved by the token
	// authority, reliant:api only); anything else goes through the configured
	// JWT/apikey validator. A machine credential that reaches a token-MINTING
	// RPC is refused by that handler (TokenService.CreateToken), not here.
	isAccessToken := auth.IsAccessTokenFormat(tokenString)
	var claims *auth.JWTClaims
	var principal *fat.Principal
	var err error
	if isAccessToken {
		if i.accessTokens == nil {
			logging.WarnContext(ctx, "[gRPC Auth] access token presented but access-token auth is not enabled",
				"procedure", procedure)
			return nil, nil, svcerr.ToConnect(svcerr.Unauthenticated("invalid or expired token"))
		}
		claims, principal, err = auth.ClaimsForAPIToken(ctx, i.accessTokens, tokenString)
	} else {
		claims, err = i.validator.ValidateToken(tokenString)
	}
	if err != nil {
		// Only a credential rejection is Unauthenticated. Anything else — the
		// token store unreachable, a query deadline, a JWKS fetch failing —
		// means verification never happened. Reporting that as Unauthenticated
		// makes every client tell the operator their credential was rejected
		// and to mint a new one, which cannot fix an unreachable dependency
		// and destroys the only real signal.
		if !auth.IsCredentialRejection(err) {
			logging.ErrorContext(ctx, "[gRPC Auth] Token verification unavailable",
				"error", err,
				"procedure", procedure)
			return nil, nil, connect.NewError(connect.CodeUnavailable,
				fmt.Errorf("token verification unavailable: %w", err))
		}
		logging.DebugContext(ctx, "[gRPC Auth] Invalid token",
			"error", err,
			"procedure", procedure)
		return nil, nil, svcerr.ToConnect(svcerr.Unauthenticated("invalid or expired token"))
	}

	// Add claims to context
	ctx = auth.WithUser(ctx, claims.Sub, claims.Role, claims.Email)
	ctx = telemetry.BindUser(ctx, claims.Sub)
	if principal != nil {
		ctx = auth.WithMachineToken(ctx, principal)
	}

	// Store the bearer so subsystems (e.g. Reliant LLM driver) can look it up
	// by userID. Only JWTs are stored: an access token is not a Supabase JWT
	// and must never be forwarded where one is expected.
	if !isAccessToken {
		auth.SetUserJWT(claims.Sub, tokenString)
	}

	logging.DebugContext(ctx, "[gRPC Auth] Authenticated request",
		"user_id", claims.Sub,
		"role", claims.Role,
		"procedure", procedure)

	return ctx, claims, nil
}

// AuthenticateHTTP authenticates a plain HTTP request (not a Connect RPC) and
// returns the caller's user id. The credential is the Authorization header, or
// a `token` query parameter for clients that cannot set headers — a <video>
// element, like the terminal WebSocket.
func (i *AuthInterceptor) AuthenticateHTTP(r *http.Request) (string, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		if token := r.URL.Query().Get("token"); token != "" {
			authHeader = "Bearer " + token
		}
	}
	header := func(key string) string {
		if key == "Authorization" {
			return authHeader
		}
		return r.Header.Get(key)
	}
	ctx, _, err := i.authenticateRequest(r.Context(), "http:"+r.URL.Path, header)
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

		ctx, claims, err := i.authenticateRequest(ctx, procedure, req.Header().Get)
		if err != nil {
			return nil, err
		}

		i.trackSession(ctx, claims)

		// Stash x-daemon-last-seen header in context for downstream daemon router.
		if v := req.Header().Get("x-daemon-last-seen"); v != "" {
			ctx = WithDaemonLastSeen(ctx, v)
		}

		return next(ctx, req)
	}
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

		ctx, claims, err := i.authenticateRequest(ctx, procedure, conn.RequestHeader().Get)
		if err != nil {
			return err
		}

		i.trackSession(ctx, claims)

		return next(ctx, conn)
	}
}

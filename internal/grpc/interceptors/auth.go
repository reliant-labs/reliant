// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

const worktreeOperationTimeout = 120 * time.Second

// sessionTracker tracks active user sessions to avoid duplicate session_start events
type sessionTracker struct {
	sessions map[string]time.Time // userID -> last seen time
	everSeen map[string]bool      // userID -> has been seen at least once
	mu       sync.RWMutex
}

func newSessionTracker() *sessionTracker {
	tracker := &sessionTracker{
		sessions: make(map[string]time.Time),
		everSeen: make(map[string]bool),
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
	t.everSeen[userID] = true
}

func (t *sessionTracker) isFirstTimeSeen(userID string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return !t.everSeen[userID]
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

// patDeniedProcedurePrefix guards the "a PAT cannot mint/revoke PATs" rule on
// the gRPC surface: PAT-authenticated requests are denied for the whole
// DaemonTokenService (mint/list/revoke of daemon tokens). Daemon-token
// management always requires an interactive session. (The api-kind
// equivalent, TokenService, is not blanket-denied here — it accepts PATs for
// list/revoke and enforces the JWT-only rule for CreateToken in the handler.
// MintManagedDaemonToken / RevokeManagedDaemonToken are public to THIS
// interceptor and gated by the internal-service interceptor instead, so they
// are unaffected.)
const patDeniedProcedurePrefix = "/reliant.v1.DaemonTokenService/"

// AuthInterceptor provides JWT authentication for gRPC requests.
// When apiTokenValidator is set, rlnt_pat_ bearer tokens are prefix-dispatched
// to it (a DB hash lookup that accepts api-kind PATs ONLY — daemon-kind
// tokens are rejected) instead of JWT signature verification, resolving to
// the same claims/identity object — the same middleware path serves both.
type AuthInterceptor struct {
	validator         auth.TokenValidator
	apiTokenValidator auth.APITokenValidator // optional; nil disables PAT bearer auth
	sessionTracker    *sessionTracker
	publicMethods     map[string]bool // Methods that don't require auth
}

// SetAPITokenValidator enables api-kind PAT bearer auth on this interceptor.
// Call before serving.
func (i *AuthInterceptor) SetAPITokenValidator(v auth.APITokenValidator) {
	i.apiTokenValidator = v
}

// NewAuthInterceptor creates a new auth interceptor.
// The auth mode is determined by auth.GetAuthMode():
//   - "apikey":   validate bearer tokens against AUTH_API_KEY env var
//   - "supabase": validate JWTs using publicKeyPEM or jwksURL (default)
func NewAuthInterceptor(publicKeyPEM string, jwksURL string, publicMethods []string) (*AuthInterceptor, error) {
	mode := auth.GetAuthMode()

	publicMethodsMap := make(map[string]bool)
	for _, method := range publicMethods {
		publicMethodsMap[method] = true
	}

	switch mode {
	case "apikey":
		apiKey := os.Getenv("AUTH_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("AUTH_MODE=apikey requires AUTH_API_KEY to be set")
		}
		validator, err := auth.NewAPIKeyValidator(apiKey)
		if err != nil {
			return nil, fmt.Errorf("failed to create API key validator: %w", err)
		}
		logging.Info("[gRPC Auth] API key auth mode enabled")
		return &AuthInterceptor{
			validator:      validator,
			sessionTracker: newSessionTracker(),
			publicMethods:  publicMethodsMap,
		}, nil

	default: // "supabase"
		var validator auth.TokenValidator
		switch {
		case publicKeyPEM != "":
			var err error
			validator, err = auth.NewJWTValidator(publicKeyPEM)
			if err != nil {
				return nil, err
			}
		case jwksURL != "":
			var err error
			validator, err = auth.LoadJWKS(context.Background(), jwksURL)
			if err != nil {
				return nil, fmt.Errorf("failed to load JWKS from %s: %w", jwksURL, err)
			}
		default:
			return nil, auth.ErrInvalidPublicKey
		}
		logging.Info("[gRPC Auth] Supabase JWT auth mode enabled")
		return &AuthInterceptor{
			validator:      validator,
			sessionTracker: newSessionTracker(),
			publicMethods:  publicMethodsMap,
		}, nil
	}
}

// authenticateRequest validates the auth token and returns the authenticated context, claims, and raw token.
// Returns an error if authentication fails.
func (i *AuthInterceptor) authenticateRequest(ctx context.Context, procedure string, header func(string) string) (context.Context, *auth.JWTClaims, string, error) {
	// Check if this method is public
	if i.publicMethods[procedure] {
		return ctx, nil, "", nil
	}

	// Extract token from Authorization header
	authHeader := header("Authorization")
	if authHeader == "" {
		logging.Warn("[gRPC Auth] Missing authorization token",
			"procedure", procedure)
		return nil, nil, "", connect.NewError(connect.CodeUnauthenticated,
			fmt.Errorf("missing authorization token"))
	}

	tokenString := strings.TrimPrefix(authHeader, "Bearer ")
	if tokenString == authHeader {
		logging.Warn("[gRPC Auth] Invalid authorization header format",
			"procedure", procedure)
		return nil, nil, "", connect.NewError(connect.CodeUnauthenticated,
			fmt.Errorf("invalid authorization header format"))
	}

	// Prefix dispatch: rlnt_pat_ bearers are PATs (DB hash lookup accepting
	// api-kind only), anything else goes through the configured JWT/apikey
	// validator.
	isPAT := auth.IsPATFormat(tokenString)
	var claims *auth.JWTClaims
	var err error
	if isPAT {
		switch {
		case i.apiTokenValidator == nil:
			logging.Warn("[gRPC Auth] PAT presented but PAT auth is not enabled",
				"procedure", procedure)
			return nil, nil, "", connect.NewError(connect.CodeUnauthenticated,
				fmt.Errorf("invalid or expired token"))
		case strings.HasPrefix(procedure, patDeniedProcedurePrefix):
			// A PAT cannot mint, list, or revoke PATs — token management
			// requires an interactive session.
			logging.Warn("[gRPC Auth] PAT presented on a session-only procedure",
				"procedure", procedure)
			return nil, nil, "", connect.NewError(connect.CodeUnauthenticated,
				fmt.Errorf("token management requires an interactive session"))
		}
		claims, err = i.apiTokenValidator.ValidateAPIToken(ctx, tokenString)
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
			logging.Error("[gRPC Auth] Token verification unavailable",
				"error", err,
				"procedure", procedure)
			return nil, nil, "", connect.NewError(connect.CodeUnavailable,
				fmt.Errorf("token verification unavailable: %w", err))
		}
		logging.Warn("[gRPC Auth] Invalid token",
			"error", err,
			"procedure", procedure)
		return nil, nil, "", connect.NewError(connect.CodeUnauthenticated,
			fmt.Errorf("invalid or expired token"))
	}

	// Add claims to context
	ctx = context.WithValue(ctx, auth.UserIDContextKey, claims.Sub)
	ctx = context.WithValue(ctx, auth.UserRoleContextKey, claims.Role)
	ctx = context.WithValue(ctx, auth.UserEmailContextKey, claims.Email)

	// Store the bearer so subsystems (e.g. Reliant LLM driver) can look it up
	// by userID. Only JWTs are stored: a PAT is not a Supabase JWT and must
	// never be forwarded where one is expected.
	if !isPAT {
		auth.SetUserJWT(claims.Sub, tokenString)
	}

	logging.Debug("[gRPC Auth] Authenticated request",
		"user_id", claims.Sub,
		"role", claims.Role,
		"email", claims.Email,
		"procedure", procedure)

	return ctx, claims, tokenString, nil
}

// trackSession handles analytics session tracking for authenticated users
func (i *AuthInterceptor) trackSession(ctx context.Context, claims *auth.JWTClaims, rawToken string) {
	if claims == nil {
		return
	}

	// Update analytics client userID and Sentry user context on first authentication
	if i.sessionTracker.isFirstTimeSeen(claims.Sub) {
		analytics.SetUserID(claims.Sub)
		telemetry.GetReporter().SetUser(claims.Sub, claims.Email)
		logging.Info("[Analytics] User authenticated, updated analytics client",
			"userID", claims.Sub,
			"email", claims.Email)
	}

	// Always update the JWT (it refreshes on each request). PATs are never
	// stored where a JWT is expected.
	if rawToken != "" && !auth.IsPATFormat(rawToken) {
		analytics.SetUserJWT(rawToken)
	}

	// Get client that respects user's privacy settings
	analyticsClient := analytics.GetClientForUser(ctx, claims.Sub)

	// Track session if new (first request in 30 minutes)
	if i.sessionTracker.isNewSession(claims.Sub) {
		i.sessionTracker.updateSession(claims.Sub)
		analyticsClient.TrackSessionStart()
		logging.Info("[Analytics] New session started",
			"userID", claims.Sub,
			"email", claims.Email)
	} else {
		// Just update the last seen time without logging event
		i.sessionTracker.updateSession(claims.Sub)
	}
}

// WrapUnary implements connect.Interceptor for unary RPCs
func (i *AuthInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		procedure := req.Spec().Procedure

		ctx, claims, rawToken, err := i.authenticateRequest(ctx, procedure, req.Header().Get)
		if err != nil {
			return nil, err
		}

		i.trackSession(ctx, claims, rawToken)

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

		ctx, claims, rawToken, err := i.authenticateRequest(ctx, procedure, conn.RequestHeader().Get)
		if err != nil {
			return err
		}

		i.trackSession(ctx, claims, rawToken)

		return next(ctx, conn)
	}
}

// TODO: Add stream interceptor when we implement streaming RPCs
// Connect handles streaming differently - will add when implementing ChatUpdates streaming

// TimeoutInterceptor adds a default timeout to unary requests
// This prevents requests from hanging indefinitely due to locks, deadlocks, or network issues
type TimeoutInterceptor struct {
	defaultTimeout time.Duration
	// Methods that need longer timeouts (e.g., file operations, LLM calls)
	longTimeoutMethods map[string]time.Duration
}

// NewTimeoutInterceptor creates a timeout interceptor with sensible defaults
// NOTE: Default timeout is 10s because browsers limit HTTP/1.1 to ~6 concurrent
// connections per origin. Since browsers don't support h2c (HTTP/2 cleartext),
// we're limited to HTTP/1.1 in local dev. Short timeouts prevent connection starvation.
func NewTimeoutInterceptor() *TimeoutInterceptor {
	return &TimeoutInterceptor{
		defaultTimeout: 10 * time.Second,
		longTimeoutMethods: map[string]time.Duration{
			// File operations - may involve large files
			"/reliant.v1.FileSystemService/ReadFile":  30 * time.Second,
			"/reliant.v1.FileSystemService/WriteFile": 30 * time.Second,
			"/reliant.v1.FileSystemService/ListFiles": 30 * time.Second,
			// Chat operations that involve workflows - initial setup can take time
			"/reliant.v1.ChatService/CreateChat":  30 * time.Second,
			"/reliant.v1.ChatService/SendMessage": 30 * time.Second,
			// MCP operations - external process startup can be slow
			"/reliant.v1.MCPService/StartServer": 60 * time.Second,
			"/reliant.v1.MCPService/CallTool":    60 * time.Second,
			// Attachment uploads - depends on file size
			"/reliant.v1.AttachmentService/Upload": 60 * time.Second,
			// Provider API key validation - Gemini requires minimum 10s deadline
			"/reliant.v1.SettingsService/ValidateProviderAPIKey": 20 * time.Second,
			"/reliant.v1.SettingsService/UpdateProviderAPIKey":   20 * time.Second,
			// Worktree operations can involve git worktree add/remove, diffing, and file copying.
			"/reliant.v1.WorktreeService/CreateWorktree":           worktreeOperationTimeout,
			"/reliant.v1.WorktreeService/DeleteWorktree":           worktreeOperationTimeout,
			"/reliant.v1.WorktreeService/ArchiveWorktree":          worktreeOperationTimeout,
			"/reliant.v1.WorktreeService/UnarchiveWorktree":        worktreeOperationTimeout,
			"/reliant.v1.WorktreeService/ImportWorktree":           worktreeOperationTimeout,
			"/reliant.v1.WorktreeService/DiscoverWorktrees":        worktreeOperationTimeout,
			"/reliant.v1.WorktreeService/RecreateWorktree":         worktreeOperationTimeout,
			"/reliant.v1.WorktreeService/GetWorktreeChanges":       worktreeOperationTimeout,
			"/reliant.v1.WorktreeService/GetWorktreeGitStatus":     worktreeOperationTimeout,
			"/reliant.v1.WorktreeService/GetWorktreeCommits":       worktreeOperationTimeout,
			"/reliant.v1.WorktreeService/ListWorktreeRepoStatuses": worktreeOperationTimeout,
			"/reliant.v1.WorktreeService/StageFiles":               worktreeOperationTimeout,
			"/reliant.v1.WorktreeService/UnstageFiles":             worktreeOperationTimeout,
			"/reliant.v1.WorktreeService/CommitWorktree":           worktreeOperationTimeout,
			"/reliant.v1.WorktreeService/PushWorktree":             worktreeOperationTimeout,
			"/reliant.v1.WorktreeService/PullWorktree":             worktreeOperationTimeout,
			"/reliant.v1.WorktreeService/GetWorktreePR":            worktreeOperationTimeout,
			"/reliant.v1.WorktreeService/CreateWorktreePR":         worktreeOperationTimeout,
			"/reliant.v1.WorktreeService/RevertFiles":              worktreeOperationTimeout,
			// MCP operations - external process management can be slow
			"/reliant.v1.MCPService/InstallServer":      60 * time.Second,
			"/reliant.v1.MCPService/RestartServer":      60 * time.Second,
			"/reliant.v1.MCPService/UpdateServerConfig": 60 * time.Second,
			"/reliant.v1.MCPService/UninstallServer":    60 * time.Second,
			// OAuth flows — no timeout, user can take as long as needed.
			// Cancellation is handled by the frontend AbortController.
			"/reliant.v1.DaemonService/StartOAuthFlow":   0,
			"/reliant.v1.SystemService/StartOAuthSignIn": 0,
			// OAuth token exchange - external network call
			"/reliant.v1.SettingsService/CompleteClaudeOAuth": 30 * time.Second,
			"/reliant.v1.SettingsService/CompleteCodexOAuth":  30 * time.Second,
		},
	}
}

// Interceptor returns a Connect unary interceptor that enforces timeouts
func (t *TimeoutInterceptor) Interceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			procedure := req.Spec().Procedure

			// Determine timeout for this method
			timeout := t.defaultTimeout
			if methodTimeout, ok := t.longTimeoutMethods[procedure]; ok {
				timeout = methodTimeout
			}

			// Guard: timeout == 0 would create an immediately-expired context.
			if timeout > 0 {
				if _, hasDeadline := ctx.Deadline(); !hasDeadline {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, timeout)
					defer cancel()
				}
			}

			return next(ctx, req)
		}
	}
}

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
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

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

// DevUser is the hardcoded user for development mode auth bypass
// Sub is read from DEV_USER_ID env var, defaulting to the original dev UUID
var DevUser = &auth.JWTClaims{
	Sub:   devUserID(),
	Role:  "authenticated",
	Email: "dev@localhost",
}

func devUserID() string {
	if id := os.Getenv("DEV_USER_ID"); id != "" {
		return id
	}
	return "530eb7d2-1f6a-4305-890e-c05becebcf03"
}

// AuthInterceptor provides JWT authentication for gRPC requests
type AuthInterceptor struct {
	validator      *auth.JWTValidator
	sessionTracker *sessionTracker
	publicMethods  map[string]bool // Methods that don't require auth
	devMode        bool            // If true, bypass auth with dev user
}

// NewAuthInterceptor creates a new auth interceptor using RSA public key
func NewAuthInterceptor(publicKeyPEM string, publicMethods []string) (*AuthInterceptor, error) {
	// Check if we're in dev mode - bypass auth entirely
	devMode := config.IsDevelopmentEnvironment()
	if devMode {
		logging.Info("[gRPC Auth] Development mode detected - auth bypass enabled",
			"dev_user_id", DevUser.Sub,
			"dev_user_email", DevUser.Email)
	}

	// In dev mode, we don't need a valid JWT key
	var validator *auth.JWTValidator
	if !devMode {
		if publicKeyPEM == "" {
			return nil, auth.ErrInvalidPublicKey
		}

		var err error
		validator, err = auth.NewJWTValidator(publicKeyPEM)
		if err != nil {
			return nil, err
		}
	}

	// Build public methods map for fast lookup
	publicMethodsMap := make(map[string]bool)
	for _, method := range publicMethods {
		publicMethodsMap[method] = true
	}

	return &AuthInterceptor{
		validator:      validator,
		sessionTracker: newSessionTracker(),
		publicMethods:  publicMethodsMap,
		devMode:        devMode,
	}, nil
}

// authenticateRequest validates the auth token and returns the authenticated context, claims, and raw token.
// Returns an error if authentication fails.
func (i *AuthInterceptor) authenticateRequest(ctx context.Context, procedure string, header func(string) string) (context.Context, *auth.JWTClaims, string, error) {
	// Check if this method is public
	if i.publicMethods[procedure] {
		return ctx, nil, "", nil
	}

	// Dev mode bypass - use hardcoded dev user
	if i.devMode {
		ctx = context.WithValue(ctx, auth.UserIDContextKey, DevUser.Sub)
		ctx = context.WithValue(ctx, auth.UserRoleContextKey, DevUser.Role)
		ctx = context.WithValue(ctx, auth.UserEmailContextKey, DevUser.Email)

		logging.Debug("[gRPC Auth] Dev mode - using dev user",
			"user_id", DevUser.Sub,
			"procedure", procedure)

		return ctx, DevUser, "", nil
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

	// Validate token
	claims, err := i.validator.ValidateToken(tokenString)
	if err != nil {
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

	// Always update the JWT (it refreshes on each request)
	if rawToken != "" {
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
			// Worktree operations - involve git commands that can take 10-30s
			"/reliant.v1.WorktreeService/CreateWorktree":       30 * time.Second,
			"/reliant.v1.WorktreeService/DeleteWorktree":       30 * time.Second,
			"/reliant.v1.WorktreeService/ArchiveWorktree":      30 * time.Second,
			"/reliant.v1.WorktreeService/UnarchiveWorktree":    30 * time.Second,
			"/reliant.v1.WorktreeService/ImportWorktree":       30 * time.Second,
			"/reliant.v1.WorktreeService/DiscoverWorktrees":    30 * time.Second,
			"/reliant.v1.WorktreeService/RecreateWorktree":     30 * time.Second,
			"/reliant.v1.WorktreeService/GetWorktreeChanges":   30 * time.Second,
			"/reliant.v1.WorktreeService/GetWorktreeGitStatus": 30 * time.Second,
			"/reliant.v1.WorktreeService/GetWorktreeCommits":   30 * time.Second,
			"/reliant.v1.WorktreeService/StageFiles":           30 * time.Second,
			"/reliant.v1.WorktreeService/UnstageFiles":         30 * time.Second,
			"/reliant.v1.WorktreeService/CommitWorktree":       30 * time.Second,
			"/reliant.v1.WorktreeService/PushWorktree":         30 * time.Second,
			"/reliant.v1.WorktreeService/PullWorktree":         30 * time.Second,
			"/reliant.v1.WorktreeService/GetWorktreePR":        30 * time.Second,
			"/reliant.v1.WorktreeService/CreateWorktreePR":     30 * time.Second,
			"/reliant.v1.WorktreeService/RevertFiles":          30 * time.Second,
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

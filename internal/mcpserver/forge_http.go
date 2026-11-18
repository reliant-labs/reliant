// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	forgecli "github.com/reliant-labs/forge/cli"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
)

// The Reliant Forge connector: forge's ACCOUNT-scoped MCP tools (monitoring,
// releases, environments, plus its skills) served to a person who signed in,
// with no install. A second MCP server beside the Reliant Workspace one, on the
// same origin, sharing its OAuth discovery, bearer validation, consent model,
// limits and audit log, at its own path with its own RFC 9728 resource.
//
// WHO CALLS THE CONTROL PLANE. The tools are forge's own
// (forgecli.AddHostedTools: the CLI's verbs, run in-process). They reach the
// control plane exactly as the web console does: as the signed-in person, with
// the person's own bearer, so the control plane's own access rules apply
// unchanged. Only an OAuth bearer can do that, so a connector credential
// (`rlat_`, mcp:connector) is refused here.
//
// WHAT THE GRANT ADDS. Signing in says who is calling; the forge connector
// grant (connectorgrant.KindForge) says what this connector may reach: one
// organization, optionally some of its projects, and which tools. Every call
// re-reads the grant and is refused before anything is read unless the target
// the call resolved to (the control plane's answer, not the caller's claim)
// is inside it. The effective authority is the intersection of the two.

// ForgeMountPath is where the Reliant Forge connector serves streamable HTTP.
const ForgeMountPath = "/forge/mcp"

// ForgeSSEMountPath is its legacy (2024-11-05) HTTP+SSE transport.
const ForgeSSEMountPath = "/forge/sse"

// ForgeConsentKind is the consent page's kind parameter for a forge grant.
const ForgeConsentKind = "forge"

// forgeResource is the Reliant Forge connector's RFC 9728 resource. Its
// metadata is path-scoped: the origin's root document is the workspace's.
var forgeResource = protectedResource{
	mountPath: ForgeMountPath, name: "Reliant Forge", realm: "reliant-forge", pathScopedMetadata: true,
}

// ForgeHTTPDeps are the collaborators of the Reliant Forge endpoint.
type ForgeHTTPDeps struct {
	Store connectorgrant.Store

	// TokenValidator validates OAuth bearer tokens. Required: the endpoint
	// acts only as a signed-in person.
	TokenValidator OAuthTokenValidator

	// Bindings resolves which forge connector an OAuth client acts through.
	Bindings ForgeBindingStore

	// OAuth configures RFC 9728 discovery; it must name an authorization
	// server.
	OAuth OAuthConfig

	// ControlPlaneURL is the control plane's platform API the tools call.
	ControlPlaneURL string

	// ConsoleURL is the dashboard base the tools link to; "" is forge's
	// default (https://forge.reliantlabs.io).
	ConsoleURL string

	// ConsentBaseURL is where the consent screen lives.
	ConsentBaseURL string

	Limits Limits
	Logger *slog.Logger

	// HTTPClient is the client control-plane calls use; nil is forge's
	// default.
	HTTPClient *http.Client
}

// ForgeSession is one authenticated connection's forge grant.
type ForgeSession struct {
	GrantID      string
	UserID       string
	OrgID        string
	ProjectNames []string
	ToolNames    []string
}

type forgeSessionCtxKey struct{}

func forgeSessionFrom(ctx context.Context) *ForgeSession {
	s, _ := ctx.Value(forgeSessionCtxKey{}).(*ForgeSession)
	return s
}

// NewForgeHTTPHandler returns the http.Handler serving the Reliant Forge
// connector at ForgeMountPath and ForgeSSEMountPath.
func NewForgeHTTPHandler(deps ForgeHTTPDeps) (http.Handler, error) {
	switch {
	case deps.Store == nil:
		return nil, errors.New("mcpserver: forge: grant store is required")
	case deps.TokenValidator == nil:
		return nil, errors.New("mcpserver: forge: an OAuth token validator is required")
	case !deps.OAuth.Enabled():
		return nil, errors.New("mcpserver: forge: an OAuth authorization server is required")
	case strings.TrimSpace(deps.ControlPlaneURL) == "":
		return nil, errors.New("mcpserver: forge: a control plane URL is required")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	audit := &storeAudit{store: deps.Store, logger: logger}
	limiter := NewLimiter(deps.Limits)
	go limiter.runCleanup(context.Background())

	auth := &forgeAuth{
		store: deps.Store, tokens: deps.TokenValidator, bindings: deps.Bindings,
		oauth: deps.OAuth, consentBase: deps.ConsentBaseURL, logger: logger,
	}
	newServer := func(r *http.Request) *mcp.Server {
		sess := forgeSessionFrom(r.Context())
		if sess == nil {
			return nil // authenticate refused it first; defense in depth
		}
		srv, err := NewForgeServer(sess, ForgeServerDeps{
			Store: deps.Store, Audit: audit, Limiter: limiter,
			ControlPlaneURL: deps.ControlPlaneURL, ConsoleURL: deps.ConsoleURL, HTTPClient: deps.HTTPClient,
			// The bearer the session opened with: the legacy SSE transport
			// carries no per-request headers to a tool call.
			SessionToken: CallerToken(r.Context()),
			BoundGrant:   auth.boundGrant,
		})
		if err != nil {
			logger.ErrorContext(r.Context(), "mcpserver: forge: could not build server for session",
				"grantID", sess.GrantID, "error", err)
			return nil
		}
		return srv
	}
	streamable := mcp.NewStreamableHTTPHandler(newServer, nil)
	sse := mcp.NewSSEHandler(newServer, nil)
	mux := http.NewServeMux()
	mux.Handle(ForgeMountPath, streamable)
	mux.Handle(ForgeMountPath+"/", streamable)
	mux.Handle(ForgeSSEMountPath, sse)
	mux.Handle(ForgeSSEMountPath+"/", sse)
	return auth.middleware(mux), nil
}

// NewForgeProtectedResourceMetadataHandler serves the Reliant Forge
// connector's RFC 9728 document.
func NewForgeProtectedResourceMetadataHandler(cfg OAuthConfig) http.Handler {
	return newProtectedResourceMetadataHandler(cfg, forgeResource)
}

// ForgeMetadataPath is where the forge connector's RFC 9728 document is
// served: path-scoped, since the origin's root document is the workspace's.
const ForgeMetadataPath = WellKnownProtectedResourcePath + ForgeMountPath

// MountForgeOAuthRoutes mounts the forge connector's discovery document.
func MountForgeOAuthRoutes(mux *http.ServeMux, cfg OAuthConfig) {
	mux.Handle(ForgeMetadataPath, NewForgeProtectedResourceMetadataHandler(cfg))
}

// forgeAuth resolves a request's OAuth bearer into a ForgeSession.
type forgeAuth struct {
	store       connectorgrant.Store
	tokens      OAuthTokenValidator
	bindings    ForgeBindingStore
	oauth       OAuthConfig
	consentBase string
	logger      *slog.Logger
}

// middleware authenticates every request from scratch, as the workspace
// endpoint does: a revoked or narrowed grant takes effect on the next request.
func (a *forgeAuth) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			a.challenge(w, "missing bearer token: sign in to connect Reliant Forge")
			return
		}
		if fat.HasFormat(token) {
			// A connector credential grants mcp:connector only; it cannot act
			// as its person at the control plane, which is all these tools do.
			a.challenge(w, "Reliant Forge signs in with OAuth; a connector credential cannot be used here")
			return
		}
		userID, clientID, err := validateOAuthClient(a.tokens, token)
		if err != nil || userID == "" {
			a.challenge(w, "credential is not valid")
			return
		}
		// The client comes from the validated token, never from a header:
		// a header is the caller's claim, and an application holding a
		// user's bearer could name another application to borrow its grant.
		sess, err := resolveForgeForUser(r.Context(), a.store, a.bindings, userID, clientID, a.consentBase)
		if err != nil {
			var consent *ConsentError
			if errors.As(err, &consent) {
				writeConsentRequired(w, consent,
					"Open the consent page to choose which organization and projects this application may read, then retry.")
				return
			}
			a.challenge(w, "credential is not valid")
			return
		}
		if err := a.store.TouchGrant(r.Context(), sess.GrantID); err != nil {
			a.logger.DebugContext(r.Context(), "mcpserver: forge: could not stamp grant last-used", "grantID", sess.GrantID, "error", err)
		}
		ctx := context.WithValue(r.Context(), forgeSessionCtxKey{}, sess)
		next.ServeHTTP(w, r.WithContext(withCallerToken(ctx, token)))
	})
}

func (a *forgeAuth) challenge(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", a.oauth.challengeHeaderFor(forgeResource))
	writeError(w, http.StatusUnauthorized, msg)
}

// writeConsentRequired tells the client where its user must choose (403, not
// 401: the caller is authenticated, and re-running OAuth cannot help).
func writeConsentRequired(w http.ResponseWriter, consent *ConsentError, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             "connector selection required",
		"error_description": description,
		"consent_url":       consent.ConsentURL,
	})
}

// ForgeBindingStore reads the client bindings a forge caller resolves
// through: every binding of a user, whatever the state of its grant, so a
// binding whose grant was revoked or has expired is still seen.
type ForgeBindingStore interface {
	ListBindingsByUser(ctx context.Context, userID string) ([]*connectorgrant.ClientBinding, error)
}

// validateOAuthClient validates token and names the OAuth client it was
// issued to; a validator that cannot name one reports "".
func validateOAuthClient(v OAuthTokenValidator, token string) (userID, clientID string, err error) {
	if cv, ok := v.(OAuthClientValidator); ok {
		return cv.ValidateTokenClient(token)
	}
	userID, err = v.ValidateToken(token)
	return userID, "", err
}

// resolveForgeForUser picks the forge grant an OAuth caller acts through:
// the one the user bound this client to, while that grant is live. Every
// other case requires consent:
//
//   - a token that names no OAuth client cannot be bound to a choice;
//   - a client whose bound grant is revoked, expired or gone never inherits
//     another grant the user holds, which may be broader or in another
//     organization;
//   - a client that was never bound, or whose binding the user deleted
//     (disconnecting the application), gets no grant by default. Unlike the
//     workspace connector there is no "the user's one connector" fallback:
//     a deleted binding leaves no trace, so a fallback would hand a
//     disconnected application the user's grant again on its next request.
func resolveForgeForUser(
	ctx context.Context,
	store connectorgrant.Store,
	bindings ForgeBindingStore,
	userID, clientID, consentBase string,
) (*ForgeSession, error) {
	consent := &ConsentError{ConsentURL: forgeConsentURL(consentBase, clientID)}
	if clientID == "" {
		return nil, consent
	}
	bound, err := forgeBindingOf(ctx, bindings, userID, clientID)
	if err != nil {
		return nil, err
	}
	if bound == nil {
		return nil, consent
	}
	if g, err := store.GetGrantByID(ctx, bound.GrantID); err == nil && isForgeGrantOf(g, userID) && g.IsLive(time.Now()) == nil {
		return forgeSessionForGrant(g), nil
	}
	return nil, consent
}

// forgeBindingOf is the user's forge binding for clientID, whatever its
// grant's state; nil when the client was never bound.
func forgeBindingOf(ctx context.Context, bindings ForgeBindingStore, userID, clientID string) (*connectorgrant.ClientBinding, error) {
	return bindingFor(ctx, bindings, userID, connectorgrant.BindingClientID(connectorgrant.KindForge, clientID))
}

func isForgeGrantOf(g *connectorgrant.Grant, userID string) bool {
	return g != nil && g.KindOrDefault() == connectorgrant.KindForge && authz.CanAccessPersonal(userID, g.UserID)
}

func forgeSessionForGrant(g *connectorgrant.Grant) *ForgeSession {
	return &ForgeSession{
		GrantID: g.ID, UserID: g.UserID, OrgID: g.OrgID,
		ProjectNames: slices.Clone(g.ProjectNames), ToolNames: slices.Clone(g.AllowedTools),
	}
}

// forgeConsentURL is the consent page in forge mode.
func forgeConsentURL(base, clientID string) string {
	u := consentURLFor(base, clientID)
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + "kind=" + ForgeConsentKind
}

// ForgeServerDeps are what one session's forge server runs on.
type ForgeServerDeps struct {
	Store           connectorgrant.Store
	Audit           AuditSink
	Limiter         CallLimiter
	ControlPlaneURL string
	ConsoleURL      string
	HTTPClient      *http.Client
	// SessionToken is the bearer the session opened with, for transports
	// whose tool calls carry no headers (legacy SSE).
	SessionToken string
	// BoundGrant answers the grant the bearer's client is bound to NOW.
	// Every tool call is refused unless it is still the grant the session
	// was opened under: a session is long-lived, and a user who rebinds the
	// application to another grant (or disconnects it) must not leave the
	// open session acting through the old one. Required.
	BoundGrant func(ctx context.Context, bearer string) (string, error)
}

// NewForgeServer builds the MCP server of one forge session: forge's hosted
// tools, limited to the grant's, behind the grant's org and project check.
func NewForgeServer(sess *ForgeSession, deps ForgeServerDeps) (*mcp.Server, error) {
	if sess == nil {
		return nil, errors.New("mcpserver: forge: session is required")
	}
	if deps.Store == nil {
		return nil, errors.New("mcpserver: forge: grant store is required")
	}
	if deps.BoundGrant == nil {
		return nil, errors.New("mcpserver: forge: a bound-grant resolver is required")
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "reliant-forge", Title: "Reliant Forge", Version: serverVersion},
		&mcp.ServerOptions{Instructions: forgecli.HostedInstructions()})
	srv.AddReceivingMiddleware(forgeCallMiddleware(sess, deps))

	err := forgecli.AddHostedTools(srv, forgecli.AccountToolsOptions{
		ControlPlaneURL: deps.ControlPlaneURL,
		ConsoleURL:      deps.ConsoleURL,
		HTTPClient:      deps.HTTPClient,
		// An empty list grants nothing (fail closed), never everything.
		Tools: append([]string{}, sess.ToolNames...),
		Token: func(ctx context.Context, req *mcp.CallToolRequest) (string, error) {
			return forgeCallBearer(ctx, req, deps.SessionToken), nil
		},
		Authorize: func(ctx context.Context, tool forgecli.AccountToolInfo, target forgecli.AccountTarget) error {
			return authorizeForgeTarget(ctx, deps.Store, sess.GrantID, tool.Name, target)
		},
		NewProjectOrg: func(ctx context.Context, _ *mcp.CallToolRequest) (string, error) {
			g, err := liveForgeGrant(ctx, deps.Store, sess.GrantID)
			if err != nil {
				return "", err
			}
			return g.OrgID, nil
		},
	})
	if err != nil {
		return nil, err
	}
	return srv, nil
}

// forgeCallBearer is the bearer a tool call is made with: its own request's,
// else the one its session opened with.
func forgeCallBearer(ctx context.Context, req *mcp.CallToolRequest, sessionToken string) string {
	if req != nil && req.Extra != nil && req.Extra.Header != nil {
		if t := bearerToken(&http.Request{Header: req.Extra.Header}); t != "" {
			return t
		}
	}
	if t := CallerToken(ctx); t != "" {
		return t
	}
	return sessionToken
}

// boundGrant is the grant bearer's client is bound to now, resolved exactly
// as a new request is (validated client, live binding only).
func (a *forgeAuth) boundGrant(ctx context.Context, bearer string) (string, error) {
	userID, clientID, err := validateOAuthClient(a.tokens, bearer)
	if err != nil || userID == "" {
		return "", errForgeGrantGone
	}
	sess, err := resolveForgeForUser(ctx, a.store, a.bindings, userID, clientID, a.consentBase)
	if err != nil {
		return "", err
	}
	return sess.GrantID, nil
}

// errForgeGrantGone is a session whose grant was revoked, expired or changed.
var errForgeGrantGone = errors.New("this connector's access was revoked or has expired: reconnect Reliant Forge to choose again")

func liveForgeGrant(ctx context.Context, store connectorgrant.Store, grantID string) (*connectorgrant.Grant, error) {
	g, err := store.GetGrantByID(ctx, grantID)
	if err != nil || g.KindOrDefault() != connectorgrant.KindForge {
		return nil, errForgeGrantGone
	}
	return g, nil
}

// authorizeForgeTarget is the grant check of one call: re-read per call, so a
// grant narrowed mid-session applies at once. target comes from the control
// plane (the project's org as it resolved through the person's credential),
// so a call cannot name its way into another organization.
func authorizeForgeTarget(ctx context.Context, store connectorgrant.Store, grantID, tool string, target forgecli.AccountTarget) error {
	g, err := liveForgeGrant(ctx, store, grantID)
	if err != nil {
		return err
	}
	if !slices.Contains(g.AllowedTools, tool) {
		return fmt.Errorf("this connector is not allowed to use %s: widen it in Reliant under Settings → Access tokens (Reliant Forge connectors)", tool)
	}
	if target.OrgID != g.OrgID {
		return fmt.Errorf("project %s belongs to an organization this connector may not read: reconnect and choose that organization", target.Project)
	}
	if !g.AllowsProject(target.Project) {
		return fmt.Errorf("this connector may read only %s, not %s: widen it in Reliant under Settings → Access tokens (Reliant Forge connectors)",
			strings.Join(g.ProjectNames, ", "), target.Project)
	}
	return nil
}

// forgeCallMiddleware limits and audits every tools/call of a forge session.
// The audit row is written before the call and resolved after it, as the
// workspace endpoint's are.
func forgeCallMiddleware(sess *ForgeSession, deps ForgeServerDeps) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			call, ok := req.(*mcp.CallToolRequest)
			if method != "tools/call" || !ok || call.Params == nil {
				return next(ctx, method, req)
			}
			if deps.Limiter != nil {
				release, err := deps.Limiter.Acquire(sess.GrantID)
				if err != nil {
					return nil, err
				}
				defer release()
			}
			started := time.Now()
			var auditID string
			if deps.Audit != nil {
				auditID = deps.Audit.Begin(ctx, AuditEntry{
					GrantID: sess.GrantID, UserID: sess.UserID, ToolName: call.Params.Name, Command: call.Params.Name,
					Arguments: call.Params.Arguments, At: started.UTC(), Status: connectorgrant.AuditStarted,
				})
			}
			var res mcp.Result
			var err error
			if g, berr := deps.BoundGrant(ctx, forgeCallBearer(ctx, call, deps.SessionToken)); berr != nil || g != sess.GrantID {
				// Rebound, disconnected or revoked since the session opened:
				// the session's grant no longer speaks for this client.
				res = &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "Error: " + errForgeGrantGone.Error()}}}
			} else {
				res, err = next(ctx, method, req)
			}
			if deps.Audit != nil {
				deps.Audit.Complete(ctx, auditID, false, forgeCallError(res, err), time.Since(started))
			}
			return res, err
		}
	}
}

// forgeCallError is the error text of a call, for its audit row.
func forgeCallError(res mcp.Result, err error) string {
	if err != nil {
		return err.Error()
	}
	r, ok := res.(*mcp.CallToolResult)
	if !ok || r == nil || !r.IsError {
		return ""
	}
	for _, c := range r.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			return t.Text
		}
	}
	return "error"
}

// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/reliant-labs/reliant/internal/connectorgrant"
)

// MountPath is where the connector MCP endpoint is served.
const MountPath = "/mcp"

// SSEMountPath is the legacy (2024-11-05) HTTP+SSE transport.
//
// Streamable-HTTP at MountPath supersedes it, but clients still probe /sse to
// decide which transport a server speaks — ChatGPT's connector discovery does
// exactly this, and a 404 there fails the whole connection with
// MCP_ACTION_DISCOVERY_FAILED even after OAuth has succeeded. Serving both
// costs one extra handler and removes a class of "authenticated but unusable"
// failures that are hard to diagnose from the client side.
const SSEMountPath = "/sse"

// RootMountPath serves the streamable-HTTP transport at the origin root.
//
// RFC 9728's `resource` is an identifier, not a connection URL, so a client
// has no obligation to derive the endpoint from it — ChatGPT posts its
// handshake to whatever base URL the user typed when adding the connector.
// Someone who enters `https://example.com` (rather than `https://example.com/mcp`)
// completes OAuth and then gets a bare `404 page not found` on the handshake,
// which surfaces as MCP_ACTION_DISCOVERY_FAILED with no indication that the
// path is the problem.
//
// The `{$}` anchor matches the root and ONLY the root, so this adds one exact
// route rather than a catch-all: every other unmatched path still 404s.
const RootMountPath = "/{$}"

// HTTPDeps are the collaborators the HTTP surface needs.
type HTTPDeps struct {
	Store  connectorgrant.Store
	Sender CommandSender
	Waker  WorkspaceWaker
	Logger *slog.Logger

	// Limits bounds per-connector request rate and concurrency. The zero
	// value uses the defaults; limiting is never disabled, because this
	// endpoint is reachable from the public internet by a caller a model
	// drives.
	Limits Limits

	// OAuth configures RFC 9728 discovery. When it names an authorization
	// server, an OAuth bearer token is accepted alongside a connector
	// credential and the 401 challenge points clients at the discovery
	// document.
	OAuth OAuthConfig

	// TokenValidator validates OAuth bearer tokens. Nil means only connector
	// credentials are accepted.
	TokenValidator OAuthTokenValidator

	// Bindings resolves which connector an OAuth client acts through. Nil
	// means a user with several connectors cannot use OAuth at all, since
	// there would be no record of which one they chose.
	Bindings BindingStore

	// ConsentBaseURL is where the consent screen lives, used to tell a client
	// where to send its user when a choice is needed.
	ConsentBaseURL string
}

// OAuthTokenValidator validates an OAuth access token and returns the user it
// identifies. It is satisfied by reliant's existing Supabase JWT validator, so
// the OAuth path reuses the identity system the app already trusts rather than
// standing up a second one.
type OAuthTokenValidator interface {
	ValidateToken(token string) (userID string, err error)
}

// NewHTTPHandler returns the http.Handler serving MCP to connector clients.
//
// Every request is authenticated from scratch: MCP sessions are long-lived,
// and a revoked grant must stop working immediately rather than at the end of
// whatever session was open when the user hit revoke.
func NewHTTPHandler(deps HTTPDeps) (http.Handler, error) {
	if deps.Store == nil {
		return nil, errors.New("mcpserver: grant store is required")
	}
	if deps.Sender == nil {
		return nil, errors.New("mcpserver: command sender is required")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	audit := &storeAudit{store: deps.Store, logger: logger}
	resolver := &storeResolver{store: deps.Store}
	limiter := NewLimiter(deps.Limits)

	// Reap idle per-grant limiters for the process lifetime. Without this a
	// service that churns connectors accumulates one entry per grant.
	go limiter.RunCleanup(context.Background())

	authenticator := &connectorAuth{
		store:       deps.Store,
		oauth:       deps.OAuth,
		tokens:      deps.TokenValidator,
		bindings:    deps.Bindings,
		consentBase: deps.ConsentBaseURL,
		logger:      logger,
	}

	newServer := func(r *http.Request) *mcp.Server {
		sess := sessionFrom(r.Context())
		if sess == nil {
			// authenticate rejects unauthenticated requests before reaching
			// here, so this is defense in depth rather than a live path.
			return nil
		}
		srv, err := NewServer(sess, Deps{
			Sender:   deps.Sender,
			Waker:    deps.Waker,
			Audit:    audit,
			Resolver: resolver,
			Limiter:  limiter,
		})
		if err != nil {
			logger.Error("mcpserver: could not build server for session",
				"grantID", sess.GrantID, "error", err)
			return nil
		}
		return srv
	}

	// Both transports, sharing one authenticator and one server factory: the
	// modern streamable-HTTP endpoint and the legacy SSE one clients probe.
	streamable := mcp.NewStreamableHTTPHandler(newServer, nil)
	sse := mcp.NewSSEHandler(newServer, nil)

	mux := http.NewServeMux()
	mux.Handle(MountPath, streamable)
	mux.Handle(MountPath+"/", streamable)
	mux.Handle(SSEMountPath, sse)
	mux.Handle(SSEMountPath+"/", sse)
	// The root speaks streamable-HTTP, matching MountPath: a client that
	// treats the base URL as the endpoint is using the modern transport, and
	// probes /sse explicitly when it wants the legacy one.
	mux.Handle(RootMountPath, streamable)

	return authenticator.middleware(mux), nil
}

type sessionCtxKey struct{}

func sessionFrom(ctx context.Context) *Session {
	s, _ := ctx.Value(sessionCtxKey{}).(*Session)
	return s
}

// connectorAuth resolves a bearer token into a Session.
//
// Two credential kinds are accepted, deliberately:
//
//   - A connector credential (rlnt_conn_), which names a grant directly. This
//     is what works today from Claude Desktop and the API, with no browser.
//   - An OAuth access token from the configured authorization server, which is
//     what consumer mobile clients produce. It identifies a USER, so the grant
//     is resolved from the user's connectors rather than carried by the token.
//
// The second kind is why the grant model matters more than the token model: an
// OAuth token says who you are, not what you may touch. The confinement still
// comes from the grant, which the user authored and can revoke.
type connectorAuth struct {
	store       connectorgrant.Store
	oauth       OAuthConfig
	tokens      OAuthTokenValidator
	bindings    BindingStore
	consentBase string
	logger      *slog.Logger
}

// middleware authenticates each request and attaches the resolved session.
//
// Authentication is per request rather than per session because MCP sessions
// are long-lived: a revoked credential must stop working on its next call, not
// whenever the client happens to reconnect.
func (a *connectorAuth) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			a.challenge(w, "missing bearer token")
			return
		}

		sess, err := a.resolve(r, token)
		if err != nil {
			// A consent requirement is NOT an authentication failure, and
			// saying so matters: a client told "unauthorized" would re-run the
			// OAuth flow and get the same token and the same problem. Telling
			// it where the user must choose is the only thing that resolves.
			var consent *ConsentError
			if errors.As(err, &consent) {
				a.consentRequired(w, consent)
				return
			}
			// Unknown, revoked, and expired are reported identically:
			// distinguishing them helps someone probing credentials and helps
			// a legitimate client not at all.
			a.challenge(w, "credential is not valid")
			return
		}

		ctx := context.WithValue(r.Context(), sessionCtxKey{}, sess)
		// Carry an OAuth bearer forward so a resume can be made AS THE USER.
		// Waking a workspace is an authenticated control-plane action, and
		// reusing the caller's own token keeps it scoped to the person who
		// owns the workspace rather than introducing a service credential
		// that could wake anyone's.
		//
		// Connector credentials (rlnt_conn_) are deliberately excluded: they
		// are not accepted by the control plane, and passing one would only
		// produce a confusing auth failure at the far end.
		if !connectorgrant.IsCredentialFormat(token) {
			ctx = withCallerToken(ctx, token)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type callerTokenCtxKey struct{}

// withCallerToken carries the caller's OAuth bearer for downstream calls made
// on their behalf.
func withCallerToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, callerTokenCtxKey{}, token)
}

// CallerToken returns the OAuth bearer of the in-flight request, or "" when
// the caller authenticated with a connector credential instead.
func CallerToken(ctx context.Context) string {
	token, _ := ctx.Value(callerTokenCtxKey{}).(string)
	return token
}

// resolve turns a bearer token into a session.
func (a *connectorAuth) resolve(r *http.Request, token string) (*Session, error) {
	if connectorgrant.IsCredentialFormat(token) {
		grant, err := a.store.GetGrantByTokenHash(r.Context(), connectorgrant.HashCredential(token))
		if err != nil {
			return nil, err
		}
		a.touch(r, grant.ID)
		return sessionForGrant(grant), nil
	}

	if a.tokens == nil {
		return nil, errors.New("no OAuth validator configured")
	}

	userID, err := a.tokens.ValidateToken(token)
	if err != nil {
		return nil, err
	}
	if userID == "" {
		return nil, errors.New("token carries no subject")
	}

	// An OAuth token identifies a user, not a grant. resolveForUser bridges
	// the two: a recorded consent choice first, a single unambiguous connector
	// second, and otherwise a consent requirement rather than a guess.
	sess, err := resolveForUser(r.Context(), a.store, a.bindings, userID, oauthClientID(r), a.consentBase)
	if err != nil {
		return nil, err
	}
	a.touch(r, sess.GrantID)
	return sess, nil
}

// oauthClientID identifies the calling application.
//
// The MCP spec does not put the client id on the resource request, so it is
// read from the token's claims where the authorization server included it, or
// from a client-supplied header. When it cannot be determined, consent still
// works — the user simply authorizes "this client" without it being named.
func oauthClientID(r *http.Request) string {
	if id := strings.TrimSpace(r.Header.Get("X-MCP-Client-Id")); id != "" {
		return id
	}
	// Client name from the MCP initialize handshake, when the transport
	// surfaced it.
	return strings.TrimSpace(r.Header.Get("X-Client-Name"))
}

// touch records use. Telemetry, so a failure must never fail the request.
func (a *connectorAuth) touch(r *http.Request, grantID string) {
	if err := a.store.TouchGrant(r.Context(), grantID); err != nil {
		a.logger.Debug("mcpserver: could not stamp grant last-used", "grantID", grantID, "error", err)
	}
}

// challenge writes a 401 carrying the discovery pointer.
//
// The WWW-Authenticate header is what turns a rejection into a usable
// discovery step: a client that receives resource_metadata knows where to
// begin the OAuth flow, rather than probing well-known paths and guessing.
func (a *connectorAuth) challenge(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", a.oauth.challengeHeader())
	writeError(w, http.StatusUnauthorized, msg)
}

// consentRequired tells the client where its user must choose a connector.
//
// 403 rather than 401: the caller IS authenticated, and the missing piece is
// authorization the user has not granted yet. A 401 would send a well-behaved
// client back through the token flow, which cannot help.
func (a *connectorAuth) consentRequired(w http.ResponseWriter, consent *ConsentError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "connector selection required",
		"error_description": "Open the consent page to choose which workspace this application may use, " +
			"then retry.",
		"consent_url": consent.ConsentURL,
	})
}

// sessionForGrant builds the session a grant authorizes.
func sessionForGrant(grant *connectorgrant.Grant) *Session {
	return &Session{
		GrantID:   grant.ID,
		UserID:    grant.UserID,
		DaemonID:  grant.DaemonID,
		ToolNames: grant.AllowedTools,
		Policy:    grant.ToPolicy(CommandsForTools),
	}
}

// bearerToken extracts a Bearer credential from the Authorization header.
func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

// writeError responds with a JSON error body. MCP clients surface these to the
// user, so the text is written for a person reading it on a phone.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// storeResolver re-reads a grant from the store on each tool call.
type storeResolver struct {
	store connectorgrant.Store
}

// Resolve returns the grant's current state, or an error if it is no longer
// usable. It is looked up by credential hash so the same liveness filtering
// (revoked, expired) applies as at authentication time.
func (r *storeResolver) Resolve(ctx context.Context, grantID string) (*Session, error) {
	grant, err := r.store.GetGrantByID(ctx, grantID)
	if err != nil {
		return nil, errors.New("the connector has been revoked or has expired")
	}
	return &Session{
		GrantID:   grant.ID,
		UserID:    grant.UserID,
		DaemonID:  grant.DaemonID,
		ToolNames: grant.AllowedTools,
		Policy:    grant.ToPolicy(CommandsForTools),
	}, nil
}

// storeAudit persists audit entries.
type storeAudit struct {
	store  connectorgrant.Store
	logger *slog.Logger
}

// Begin writes an audit row before the outcome is known.
//
// A failure to record is logged loudly rather than returned: the caller is
// mid-request and failing the tool call over a logging problem would be worse
// than the gap. But an audit gap is a real problem — it is exactly the record
// someone wants after an incident — so it must not pass silently.
func (a *storeAudit) Begin(ctx context.Context, entry AuditEntry) string {
	// Detached from the request context: the client may disconnect the moment
	// it has its answer, and a cancelled write would drop exactly the records
	// most worth keeping.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	status := entry.Status
	if status == "" {
		status = connectorgrant.AuditCompleted
	}

	rec := &connectorgrant.AuditRecord{
		ID:          uuid.New().String(),
		GrantID:     entry.GrantID,
		UserID:      entry.UserID,
		DaemonID:    entry.DaemonID,
		ToolName:    entry.ToolName,
		CommandType: entry.Command,
		Arguments:   entry.Arguments,
		Denied:      entry.Denied,
		ErrorMsg:    entry.Error,
		DurationMS:  int(entry.Duration.Milliseconds()),
		CreatedAt:   entry.At,
		Status:      status,
	}

	if err := a.store.RecordAudit(writeCtx, rec); err != nil {
		a.logger.Error("mcpserver: failed to record connector audit entry",
			"grantID", entry.GrantID, "tool", entry.ToolName, "denied", entry.Denied, "error", err)
		return ""
	}
	return rec.ID
}

// Complete resolves a record begun before dispatch.
func (a *storeAudit) Complete(ctx context.Context, id string, denied bool, errMsg string, duration time.Duration) {
	if id == "" {
		return
	}

	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	status := connectorgrant.AuditCompleted
	if denied {
		status = connectorgrant.AuditDenied
	}

	if err := a.store.CompleteAudit(writeCtx, id, status, errMsg, int(duration.Milliseconds())); err != nil {
		// The intent row survives in 'started', which is the correct residue:
		// it says a command was dispatched and never accounted for.
		a.logger.Error("mcpserver: failed to complete connector audit entry",
			"auditID", id, "error", err)
	}
}

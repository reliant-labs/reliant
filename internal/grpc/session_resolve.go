package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/reliant-labs/forge/pkg/session"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
)

// SessionResolvePath is control-plane's resolution of a reliant sign-in
// session id into the managed identity's access token it stands for
// (control-plane internal/auth reliant_session.go, remoteSessionResolver):
// how control-plane honours reliant's session cookie on its own /_cp routes.
const SessionResolvePath = "/internal/identity/sessions/resolve"

// SessionResolveSessions is what the resolver reads: forge/pkg/session's
// Manager.
type SessionResolveSessions interface {
	// AccessToken is the session's access token, renewed first when it is
	// about to expire; session.ErrNoSession when the session is unknown,
	// ended or revoked.
	AccessToken(ctx context.Context, id string) (string, error)
	// Info is the session's subject (and more), without renewing it.
	Info(ctx context.Context, id string) (session.Info, error)
}

// SessionResolveVerifier verifies a token as the managed identity's
// (auth.Authenticator.VerifyIdentityToken).
type SessionResolveVerifier interface {
	VerifyIdentityToken(token string) (*auth.JWTClaims, error)
}

// sessionResolveRequest is the resolver's body.
type sessionResolveRequest struct {
	SessionID string `json:"session_id"`
}

// sessionResolveResponse is the resolver's answer: the session's VERIFIED
// access token and whose it is.
type sessionResolveResponse struct {
	AccessToken string `json:"access_token"`
	Subject     string `json:"subject"`
}

// NewSessionResolveHandler is the service-only session resolver: POST
// {"session_id": "fs_…"} answers {"access_token", "subject"} — the access
// token the session holds, renewed first when it is near expiry
// (session.Manager.AccessToken), and verified here as the managed identity's
// before it is handed out — or 404 for a session that is unknown, ended or
// revoked (a revocation on another replica reaches this one within the
// session cache's TTL, 15 seconds by default). 503 means the store or the
// issuer could not be reached: the session may be fine.
//
// It accepts only the short-lived internal-service JWT control-plane signs
// with INTERNAL_SERVICE_SECRET (the one /internal/hosted-http/redeem
// accepts), never a user's credential: not a bearer token, not an `rlat_`
// token, not the session cookie, not the session id itself. An absent secret
// or session store leaves it closed. The answer is a credential: it is never
// cached and never logged.
func NewSessionResolveHandler(sessions SessionResolveSessions, verifier SessionResolveVerifier, secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if secret == "" || sessions == nil || verifier == nil || !validHostedRedeemBearer(r.Header.Get("Authorization"), secret) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var req sessionResolveRequest
		if err := decoder.Decode(&req); err != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		notFound := func() { http.Error(w, "no such session", http.StatusNotFound) }
		if !session.IsID(req.SessionID) {
			notFound()
			return
		}
		ctx := r.Context()
		token, err := sessions.AccessToken(ctx, req.SessionID)
		if errors.Is(err, session.ErrNoSession) {
			notFound()
			return
		}
		if err != nil {
			logging.ErrorContext(ctx, "session resolver: the session could not be read", "error", err)
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		info, err := sessions.Info(ctx, req.SessionID)
		if errors.Is(err, session.ErrNoSession) {
			// Revoked between the two reads.
			notFound()
			return
		}
		if err != nil {
			logging.ErrorContext(ctx, "session resolver: the session could not be read", "error", err)
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		claims, err := verifier.VerifyIdentityToken(token)
		if err != nil || claims.Sub == "" || claims.Sub != info.Subject {
			// The store holds only what the issuer issued for this
			// session's subject, so this is a server-side fault, never the
			// caller's: refuse it, loudly.
			logging.ErrorContext(ctx, "session resolver: the session's token did not verify as its subject's",
				slog.Bool("verified", err == nil), "error", err)
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sessionResolveResponse{AccessToken: token, Subject: claims.Sub})
	})
}

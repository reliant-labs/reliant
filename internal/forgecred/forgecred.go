// Copyright (c) 2025 Reliant Labs
package forgecred

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/cloudcred"
	"github.com/reliant-labs/forge/pkg/credentials"
)

// reuseWhileRemaining is how much life a cached token must have left to be
// handed out again. The server mints for an hour, so a token is reused for its
// first half and every command forge starts gets at least thirty minutes —
// enough for a slow hosted deploy that holds one token throughout. Reusing it
// closer to expiry would trade a mint for a deploy dying halfway.
const reuseWhileRemaining = 30 * time.Minute

// signInHint is the remedy for "no Reliant session". Under Reliant the user is
// meant to sign in once, to Reliant — never to forge separately.
const signInHint = "sign in to Reliant (`reliant auth login` / the app)"

type helper struct{ deps Deps }

// New returns the helper.
//
// forge:constructor
func New(deps Deps) Service {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &helper{deps: deps}
}

// attempt is what happened to one candidate session, for the refusal message.
type attempt struct {
	session Session
	code    connect.Code
	reason  string
}

func (h *helper) Mint(ctx context.Context, req cloudcred.Request) (cloudcred.Token, error) {
	endpoint, err := credentials.Normalize(req.Endpoint)
	if err != nil {
		return cloudcred.Token{}, &cloudcred.HelperError{Code: cloudcred.CodeUnavailable,
			Message: fmt.Sprintf("forge asked for a credential for %q, which is not a control-plane URL: %v", req.Endpoint, err)}
	}
	sessions, err := h.deps.Sessions()
	if err != nil {
		return cloudcred.Token{}, &cloudcred.HelperError{Code: cloudcred.CodeUnavailable,
			Message: fmt.Sprintf("reading this machine's Reliant sessions: %v", err)}
	}
	if len(sessions) == 0 {
		return cloudcred.Token{}, &cloudcred.HelperError{Code: cloudcred.CodeNoSession,
			Message: "not signed in to Reliant on this machine — " + signInHint}
	}

	now := h.deps.Now()
	c := h.cache()
	// Any session's still-fresh token beats a mint: the candidates are all
	// this machine's, and a cached token is one the server already scoped.
	for _, s := range sessions {
		if tok, ok := c.lookup(endpoint, s, now); ok {
			return tok, nil
		}
	}

	var attempts []attempt
	for _, s := range sessions {
		got, err := h.deps.Exchanger.Exchange(ctx, s.Server, s.Token, endpoint, req.Scopes)
		if err != nil {
			attempts = append(attempts, attempt{session: s, code: connect.CodeOf(err), reason: reasonOf(err)})
			if ctx.Err() != nil {
				break
			}
			continue
		}
		tok := cloudcred.Token{
			Token:     got.Token,
			ExpiresAt: &got.ExpiresAt,
			Scopes:    got.Scopes,
			Source:    describe(s),
		}
		if got.ExpiresAt.IsZero() {
			tok.ExpiresAt = nil
		}
		c.store(endpoint, s, tok, now)
		return tok, nil
	}
	return cloudcred.Token{}, refusal(endpoint, attempts)
}

func (h *helper) ForgetServer(server string) error {
	return h.cache().forgetServer(server)
}

func (h *helper) cache() tokenCache { return tokenCache{path: h.deps.CachePath} }

// describe names a session for a human: which kind, where, which account.
// It is what forge prints as the credential's source.
func describe(s Session) string {
	key, err := credentials.Normalize(s.Server)
	if err != nil {
		key = s.Server
	}
	switch s.Kind {
	case "daemon":
		if s.Account != "" && s.Account != "_default" {
			return fmt.Sprintf("Reliant session (daemon at %s, account %s)", key, s.Account)
		}
		return fmt.Sprintf("Reliant session (daemon at %s)", key)
	default:
		return fmt.Sprintf("Reliant session (CLI login at %s)", key)
	}
}

// reasonOf is the server's own message for a Connect error, or the transport
// error. Never a token: neither the server nor net/http echoes the bearer.
func reasonOf(err error) string {
	switch connect.CodeOf(err) {
	case connect.CodeUnimplemented, connect.CodeNotFound:
		return "this Reliant server predates credential exchange; it must be updated before forge can use this session"
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		if msg := strings.TrimSpace(ce.Message()); msg != "" {
			return msg
		}
		return ce.Code().String()
	}
	return err.Error()
}

// refusal turns every candidate's failure into one answer for forge.
//
// The code is the most actionable thing that happened: a session that EXISTS
// but cannot authorize (denied) beats one that could not be reached
// (unavailable), which beats "no session belongs to this control plane"
// (no_session) — the user can fix a denial, should retry an outage, and must
// sign in somewhere else for the last.
func refusal(endpoint string, attempts []attempt) error {
	code := cloudcred.CodeNoSession
	for _, a := range attempts {
		switch classify(a.code) {
		case cloudcred.CodeDenied:
			code = cloudcred.CodeDenied
		case cloudcred.CodeUnavailable:
			if code == cloudcred.CodeNoSession {
				code = cloudcred.CodeUnavailable
			}
		}
	}

	var b strings.Builder
	switch code {
	case cloudcred.CodeDenied:
		fmt.Fprintf(&b, "you are signed in to Reliant, but no session on this machine can authorize %s:", endpoint)
	case cloudcred.CodeUnavailable:
		fmt.Fprintf(&b, "could not reach Reliant to get a credential for %s (try again):", endpoint)
	default:
		fmt.Fprintf(&b, "no Reliant session on this machine belongs to %s — %s for the deployment whose control plane that is:", endpoint, signInHint)
	}
	for _, a := range attempts {
		fmt.Fprintf(&b, "\n  - %s: %s", describe(a.session), a.reason)
	}
	return &cloudcred.HelperError{Code: code, Message: b.String()}
}

// classify maps one exchange failure onto the protocol's codes.
//
//   - PermissionDenied: the session exists and acts for this control plane,
//     but does not hold the authority (its message says how to get it).
//   - Unauthenticated: the session is no longer valid — signing in again is
//     the fix, which is the same remedy as a denial.
//   - FailedPrecondition / InvalidArgument: this server's tokens are for
//     another control plane, or it has none — not this session's endpoint.
//   - Unimplemented / NotFound: a Reliant server older than the exchange.
//   - anything else (Unavailable, transport): could not ask.
func classify(code connect.Code) cloudcred.ErrorCode {
	switch code {
	case connect.CodePermissionDenied, connect.CodeUnauthenticated:
		return cloudcred.CodeDenied
	case connect.CodeFailedPrecondition, connect.CodeInvalidArgument, connect.CodeUnimplemented, connect.CodeNotFound:
		return cloudcred.CodeNoSession
	default:
		return cloudcred.CodeUnavailable
	}
}

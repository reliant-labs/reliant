// Copyright (c) 2025 Reliant Labs

// Package forgecred is reliant's half of forge's credential-helper protocol
// (forge/pkg/cloudcred): it turns the Reliant session on this machine into a
// short-lived control-plane token, so a user signed in to Reliant never runs
// `forge login`.
//
// forge:outbound-io
//
// It is the outbound adapter to reliant's API (TokenService.ExchangeToken)
// and to a small on-disk token cache; nothing calls in but the
// `reliant auth forge-credential` command.
//
// ── WHAT IT NEVER DOES ───────────────────────────────────────────────
//
// It never hands forge the session credential itself. A daemon's credential
// is permanent and can connect as that daemon; a forge deploy passes its
// token to a registry login and to subprocesses. So the session is EXCHANGED
// at the server the session belongs to, for a token holding only what forge
// asked for (deploy, secret, domain), only what the session itself holds, and
// for at most an hour — and only for the control plane that server's tokens
// are valid at, which the SERVER checks (forge's endpoint comes from a repo's
// KCL, and a repo is not a trusted party).
//
// It never prints a token. The protocol's stdout is the only place a token
// leaves this package; every error and every log line carries origins,
// account names and scopes, never a secret.
//
// ── WHICH SESSION ────────────────────────────────────────────────────
//
// In an agent's shell the daemon PINS itself (--server, --account), so forge
// acts as the daemon the agent runs on. Elsewhere (`reliant forge …` in a
// terminal) every session on the machine is a candidate, the CLI's resolved
// server first. The server answers "not my control plane" without minting, so
// trying candidates in order is safe.
package forgecred

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/cloudcred"
)

// Service answers forge's credential requests.
type Service interface {
	// Mint answers one forge request: a cached token while plenty of its life
	// remains, else a fresh exchange. Refusals are *cloudcred.HelperError
	// with the code forge words its failure by.
	Mint(ctx context.Context, req cloudcred.Request) (cloudcred.Token, error)
	// ForgetServer drops every cached token exchanged from a session on
	// server — called on logout, so a signed-out machine stops handing out
	// tokens that outlive the sign-in.
	ForgetServer(server string) error
}

// Session is one Reliant credential that may be exchanged.
type Session struct {
	// Server is the reliant API origin the credential is presented to, and
	// where it is exchanged.
	Server string
	// Token is the session credential. Never logged, never returned.
	Token string
	// Kind is "daemon" or "cli", for messages.
	Kind string
	// Account is the daemon store account, empty for a CLI login.
	Account string
}

// Exchanged is what the server returned for one exchange.
type Exchanged struct {
	Token     string
	ExpiresAt time.Time
	Scopes    []string
}

// Exchanger calls TokenService.ExchangeToken on server with bearer.
type Exchanger interface {
	Exchange(ctx context.Context, server, bearer, audience string, scopes []string) (Exchanged, error)
}

// Deps are the Service's collaborators.
type Deps struct {
	// Sessions lists the candidate sessions, most preferred first.
	Sessions func() ([]Session, error)
	// Exchanger performs the exchange.
	Exchanger Exchanger
	// CachePath is the token cache file. Empty disables caching.
	CachePath string
	// Now is the clock. Defaults to time.Now.
	Now func() time.Time
}

var _ Service = (*helper)(nil)

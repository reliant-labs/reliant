// Package cliauth is the reliant CLI's login: the OAuth 2.0 authorization-code
// flow with PKCE against the control plane, which issues `rlat_` access tokens.
// The token lands in forge's shared credentials file, keyed by the server it is
// presented to and this client.
//
// forge:outbound-io
//
// It is the outbound adapter to the control plane's authorization server (RFC
// 8414 discovery + the token exchange, over HTTP) and to the shared
// credentials file; nothing calls in.
//
// ── WHO DOES WHAT ─────────────────────────────────────────────────────
// The mechanics are forge's (pkg/oauth2.LoopbackLogin, the same code
// `forge login` runs) and so is the file (pkg/credentials). This package
// decides only reliant's parts: its client id, the scopes it asks for, and
// how to find the authorization server for a --server.
//
// ── DISCOVERY ─────────────────────────────────────────────────────────
// A user names the server their CLI TALKS to (--server, the reliant API). The
// server that ISSUES tokens is the control plane, which is a different origin
// in prod (api. vs admin.). The API server publishes RFC 8414 metadata naming
// its authorization server, so the CLI asks the server it was given rather
// than guessing from hostnames.
//
// reliant is not a forge-managed project, so nothing generates this package's
// mock; tests fake Service or stand up an httptest authorization server.
package cliauth

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/credentials"
	"github.com/reliant-labs/forge/pkg/oauth2"
)

// Service is the CLI's login surface.
type Service interface {
	// Login runs one browser login and returns the credential, unstored — a
	// daemon registration keeps its credential in the daemon store, not the
	// CLI's. Unset Login.HTTPClient / Login.OpenURL fall back to Deps.
	Login(ctx context.Context, l Login) (credentials.Credential, error)
	// CredentialsPath is the shared credentials file this adapter reads and
	// writes.
	CredentialsPath() (string, error)
	// Store saves c as the reliant CLI's credential for server and returns
	// the file path.
	Store(server string, c credentials.Credential) (string, error)
	// Lookup returns the stored credential for server, or ErrNotLoggedIn.
	// An entry past its issuer-reported expiry counts as not logged in.
	Lookup(server string) (credentials.Credential, string, error)
	// Remove forgets the stored credential for server.
	Remove(server string) (bool, string, error)
}

// Deps are the adapter's collaborators.
type Deps struct {
	// Dirs locates the shared credentials file (see DepsFromEnv).
	Dirs credentials.Dirs
	// HTTPClient performs discovery and the token exchange; nil uses
	// bounded defaults.
	HTTPClient oauth2.HTTPDoer
	// OpenURL launches the browser; nil uses the platform opener.
	OpenURL func(string) error
	// Now is the clock expiry is judged against. Defaults to time.Now.
	Now func() time.Time
}

var _ Service = (*adapter)(nil)

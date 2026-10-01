// Copyright (c) 2025 Reliant Labs
package cliauth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/credentials"
)

// DepositTokenForServer deposits a bare token into forge's credential store
// for the control plane that issues server's tokens, discovering that origin
// from server's RFC 8414 metadata.
//
// It is DepositForForge for every credential that did not come from this
// process's own browser login — which is every credential an Electron user
// ever has. Electron mints the daemon PAT itself in JavaScript, a managed
// daemon receives one through a mounted Secret, and `--token` takes one pasted
// from the web UI; none of those carry an issuer, so none of them could reach
// a deposit keyed by one. That is why a laptop signed in to prod found only
// the local dev origin in ~/.config/forge/credentials.json.
//
// ── WHY A SELF-HOSTED SERVER IS NOT AN ERROR ──────────────────────────
//
// This runs on every daemon start, including for users with no hosted control
// plane at all. For them there is genuinely nothing to deposit, and returning
// an error would make the common case log a warning forever. ErrNoControlPlane
// is therefore swallowed here rather than at each of the three call sites,
// where it would be swallowed inconsistently.
//
// A network failure reaching a control plane that DOES exist is still
// returned: that is a real "forge will say log in" outcome and the caller
// logs it.
//
// expiresAt is carried through verbatim, nil included. A daemon PAT is
// permanent (control-plane migration 00110) and inventing an expiry would make
// forge abandon a token that is still valid.
func (a *adapter) DepositTokenForServer(ctx context.Context, server, token string, expiresAt *time.Time) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("cliauth: refusing to deposit an empty token")
	}
	issuer, err := DiscoverIssuer(ctx, a.deps.HTTPClient, server)
	if err != nil {
		if errors.Is(err, ErrNoControlPlane) {
			// Self-hosted: no control plane, nothing to deposit, not a fault.
			return nil
		}
		return fmt.Errorf("cliauth: finding the control plane for %s: %w", server, err)
	}
	cred := credentials.Credential{
		Token:     token,
		Issuer:    issuer,
		ExpiresAt: expiresAt,
	}
	if len(token) > 13 {
		cred.TokenPrefix = token[:13]
	}
	return a.DepositForForge(cred)
}

// DepositTokenForServer deposits through an adapter built from the environment.
func DepositTokenForServer(ctx context.Context, server, token string, expiresAt *time.Time) error {
	return New(DepsFromEnv()).DepositTokenForServer(ctx, server, token, expiresAt)
}

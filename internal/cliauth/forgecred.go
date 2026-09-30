// Copyright (c) 2025 Reliant Labs
package cliauth

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/cloudcred"
	"github.com/reliant-labs/forge/pkg/credentials"
)

// ONE LOGIN: being signed in to Reliant means forge is signed in too.
//
// ── WHY THE ARROW POINTS THIS WAY ─────────────────────────────────────
//
// The obvious implementation — teach forge to go and find reliant's
// credential — is the one thing forge must not do: a user deploying to hosted
// infra without the reliant harness would then have to install reliant first.
// So forge publishes the format and location of its credential store
// (forge/pkg/cloudcred) and reliant, which already embeds forge, writes into
// it. forge reads its own file exactly as it always has and never learns
// reliant exists.
//
// ── WHY THE ISSUER, NOT THE SERVER ────────────────────────────────────
//
// reliant's own entry is keyed by the API server the CLI talks to. forge talks
// to the CONTROL PLANE, which is a DIFFERENT ORIGIN in prod (api. versus
// admin.). Credential.Issuer is that origin — discovered via RFC 8414 during
// login, so it is what the deployment actually said rather than a guess from
// hostnames. Keying the deposit by the API server instead would write a
// credential forge never looks up, and the symptom is the confusing one:
// signed in to Reliant, and forge still says log in.
//
// ── WHY IT CANNOT HURT A `forge login` ────────────────────────────────
//
// Entries are keyed by (endpoint, client) and the deposit uses
// cloudcred.HostClientID, not forge's own. So a deposit can never overwrite a
// credential a human created deliberately with `forge login`, a Reliant logout
// can never delete theirs, and forge resolves its own entry FIRST — the
// deposit is a fallback, not an override.

// DepositForForge writes cred into forge's credential store for the control
// plane that issued it, so `forge deploy` and friends authenticate as this
// user with no second login.
//
// Idempotent: it replaces any previous deposit for that origin, which is what
// makes it safe to call on every login and every daemon registration without
// checking first.
//
// A credential with no Issuer is REFUSED rather than guessed at: there would
// be no origin to key by, and writing under the wrong one produces a store
// nothing reads.
func (a *adapter) DepositForForge(cred credentials.Credential) error {
	issuer := strings.TrimRight(strings.TrimSpace(cred.Issuer), "/")
	if issuer == "" {
		return fmt.Errorf("cliauth: refusing to deposit a credential with no issuer: " +
			"forge keys its store by the control-plane origin, and there is nothing to key by")
	}
	path, err := a.CredentialsPath()
	if err != nil {
		return err
	}
	return cloudcred.Save(path, issuer, cloudcred.Credential{
		Token:       cred.Token,
		TokenPrefix: cred.TokenPrefix,
		Scopes:      cred.Scopes,
		// Carried through verbatim, nil included: a daemon credential is
		// permanent (control-plane migration 00110), and inventing an expiry
		// would make forge abandon a token that is still valid.
		ExpiresAt: cred.ExpiresAt,
		Issuer:    issuer,
	})
}

// WithdrawFromForge removes the deposited credential for issuer and reports
// whether one was there. A credential a human stored with `forge login` at the
// same origin is untouched.
//
// Absent is NOT an error: a user who logged in before this shipped, or one on
// a self-hosted server with no control plane, has no deposit, and logout must
// still succeed for them.
func (a *adapter) WithdrawFromForge(issuer string) (bool, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	if issuer == "" {
		return false, nil
	}
	path, err := a.CredentialsPath()
	if err != nil {
		return false, err
	}
	return cloudcred.Delete(path, issuer)
}

// DepositForForge deposits through an adapter built from the environment.
func DepositForForge(cred credentials.Credential) error {
	return New(DepsFromEnv()).DepositForForge(cred)
}

// WithdrawFromForge withdraws through an adapter built from the environment.
func WithdrawFromForge(issuer string) (bool, error) {
	return New(DepsFromEnv()).WithdrawFromForge(issuer)
}

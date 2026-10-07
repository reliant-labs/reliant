// Copyright (c) 2025 Reliant Labs
package cliauth

import (
	"github.com/reliant-labs/forge/pkg/cloudcred"
)

// ONE LOGIN: being signed in to Reliant means forge is signed in too.
//
// ── HOW, NOW ──────────────────────────────────────────────────────────
//
// forge asks a credential helper (forge/pkg/cloudcred, $FORGE_CREDENTIAL_HELPER)
// for a token when it has none of its own, and `reliant auth forge-credential`
// answers by EXCHANGING the Reliant session at reliant's API for a short-lived
// token carrying only control-plane authority (internal/forgecred,
// TokenService.ExchangeToken). The session credential itself never reaches
// forge.
//
// ── WHAT THIS REPLACED ────────────────────────────────────────────────
//
// reliant used to DEPOSIT its own session token into forge's credentials file
// (client `host-app`), and forge presented it to the control plane's deploy
// API. That token was permanent and multi-purpose (it could also connect as
// the daemon), it mostly did not hold deploy authority at all — so deploys
// 403'd and users ran `forge login` anyway — and it sat in a second file.
// RemoveLegacyForgeDeposits cleans up what those releases wrote.

// ForgeScopes is the control-plane authority a Reliant sign-in asks for on
// forge's behalf: deploy, managed secrets, custom domains. The control plane
// clips it to the user's org grants, and the session holds it only as a
// CEILING — forge never sees the session; it gets an hour-long token
// exchanged from it, which can carry no more than the session holds.
func ForgeScopes() []string {
	return []string{"deploy:read", "deploy:write", "secret:read", "secret:write", "domain:read", "domain:write"}
}

func (a *adapter) RemoveLegacyForgeDeposits() (int, error) {
	path, err := a.CredentialsPath()
	if err != nil {
		return 0, err
	}
	return cloudcred.RemoveLegacyHostDeposits(path)
}

// RemoveLegacyForgeDeposits purges through an adapter built from the
// environment.
func RemoveLegacyForgeDeposits() (int, error) {
	return New(DepsFromEnv()).RemoveLegacyForgeDeposits()
}

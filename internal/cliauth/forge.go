// Copyright (c) 2025 Reliant Labs
package cliauth

import (
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
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

// ForgeScopes is the explicit control-plane capability ceiling requested by
// Reliant sign-ins and daemon registration, and exchangeable by the credential
// helper. Defaults follow the user's org authority; restrictions are opt-in.
// The control plane clips every mint to current org grants, and exchange also
// intersects the parent's scopes. Session and resource-specialized capabilities
// never reach forge. Keep this explicit: a new registry entry is not implicitly
// safe to delegate.
//
// token:read and token:write are deliberately absent. They administer member
// permissions and tokens, which the control plane reserves to a signed-in
// person (every token RPC refuses a machine caller), so on a long-lived daemon
// or CLI credential they are dead authority and a latent escalation path.
func ForgeScopes() []string {
	return []string{
		string(fat.ScopeDeployRead), string(fat.ScopeDeployWrite),
		string(fat.ScopeClusterManage),
		string(fat.ScopeSecretRead), string(fat.ScopeSecretWrite),
		string(fat.ScopeDomainRead), string(fat.ScopeDomainWrite),
		string(fat.ScopeIdentityRead), string(fat.ScopeIdentityManage),
		string(fat.ScopeObsRead), string(fat.ScopeObsTriage), string(fat.ScopeObsWrite),
		string(fat.ScopeDBQuery),
	}
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

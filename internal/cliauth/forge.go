// Copyright (c) 2025 Reliant Labs
package cliauth

import (
	"time"

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

// ForgeScopes is the control-plane capability ceiling requested by Reliant
// sign-ins and daemon registration, and exchangeable by the credential helper:
// by default a daemon's credential carries EVERY control-plane scope its user
// holds (owner decision), so agents on the machine can deploy, read telemetry,
// and manage secrets and domains. Restrictions are opt-in. The control plane
// clips every mint to current org grants, and exchange also intersects the
// parent's scopes, so the ceiling never exceeds the person.
//
// DERIVED FROM FORGE'S CATALOG, not listed here, so a scope forge adds is
// requested the release it ships in. A scope is a control-plane capability
// when it is the user's org authority that forge lets ride the user's own
// daemon credential (Grant.Validate with a daemon binding) and it does not act
// as a session or a personal resource (RequiresActingUser: reliant:api,
// daemon:connect, llm:invoke, mcp:connector, daemon:resume). That excludes the
// port share (proxy:port) too: forge refuses it on a daemon binding.
//
// token:read and token:write are deliberately absent. They administer member
// permissions and tokens, which the control plane reserves to a signed-in
// person (every token RPC refuses a machine caller), so on a long-lived daemon
// or CLI credential they are dead authority and a latent escalation path.
func ForgeScopes() []string {
	out := make([]string, 0, len(fat.AllScopes))
	for _, scope := range fat.AllScopes {
		if isControlPlaneCapability(scope) {
			out = append(out, string(scope))
		}
	}
	return out
}

// tokenAdministration is the one carve-out from the derived set; see
// ForgeScopes.
var tokenAdministration = fat.SetOf(fat.ScopeTokenRead, fat.ScopeTokenWrite)

// daemonProbe is a structurally valid daemon-bound grant; only its scope
// varies. Its ids are placeholders: Validate checks shape, never existence.
var daemonProbe = fat.Grant{
	OrgID: "org", Name: "probe", ActingUserID: "user",
	Resource: &fat.Resource{Kind: fat.ResourceDaemon, ID: "daemon"},
}

func isControlPlaneCapability(scope fat.Scope) bool {
	if fat.RequiresActingUser(scope) || tokenAdministration.Has(scope) {
		return false
	}
	probe := daemonProbe
	probe.Scopes = fat.SetOf(scope)
	return probe.Validate(time.Time{}) == nil
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

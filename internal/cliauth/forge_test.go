package cliauth

import (
	"testing"
	"time"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
)

// TestForgeScopes_IsEveryControlPlaneCapability: by default a daemon's (and a
// sign-in's) credential asks for every control-plane scope, so agents on the
// machine can deploy, read telemetry and manage secrets and domains. The set
// is derived from forge's catalog: every scope is either requested or one of
// the named exclusions, so a scope forge adds is requested without an edit
// here — and a new exclusion is a decision this test makes someone write down.
func TestForgeScopes_IsEveryControlPlaneCapability(t *testing.T) {
	excluded := fat.SetOf(
		// Session and personal-resource authority: never delegated to forge.
		fat.ScopeReliantAPI, fat.ScopeDaemonConnect, fat.ScopeLLMInvoke,
		fat.ScopeMCPConnector, fat.ScopeDaemonResume,
		// A port share: forge refuses it on a daemon binding.
		fat.ScopeProxyPort,
		// Token administration: reserved to a signed-in person.
		fat.ScopeTokenRead, fat.ScopeTokenWrite,
		// Machine management spends a person's money: forge never binds it
		// to a daemon (accesstoken's personOnlyScopes).
		fat.ScopeDaemonManage,
	)
	requested, err := fat.NewSet(ForgeScopes())
	if err != nil {
		t.Fatalf("ForgeScopes() names an unknown scope: %v", err)
	}
	for _, scope := range fat.AllScopes {
		switch {
		case excluded.Has(scope) && requested.Has(scope):
			t.Errorf("ForgeScopes() requests %s, which must never reach forge", scope)
		case !excluded.Has(scope) && !requested.Has(scope):
			t.Errorf("ForgeScopes() omits control-plane scope %s: a daemon credential must carry every one by default", scope)
		}
	}
	// What the owner named, spelled out so a regression reads plainly.
	for _, want := range []fat.Scope{
		fat.ScopeObsRead, fat.ScopeObsTriage, fat.ScopeObsWrite,
		fat.ScopeDeployRead, fat.ScopeDeployWrite,
		fat.ScopeSecretRead, fat.ScopeSecretWrite,
		fat.ScopeDomainRead, fat.ScopeDomainWrite,
	} {
		if !requested.Has(want) {
			t.Errorf("ForgeScopes() lacks %s", want)
		}
	}
}

// TestForgeScopes_RideADaemonBinding: every requested scope must be mintable on
// a daemon-bound credential, or a managed daemon's mint would be refused.
func TestForgeScopes_RideADaemonBinding(t *testing.T) {
	scopes, err := fat.NewSet(ForgeScopes())
	if err != nil {
		t.Fatal(err)
	}
	g := daemonProbe
	g.Scopes = scopes
	g.Scopes[fat.ScopeDaemonConnect] = struct{}{}
	if err := g.Validate(time.Time{}); err != nil {
		t.Fatalf("a daemon credential carrying ForgeScopes() is refused: %v", err)
	}
}

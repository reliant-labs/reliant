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
		// Slices of a person's Reliant API (forge #1757): they act as a
		// person (RequiresActingUser), like reliant:api, so they never ride a
		// forge credential either.
		fat.ScopeRunsStart, fat.ScopeRunsRead, fat.ScopeRunsControl,
		fat.ScopeTriggersFire, fat.ScopeTriggersManage,
		fat.ScopeInboxRead, fat.ScopeInboxManage,
		fat.ScopeApprovalsDecide, fat.ScopeQuestionsAnswer,
		// A port share: forge refuses it on a daemon binding.
		fat.ScopeProxyPort,
		// Token administration: reserved to a signed-in person.
		fat.ScopeTokenRead, fat.ScopeTokenWrite,
		// Machine management spends a person's money: forge never binds it
		// to a daemon (accesstoken's personOnlyScopes).
		fat.ScopeDaemonManage,
		// A registry-only credential (forge #2059): a grant naming it names
		// no other scope and is bound to nothing, so forge refuses it on a
		// daemon binding, and a control plane refuses it on every RPC.
		fat.ScopeSourcemapsWrite,
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

// TestForgeScopes_NeverActAsAPerson: the exclusion list above is written by
// hand; this walk is not. Every scope forge's catalog marks as acting for a
// person (RequiresActingUser) — today and any it adds later — stays out of the
// CLI sign-in's forge ceiling, and so out of every exchange derived from it.
func TestForgeScopes_NeverActAsAPerson(t *testing.T) {
	requested, err := fat.NewSet(ForgeScopes())
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range fat.AllScopes {
		if fat.RequiresActingUser(scope) && requested.Has(scope) {
			t.Errorf("ForgeScopes() requests %s, which acts as a person", scope)
		}
	}
	for _, scope := range []fat.Scope{
		fat.ScopeRunsStart, fat.ScopeRunsRead, fat.ScopeRunsControl,
		fat.ScopeTriggersFire, fat.ScopeTriggersManage,
	} {
		if !fat.RequiresActingUser(scope) {
			t.Errorf("forge no longer marks %s as acting for a person: revisit ForgeScopes' exclusions", scope)
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

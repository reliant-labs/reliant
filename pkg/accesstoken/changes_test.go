package accesstoken

import (
	"testing"
	"time"
)

// TestChangesScopes_Parse pins the wire spellings.
func TestChangesScopes_Parse(t *testing.T) {
	for wire, want := range map[string]Scope{
		"changes:write":  ScopeChangesWrite,
		"changes:launch": ScopeChangesLaunch,
	} {
		got, ok := ParseScope(wire)
		if !ok || got != want {
			t.Errorf("ParseScope(%q) = %q, %v", wire, got, ok)
		}
	}
}

// TestChangesScopes_Permits: launch implies write (a launcher can stop),
// write implies nothing, and no other scope implies either.
func TestChangesScopes_Permits(t *testing.T) {
	if !SetOf(ScopeChangesLaunch).Permits(ScopeChangesWrite) {
		t.Error("changes:launch must permit changes:write (stop)")
	}
	if SetOf(ScopeChangesWrite).Permits(ScopeChangesLaunch) {
		t.Error("changes:write permitted changes:launch")
	}
	for _, other := range AllScopes {
		if other == ScopeChangesWrite || other == ScopeChangesLaunch {
			continue
		}
		for _, c := range ChangesScopes {
			if SetOf(other).Permits(c) {
				t.Errorf("%s permitted %s", other, c)
			}
			if c == ScopeChangesLaunch && SetOf(c).Permits(other) {
				t.Errorf("%s permitted %s", c, other)
			}
		}
	}
	// Covers is exact: implying write at use is not a minting power.
	if SetOf(ScopeTokenWrite, ScopeChangesLaunch).Covers(SetOf(ScopeChangesWrite)) {
		t.Error("changes:launch let a holder mint changes:write")
	}
}

// TestChangesScopes_OrgTokenAndEnvBinding: an org automation token carries
// them with no acting user, and either may be confined to one environment.
func TestChangesScopes_OrgTokenAndEnvBinding(t *testing.T) {
	now := time.Now()
	env := &Resource{Kind: ResourceEnvironment, ID: "env-prod"}
	for _, s := range ChangesScopes {
		if RequiresActingUser(s) {
			t.Errorf("%s must not require an acting user", s)
		}
		if err := (Grant{OrgID: "org", Name: "ci", Scopes: SetOf(s)}).Validate(now); err != nil {
			t.Errorf("an org token carrying %s: %v", s, err)
		}
		if err := (Grant{OrgID: "org", Name: "ci", Scopes: SetOf(s), Resource: env}).Validate(now); err != nil {
			t.Errorf("%s bound to an environment was refused: %v", s, err)
		}
		p := &Principal{Scopes: SetOf(s), Resource: env}
		if !p.MayReadEnvironment("env-prod") || p.MayReadEnvironment("env-staging") {
			t.Errorf("an env-bound %s token escaped its environment", s)
		}
	}
}

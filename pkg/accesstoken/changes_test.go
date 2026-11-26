package accesstoken

import (
	"testing"
	"time"
)

// TestChangesScopes_Parse pins the wire spellings.
func TestChangesScopes_Parse(t *testing.T) {
	for wire, want := range map[string]Scope{
		"changes:read":   ScopeChangesRead,
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
// write and launch imply read, read implies nothing, and no other scope
// implies any of them.
func TestChangesScopes_Permits(t *testing.T) {
	if !SetOf(ScopeChangesLaunch).Permits(ScopeChangesWrite) {
		t.Error("changes:launch must permit changes:write (stop)")
	}
	for _, s := range []Scope{ScopeChangesWrite, ScopeChangesLaunch} {
		if !SetOf(s).Permits(ScopeChangesRead) {
			t.Errorf("%s must permit changes:read", s)
		}
	}
	if SetOf(ScopeChangesWrite).Permits(ScopeChangesLaunch) {
		t.Error("changes:write permitted changes:launch")
	}
	for _, s := range []Scope{ScopeChangesWrite, ScopeChangesLaunch} {
		if SetOf(ScopeChangesRead).Permits(s) {
			t.Errorf("changes:read permitted %s", s)
		}
	}
	isChanges := func(s Scope) bool {
		for _, c := range ChangesScopes {
			if s == c {
				return true
			}
		}
		return false
	}
	for _, other := range AllScopes {
		if isChanges(other) {
			continue
		}
		for _, c := range ChangesScopes {
			if SetOf(other).Permits(c) {
				t.Errorf("%s permitted %s", other, c)
			}
			if SetOf(c).Permits(other) {
				t.Errorf("%s permitted %s", c, other)
			}
		}
	}
	// Covers is exact: implying a scope at use is not a minting power.
	if SetOf(ScopeTokenWrite, ScopeChangesLaunch).Covers(SetOf(ScopeChangesWrite)) {
		t.Error("changes:launch let a holder mint changes:write")
	}
	if SetOf(ScopeTokenWrite, ScopeChangesWrite).Covers(SetOf(ScopeChangesRead)) {
		t.Error("changes:write let a holder mint changes:read")
	}
}

// TestChangesScopes_ReadIsNarrowestFirst: ChangesScopes stays ordered by
// authority, and changes:read is appended to AllScopes so no store's CHECK
// order shifts.
func TestChangesScopes_ReadIsNarrowestFirst(t *testing.T) {
	if len(ChangesScopes) != 3 || ChangesScopes[0] != ScopeChangesRead ||
		ChangesScopes[1] != ScopeChangesWrite || ChangesScopes[2] != ScopeChangesLaunch {
		t.Fatalf("ChangesScopes = %v, want [changes:read changes:write changes:launch]", ChangesScopes)
	}
	pos := map[Scope]int{}
	for i, s := range AllScopes {
		pos[s] = i
	}
	if pos[ScopeChangesRead] < pos[ScopeMCPForge] {
		t.Error("changes:read must be appended after mcp:forge, the member before it, so no store's CHECK order shifts")
	}
}

// TestChangesScopes_OrgTokenAndEnvBinding: an org automation token carries
// them with no acting user, and each may be confined to one environment.
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

package accesstoken

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// federatableRows returns every catalog row whose federation class is not
// FederationNone: what the rows CLAIM, before Federatable's own guard. The
// rules below are checked against the rows, so a mistaken row fails here
// rather than being silently dropped at mint.
func federatableRows() []scopeEntry {
	var out []scopeEntry
	for _, e := range catalog {
		if e.federation != FederationNone {
			out = append(out, e)
		}
	}
	return out
}

// TestFederatableScopesAreInBundleCI keeps a federated token a subset of what
// an org's CI token may already hold.
func TestFederatableScopesAreInBundleCI(t *testing.T) {
	rows := federatableRows()
	if len(rows) == 0 {
		t.Fatal("no federatable scopes: the test proves nothing")
	}
	ci := ExpandBundle(BundleCI)
	for _, e := range rows {
		if !ci.Has(e.scope) {
			t.Errorf("%s is federatable (%d) but not in BundleCI: a federated token must be a subset of an org CI token", e.scope, e.federation)
		}
	}
}

// memberFederatable is the one membership-implied scope a CI identity may be
// minted: tests:write, membership-implied since 2026-11-25 (owner: daemons
// hold it by default) and append-only evidence any CI run may record. A member
// scope joins only as a reviewed diff of this list, and only while it is also
// in BundleCI (TestFederatableScopesAreInBundleCI).
var memberFederatable = []Scope{ScopeTestsWrite}

// TestNoPrivilegedScopeIsFederatable holds the classes and kinds of scope a
// CI identity must never be minted: sensitive and personal scopes, member
// scopes other than memberFederatable, registry-only credentials, and scopes
// that act as a person.
func TestNoPrivilegedScopeIsFederatable(t *testing.T) {
	for _, e := range federatableRows() {
		switch {
		case e.class == ClassSensitive, e.class == ClassPersonal,
			e.class == ClassMember && !slices.Contains(memberFederatable, e.scope):
			t.Errorf("%s is %s but federatable: only standard org authority (and %v) may be minted for a CI identity", e.scope, e.class, memberFederatable)
		}
		if RegistryOnly(e.scope) {
			t.Errorf("%s is registry-only but federatable: it is a whole credential of its own", e.scope)
		}
		if RequiresActingUser(e.scope) {
			t.Errorf("%s acts as a person but is federatable: a CI identity is nobody", e.scope)
		}
	}
	for _, scope := range AllScopes {
		for _, protected := range []bool{false, true} {
			if !Federatable(scope, protected) {
				continue
			}
			if c := ClassOf(scope); c != ClassStandard && !(c == ClassMember && slices.Contains(memberFederatable, scope)) {
				t.Errorf("Federatable(%s, %v) = true for class %s", scope, protected, c)
			}
			if RegistryOnly(scope) || RequiresActingUser(scope) {
				t.Errorf("Federatable(%s, %v) = true for a registry-only or acting-user scope", scope, protected)
			}
		}
	}
}

// TestDeployAndTokenScopesAreNeverFederatable: deploy:write promotes releases
// and authorizes exchanges, and token:* issues credentials. Neither is ever
// minted for a CI identity, on any ref.
func TestDeployAndTokenScopesAreNeverFederatable(t *testing.T) {
	never := []Scope{ScopeDeployWrite}
	for _, scope := range AllScopes {
		if strings.HasPrefix(string(scope), "token:") {
			never = append(never, scope)
		}
	}
	if len(never) < 3 {
		t.Fatalf("found %v: expected deploy:write and the token:* scopes", never)
	}
	for _, scope := range never {
		if got := FederationOf(scope); got != FederationNone {
			t.Errorf("FederationOf(%s) = %d, want FederationNone", scope, got)
		}
		if Federatable(scope, true) {
			t.Errorf("Federatable(%s, true) = true", scope)
		}
	}
}

// TestFederatableScopes pins today's lists: any ref may upload test runs, and
// only a protected ref may also record SBOMs. A scope that joins either list
// changes this test in the same PR, where it is reviewed.
func TestFederatableScopes(t *testing.T) {
	if got, want := FederatableScopes(false), []Scope{ScopeTestsWrite}; !reflect.DeepEqual(got, want) {
		t.Errorf("FederatableScopes(false) = %v, want %v", got, want)
	}
	if got, want := FederatableScopes(true), []Scope{ScopeTestsWrite, ScopeSBOMWrite}; !reflect.DeepEqual(got, want) {
		t.Errorf("FederatableScopes(true) = %v, want %v", got, want)
	}
	if got := FederationOf(ScopeTestsWrite); got != FederationAnyRef {
		t.Errorf("FederationOf(tests:write) = %d, want FederationAnyRef", got)
	}
	if got := FederationOf(ScopeSBOMWrite); got != FederationProtectedRef {
		t.Errorf("FederationOf(sbom:write) = %d, want FederationProtectedRef", got)
	}
	if got := FederationOf("tests:wrIte"); got != FederationNone {
		t.Errorf("FederationOf(unknown) = %d, want FederationNone", got)
	}
	if Federatable("tests:wrIte", true) {
		t.Error("Federatable(unknown, true) = true")
	}
}

// TestFederatableHonoursRefProtection exercises the protected-ref class and
// Federatable's guard with rows the real catalog does not have yet: a
// protected-ref scope is minted only on a protected ref, and a row that
// claims federation for a scope outside BundleCI, or a sensitive one, widens
// nothing.
func TestFederatableHonoursRefProtection(t *testing.T) {
	saved := catalog
	t.Cleanup(func() { catalog = saved })
	catalog = append(slices.Clone(saved),
		scopeEntry{scope: "x:protected", class: ClassStandard, bundles: inCIAndUp, federation: FederationProtectedRef},
		scopeEntry{scope: "x:notci", class: ClassStandard, bundles: inDeveloperAndUp, federation: FederationAnyRef},
		scopeEntry{scope: "x:sensitive", class: ClassSensitive, bundles: inCIAndUp, federation: FederationAnyRef},
	)

	if Federatable("x:protected", false) {
		t.Error("Federatable(protected-ref scope, unprotected ref) = true")
	}
	if !Federatable("x:protected", true) {
		t.Error("Federatable(protected-ref scope, protected ref) = false")
	}
	for _, scope := range []Scope{"x:notci", "x:sensitive"} {
		if Federatable(scope, true) {
			t.Errorf("Federatable(%s, true) = true: a row outside BundleCI or sensitive must widen nothing", scope)
		}
	}
	if got, want := FederatableScopes(false), []Scope{ScopeTestsWrite}; !reflect.DeepEqual(got, want) {
		t.Errorf("FederatableScopes(false) = %v, want %v", got, want)
	}
	if got, want := FederatableScopes(true), []Scope{ScopeTestsWrite, ScopeSBOMWrite, "x:protected"}; !reflect.DeepEqual(got, want) {
		t.Errorf("FederatableScopes(true) = %v, want %v", got, want)
	}
}

// TestSBOMWriteIsProtectedRefOnly: a recorded SBOM is write-once, so
// sbom:write is minted for a CI identity only on a protected ref, and it is
// the narrow scope that does so — standard, in BundleCI, bound to nothing,
// acting as nobody, never deploy:write.
func TestSBOMWriteIsProtectedRefOnly(t *testing.T) {
	if Federatable(ScopeSBOMWrite, false) {
		t.Error("Federatable(sbom:write, unprotected ref) = true: the first SBOM written stands, so an unprotected branch must not write it")
	}
	if !Federatable(ScopeSBOMWrite, true) {
		t.Error("Federatable(sbom:write, protected ref) = false")
	}
	if c := ClassOf(ScopeSBOMWrite); c != ClassStandard {
		t.Errorf("ClassOf(sbom:write) = %s, want standard", c)
	}
	if !ExpandBundle(BundleCI).Has(ScopeSBOMWrite) {
		t.Error("sbom:write is not in BundleCI")
	}
	if _, bindable := scopeResourceRules[ScopeSBOMWrite]; bindable {
		t.Error("sbom:write has a binding rule: an upload names its release in the request, which a binding would not confine")
	}
	if RequiresActingUser(ScopeSBOMWrite) || RegistryOnly(ScopeSBOMWrite) {
		t.Error("sbom:write acts as a person or is registry-only")
	}
	held := SetOf(ScopeSBOMWrite)
	for _, scope := range AllScopes {
		if scope != ScopeSBOMWrite && held.Permits(scope) {
			t.Errorf("sbom:write permits %s: it implies nothing", scope)
		}
	}
	if SetOf(ScopeDeployWrite).Permits(ScopeSBOMWrite) {
		t.Error("deploy:write permits sbom:write in this package: a consumer that accepts both says so in its own rpc mapping")
	}
}

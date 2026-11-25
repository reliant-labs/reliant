package accesstoken

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestEveryScopeIsClassified is what makes an unclassified scope impossible
// to ship: the catalog lists every scope of AllScopes, in its order, once,
// with one of the four classes.
func TestEveryScopeIsClassified(t *testing.T) {
	if len(catalog) != len(AllScopes) {
		t.Fatalf("catalog has %d rows, AllScopes %d scopes: every scope needs exactly one catalog row", len(catalog), len(AllScopes))
	}
	seen := map[Scope]bool{}
	for i, e := range catalog {
		if e.scope != AllScopes[i] {
			t.Errorf("catalog row %d is %s, AllScopes[%d] is %s: keep the catalog in AllScopes order", i, e.scope, i, AllScopes[i])
		}
		if seen[e.scope] {
			t.Errorf("%s has two catalog rows", e.scope)
		}
		seen[e.scope] = true
		if !slices.Contains(AllClasses, e.class) {
			t.Errorf("%s has class %q, not one of %v", e.scope, e.class, AllClasses)
		}
		if ClassOf(e.scope) != e.class {
			t.Errorf("ClassOf(%s) = %q, catalog says %q", e.scope, ClassOf(e.scope), e.class)
		}
	}
	if got := ClassOf("deploy:wrIte"); got != "" {
		t.Errorf("ClassOf(unknown) = %q, want empty", got)
	}
	var total int
	for _, c := range AllClasses {
		total += len(ScopesOfClass(c))
	}
	if total != len(AllScopes) {
		t.Errorf("ScopesOfClass over every class yields %d scopes, want %d", total, len(AllScopes))
	}
}

// TestNoSensitiveScopeIsReachableThroughABundle is the owner's rule: a
// sensitive scope is held only by a grant naming it. It holds on every path a
// bundle is read — the catalog row, ExpandBundle, BundlesOf, and the stored
// "bundle:<name>" reference — whatever Elevatable says about the scope.
func TestNoSensitiveScopeIsReachableThroughABundle(t *testing.T) {
	sensitive := ScopesOfClass(ClassSensitive)
	if len(sensitive) == 0 {
		t.Fatal("no sensitive scopes: the test proves nothing")
	}
	for _, e := range catalog {
		if e.class == ClassSensitive && len(e.bundles) > 0 {
			t.Errorf("catalog lists sensitive %s in bundles %v: a sensitive scope joins no bundle", e.scope, e.bundles)
		}
	}
	var allRefs []string
	for _, b := range AllBundles {
		allRefs = append(allRefs, BundleRef(b))
		resolved, skipped := ResolveStored([]string{BundleRef(b)})
		if len(skipped) > 0 {
			t.Errorf("ResolveStored(%s) skipped %v", BundleRef(b), skipped)
		}
		for _, s := range sensitive {
			if ExpandBundle(b).Has(s) {
				t.Errorf("ExpandBundle(%s) grants sensitive %s", b, s)
			}
			if resolved.Has(s) {
				t.Errorf("ResolveStored(%s) grants sensitive %s (elevatable: %v)", BundleRef(b), s, Elevatable(s))
			}
		}
	}
	all, _ := ResolveStored(allRefs)
	for _, s := range sensitive {
		if all.Has(s) {
			t.Errorf("every bundle together grants sensitive %s", s)
		}
		if got := BundlesOf(s); len(got) > 0 {
			t.Errorf("BundlesOf(%s) = %v, want none", s, got)
		}
	}
}

// TestMembershipImpliedScopesAreNarrow guards the widest grant there is:
// every member of an org, and every credential acting as one (their daemon,
// its exchanged helpers, their personal tokens), holds a ClassMember scope
// with no grant naming it. So none may be sensitive, NeverOnDaemon, personal
// or a whole credential of its own, each must ride a daemon's credential
// (Grant.Admissible on a daemon binding), and the set is pinned exactly: a
// scope joins it only as a reviewed diff of this list.
func TestMembershipImpliedScopesAreNarrow(t *testing.T) {
	member := ScopesOfClass(ClassMember)
	if want := []Scope{ScopeObsRead, ScopeObsTriage, ScopeTestsWrite}; !reflect.DeepEqual(member, want) {
		t.Fatalf("membership-implied = %v, want exactly %v: a scope every member holds is an owner decision", member, want)
	}
	daemon := Grant{OrgID: "org", Name: "probe", ActingUserID: "user",
		Resource: &Resource{Kind: ResourceDaemon, ID: "daemon"}}.Admissible()
	for _, s := range member {
		if ClassOf(s) == ClassSensitive || NeverOnDaemon(s) || RequiresActingUser(s) || wholeCredential(s) {
			t.Errorf("membership-implied %s is sensitive, never-on-daemon, personal or a whole credential", s)
		}
		if !daemon.Has(s) {
			t.Errorf("membership-implied %s cannot ride a daemon's credential", s)
		}
	}
	for _, s := range ScopesOfClass(ClassSensitive) {
		if slices.Contains(member, s) {
			t.Errorf("sensitive %s is membership-implied", s)
		}
	}
}

// TestExpandBundle_SkipsASensitiveRowEvenIfListed proves the expansion's own
// guard, not just the table: a mistaken row cannot widen a bundle.
func TestExpandBundle_SkipsASensitiveRowEvenIfListed(t *testing.T) {
	saved := catalog
	t.Cleanup(func() { catalog = saved })
	catalog = append(slices.Clone(saved), scopeEntry{scope: "x:sensitive", class: ClassSensitive, bundles: inCIAndUp})
	for _, b := range AllBundles {
		if ExpandBundle(b).Has("x:sensitive") {
			t.Errorf("ExpandBundle(%s) expanded a sensitive row", b)
		}
	}
}

// wholeCredential reports whether scope can only be a credential of its own
// shape, never one scope among a bundle's: registry-only, an MCP client's
// token, a scope that must be bound to one resource, and a daemon's own
// connection and machine-management credentials.
func wholeCredential(scope Scope) bool {
	rule, bindable := scopeResourceRules[scope]
	return RegistryOnly(scope) || IsOAuthClientScope(scope) || (bindable && rule.required) ||
		scope == ScopeDaemonConnect || scope == ScopeDaemonManage
}

// TestAdminIsEveryMemberAndStandardScopeButWholeCredentials pins admin to its
// rule, so a new standard scope reaches every admin the release it ships: the
// catalog row that forgets admin fails here.
func TestAdminIsEveryMemberAndStandardScopeButWholeCredentials(t *testing.T) {
	want := Set{}
	for _, scope := range AllScopes {
		c := ClassOf(scope)
		if (c == ClassMember || c == ClassStandard) && !wholeCredential(scope) {
			want[scope] = struct{}{}
		}
	}
	got := ExpandBundle(BundleAdmin)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("admin = %s\nwant    %s", got, want)
	}
	for scope := range got {
		if ClassOf(scope) == ClassPersonal {
			t.Errorf("admin holds personal %s: a person holds it already", scope)
		}
	}
}

// TestBundles_HoldNoWholeCredentialAndValidateAsOneGrant holds each bundle to
// the grant shape its holder has, so expanding it can never produce a set the
// grant rules refuse: ci and admin as an org grant with no acting user,
// developer acting as a person. Unbound in every case.
func TestBundles_HoldNoWholeCredentialAndValidateAsOneGrant(t *testing.T) {
	for _, b := range AllBundles {
		set := ExpandBundle(b)
		if len(set) == 0 {
			t.Errorf("bundle %s is empty", b)
		}
		for scope := range set {
			if wholeCredential(scope) {
				t.Errorf("bundle %s holds %s, a whole credential of its own", b, scope)
			}
		}
		g := Grant{OrgID: "org", Name: string(b), Scopes: set}
		if b == BundleDeveloper {
			g.ActingUserID = "user"
		}
		if err := g.Validate(time.Time{}); err != nil {
			t.Errorf("bundle %s does not validate as one grant: %v", b, err)
		}
	}
	for scope := range ExpandBundle(BundleCI) {
		if RequiresActingUser(scope) {
			t.Errorf("ci holds %s, which acts as a person: an org CI token has none", scope)
		}
	}
	for scope := range ExpandBundle(BundleAdmin) {
		if RequiresActingUser(scope) {
			t.Errorf("admin holds %s, which acts as a person", scope)
		}
	}
}

// TestBundles_AreNestedOverOrgAuthority: every org scope CI holds, a
// developer holds; every org scope a developer holds, an admin holds.
func TestBundles_AreNestedOverOrgAuthority(t *testing.T) {
	dev := ExpandBundle(BundleDeveloper)
	for scope := range ExpandBundle(BundleCI) {
		if !dev.Has(scope) {
			t.Errorf("ci holds %s and developer does not", scope)
		}
	}
	admin := ExpandBundle(BundleAdmin)
	for scope := range dev {
		if ClassOf(scope) != ClassPersonal && !admin.Has(scope) {
			t.Errorf("developer holds org scope %s and admin does not", scope)
		}
	}
}

// TestDeveloper_HoldsNoSecret: secrets are the highest-value thing a leaked
// developer token could reach, so a developer is granted secret:read
// explicitly. Admin carries it.
func TestDeveloper_HoldsNoSecret(t *testing.T) {
	if ExpandBundle(BundleDeveloper).Has(ScopeSecretRead) || ExpandBundle(BundleDeveloper).Permits(ScopeSecretRead) {
		t.Error("developer reaches secret:read")
	}
	if !ExpandBundle(BundleAdmin).Has(ScopeSecretRead) {
		t.Error("admin lacks secret:read")
	}
	if got := BundlesOf(ScopeSecretRead); !reflect.DeepEqual(got, []Bundle{BundleAdmin}) {
		t.Errorf("BundlesOf(secret:read) = %v, want [admin]", got)
	}
}

// TestBundleContents pins each bundle exactly, so a change to one is a
// reviewed diff of this list.
func TestBundleContents(t *testing.T) {
	want := map[Bundle][]Scope{
		BundleCI: {ScopeDeployRead, ScopeDeployWrite, ScopeVulnRead, ScopeTestsWrite},
		BundleDeveloper: {
			ScopeDeployRead, ScopeDeployWrite, ScopeReliantAPI, ScopeLLMInvoke,
			ScopeDomainRead, ScopeObsRead, ScopeObsTriage, ScopeObsWrite,
			ScopeRunsStart, ScopeRunsRead, ScopeRunsControl, ScopeTriggersFire, ScopeTriggersManage,
			ScopeInboxRead, ScopeInboxManage, ScopeQuestionsAnswer,
			ScopeChangesWrite, ScopeVulnRead, ScopeTestsRead, ScopeTestsWrite,
		},
		BundleAdmin: {
			ScopeDeployRead, ScopeDeployWrite, ScopeBillingRead, ScopeClusterManage,
			ScopeSecretRead, ScopeDomainRead, ScopeDomainWrite, ScopeIdentityRead,
			ScopeObsRead, ScopeObsTriage, ScopeObsWrite,
			ScopeChangesWrite, ScopeVulnRead, ScopeTestsRead, ScopeTestsWrite,
		},
	}
	for b, scopes := range want {
		if got := ExpandBundle(b); !reflect.DeepEqual(got, SetOf(scopes...)) {
			t.Errorf("%s = %s\nwant %s", b, got, SetOf(scopes...))
		}
	}
	if got := ExpandBundle("readonly"); len(got) != 0 {
		t.Errorf("unknown bundle expands to %s", got)
	}
}

// TestClasses_MatchWhatConsumersHeld pins the classes to the lists
// control-plane kept by hand before the catalog had them, so deriving those
// lists from ClassOf changes no holder's authority.
func TestClasses_MatchWhatConsumersHeld(t *testing.T) {
	personal := []Scope{
		ScopeReliantAPI, ScopeDaemonConnect, ScopeDaemonManage, ScopeDaemonResume, ScopeLLMInvoke,
		ScopeProxyPort, ScopeMCPConnector, ScopeMCPOAuth, ScopeMCPForge,
		ScopeRunsStart, ScopeRunsRead, ScopeRunsControl, ScopeTriggersFire, ScopeTriggersManage,
		ScopeInboxRead, ScopeInboxManage, ScopeApprovalsDecide, ScopeQuestionsAnswer,
	}
	if got := SetOf(ScopesOfClass(ClassPersonal)...); !reflect.DeepEqual(got, SetOf(personal...)) {
		t.Errorf("personal = %s\nwant      %s", got, SetOf(personal...))
	}
	// tests:write joined on 2026-11-25 (owner: daemons hold it by default);
	// it was standard before.
	if got := ScopesOfClass(ClassMember); !reflect.DeepEqual(got, []Scope{ScopeObsRead, ScopeObsTriage, ScopeTestsWrite}) {
		t.Errorf("member = %v, want [obs:read obs:triage tests:write]", got)
	}
	sensitive := []Scope{
		ScopeDeployConfigRead, ScopeTokenRead, ScopeTokenWrite, ScopeSecretWrite,
		ScopeIdentityManage, ScopeDBQuery, ScopeStorageRead, ScopeChangesLaunch,
	}
	if got := SetOf(ScopesOfClass(ClassSensitive)...); !reflect.DeepEqual(got, SetOf(sensitive...)) {
		t.Errorf("sensitive = %s\nwant       %s", got, SetOf(sensitive...))
	}
	for _, scope := range actingUserScopes {
		if ClassOf(scope) != ClassPersonal {
			t.Errorf("%s acts as a person but is class %s, not personal", scope, ClassOf(scope))
		}
	}
}

// TestNeverOnDaemonAndElevatable pins the two flags to control-plane's
// neverOnADaemon and exchange exclusions.
func TestNeverOnDaemonAndElevatable(t *testing.T) {
	var never []Scope
	for _, scope := range AllScopes {
		if NeverOnDaemon(scope) {
			never = append(never, scope)
		}
	}
	if want := []Scope{ScopeTokenRead, ScopeTokenWrite, ScopeApprovalsDecide}; !reflect.DeepEqual(never, want) {
		t.Errorf("NeverOnDaemon = %v, want %v", never, want)
	}
	excluded := SetOf(ScopeDeployWrite, ScopeTokenRead, ScopeTokenWrite, ScopeSourcemapsWrite, ScopeChangesLaunch)
	for _, scope := range AllScopes {
		want := ClassOf(scope) != ClassPersonal && !excluded.Has(scope)
		if got := Elevatable(scope); got != want {
			t.Errorf("Elevatable(%s) = %v, want %v", scope, got, want)
		}
	}
	if !Elevatable(ScopeIdentityManage) || !Elevatable(ScopeDBQuery) {
		t.Error("identity:manage and db:query are elevatable today; elevation is not the bundle rule")
	}
	if Elevatable("deploy:wrIte") || NeverOnDaemon("deploy:wrIte") {
		t.Error("an unknown scope has no flags")
	}
}

func TestBundleRefs(t *testing.T) {
	for _, b := range AllBundles {
		ref := BundleRef(b)
		if ref != "bundle:"+string(b) {
			t.Errorf("BundleRef(%s) = %q", b, ref)
		}
		if got, ok := ParseBundleRef(ref); !ok || got != b {
			t.Errorf("ParseBundleRef(%q) = %q, %v", ref, got, ok)
		}
		if got, ok := ParseBundle(string(b)); !ok || got != b {
			t.Errorf("ParseBundle(%q) = %q, %v", b, got, ok)
		}
	}
	for _, bad := range []string{"ci", "bundle:", "bundle:readonly", "bundle:CI", "deploy:read"} {
		if _, ok := ParseBundleRef(bad); ok {
			t.Errorf("ParseBundleRef(%q) accepted", bad)
		}
	}
	for _, scope := range AllScopes {
		if strings.HasPrefix(string(scope), BundleRefPrefix) {
			t.Errorf("scope %s is spelled like a bundle reference", scope)
		}
	}
}

func TestResolveStored(t *testing.T) {
	cases := []struct {
		name        string
		raw         []string
		want        Set
		wantSkipped []string
	}{
		{name: "empty", raw: nil, want: Set{}},
		{name: "plain scopes", raw: []string{"deploy:read", "db:query"}, want: SetOf(ScopeDeployRead, ScopeDBQuery)},
		{name: "bundle expands", raw: []string{"bundle:ci"}, want: ExpandBundle(BundleCI)},
		{name: "unknown scope skipped", raw: []string{"deploy:read", "future:scope"},
			want: SetOf(ScopeDeployRead), wantSkipped: []string{"future:scope"}},
		{name: "unknown bundle skipped", raw: []string{"bundle:readonly", "tests:read"},
			want: SetOf(ScopeTestsRead), wantSkipped: []string{"bundle:readonly"}},
		{name: "one known, one unknown", raw: []string{"obs:read", "obs:future"},
			want: SetOf(ScopeObsRead), wantSkipped: []string{"obs:future"}},
		{name: "all unknown: empty set, every input skipped", raw: []string{"x:y", "bundle:x", "deploy:wrIte"},
			want: Set{}, wantSkipped: []string{"x:y", "bundle:x", "deploy:wrIte"}},
		{
			name: "mixed: bundle, explicit sensitive grant, unknowns",
			raw:  []string{"bundle:admin", "secret:write", "future:scope", "bundle:future"},
			want: func() Set {
				s := ExpandBundle(BundleAdmin)
				s[ScopeSecretWrite] = struct{}{}
				return s
			}(),
			wantSkipped: []string{"future:scope", "bundle:future"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, skipped := ResolveStored(tc.raw)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("resolved %s, want %s", got, tc.want)
			}
			if !reflect.DeepEqual(skipped, tc.wantSkipped) {
				t.Errorf("skipped %v, want %v", skipped, tc.wantSkipped)
			}
		})
	}
	// NewSet still refuses what the tolerant read skips.
	if _, err := NewSet([]string{"bundle:ci"}); err == nil {
		t.Error("NewSet accepted a bundle reference: the mint path's refusal must not change")
	}
}

package accesstoken

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMint_ProducesTheExactFormat(t *testing.T) {
	for i := 0; i < 100; i++ {
		m, err := Mint()
		if err != nil {
			t.Fatalf("Mint: %v", err)
		}
		if !HasFormat(m.Plaintext) {
			t.Fatalf("minted token %q fails HasFormat", m.DisplayPrefix)
		}
		if m.Hash != Hash(m.Plaintext) || !EqualHash(m.Hash, Hash(m.Plaintext)) {
			t.Fatal("Minted.Hash is not Hash(Plaintext)")
		}
		if !strings.HasPrefix(m.Plaintext, m.DisplayPrefix) || len(m.DisplayPrefix) != len(Prefix)+DisplayPrefixLen {
			t.Fatalf("display prefix %q is not the token's leading %d chars", m.DisplayPrefix, len(Prefix)+DisplayPrefixLen)
		}
	}
}

// TestHasFormat_RejectsEveryRetiredFamily pins that `rlat_` is disjoint from
// every credential family it replaced, and from JWTs. Each was a real minted
// shape; none may pass the pre-filter.
func TestHasFormat_RejectsEveryRetiredFamily(t *testing.T) {
	foreign := map[string]string{
		"daemon/api PAT (rlnt_pat_)":     "rlnt_pat_" + strings.Repeat("A1b2C3", 5),
		"connector credential":           "rlnt_conn_" + strings.Repeat("Z9y8X7", 5),
		"per-user LLM key (base64url)":   "rlnt_" + strings.Repeat("a-_B", 10) + "abc",
		"managed LLM key (hex)":          "rlnt_" + strings.Repeat("0a1b2c3d", 8),
		"legacy rly_ key":                "rly_" + strings.Repeat("0a1b2c3d", 8),
		"port-access token (dpat_)":      "dpat_123e4567-e89b-12d3-a456-426614174000",
		"retired deploy token (rlntci_)": "rlntci_" + strings.Repeat("c", 43),
		"JWT":                            "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.sig",
		"truncated rlat_":                Prefix + strings.Repeat("a", randomBytes-1),
		"padded rlat_":                   Prefix + strings.Repeat("a", randomBytes+1),
		"rlat_ with non-base62 body":     Prefix + strings.Repeat("a", randomBytes-1) + "-",
	}
	for name, tok := range foreign {
		if HasFormat(tok) {
			t.Errorf("HasFormat accepted a %s", name)
		}
	}
}

func TestEqualHash(t *testing.T) {
	a := Hash("x")
	if !EqualHash(a, Hash("x")) || EqualHash(a, Hash("y")) || EqualHash(a, a[:10]) {
		t.Fatal("EqualHash is wrong")
	}
}

func TestNewSet_RefusesUnknownAndEmpty(t *testing.T) {
	if _, err := NewSet(nil); err == nil {
		t.Fatal("empty scope set accepted")
	}
	for _, bad := range []string{"deploy", "deploy:wrIte", "billing:write", "*", "secret:list"} {
		if _, err := NewSet([]string{string(ScopeDeployRead), bad}); err == nil {
			t.Errorf("NewSet accepted unknown scope %q", bad)
		}
	}
	for _, good := range AllScopes {
		if _, err := NewSet([]string{string(good)}); err != nil {
			t.Errorf("NewSet refused known scope %q: %v", good, err)
		}
	}
}

func TestPermits_WriteImpliesReadWithinAProductOnly(t *testing.T) {
	s := SetOf(ScopeDeployWrite, ScopeSecretWrite)
	if !s.Permits(ScopeDeployRead) || !s.Permits(ScopeSecretRead) {
		t.Fatal("write must imply read within its product")
	}
	if s.Permits(ScopeTokenRead) || SetOf(ScopeDeployRead).Permits(ScopeDeployWrite) {
		t.Fatal("an implication crossed products or read implied write")
	}
}

// TestClusterManage_IsItsOwnAuthority pins cluster:manage as a first-class
// scope: it parses from the wire, and it neither implies nor is implied by the
// deploy scopes — a CI deploy token must not be able to change which clusters
// the org trusts, and a cluster admin credential must not ship releases.
func TestClusterManage_IsItsOwnAuthority(t *testing.T) {
	got, ok := ParseScope("cluster:manage")
	if !ok || got != ScopeClusterManage {
		t.Fatalf("ParseScope(cluster:manage) = %q, %v", got, ok)
	}
	if SetOf(ScopeDeployWrite).Permits(ScopeClusterManage) {
		t.Fatal("deploy:write permitted cluster:manage")
	}
	cm := SetOf(ScopeClusterManage)
	for _, other := range AllScopes {
		if other != ScopeClusterManage && cm.Permits(other) {
			t.Errorf("cluster:manage permitted %s", other)
		}
	}
}

// TestDomainScopes_AreTheirOwnAuthority pins domain:read/domain:write as a
// first-class product, separate from deploy. A domain is a public identity and
// a certificate is issued in the org's name, so a token that can ship a
// release must not also be able to change which names point at it.
func TestDomainScopes_AreTheirOwnAuthority(t *testing.T) {
	for raw, want := range map[string]Scope{
		"domain:read":  ScopeDomainRead,
		"domain:write": ScopeDomainWrite,
	} {
		got, ok := ParseScope(raw)
		if !ok || got != want {
			t.Fatalf("ParseScope(%s) = %q, %v", raw, got, ok)
		}
	}

	// Write implies read WITHIN the product, as it does for deploy and secret.
	if !SetOf(ScopeDomainWrite).Permits(ScopeDomainRead) {
		t.Error("domain:write must permit domain:read at the point of use")
	}
	if SetOf(ScopeDomainRead).Permits(ScopeDomainWrite) {
		t.Error("domain:read must not permit domain:write")
	}

	// And no implication crosses the product boundary, in either direction.
	if SetOf(ScopeDeployWrite).Permits(ScopeDomainWrite) {
		t.Error("deploy:write permitted domain:write")
	}
	if SetOf(ScopeDomainWrite).Permits(ScopeDeployWrite) {
		t.Error("domain:write permitted deploy:write")
	}
	dw := SetOf(ScopeDomainWrite)
	for _, other := range AllScopes {
		if other != ScopeDomainWrite && other != ScopeDomainRead && dw.Permits(other) {
			t.Errorf("domain:write permitted %s", other)
		}
	}
}

// TestIdentityScopes_AreTheirOwnAuthority pins identity:read/identity:manage
// as a product of their own. identity:manage issues codes that set an end
// user's password, so nothing outside it may imply it, and it implies nothing
// beyond identity:read.
func TestIdentityScopes_AreTheirOwnAuthority(t *testing.T) {
	for raw, want := range map[string]Scope{
		"identity:read":   ScopeIdentityRead,
		"identity:manage": ScopeIdentityManage,
	} {
		got, ok := ParseScope(raw)
		if !ok || got != want {
			t.Fatalf("ParseScope(%s) = %q, %v", raw, got, ok)
		}
	}
	if !SetOf(ScopeIdentityManage).Permits(ScopeIdentityRead) {
		t.Error("identity:manage must permit identity:read at the point of use")
	}
	if SetOf(ScopeIdentityRead).Permits(ScopeIdentityManage) {
		t.Error("identity:read must not permit identity:manage")
	}
	im := SetOf(ScopeIdentityManage)
	for _, other := range AllScopes {
		if other != ScopeIdentityManage && other != ScopeIdentityRead && im.Permits(other) {
			t.Errorf("identity:manage permitted %s", other)
		}
		if other != ScopeIdentityManage && SetOf(other).Permits(ScopeIdentityManage) {
			t.Errorf("%s permitted identity:manage", other)
		}
		if other != ScopeIdentityManage && other != ScopeIdentityRead && SetOf(other).Permits(ScopeIdentityRead) {
			t.Errorf("%s permitted identity:read", other)
		}
	}
	for _, s := range []Scope{ScopeIdentityRead, ScopeIdentityManage} {
		if RequiresActingUser(s) {
			t.Errorf("%s must not require an acting user: an org token may administer identity", s)
		}
		// A Reliant agent's session credential is daemon-bound; on-demand
		// elevation keeps the binding, so the scope must be able to ride it.
		g := Grant{OrgID: "org", Name: "n", Scopes: SetOf(ScopeDaemonConnect, s), ActingUserID: "u",
			Resource: &Resource{Kind: ResourceDaemon, ID: "d"}}
		if err := g.Validate(time.Now()); err != nil {
			t.Errorf("a daemon-bound token carrying %s: %v", s, err)
		}
	}
}

// TestFeedbackLoopScopes_AreTheirOwnAuthority pins obs:read, obs:triage and
// db:query. obs:triage implies obs:read at the point of use and nothing else;
// db:query is a product of its own that nothing implies and that implies
// nothing, because an app's data is not its telemetry.
func TestFeedbackLoopScopes_AreTheirOwnAuthority(t *testing.T) {
	for raw, want := range map[string]Scope{
		"obs:read":   ScopeObsRead,
		"obs:triage": ScopeObsTriage,
		"db:query":   ScopeDBQuery,
	} {
		got, ok := ParseScope(raw)
		if !ok || got != want {
			t.Fatalf("ParseScope(%s) = %q, %v", raw, got, ok)
		}
		if _, err := NewSet([]string{raw}); err != nil {
			t.Errorf("NewSet(%s): %v", raw, err)
		}
		if !SetOf(want).Permits(want) {
			t.Errorf("%s must permit itself", want)
		}
	}
	if !SetOf(ScopeObsTriage).Permits(ScopeObsRead) {
		t.Error("obs:triage must permit obs:read at the point of use")
	}
	if SetOf(ScopeObsRead).Permits(ScopeObsTriage) {
		t.Error("obs:read must not permit obs:triage: a read-only token must not silence an issue")
	}
	if SetOf(ScopeObsTriage).Covers(SetOf(ScopeObsRead)) {
		t.Error("Covers is exact membership: obs:triage alone must not let a holder mint obs:read")
	}
	if got, ok := ParseScope("obs:write"); !ok || got != ScopeObsWrite {
		t.Fatalf("ParseScope(obs:write) = %q, %v", got, ok)
	}
	if !SetOf(ScopeObsWrite).Permits(ScopeObsRead) {
		t.Error("obs:write must permit obs:read at the point of use")
	}
	if SetOf(ScopeObsRead).Permits(ScopeObsWrite) || SetOf(ScopeObsTriage).Permits(ScopeObsWrite) {
		t.Error("neither obs:read nor obs:triage may permit obs:write")
	}
	if SetOf(ScopeObsWrite).Permits(ScopeObsTriage) {
		t.Error("obs:write must not permit obs:triage: authoring dashboards is not silencing issues")
	}
	if SetOf(ScopeObsWrite).Covers(SetOf(ScopeObsRead)) {
		t.Error("Covers is exact membership: obs:write alone must not let a holder mint obs:read")
	}
	if RequiresActingUser(ScopeObsWrite) {
		t.Error("obs:write must not require an acting user: org automation may author")
	}
	ow := Grant{OrgID: "org", Name: "n", Scopes: SetOf(ScopeDaemonConnect, ScopeObsWrite), ActingUserID: "u",
		Resource: &Resource{Kind: ResourceDaemon, ID: "d"}}
	if err := ow.Validate(time.Now()); err != nil {
		t.Errorf("obs:write on a daemon-bound token: %v", err)
	}
	envBound := Grant{OrgID: "org", Name: "n", Scopes: SetOf(ScopeObsWrite), Resource: &Resource{Kind: ResourceEnvironment, ID: "e"}}
	if err := envBound.Validate(time.Now()); err == nil {
		t.Error("obs:write must not be confinable to one environment: what it writes is org-wide")
	}
	isObs := func(s Scope) bool { return s == ScopeObsRead || s == ScopeObsTriage || s == ScopeObsWrite }
	for _, other := range AllScopes {
		if other != ScopeDBQuery && SetOf(other).Permits(ScopeDBQuery) {
			t.Errorf("%s permitted db:query", other)
		}
		if other != ScopeDBQuery && SetOf(ScopeDBQuery).Permits(other) {
			t.Errorf("db:query permitted %s", other)
		}
		if !isObs(other) && (SetOf(other).Permits(ScopeObsRead) || SetOf(other).Permits(ScopeObsTriage) || SetOf(other).Permits(ScopeObsWrite)) {
			t.Errorf("%s permitted an obs scope", other)
		}
		if !isObs(other) && (SetOf(ScopeObsRead).Permits(other) || SetOf(ScopeObsTriage).Permits(other) || SetOf(ScopeObsWrite).Permits(other)) {
			t.Errorf("an obs scope permitted %s", other)
		}
	}
}

// TestFeedbackLoopScopes_NeedNoActingUserAndRideADaemon: obs:read gates CI, so
// an org automation token carries it without acting as a person; a Reliant
// agent's daemon-bound session credential can carry all three.
func TestFeedbackLoopScopes_NeedNoActingUserAndRideADaemon(t *testing.T) {
	for _, s := range []Scope{ScopeObsRead, ScopeObsTriage, ScopeDBQuery} {
		if RequiresActingUser(s) {
			t.Errorf("%s must not require an acting user", s)
		}
		org := Grant{OrgID: "org", Name: "ci", Scopes: SetOf(s)}
		if err := org.Validate(time.Now()); err != nil {
			t.Errorf("an org token carrying %s: %v", s, err)
		}
		g := Grant{OrgID: "org", Name: "n", Scopes: SetOf(ScopeDaemonConnect, s), ActingUserID: "u",
			Resource: &Resource{Kind: ResourceDaemon, ID: "d"}}
		if err := g.Validate(time.Now()); err != nil {
			t.Errorf("a daemon-bound token carrying %s: %v", s, err)
		}
	}
}

// TestDomainScopes_NeedNoActingUser: a domain is org property, so an org CI
// token manages one without acting as a person — the same as deploy and secret.
func TestDomainScopes_NeedNoActingUser(t *testing.T) {
	for _, s := range []Scope{ScopeDomainRead, ScopeDomainWrite} {
		if RequiresActingUser(s) {
			t.Errorf("%s must not require an acting user", s)
		}
	}
}

// TestCovers_IsTheNoMintBeyondYourScopesRule covers every scope, including the
// new ones: a holder can grant exactly what it holds.
func TestCovers_IsTheNoMintBeyondYourScopesRule(t *testing.T) {
	for _, held := range AllScopes {
		grantor := SetOf(ScopeTokenWrite, held)
		if !grantor.Covers(SetOf(held)) {
			t.Errorf("a holder of %s cannot grant %s", held, held)
		}
		for _, other := range AllScopes {
			if other == held || other == ScopeTokenWrite {
				continue
			}
			if grantor.Covers(SetOf(other)) {
				t.Errorf("a holder of {token:write, %s} was allowed to grant %s", held, other)
			}
		}
	}
	// Covers uses membership, not Permits: write does not grant read.
	if SetOf(ScopeDeployWrite).Covers(SetOf(ScopeDeployRead)) {
		t.Fatal("Covers routed through Permits")
	}
	if SetOf(ScopeDeployRead).Covers(Set{}) {
		t.Fatal("the empty request was covered")
	}
}

func validGrant(now time.Time) Grant {
	exp := now.Add(time.Hour)
	return Grant{
		OrgID:        "org",
		Name:         "laptop daemon",
		Scopes:       SetOf(ScopeDaemonConnect),
		ActingUserID: "user",
		Resource:     &Resource{Kind: ResourceDaemon, ID: "d1"},
		Ephemeral:    true,
		ExpiresAt:    &exp,
	}
}

func TestGrant_Validate(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Minute)

	if err := validGrant(now).Validate(now); err != nil {
		t.Fatalf("a valid bound, ephemeral, expiring daemon grant was refused: %v", err)
	}

	cases := map[string]struct {
		mutate func(*Grant)
		want   error
	}{
		"no org":                         {func(g *Grant) { g.OrgID = "" }, ErrInvalidGrant},
		"no name":                        {func(g *Grant) { g.Name = " " }, ErrInvalidGrant},
		"no scopes":                      {func(g *Grant) { g.Scopes = Set{} }, ErrInvalidGrant},
		"user scope with no acting user": {func(g *Grant) { g.ActingUserID = "" }, ErrInvalidGrant},
		"ephemeral unbound":              {func(g *Grant) { g.Resource = nil }, ErrInvalidGrant},
		"ephemeral never expires":        {func(g *Grant) { g.ExpiresAt = nil }, ErrInvalidGrant},
		"expiry in the past":             {func(g *Grant) { g.ExpiresAt = &past }, ErrInvalidGrant},
		"wrong binding kind": {func(g *Grant) {
			g.Resource = &Resource{Kind: ResourcePort, ID: PortResourceID("d1", 80)}
		}, ErrInvalidGrant},
		"unknown binding kind": {func(g *Grant) { g.Resource = &Resource{Kind: "bucket", ID: "b"} }, ErrInvalidGrant},
		"binding with no id":   {func(g *Grant) { g.Resource = &Resource{Kind: ResourceDaemon} }, ErrInvalidGrant},
		// Org-administration scopes ride a daemon binding (an acting user's
		// daemon does what the user can) but never a port or connector one.
		"org-administration scope bound to a port": {func(g *Grant) {
			g.Scopes = SetOf(ScopeProxyPort, ScopeClusterManage)
			g.Resource = &Resource{Kind: ResourcePort, ID: PortResourceID("d1", 80)}
		}, ErrInvalidGrant},
		"token scope bound to a connector": {func(g *Grant) {
			g.Scopes = SetOf(ScopeMCPConnector, ScopeTokenWrite)
			g.Resource = &Resource{Kind: ResourceConnector, ID: "c1"}
		}, ErrInvalidGrant},
		// A user-authority scope may ride a DAEMON binding but not a port one:
		// a share link has no acting user, so deploy authority on it would be
		// attributed to nobody.
		"user-authority scope bound to a port": {func(g *Grant) {
			g.Scopes = SetOf(ScopeProxyPort, ScopeDeployRead)
			g.Resource = &Resource{Kind: ResourcePort, ID: PortResourceID("d1", 80)}
		}, ErrInvalidGrant},
		"user-authority scope bound to a connector": {func(g *Grant) {
			g.Scopes = SetOf(ScopeMCPConnector, ScopeDeployRead)
			g.Resource = &Resource{Kind: ResourceConnector, ID: "c1"}
		}, ErrInvalidGrant},
		"port scope unbound": {func(g *Grant) {
			g.Scopes, g.Resource, g.Ephemeral = SetOf(ScopeProxyPort), nil, false
		}, ErrInvalidGrant},
		"connector scope unbound": {func(g *Grant) {
			g.Scopes, g.Resource, g.Ephemeral = SetOf(ScopeMCPConnector), nil, false
		}, ErrInvalidGrant},
		"grantor escalates": {func(g *Grant) {
			g.GrantorScopes = SetOf(ScopeTokenWrite, ScopeLLMInvoke)
		}, ErrEscalation},
		// "A PAT cannot mint a PAT" is this rule: a daemon credential without
		// token:write cannot grant its own scope, because Covers requires
		// holding every requested scope and the handler requires token:write.
		"grantor without the scope": {func(g *Grant) {
			g.GrantorScopes = SetOf(ScopeLLMInvoke)
		}, ErrEscalation},
	}
	for name, tc := range cases {
		g := validGrant(now)
		tc.mutate(&g)
		err := g.Validate(now)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}

	// Positive rows for each new scope's legitimate shape.
	ok := map[string]Grant{
		"org CI token, no user, unbound": {OrgID: "o", Name: "ci", Scopes: SetOf(ScopeDeployWrite, ScopeSecretWrite)},
		"LLM key acting as a user":       {OrgID: "o", Name: "laptop", Scopes: SetOf(ScopeLLMInvoke), ActingUserID: "u"},
		"CLI token":                      {OrgID: "o", Name: "cli", Scopes: SetOf(ScopeReliantAPI), ActingUserID: "u"},
		"unbound daemon registration":    {OrgID: "o", Name: "reg", Scopes: SetOf(ScopeDaemonConnect), ActingUserID: "u"},
		"port share link": {OrgID: "o", Name: "share", Scopes: SetOf(ScopeProxyPort),
			Resource: &Resource{Kind: ResourcePort, ID: PortResourceID("d", 3000)}},
		"connector credential": {OrgID: "o", Name: "claude", Scopes: SetOf(ScopeMCPConnector), ActingUserID: "u",
			Resource: &Resource{Kind: ResourceConnector, ID: "g1"}},
		"grantor narrows": {OrgID: "o", Name: "n", Scopes: SetOf(ScopeDeployRead),
			GrantorScopes: SetOf(ScopeTokenWrite, ScopeDeployRead, ScopeDeployWrite)},
	}
	for name, g := range ok {
		if err := g.Validate(now); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}
}

// TestGrant_DaemonBindingCarriesUserAuthority is the managed-daemon
// credential: ONE token, bound to the daemon so teardown revokes it, carrying
// the authority its acting user already holds.
//
// Before daemons admitted user authority this grant was REFUSED ("scope deploy:read
// cannot be bound to a resource"), which left a managed daemon's forge unable
// to authenticate at all — `forge env status` returned 403 permission_denied
// on DeployService/ListEnvironments. The alternative was minting UNBOUND,
// which would have dropped the blast-radius property the binding exists for.
func TestGrant_DaemonBindingCarriesUserAuthority(t *testing.T) {
	now := time.Now()

	// The full reliant-cli scope set, bound to one daemon, acting as the user.
	managed := Grant{
		OrgID:        "org",
		Name:         "managed:ws-1",
		Scopes:       SetOf(ScopeDaemonConnect, ScopeReliantAPI, ScopeDeployRead, ScopeDeployWrite, ScopeSecretRead, ScopeSecretWrite, ScopeDomainRead, ScopeDomainWrite),
		ActingUserID: "user",
		Resource:     &Resource{Kind: ResourceDaemon, ID: "d1"},
	}
	if err := managed.Validate(now); err != nil {
		t.Fatalf("the managed-daemon credential was refused: %v", err)
	}

	// Each scope a daemon admits is independently bindable to it, so a
	// member clipped down to a subset still mints.
	for s := range managed.Admissible() {
		clipped := managed
		clipped.Scopes = SetOf(ScopeDaemonConnect, s)
		if err := clipped.Validate(now); err != nil {
			t.Errorf("daemon:connect + %s was refused: %v", s, err)
		}
	}

	// THE BINDING IS STILL A BINDING. Widening which scopes may ride it must
	// not make the token usable against another daemon.
	p := &Principal{Resource: &Resource{Kind: ResourceDaemon, ID: "d1"}, Scopes: managed.Scopes}
	if !p.MayActOn(ResourceDaemon, "d1") || p.MayActOn(ResourceDaemon, "d2") {
		t.Fatal("a wide daemon-bound token escaped its binding")
	}
}

// TestAdmissible_DaemonCarriesEveryScope is the owner's rule: a daemon
// credential holds EVERY scope, so adding a scope to AllScopes must never
// again leave daemons without it. It FAILS when a scope is added that a
// daemon-bound credential acting as its user cannot carry, unless that scope
// requires a binding of another kind (and so names it in scopeResourceRules).
func TestAdmissible_DaemonCarriesEveryScope(t *testing.T) {
	daemon := Grant{ActingUserID: "user", Resource: &Resource{Kind: ResourceDaemon, ID: "d1"}}
	got := daemon.Admissible()
	for _, s := range AllScopes {
		rule, bound := scopeResourceRules[s]
		elsewhere := (bound && rule.required && rule.kind != ResourceDaemon) || personOnly(s)
		if got.Has(s) == elsewhere {
			t.Errorf("daemon wildcard holds %s = %v; it must hold every scope that does not require a %s binding",
				s, got.Has(s), rule.kind)
		}
	}
	// The two that bind elsewhere, by name, so the exclusion cannot grow
	// silently.
	if got.Has(ScopeProxyPort) || got.Has(ScopeMCPConnector) {
		t.Fatalf("a daemon wildcard resolved to a port share or connector credential: %s", got)
	}
	// The person-only scopes, by name, for the same reason.
	if len(personOnlyScopes) != 1 || personOnlyScopes[0] != ScopeDaemonManage {
		t.Fatalf("personOnlyScopes changed: %v; name the new member here on purpose", personOnlyScopes)
	}

	// Unbound (a laptop daemon's registration) holds everything that does not
	// require a binding at all.
	unbound := Grant{ActingUserID: "user"}.Admissible()
	for _, s := range AllScopes {
		rule, bound := scopeResourceRules[s]
		if unbound.Has(s) == ((bound && rule.required) || personOnly(s)) {
			t.Errorf("unbound wildcard holds %s = %v", s, unbound.Has(s))
		}
	}
	// No acting user: nothing that acts as a person.
	for s := range (Grant{}).Admissible() {
		if RequiresActingUser(s) {
			t.Errorf("an org automation wildcard resolved to the person-scope %s", s)
		}
	}
}

func TestKnownScopes_SkipsWhatThisBinaryCannotEvaluate(t *testing.T) {
	got := KnownScopes([]string{"deploy:read", "future:scope", "daemon:connect"})
	if len(got) != 2 || !got.Has(ScopeDeployRead) || !got.Has(ScopeDaemonConnect) {
		t.Fatalf("KnownScopes = %s", got)
	}
}

func TestPrincipal_MayActOn(t *testing.T) {
	bound := &Principal{Resource: &Resource{Kind: ResourceDaemon, ID: "d1"}}
	if !bound.MayActOn(ResourceDaemon, "d1") || bound.MayActOn(ResourceDaemon, "d2") || bound.MayActOn(ResourcePort, "d1") {
		t.Fatal("a bound principal escaped its binding")
	}
	if !(&Principal{}).MayActOn(ResourceDaemon, "any") {
		t.Fatal("an unbound principal was confined")
	}
	var nilP *Principal
	if nilP.MayActOn(ResourceDaemon, "d1") {
		t.Fatal("nil principal authorized")
	}
}

// TestDaemonResume_IsOneDaemonActingAsOnePerson pins the shape of the
// unattended-wake credential (see ScopeDaemonResume). Every refusal below is a
// way it could otherwise grow into more than "wake this one machine for its
// owner".
func TestDaemonResume_IsOneDaemonActingAsOnePerson(t *testing.T) {
	now := time.Now()

	got, ok := ParseScope("daemon:resume")
	if !ok || got != ScopeDaemonResume {
		t.Fatalf("ParseScope(daemon:resume) = %q, %v", got, ok)
	}

	bound := Grant{
		OrgID: "o", Name: "reliant-automation:d1",
		Scopes:       SetOf(ScopeDaemonResume),
		ActingUserID: "u",
		Resource:     &Resource{Kind: ResourceDaemon, ID: "d1"},
	}
	if err := bound.Validate(now); err != nil {
		t.Fatalf("a daemon-bound resume credential acting as its owner was refused: %v", err)
	}

	refused := map[string]func(*Grant){
		// Unbound would wake every machine the person owns.
		"unbound": func(g *Grant) { g.Resource = nil },
		// A wake is authorized and billed as a person.
		"no acting user": func(g *Grant) { g.ActingUserID = "" },
		"bound to a port": func(g *Grant) {
			g.Resource = &Resource{Kind: ResourcePort, ID: PortResourceID("d1", 80)}
		},
		"bound to a connector": func(g *Grant) { g.Resource = &Resource{Kind: ResourceConnector, ID: "c1"} },
	}
	for name, mutate := range refused {
		g := bound
		mutate(&g)
		if err := g.Validate(now); !errors.Is(err, ErrInvalidGrant) {
			t.Errorf("%s: got %v, want ErrInvalidGrant", name, err)
		}
	}

	// Wake is its own authority: it neither implies nor is implied by the
	// daemon's own connection credential, nor by anything else.
	resume := SetOf(ScopeDaemonResume)
	for _, other := range AllScopes {
		if other == ScopeDaemonResume {
			continue
		}
		if resume.Permits(other) {
			t.Errorf("daemon:resume permitted %s", other)
		}
		if SetOf(other).Permits(ScopeDaemonResume) {
			t.Errorf("%s permitted daemon:resume", other)
		}
	}
	// A daemon's own credential must not be able to mint a wake credential —
	// that would let a compromised daemon keep itself (or a sibling) running.
	if SetOf(ScopeTokenWrite, ScopeDaemonConnect).Covers(resume) {
		t.Fatal("a daemon:connect holder could mint daemon:resume")
	}

	// The binding is a real confinement: a token for d1 cannot act on d2.
	p := &Principal{Resource: bound.Resource, Scopes: bound.Scopes, ActingUserID: "u"}
	if !p.MayActOn(ResourceDaemon, "d1") || p.MayActOn(ResourceDaemon, "d2") {
		t.Fatal("a daemon:resume credential escaped its daemon binding")
	}
}

// TestEnvironmentBinding pins the env-bound telemetry/database token: obs:read,
// obs:triage and db:query may be confined to one environment, nothing else may,
// and the binding confines reads to that environment only.
func TestEnvironmentBinding(t *testing.T) {
	now := time.Now()
	env := &Resource{Kind: ResourceEnvironment, ID: "env-prod"}
	for _, s := range []Scope{ScopeObsRead, ScopeObsTriage, ScopeDBQuery} {
		bound := Grant{OrgID: "org", Name: "ci", Scopes: SetOf(s), Resource: env}
		if err := bound.Validate(now); err != nil {
			t.Errorf("%s bound to an environment was refused: %v", s, err)
		}
		unbound := Grant{OrgID: "org", Name: "ci", Scopes: SetOf(s)}
		if err := unbound.Validate(now); err != nil {
			t.Errorf("%s unbound was refused: %v", s, err)
		}
	}
	// Every other scope refuses an environment binding: a deploy token bound
	// to an env would claim a confinement deploy does not enforce.
	for _, s := range AllScopes {
		switch s {
		case ScopeObsRead, ScopeObsTriage, ScopeDBQuery:
			continue
		}
		g := Grant{OrgID: "org", Name: "x", Scopes: SetOf(s), ActingUserID: "user", Resource: env}
		if err := g.Validate(now); err == nil {
			t.Errorf("%s accepted an environment binding", s)
		}
	}
	// obs:read still rides the acting user's daemon credential.
	daemon := Grant{OrgID: "org", Name: "managed", Scopes: SetOf(ScopeDaemonConnect, ScopeObsRead), ActingUserID: "user", Resource: &Resource{Kind: ResourceDaemon, ID: "d1"}}
	if err := daemon.Validate(now); err != nil {
		t.Errorf("obs:read on a daemon credential was refused: %v", err)
	}

	p := &Principal{Scopes: SetOf(ScopeObsRead), Resource: env}
	if !p.MayReadEnvironment("env-prod") || p.MayReadEnvironment("env-staging") {
		t.Error("an environment-bound token escaped its binding")
	}
	for name, other := range map[string]*Principal{
		"unbound":      {Scopes: SetOf(ScopeObsRead)},
		"daemon-bound": {Scopes: SetOf(ScopeObsRead), Resource: &Resource{Kind: ResourceDaemon, ID: "d1"}},
	} {
		if !other.MayReadEnvironment("env-staging") {
			t.Errorf("a %s token was confined to no environment", name)
		}
	}
	var nilP *Principal
	if nilP.MayReadEnvironment("env-prod") {
		t.Error("nil principal authorized")
	}
}

// TestOrgAdministrationRidesADaemonBinding pins that cluster:manage and
// token:* may be carried by an acting user's daemon-bound token, and that an
// environment binding still refuses them.
func TestOrgAdministrationRidesADaemonBinding(t *testing.T) {
	now := time.Now()
	for _, s := range []Scope{ScopeClusterManage, ScopeTokenRead, ScopeTokenWrite} {
		g := Grant{OrgID: "org", Name: "n", Scopes: SetOf(ScopeDaemonConnect, s), ActingUserID: "u",
			Resource: &Resource{Kind: ResourceDaemon, ID: "d"}}
		if err := g.Validate(now); err != nil {
			t.Errorf("a daemon-bound token carrying %s: %v", s, err)
		}
		g.Resource = &Resource{Kind: ResourceEnvironment, ID: "e"}
		g.Scopes = SetOf(s)
		if err := g.Validate(now); err == nil {
			t.Errorf("%s accepted an environment binding", s)
		}
	}
}

// TestDaemonManage_IsAPersonsOwnUnboundAuthority pins the CLI machine-
// management credential (see ScopeDaemonManage): it acts as a person, is
// never bound — so no daemon's own credential can carry it — and implies and
// is implied by nothing.
func TestDaemonManage_IsAPersonsOwnUnboundAuthority(t *testing.T) {
	now := time.Now()
	if got, ok := ParseScope("daemon:manage"); !ok || got != ScopeDaemonManage {
		t.Fatalf("ParseScope(daemon:manage) = %q, %v", got, ok)
	}
	login := Grant{OrgID: "o", Name: "laptop", ActingUserID: "u",
		Scopes: SetOf(ScopeReliantAPI, ScopeDaemonManage)}
	if err := login.Validate(now); err != nil {
		t.Fatalf("a person's unbound CLI login carrying daemon:manage was refused: %v", err)
	}
	refused := map[string]func(*Grant){
		"no acting user":       func(g *Grant) { g.ActingUserID = "" },
		"bound to a daemon":    func(g *Grant) { g.Resource = &Resource{Kind: ResourceDaemon, ID: "d1"} },
		"bound to a port":      func(g *Grant) { g.Resource = &Resource{Kind: ResourcePort, ID: PortResourceID("d1", 80)} },
		"bound to a connector": func(g *Grant) { g.Resource = &Resource{Kind: ResourceConnector, ID: "c1"} },
		"bound to an env":      func(g *Grant) { g.Resource = &Resource{Kind: ResourceEnvironment, ID: "e1"} },
	}
	for name, mutate := range refused {
		g := login
		mutate(&g)
		if err := g.Validate(now); !errors.Is(err, ErrInvalidGrant) {
			t.Errorf("%s: got %v, want ErrInvalidGrant", name, err)
		}
	}
	// A wildcard credential (a daemon's, bound or not) never resolves to it.
	for _, shape := range []Grant{
		{ActingUserID: "u", Resource: &Resource{Kind: ResourceDaemon, ID: "d1"}},
		{ActingUserID: "u"},
	} {
		if shape.Admissible().Has(ScopeDaemonManage) {
			t.Fatalf("a wildcard credential shaped %+v resolves to daemon:manage", shape)
		}
	}
	manage := SetOf(ScopeDaemonManage)
	for _, other := range AllScopes {
		if other == ScopeDaemonManage {
			continue
		}
		if manage.Permits(other) || SetOf(other).Permits(ScopeDaemonManage) {
			t.Errorf("daemon:manage and %s imply each other", other)
		}
	}
	if SetOf(ScopeTokenWrite, ScopeDaemonConnect, ScopeDaemonResume).Covers(manage) {
		t.Fatal("a daemon credential could mint daemon:manage")
	}
}

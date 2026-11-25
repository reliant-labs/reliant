package accesstoken

import "strings"

// Class says WHO holds a scope and HOW: by being a person, by being a member
// of an org, by an ordinary grant, or by a grant made one scope at a time.
//
// ── WHY THE CATALOG CLASSIFIES, NOT EACH CONSUMER ─────────────────────
//
// A store has to know, for every scope, whether a person holds it over their
// own resources, whether every member holds it, and whether it may be handed
// out in a bundle. When each consumer kept that in its own lists, every scope
// this package added turned the consumer's build red on the pin bump that
// brought it (an unclassified scope), or — worse — reached nobody until a
// backfill. Classifying here, beside the constant, makes adding a scope one
// catalog row: a scope with no class fails this package's own tests, so it
// cannot ship, and a consumer derives its lists from ClassOf and needs no edit.
//
// The classes:
//
//   - ClassPersonal: a person's authority over their OWN resources (their
//     daemons, their runs, their inbox, their API). Every signed-in person
//     holds it implicitly; no org grant names it. Every scope that acts as a
//     person (RequiresActingUser) is personal.
//   - ClassMember: what every MEMBER of an org holds without a grant (reading
//     and triaging the org's telemetry, uploading test runs). Never a
//     sensitive scope, never one a daemon must not carry, never a whole
//     credential of its own: membership is the widest grant there is, so
//     the tests pin this class's members one by one.
//   - ClassStandard: org authority held by grant, directly or through a
//     bundle.
//   - ClassSensitive: org authority whose leak is not recoverable by revoking
//     the token — it reads secrets or users' data, issues credentials or
//     passwords, or shifts production traffic. Held only by an explicit grant
//     naming the scope: no bundle ever expands to one (ExpandBundle), and that
//     includes admin. A future billing:write (changing what an org pays, or
//     how) is classified sensitive from the day it is added; no such scope
//     exists yet.
type Class string

const (
	// ClassPersonal is a person's authority over their own resources.
	ClassPersonal Class = "personal"
	// ClassMember is what every org member holds without a grant.
	ClassMember Class = "member"
	// ClassStandard is org authority held by grant or bundle.
	ClassStandard Class = "standard"
	// ClassSensitive is org authority held only by a grant naming it.
	ClassSensitive Class = "sensitive"
)

// AllClasses is every class, least guarded first.
var AllClasses = []Class{ClassPersonal, ClassMember, ClassStandard, ClassSensitive}

// Bundle names a reviewed set of scopes for one kind of holder. A grant row or
// a token may name a bundle (stored as BundleRef, "bundle:<name>") instead of
// listing scopes, and the bundle is EXPANDED AT CHECK TIME against this
// binary's catalog (ResolveStored).
//
// ── WHY EXPANDED, NOT COPIED ─────────────────────────────────────────
//
// A list copied at mint is the catalog of that day: a scope added later
// reaches no existing holder until somebody re-mints, migrates or backfills.
// A bundle reference keeps the INTENT ("this is the org's CI", "this person
// administers the org"), so a scope that joins a bundle reaches every holder
// of it from the release that ships it. That growth is bounded: membership is
// decided here, one row per scope, reviewed in forge's PR, and a sensitive
// scope never joins any bundle.
type Bundle string

const (
	// BundleCI is an org's CI: record and promote releases, upload test
	// runs, read vulnerability findings to gate on them. It acts as no
	// person, so it holds no scope that needs an acting user: an org
	// automation token has nobody to clip its authority against. It holds no
	// registry-only scope either (sourcemaps:write is a whole credential of
	// its own, minted separately).
	BundleCI Bundle = "ci"
	// BundleDeveloper is a person building and shipping the org's software:
	// their own API, runs, triggers and inbox, deploys, telemetry, changes
	// (drafting and stopping, never launching) and test and vulnerability
	// records.
	//
	// NOT secret:read. A developer's token is the one most often pasted into
	// a terminal, a CI variable or an agent's environment, and the org's
	// secrets are the highest-value thing such a leak could reach. A
	// developer who needs them is granted secret:read explicitly; admin
	// carries it.
	BundleDeveloper Bundle = "developer"
	// BundleAdmin administers the org: EVERY member and standard scope that
	// is not a whole credential of its own (registry-only, an MCP client's
	// token, a scope that must be bound to one resource). It holds no
	// personal scope — a person holds those already — and no sensitive one:
	// an admin is granted each of those explicitly.
	BundleAdmin Bundle = "admin"
)

// AllBundles is every bundle, least authority first.
var AllBundles = []Bundle{BundleCI, BundleDeveloper, BundleAdmin}

// BundleRefPrefix marks a stored entry that names a bundle rather than a
// scope. No scope is spelled with it (the catalog's tests hold that), so a
// stored list may mix both.
const BundleRefPrefix = "bundle:"

// scopeEntry is one row of the catalog: everything a consumer derives about a
// scope beyond its binding and acting-user rules (grant.go).
type scopeEntry struct {
	scope   Scope
	class   Class
	bundles []Bundle
	// neverOnDaemon: see NeverOnDaemon.
	neverOnDaemon bool
	// notElevatable: see Elevatable.
	notElevatable bool
}

// The bundle lists a catalog row uses. Each bundle is a superset of the
// org-authority part of the one before it, so a CI scope is a developer's and
// an admin's too; the personal scopes are a developer's alone (an admin holds
// them as a person already).
var (
	inCIAndUp        = []Bundle{BundleCI, BundleDeveloper, BundleAdmin}
	inDeveloperAndUp = []Bundle{BundleDeveloper, BundleAdmin}
	inAdminOnly      = []Bundle{BundleAdmin}
	inDeveloperOnly  = []Bundle{BundleDeveloper}
)

// catalog classifies every scope, in AllScopes order. ADDING A SCOPE IS ONE
// ROW HERE (beside its constant and its AllScopes entry); the tests fail a
// scope with no row, a row out of order, a sensitive scope in a bundle, and an
// admin bundle that misses a standard scope.
//
// AllScopes stays a literal rather than being derived from this table: a
// store's closed-set CHECK names it, and `forge lint` reads its initializer as
// a literal of constants.
var catalog = []scopeEntry{
	{scope: ScopeDeployRead, class: ClassStandard, bundles: inCIAndUp},
	// deploy:write AUTHORIZES an exchange, so an exchange that could return
	// it would let an elevated token mint its own successor.
	{scope: ScopeDeployWrite, class: ClassStandard, bundles: inCIAndUp, notElevatable: true},
	{scope: ScopeDeployConfigRead, class: ClassSensitive},
	{scope: ScopeBillingRead, class: ClassStandard, bundles: inAdminOnly},
	{scope: ScopeClusterManage, class: ClassStandard, bundles: inAdminOnly},
	// Org administration: the inventory and minting of the org's
	// credentials. Never on a daemon, never elevated to on demand.
	{scope: ScopeTokenRead, class: ClassSensitive, neverOnDaemon: true, notElevatable: true},
	{scope: ScopeTokenWrite, class: ClassSensitive, neverOnDaemon: true, notElevatable: true},
	{scope: ScopeReliantAPI, class: ClassPersonal, bundles: inDeveloperOnly},
	// A daemon's own connection credential: no person's token carries it
	// in a bundle.
	{scope: ScopeDaemonConnect, class: ClassPersonal},
	{scope: ScopeLLMInvoke, class: ClassPersonal, bundles: inDeveloperOnly},
	// proxy:port, mcp:connector and daemon:resume must be bound to one
	// resource, and mcp:oauth / mcp:forge are a whole credential each: none
	// can ride a bundle's unbound grant.
	{scope: ScopeProxyPort, class: ClassPersonal},
	{scope: ScopeMCPConnector, class: ClassPersonal},
	{scope: ScopeMCPOAuth, class: ClassPersonal},
	{scope: ScopeDaemonResume, class: ClassPersonal},
	// Starting and resizing machines spends the person's money; held only
	// by a person's own grant (personOnlyScopes).
	{scope: ScopeDaemonManage, class: ClassPersonal},
	{scope: ScopeSecretRead, class: ClassStandard, bundles: inAdminOnly},
	{scope: ScopeSecretWrite, class: ClassSensitive},
	{scope: ScopeDomainRead, class: ClassStandard, bundles: inDeveloperAndUp},
	{scope: ScopeDomainWrite, class: ClassStandard, bundles: inAdminOnly},
	{scope: ScopeIdentityRead, class: ClassStandard, bundles: inAdminOnly},
	{scope: ScopeIdentityManage, class: ClassSensitive},
	{scope: ScopeObsRead, class: ClassMember, bundles: inDeveloperAndUp},
	{scope: ScopeObsTriage, class: ClassMember, bundles: inDeveloperAndUp},
	{scope: ScopeObsWrite, class: ClassStandard, bundles: inDeveloperAndUp},
	{scope: ScopeDBQuery, class: ClassSensitive},
	{scope: ScopeStorageRead, class: ClassSensitive},
	{scope: ScopeRunsStart, class: ClassPersonal, bundles: inDeveloperOnly},
	{scope: ScopeRunsRead, class: ClassPersonal, bundles: inDeveloperOnly},
	{scope: ScopeRunsControl, class: ClassPersonal, bundles: inDeveloperOnly},
	{scope: ScopeTriggersFire, class: ClassPersonal, bundles: inDeveloperOnly},
	{scope: ScopeTriggersManage, class: ClassPersonal, bundles: inDeveloperOnly},
	{scope: ScopeInboxRead, class: ClassPersonal, bundles: inDeveloperOnly},
	{scope: ScopeInboxManage, class: ClassPersonal, bundles: inDeveloperOnly},
	// Deciding an approval is the person's own act: an agent on their
	// daemon reaches it only through the approval card, and no bundle hands
	// it to a token.
	{scope: ScopeApprovalsDecide, class: ClassPersonal, neverOnDaemon: true},
	{scope: ScopeQuestionsAnswer, class: ClassPersonal, bundles: inDeveloperOnly},
	// A registry-only credential (registryOnlyScopes): no bundle, no
	// exchange (an exchange acts as a person and rides a binding, both of
	// which the credential refuses).
	{scope: ScopeSourcemapsWrite, class: ClassStandard, notElevatable: true},
	{scope: ScopeChangesWrite, class: ClassStandard, bundles: inDeveloperAndUp},
	// Launching shifts production traffic onto a change: minted directly,
	// never elevated to on demand.
	{scope: ScopeChangesLaunch, class: ClassSensitive, notElevatable: true},
	{scope: ScopeVulnRead, class: ClassStandard, bundles: inCIAndUp},
	{scope: ScopeTestsRead, class: ClassStandard, bundles: inDeveloperAndUp},
	// Membership-implied (owner, 2026-11-25: "daemons should have them by
	// default"): uploading a test run is evidence about a commit, changes
	// nothing that runs and reads nothing back, so every member holds it,
	// and so does every credential acting as one: their daemon, its
	// exchanged helpers, their personal tokens. A daemon still never exceeds
	// its person. An org's CI token holds it through the ci bundle.
	{scope: ScopeTestsWrite, class: ClassMember, bundles: inCIAndUp},
	{scope: ScopeMCPForge, class: ClassPersonal},
}

// entryOf returns scope's catalog row.
func entryOf(scope Scope) (scopeEntry, bool) {
	for _, e := range catalog {
		if e.scope == scope {
			return e, true
		}
	}
	return scopeEntry{}, false
}

// ClassOf returns scope's class, or "" for a string this binary does not
// know (every Scope in AllScopes has one; the tests hold that).
func ClassOf(scope Scope) Class {
	e, _ := entryOf(scope)
	return e.class
}

// ScopesOfClass returns every scope of class c, in AllScopes order. A
// consumer derives its per-class lists from this — the scopes a person holds
// over their own resources, the ones every member holds — instead of keeping
// its own.
func ScopesOfClass(c Class) []Scope {
	var out []Scope
	for _, e := range catalog {
		if e.class == c {
			out = append(out, e.scope)
		}
	}
	return out
}

// ParseBundle maps a bundle name ("ci", not "bundle:ci") onto a known
// Bundle.
func ParseBundle(s string) (Bundle, bool) {
	for _, b := range AllBundles {
		if string(b) == s {
			return b, true
		}
	}
	return "", false
}

// BundleRef renders the stored spelling of a bundle, "bundle:<name>".
func BundleRef(b Bundle) string { return BundleRefPrefix + string(b) }

// ParseBundleRef reads a stored "bundle:<name>" entry. It reports false for
// anything else, including a reference to a bundle this binary does not know.
func ParseBundleRef(s string) (Bundle, bool) {
	name, ok := strings.CutPrefix(s, BundleRefPrefix)
	if !ok {
		return "", false
	}
	return ParseBundle(name)
}

// ExpandBundle returns the scopes bundle b grants under THIS binary's
// catalog: a fresh Set the caller may keep. An unknown bundle is empty.
//
// A SENSITIVE SCOPE IS NEVER IN THE RESULT. The catalog lists none in a
// bundle and the tests fail one that does; this skips them anyway, so a
// mistaken row widens nothing. A sensitive scope is held only by a grant
// that names it.
func ExpandBundle(b Bundle) Set {
	out := Set{}
	for _, e := range catalog {
		if e.class == ClassSensitive {
			continue
		}
		for _, in := range e.bundles {
			if in == b {
				out[e.scope] = struct{}{}
			}
		}
	}
	return out
}

// BundlesOf returns the bundles that grant scope, in AllBundles order: what
// an operator reads to learn who gains a scope without a grant naming it.
func BundlesOf(scope Scope) []Bundle {
	var out []Bundle
	for _, b := range AllBundles {
		if ExpandBundle(b).Has(scope) {
			out = append(out, b)
		}
	}
	return out
}

// ResolveStored reads a STORED list of grants — scopes and "bundle:<name>"
// references, as a grant row or a token row holds them — into the authority
// it names under this binary's catalog, and returns every entry it skipped
// (an unknown scope, an unknown bundle), in input order. It never fails.
//
// ── TOLERANT, BECAUSE SKIPPING ONLY NARROWS ──────────────────────────
//
// A row written by a newer binary may name a scope, or a bundle, this one has
// never heard of. NewSet refuses such a list, which is right for a list a
// caller is about to MINT: a typo must not mint a token that authenticates
// and then refuses everything. A stored grant is different: it was validated
// by the binary that wrote it, and a rollback must keep reading it. Dropping
// what this binary cannot evaluate can only take authority away — nothing
// here can exercise an unknown scope anyway — so the result is always a
// subset of what the writer meant, never more.
//
// ── THE CALLER LOGS THE SKIPPED AND REFUSES THE EMPTY ────────────────
//
// skipped is returned, not swallowed, so every consumer logs and counts what
// it dropped beside the token or grant it read it from: a skip that nobody
// sees is a token quietly narrower than its holder believes. An empty result
// is not refused here either; the consumer refuses a token whose resolved set
// is empty (a token with no authority that authenticates reads as an outage).
//
// A bundle reference expands through ExpandBundle, so it never yields a
// sensitive scope; a sensitive scope named on its own is kept.
func ResolveStored(raw []string) (resolved Set, skipped []string) {
	resolved = Set{}
	for _, s := range raw {
		if strings.HasPrefix(s, BundleRefPrefix) {
			b, ok := ParseBundleRef(s)
			if !ok {
				skipped = append(skipped, s)
				continue
			}
			for scope := range ExpandBundle(b) {
				resolved[scope] = struct{}{}
			}
			continue
		}
		scope, ok := ParseScope(s)
		if !ok {
			skipped = append(skipped, s)
			continue
		}
		resolved[scope] = struct{}{}
	}
	return resolved, skipped
}

// NeverOnDaemon reports whether scope must never ride a daemon's credential,
// even when its person holds it: org credential administration (token:read,
// token:write) and deciding an approval (approvals:decide). A daemon's
// credential is what an agent on that daemon holds, and these are acts the
// person performs themselves. A consumer removes them after resolving a
// daemon's wildcard (Grant.Admissible), which already leaves out the
// person-only and whole-credential scopes.
func NeverOnDaemon(scope Scope) bool {
	e, _ := entryOf(scope)
	return e.neverOnDaemon
}

// Elevatable reports whether an on-demand exchange — a person's session or
// daemon trading up for an hour when a call's refusal names the scope — may
// produce scope. It is org authority (member, standard or sensitive; a
// personal scope is held already and never exchanged for) that the catalog
// does not exclude: deploy:write (it authorizes the exchange itself),
// token:read and token:write, sourcemaps:write (a registry-only credential)
// and changes:launch (minted directly, never on demand).
//
// ELEVATION IS A SEPARATE, EXPLICIT PATH. A true here says nothing about
// bundles: a sensitive scope is elevatable (identity:manage, db:query) and is
// still never reachable through any bundle. An exchange still yields a scope
// only while the person behind it holds that scope by grant.
func Elevatable(scope Scope) bool {
	e, ok := entryOf(scope)
	if !ok || e.class == ClassPersonal {
		return false
	}
	return !e.notElevatable
}

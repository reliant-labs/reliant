package accesstoken

import "strings"

// Scope names one authority a token may carry, spelled <product>:<action>.
//
// SCOPES ARE THE ONLY THING THAT DISTINGUISHES ONE CREDENTIAL FROM ANOTHER.
// A CI deploy token, a daemon's connection credential, an LLM gateway key and
// a connector credential differ in their scope set (and binding), not in their
// table, prefix, hashing or authentication path.
//
// THE SET IS CLOSED. A scope that cannot be spelled cannot be granted, which is
// a stronger guarantee than a handler remembering to refuse it. A store backs
// this with a database CHECK over the same list, declared
// `COMMENT ON CONSTRAINT … IS 'forge:closed-set accesstoken.AllScopes'`, and
// `forge lint` fails when the CHECK and AllScopes differ, so neither can gain
// a member alone.
type Scope string

const (
	// ScopeDeployRead reads the org's deploy state.
	ScopeDeployRead Scope = "deploy:read"
	// ScopeDeployWrite mutates it (releases, environments, publish,
	// promote). Implies deploy:read at the point of use.
	ScopeDeployWrite Scope = "deploy:write"

	// ScopeBillingRead reads the org's billing-backed cost and usage. It grants
	// no billing management rights and is not implied by deploy or telemetry
	// authority. Consumers must check the tenant and current grants at use.
	ScopeBillingRead Scope = "billing:read"

	// ScopeClusterManage registers, rotates and removes the org's BYO
	// clusters — the credentials the platform uses to reach infrastructure
	// the org owns. Its own product, not a deploy action: deploy:write moves
	// a release onto clusters the org already trusts, and must not be able
	// to change WHICH clusters those are. An org-admin authority.
	ScopeClusterManage Scope = "cluster:manage"

	// ScopeTokenRead lists the org's machine credentials — an inventory of
	// its automation, so a separate authority from deploy:read.
	ScopeTokenRead Scope = "token:read"
	// ScopeTokenWrite mints and revokes credentials, bounded by Covers: a
	// holder can never grant a scope it does not hold. Without this scope a
	// token cannot mint at all, which is the general form of "a PAT cannot
	// mint a PAT".
	ScopeTokenWrite Scope = "token:write"

	// ScopeReliantAPI acts as the token's acting user against the reliant
	// API — the CLI / automation credential. Requires an acting user.
	ScopeReliantAPI Scope = "reliant:api"

	// ScopeDaemonConnect authenticates a daemon's stream to the gateway.
	// When bound to a daemon resource it authenticates as that daemon only.
	ScopeDaemonConnect Scope = "daemon:connect"

	// ScopeLLMInvoke calls the LLM gateway, billed to the acting user.
	ScopeLLMInvoke Scope = "llm:invoke"

	// ScopeProxyPort reaches one proxied daemon port. Always bound to a
	// port resource: an unbound port credential would open every port.
	ScopeProxyPort Scope = "proxy:port"

	// ScopeMCPConnector is a third-party MCP client's credential, confined to
	// one connector grant. Always bound to a connector resource.
	ScopeMCPConnector Scope = "mcp:connector"

	// ScopeDaemonResume resolves and wakes ONE suspended daemon on its acting
	// user's behalf, so a run nobody is watching (a scheduled trigger) can
	// reach the machine it was told to run on. Always bound to that daemon
	// resource and always acting as a user: an unbound resume credential
	// could wake every machine the person owns, and the point of a trigger
	// naming its daemon is that it never needs to.
	//
	// Wake only. It cannot connect AS the daemon (daemon:connect), run
	// anything on it, or read anything from it; the worst a leaked one does
	// is start a machine its owner already pays for.
	ScopeDaemonResume Scope = "daemon:resume"

	// ScopeDaemonManage lists, starts, stops and resizes the daemons its
	// acting user owns, as the machine settings page does: a person's CLI
	// login managing their own machines. Always acting as a user, and never
	// bound to anything — in particular never to a daemon, so no daemon's
	// own credential (a daemon wildcard resolves through Grant.Admissible)
	// and nothing an agent running on a daemon holds can carry it. Starting
	// and resizing spend the person's money, which is why a machine must not
	// be able to do it for itself or its siblings.
	//
	// It implies nothing and nothing implies it: not daemon:resume (one
	// bound machine, wake only), not daemon:connect, not reliant:api. The
	// consumer still resolves ownership, plan and funding from the acting
	// user exactly as for the person's own session.
	ScopeDaemonManage Scope = "daemon:manage"

	// ScopeSecretRead lists the org's managed secrets' METADATA (names,
	// versions) for every environment, and — for LOCAL environments ONLY —
	// pulls their VALUES (LocalSecretService/PullSecrets), because a LOCAL
	// env's workloads run on the developer's machine (`forge env up`) and
	// need them there. A PERSISTENT environment's values are never readable
	// through a token: the platform materializes them into its workloads and
	// the pull path is structurally confined to local envs.
	ScopeSecretRead Scope = "secret:read"
	// ScopeSecretWrite sets and deletes managed secret values. Implies
	// secret:read at the point of use.
	ScopeSecretWrite Scope = "secret:write"

	// ScopeDomainRead lists the org's custom domains and their bindings.
	ScopeDomainRead Scope = "domain:read"
	// ScopeDomainWrite registers domains, requests certificates and binds
	// them to environments. Implies domain:read at the point of use.
	//
	// ITS OWN PRODUCT, NOT A DEPLOY ACTION. A domain is a PUBLIC IDENTITY
	// and a certificate is issued in the org's name; binding one repoints
	// live traffic. deploy:write moves a release onto hostnames the org
	// already owns, and must not be able to change which names those are —
	// the same separation cluster:manage has from deploy:write.
	ScopeDomainWrite Scope = "domain:write"

	// ScopeIdentityRead reads an environment's managed identity: its status
	// and the end users of its instance, with their roles.
	ScopeIdentityRead Scope = "identity:read"
	// ScopeIdentityManage administers those end users: invite, change roles,
	// lock, delete, end sessions, issue password-set codes, and export the
	// instance. Implies identity:read at the point of use.
	//
	// ITS OWN PRODUCT, NOT A DEPLOY ACTION. It acts on the PEOPLE who sign in
	// to the org's apps, not on the org's infrastructure: a code it issues
	// sets an end user's password. deploy:write ships a release and must not
	// be able to take over the accounts of the people who use it.
	ScopeIdentityManage Scope = "identity:manage"

	// ScopeObsRead reads an environment's telemetry and what is derived from
	// it: traces, logs, metrics, issues and release health. Org automation
	// tokens carry it so CI and agents can gate a deploy on `forge obs check`.
	ScopeObsRead Scope = "obs:read"
	// ScopeObsTriage changes an issue's triage state (resolve, ignore,
	// unresolve). Implies obs:read at the point of use.
	//
	// ITS OWN SCOPE, NOT A READ. A read-only automation token must not be
	// able to silence an alert, so triage is held explicitly even by a
	// holder that reads.
	ScopeObsTriage Scope = "obs:triage"
	// ScopeObsWrite authors the org's observability workspace: dashboards
	// (save, restore, archive, promote) and anything else a person edits in
	// the telemetry console. Implies obs:read at the point of use.
	//
	// ITS OWN SCOPE, NOT TRIAGE. obs:triage changes the state of an issue; this
	// changes shared, versioned artifacts the whole org reads. Neither implies
	// the other, so a token that silences alerts cannot rewrite dashboards and
	// the reverse. Never bound to an environment: what it writes is org-wide.
	ScopeObsWrite Scope = "obs:write"

	// ScopeDBQuery runs a read-only query against an environment's database.
	// Not implied by any other scope and implying none: the data in an
	// app's database is the app's users' data, a different class of authority
	// from the telemetry obs:read reads. A real environment's policy may
	// further restrict it to people (audited); the scope alone never opens
	// one.
	ScopeDBQuery Scope = "db:query"
)

// AllScopes is the closed set, grouped by product, least authority first.
var AllScopes = []Scope{
	ScopeDeployRead,
	ScopeDeployWrite,
	ScopeBillingRead,
	ScopeClusterManage,
	ScopeTokenRead,
	ScopeTokenWrite,
	ScopeReliantAPI,
	ScopeDaemonConnect,
	ScopeLLMInvoke,
	ScopeProxyPort,
	ScopeMCPConnector,
	ScopeDaemonResume,
	ScopeDaemonManage,
	ScopeSecretRead,
	ScopeSecretWrite,
	ScopeDomainRead,
	ScopeDomainWrite,
	ScopeIdentityRead,
	ScopeIdentityManage,
	ScopeObsRead,
	ScopeObsTriage,
	ScopeObsWrite,
	ScopeDBQuery,
}

// ParseScope maps a wire string onto a known Scope. An unknown string is an
// error, never a silently dropped entry: dropping "deploy:wrIte" would mint a
// token that authenticates and then refuses every write.
func ParseScope(s string) (Scope, bool) {
	for _, known := range AllScopes {
		if string(known) == s {
			return known, true
		}
	}
	return "", false
}

// Set is the authority a token carries. Build it with NewSet, the single place
// an unknown scope is rejected.
type Set map[Scope]struct{}

// NewSet builds a Set, refusing unknown and empty input.
//
// Refusing unknown scopes is the fail-closed direction on the READ path: a row
// written by a newer server may hold a scope this binary cannot evaluate, and
// proceeding with the understood subset would make a token's authority depend
// on which server version answered. An empty set is refused because a token
// with no authority that authenticates reads as an outage.
func NewSet(scopes []string) (Set, error) {
	if len(scopes) == 0 {
		return nil, &UnknownScopeError{Reason: "a token must carry at least one scope"}
	}
	out := make(Set, len(scopes))
	for _, raw := range scopes {
		scope, ok := ParseScope(raw)
		if !ok {
			return nil, &UnknownScopeError{Scope: raw, Reason: "not a scope this server recognises"}
		}
		out[scope] = struct{}{}
	}
	return out, nil
}

// SetOf builds a Set from known Scope constants. For call sites that name
// scopes in code, where an unknown value is a compile-time impossibility.
func SetOf(scopes ...Scope) Set {
	out := make(Set, len(scopes))
	for _, s := range scopes {
		out[s] = struct{}{}
	}
	return out
}

// UnknownScopeError names the scope that could not be evaluated.
type UnknownScopeError struct {
	Scope  string
	Reason string
}

func (e *UnknownScopeError) Error() string {
	if e.Scope == "" {
		return "accesstoken: " + e.Reason
	}
	return "accesstoken: scope " + e.Scope + ": " + e.Reason
}

// Has reports exact membership.
func (s Set) Has(scope Scope) bool {
	_, ok := s[scope]
	return ok
}

// Permits reports whether this set authorizes an action requiring `want`.
//
// WRITE IMPLIES READ, WITHIN ONE PRODUCT, AT THE POINT OF USE ONLY. The stored
// grant is not widened; a caller holding deploy:write may perform deploy reads
// because every write path reads first. No implication crosses products —
// deploy:write never implies token:read.
func (s Set) Permits(want Scope) bool {
	if s.Has(want) {
		return true
	}
	switch want {
	case ScopeDeployRead:
		return s.Has(ScopeDeployWrite)
	case ScopeTokenRead:
		return s.Has(ScopeTokenWrite)
	case ScopeSecretRead:
		return s.Has(ScopeSecretWrite)
	case ScopeDomainRead:
		return s.Has(ScopeDomainWrite)
	case ScopeIdentityRead:
		return s.Has(ScopeIdentityManage)
	case ScopeObsRead:
		return s.Has(ScopeObsTriage) || s.Has(ScopeObsWrite)
	default:
		return false
	}
}

// Covers reports whether this set may GRANT every scope in `requested`.
//
// THE RULE: a token may never mint a token carrying scopes it does not itself
// hold. Authority can be narrowed on delegation and never widened.
//
// It uses EXACT MEMBERSHIP, not Permits. Permits' write-implies-read is a
// statement about what an action needs; routing grants through it would make
// every future usability implication a minting power.
//
// An empty request is not covered — a subset test that returns true for the
// empty set is how a caller concludes a token may grant anything.
func (s Set) Covers(requested Set) bool {
	if len(requested) == 0 {
		return false
	}
	for scope := range requested {
		if !s.Has(scope) {
			return false
		}
	}
	return true
}

// Missing returns the scopes in `requested` this set does not hold, in
// AllScopes order, so a refusal can name what was over-reached.
func (s Set) Missing(requested Set) []Scope {
	out := make([]Scope, 0, len(requested))
	for _, scope := range AllScopes {
		if requested.Has(scope) && !s.Has(scope) {
			out = append(out, scope)
		}
	}
	return out
}

// Strings returns the scopes as wire strings in AllScopes order — stable, so
// two reads of one unchanged token serialize identically.
func (s Set) Strings() []string {
	out := make([]string, 0, len(s))
	for _, scope := range AllScopes {
		if s.Has(scope) {
			out = append(out, string(scope))
		}
	}
	return out
}

// String renders the set for diagnostics. Scopes are not secret.
func (s Set) String() string { return strings.Join(s.Strings(), ",") }

// JoinScopes renders a scope slice for an error message.
func JoinScopes(scopes []Scope) string {
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, string(scope))
	}
	return strings.Join(out, ", ")
}

// ReasonScopeMissing is the error reason (svcerr.ReasonHeader) a server gives
// when the caller authenticated but its credential, or the person it acts
// for, lacks a scope the call needs. Signing in again does not fix it; a
// grant does. Carry the missing scopes in [ScopesHeader].
const ReasonScopeMissing = "scope_missing"

// ScopesHeader is the Connect error-metadata key that names the scopes a
// [ReasonScopeMissing] refusal is about, comma-separated with no spaces
// ("deploy:write,secret:read"; [ScopesHeaderValue]). Structured so a client
// can say which grant to ask for without parsing the message. A server
// attaches it with svcerr.WithMetadata; forge's CORS defaults expose it.
const ScopesHeader = "x-forge-error-scopes"

// ScopesHeaderValue renders scopes for [ScopesHeader].
func ScopesHeaderValue[S ~string](scopes []S) string {
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if s := strings.TrimSpace(string(scope)); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, ",")
}

// ParseScopesHeader reads a [ScopesHeader] value back into its scopes, in
// order. Blank entries are dropped; an empty value is nil.
func ParseScopesHeader(value string) []string {
	var out []string
	for _, s := range strings.Split(value, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

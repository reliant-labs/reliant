package accesstoken

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ResourceKind names what a token may be BOUND to. The set is closed.
//
// A binding confines a token to one resource: a daemon credential that works
// for that daemon only, a share link that opens one port, a connector
// credential confined to one grant. It bounds a leaked token's blast radius to
// the one thing it was issued for, which is why it is carried over on merit
// from reliant's per-daemon PATs.
type ResourceKind string

const (
	// ResourceDaemon binds to one daemon id.
	ResourceDaemon ResourceKind = "daemon"
	// ResourcePort binds to one proxied port of one daemon. The id is
	// PortResourceID(daemonID, port).
	ResourcePort ResourceKind = "port"
	// ResourceConnector binds to one MCP connector grant id.
	ResourceConnector ResourceKind = "connector"
	// ResourceEnvironment binds to one environment id: a CI token that may
	// read one env's telemetry (obs:read), triage its issues (obs:triage) or
	// query its database (db:query), and no other env of the org.
	ResourceEnvironment ResourceKind = "environment"
)

// AllResourceKinds is the closed set; a store's CHECK declares
// 'forge:closed-set accesstoken.AllResourceKinds' and `forge lint` pins it.
var AllResourceKinds = []ResourceKind{ResourceDaemon, ResourcePort, ResourceConnector, ResourceEnvironment}

// Resource is a binding: a kind and an id.
type Resource struct {
	Kind ResourceKind
	ID   string
}

// PortResourceID renders the id of a port binding. One spelling, here, so a
// minting site and a checking site cannot disagree about the separator.
func PortResourceID(daemonID string, port int32) string {
	return fmt.Sprintf("%s:%d", daemonID, port)
}

// scopeResourceRules says, per scope, which binding it requires or allows.
//
//	required — the scope is meaningless (or dangerous) unbound: a port
//	           credential with no port would open every port.
//	allowed  — the scope may be bound or unbound. An unbound daemon:connect
//	           is a user registering a new daemon; a bound one is a managed
//	           daemon that may authenticate as that daemon only.
//
// A scope with no entry may not be bound at all, except to a DAEMON (see
// admitsOnDaemon): a deploy token bound to a PORT is a contradiction, and
// refusing it keeps a binding's meaning exact.
var scopeResourceRules = map[Scope]struct {
	kind     ResourceKind
	required bool
}{
	ScopeDaemonConnect: {kind: ResourceDaemon, required: false},
	ScopeProxyPort:     {kind: ResourcePort, required: true},
	ScopeMCPConnector:  {kind: ResourceConnector, required: true},
	// Required, unlike daemon:connect: there is no legitimate unbound resume
	// credential. A trigger names the daemon its runs use, so the token is
	// minted for exactly that one and teardown of the daemon revokes it.
	ScopeDaemonResume: {kind: ResourceDaemon, required: true},
	// Allowed, not required: an org-wide obs:read token is the common CI
	// case, and an env-bound one confines a leaked credential to one env.
	ScopeObsRead:   {kind: ResourceEnvironment, required: false},
	ScopeObsTriage: {kind: ResourceEnvironment, required: false},
	ScopeDBQuery:   {kind: ResourceEnvironment, required: false},
}

// admitsOnDaemon reports whether scope may ride a token bound to a DAEMON:
// every scope in the catalog EXCEPT one whose own binding rule requires a
// different kind of resource (a port share, a connector credential). It is
// the authority the acting user already holds, exercised from inside their
// own daemon.
//
// ── A RULE, NOT A LIST ────────────────────────────────────────────────
//
// This was an allow-list (daemonBindableScopes) and every scope added to the
// catalog had to be remembered here too, or no daemon could carry it: a
// daemon credential holds EVERY scope (owner decision, 2026-10-29) and the
// store resolves that against this rule at use (Grant.Admissible), so a scope
// left off the list silently went missing from every daemon on the machine.
// Deriving it from scopeResourceRules makes a new scope ride a daemon by
// default; a scope that must not is one that requires its own binding kind,
// which it has to declare anyway. TestAdmissible_DaemonCarriesEveryScope holds
// it.
//
// ── WHY A DAEMON BINDING IS DIFFERENT FROM A PORT OR CONNECTOR ────────
//
// A managed daemon runs forge on the user's behalf: the reliant UI's Deploy
// button re-execs the daemon, and that subprocess inherits the daemon's
// credential and nothing else. Without this, forge inside a managed daemon
// cannot authenticate at all — the only way in would be `forge login` in a
// daemon shell, which needs a browser loopback a remote pod does not have.
//
// The binding is what makes this SAFE rather than a widening. The token acts
// as the user (ActingUserID), in the one org on the row, and is confined to
// the one daemon — so it does what that person could do from their laptop, in
// a place they already control, and teardown of that daemon revokes it. The
// minting site still clips these scopes to the user's real authority, so this
// list is a CEILING, never a grant.
//
// A port or connector binding gets none of this. Those credentials are not a
// person acting in their own environment: a share link has no acting user at
// all and may be held by anyone with the URL. Carrying deploy authority on one
// would be authority attributed to nobody, which is why the kinds are
// distinguished here rather than the rule being relaxed for every binding.
//
// Org administration (cluster:manage, token:read, token:write) rides it too.
// That is a CEILING, not a grant: the authority clips a daemon credential to
// its user's current org grants at every use, an exchange attenuates to the
// caller's held scopes, and token:write can never mint a scope its holder
// lacks (Covers).
//
// The one other exception is a person-only scope (personOnlyScopes).
func admitsOnDaemon(scope Scope) bool {
	if personOnly(scope) {
		return false
	}
	rule, ok := scopeResourceRules[scope]
	return !ok || !rule.required || rule.kind == ResourceDaemon
}

// personOnlyScopes are held only when a PERSON's grant names them (a CLI
// login): no binding admits them, and a wildcard credential — a daemon's,
// bound or not — never resolves to them (Grant.Admissible).
//
// ── THE EXCEPTION TO "A DAEMON HOLDS EVERY SCOPE" ─────────────────────
//
// A daemon credential is what an agent running on that daemon holds. A scope
// here is one whose use spends the person's money with nobody approving it,
// so the owner decided (2026-10-29) that an agent reaches it only through an
// approval card in chat, never through its machine's credential. It is a
// list, not a rule, because nothing structural distinguishes such a scope;
// TestAdmissible_DaemonCarriesEveryScope names each member.
var personOnlyScopes = []Scope{
	ScopeDaemonManage,
}

// personOnly reports whether scope is in personOnlyScopes.
func personOnly(scope Scope) bool {
	for _, s := range personOnlyScopes {
		if s == scope {
			return true
		}
	}
	return false
}

// actingUserScopes are the scopes that ACT AS A PERSON: their authority is the
// acting user's, so a token carrying one without an acting user would be
// authority attributed to nobody. Org-level automation (deploy, token, secret)
// needs no acting user; that is the org CI token.
var actingUserScopes = []Scope{
	ScopeReliantAPI,
	ScopeDaemonConnect,
	ScopeLLMInvoke,
	ScopeMCPConnector,
	// A wake is billed to, and authorized as, the person who owns the
	// daemon; the consumer resolves ownership from the acting user.
	ScopeDaemonResume,
	// Machines are owned by, and billed to, a person.
	ScopeDaemonManage,
}

// RequiresActingUser reports whether a scope acts as a person.
func RequiresActingUser(scope Scope) bool {
	for _, s := range actingUserScopes {
		if s == scope {
			return true
		}
	}
	return false
}

// ErrEscalation reports a mint that would grant authority the grantor does not
// hold. Distinct from ErrInvalidGrant because it is an authority problem
// (PermissionDenied), not a malformed request (InvalidArgument).
var ErrEscalation = errors.New("accesstoken: a token cannot grant scopes it does not hold")

// ErrInvalidGrant reports a malformed mint request.
var ErrInvalidGrant = errors.New("accesstoken: invalid grant")

// Grant is everything a mint decides. Every store validates it with Validate
// before inserting, so the rules below have one implementation.
type Grant struct {
	// OrgID is the one organization the token acts on, for its whole life.
	OrgID string
	// Name is the operator's label. Descriptive only.
	Name string
	// Scopes is the authority granted.
	Scopes Set
	// ActingUserID, when set, is the person the token acts as. Required by
	// any scope in actingUserScopes; absent for an org automation token.
	ActingUserID string
	// Resource, when set, confines the token to one resource.
	Resource *Resource
	// Ephemeral marks a token tied to a session's lifetime (a desktop
	// daemon's run), revoked when that session ends. It must be bound and
	// must expire: an ephemeral token that outlives everything is a
	// contradiction.
	Ephemeral bool
	// ExpiresAt is optional for non-ephemeral tokens. Checked at every
	// authentication; never defaulted.
	ExpiresAt *time.Time

	// GrantorScopes is the authority of the CALLER when the caller is itself
	// a token. NIL MEANS A HUMAN CALLER (or trusted internal service) whose
	// authority was checked by the handler before this; it is absence, not a
	// synthetic "every scope" value that could leak into another check.
	GrantorScopes Set
}

// Validate enforces every structural rule of a grant at time `now`.
func (g Grant) Validate(now time.Time) error {
	if strings.TrimSpace(g.OrgID) == "" {
		return fmt.Errorf("%w: org id is required", ErrInvalidGrant)
	}
	if strings.TrimSpace(g.Name) == "" {
		return fmt.Errorf("%w: a token needs a name", ErrInvalidGrant)
	}
	if len(g.Scopes) == 0 {
		return fmt.Errorf("%w: a token needs at least one scope", ErrInvalidGrant)
	}

	// The escalation rule. Nil grantor = human / internal caller.
	if g.GrantorScopes != nil && !g.GrantorScopes.Covers(g.Scopes) {
		return fmt.Errorf("%w: missing %s", ErrEscalation, JoinScopes(g.GrantorScopes.Missing(g.Scopes)))
	}

	for _, scope := range AllScopes {
		if g.Scopes.Has(scope) && RequiresActingUser(scope) && strings.TrimSpace(g.ActingUserID) == "" {
			return fmt.Errorf("%w: scope %s acts as a user and needs an acting user", ErrInvalidGrant, scope)
		}
	}

	if err := g.validateResource(); err != nil {
		return err
	}

	if g.Ephemeral {
		if g.Resource == nil {
			return fmt.Errorf("%w: an ephemeral token must be bound to a resource", ErrInvalidGrant)
		}
		if g.ExpiresAt == nil {
			return fmt.Errorf("%w: an ephemeral token must expire", ErrInvalidGrant)
		}
	}
	if g.ExpiresAt != nil && !g.ExpiresAt.After(now) {
		return fmt.Errorf("%w: expiry must be in the future", ErrInvalidGrant)
	}
	return nil
}

// Admissible returns every scope in AllScopes that a grant shaped like g — its
// acting user and its binding; its scopes, org, name and expiry are ignored —
// may carry. It is what "every scope" MEANS for a credential of that shape.
//
// ── THE WILDCARD IS RESOLVED, NEVER STORED AS A LIST ──────────────────
//
// A credential that holds every scope (a daemon's: Principal.AllScopes) must
// not be written down as the catalog of the day it was minted, or every scope
// added later is missing from it until somebody re-mints or migrates. A store
// keeps the wildcard as a flag and resolves it with this at each use, so a
// scope this binary learns is held from the next request. The resolution is
// still bounded by Validate's structural rules — a daemon binding never
// resolves to a port share (proxy:port) — and the caller still clips it to
// what the acting user currently holds.
func (g Grant) Admissible() Set {
	out := Set{}
	for _, scope := range AllScopes {
		if personOnly(scope) {
			continue
		}
		probe := Grant{OrgID: "probe", Name: "probe", ActingUserID: g.ActingUserID,
			Resource: g.Resource, Scopes: SetOf(scope)}
		if probe.Validate(time.Time{}) == nil {
			out[scope] = struct{}{}
		}
	}
	return out
}

// KnownScopes builds a Set from the strings this binary recognises and SKIPS
// the rest. It is for a WILDCARD credential's resolved scopes only (see
// Principal.AllScopes): a newer authority lists scopes this binary has never
// heard of, which the credential holds by definition and nothing here can
// exercise, so dropping them narrows nothing. Every explicit scope list goes
// through NewSet, which refuses an unknown scope rather than authenticating
// with the understood subset.
func KnownScopes(raw []string) Set {
	out := Set{}
	for _, s := range raw {
		if scope, ok := ParseScope(s); ok {
			out[scope] = struct{}{}
		}
	}
	return out
}

func (g Grant) validateResource() error {
	if g.Resource != nil {
		if !isResourceKind(g.Resource.Kind) {
			return fmt.Errorf("%w: unknown resource kind %q", ErrInvalidGrant, g.Resource.Kind)
		}
		if strings.TrimSpace(g.Resource.ID) == "" {
			return fmt.Errorf("%w: a resource binding needs an id", ErrInvalidGrant)
		}
	}
	for _, scope := range AllScopes {
		if !g.Scopes.Has(scope) {
			continue
		}
		rule, bindable := scopeResourceRules[scope]
		// A DAEMON binding additionally admits the user-authority scopes —
		// see admitsOnDaemon for why this kind and no other. Checked
		// before the scope's own rule: obs:read binds to an environment, and
		// may still ride the acting user's daemon credential.
		if g.Resource != nil && g.Resource.Kind == ResourceDaemon && admitsOnDaemon(scope) {
			continue
		}
		switch {
		case !bindable && g.Resource != nil:
			return fmt.Errorf("%w: scope %s cannot be bound to a resource", ErrInvalidGrant, scope)
		case bindable && g.Resource == nil && rule.required:
			return fmt.Errorf("%w: scope %s must be bound to a %s", ErrInvalidGrant, scope, rule.kind)
		case bindable && g.Resource != nil && g.Resource.Kind != rule.kind:
			return fmt.Errorf("%w: scope %s binds to a %s, not a %s", ErrInvalidGrant, scope, rule.kind, g.Resource.Kind)
		}
	}
	return nil
}

func isResourceKind(k ResourceKind) bool {
	for _, known := range AllResourceKinds {
		if known == k {
			return true
		}
	}
	return false
}

// Principal is what an authenticated token resolves to. Every validator —
// control-plane's in-process one, reliant's introspecting one, a self-host
// store — yields this shape.
type Principal struct {
	TokenID string
	OrgID   string
	// ActingUserID is empty for an org automation token. A consumer that
	// needs a person (a reliant API call, an LLM request) refuses an empty
	// one; it never substitutes the creator.
	ActingUserID string
	Scopes       Set
	// AllScopes marks a credential that holds EVERY scope — a daemon's —
	// rather than a list. Scopes is then its resolution against this binary's
	// catalog (Grant.Admissible), clipped by the authority to what the acting
	// user holds; a validator reading a remote authority's answer builds it
	// with KnownScopes, so a scope only a newer authority knows is skipped
	// rather than failing the credential.
	AllScopes bool
	// Resource is the binding, or nil for an unbound token.
	Resource  *Resource
	Ephemeral bool
	ExpiresAt *time.Time
}

// BoundTo reports whether the token is bound to exactly this resource.
func (p *Principal) BoundTo(kind ResourceKind, id string) bool {
	return p != nil && p.Resource != nil && p.Resource.Kind == kind && p.Resource.ID == id
}

// MayReadEnvironment reports whether the token may act on environment envID
// of its org: an environment-bound token on its own environment only; any
// other token (unbound, or bound to the acting user's daemon) on every
// environment of the org. Org membership and scopes are the caller's checks.
func (p *Principal) MayReadEnvironment(envID string) bool {
	if p == nil {
		return false
	}
	if p.Resource == nil || p.Resource.Kind != ResourceEnvironment {
		return true
	}
	return p.Resource.ID == envID
}

// MayActOn reports whether the token may act on this resource: an unbound
// token may (its authority is its scopes and acting user), a bound one only on
// its own resource. Required-binding scopes never reach the unbound case,
// because Validate refuses to mint them unbound.
func (p *Principal) MayActOn(kind ResourceKind, id string) bool {
	if p == nil {
		return false
	}
	if p.Resource == nil {
		return true
	}
	return p.BoundTo(kind, id)
}

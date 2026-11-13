// Copyright (c) 2025 Reliant Labs

// Package orgs answers reliant's organization questions about a caller: which
// org a creating request acts in, and whether a caller is a live member of an
// org a list names. Membership's source of truth is control-plane
// (docs/design/orgs-and-members.md §5 there): this package asks it, through
// AccessTokenInternalService.ResolveOrgMembership, and keeps no copy beyond a
// short-lived cache.
//
// ── THE CACHE ─────────────────────────────────────────────────────────
//
// Answers, positive and negative, are kept for at most CacheTTL. That bound is
// how long a removed member can keep acting on an org's resources here, and
// how long a new member waits to see them. Errors are never cached: an outage
// must not be remembered as someone's removal. Every question fails CLOSED on
// an error (Unavailable), except the one that cannot widen anything: a
// creating request that names no org stores no org (see ActingOrg).
//
// ── NO CONTROL PLANE ──────────────────────────────────────────────────
//
// A self-hosted deployment (AUTH_MODE=apikey, or no RELIANT_CONTROL_PLANE_URL)
// has no orgs. Its Resolver is nil, every method of a nil *Resolver answers
// "no org", nothing is stamped with one, and lists stay "everything the caller
// owns", as before orgs. A control plane that predates the rpc answers the
// same way.
//
//forge:exclude-contract: a cache over one control-plane call; consumers declare the one- or two-method slice they use, and tests pass a fake Source
package orgs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// BackfillInterval is how often ListScope re-stamps one user's unassigned
// rows. Rows only become unassigned when a create could not resolve an org,
// so after the first stamp there is rarely anything to do.
const BackfillInterval = 10 * time.Minute

// CacheTTL bounds how long an answer about one (user, org) is trusted. It is
// the documented lag between a removal at control-plane and its effect here
// (decision D14 of the orgs design).
const CacheTTL = 30 * time.Second

// ReasonNotOrgMember is the x-forge-error-reason of a request naming an org
// its caller does not belong to: control-plane's string for the same refusal.
const ReasonNotOrgMember = "not_org_member"

// ReasonOrgMismatch is the reason of a request whose credential acts in one
// org naming another: control-plane's string for the same refusal.
const ReasonOrgMismatch = "org_mismatch"

// Source is control-plane's answer. accesstokenclient.Service is one.
type Source interface {
	ResolveOrgMembership(ctx context.Context, userID, orgID string) (accesstokenclient.OrgMembership, error)
}

// Resolver caches Source's answers. A nil *Resolver is a deployment with no
// orgs.
type Resolver struct {
	src Source
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	answers map[key]answer
	// stamped is when each user's unassigned rows were last stamped with
	// their default org (ListScope), so a busy list stamps at most once per
	// BackfillInterval.
	stamped map[string]time.Time
}

type key struct{ userID, orgID string }

type answer struct {
	m       accesstokenclient.OrgMembership
	expires time.Time
}

// New returns a Resolver over src. A nil src is no control plane: New
// returns nil, the Resolver of a deployment with no orgs.
func New(src Source) *Resolver {
	if src == nil {
		return nil
	}
	return &Resolver{src: src, ttl: CacheTTL, now: time.Now, answers: map[key]answer{}, stamped: map[string]time.Time{}}
}

// errUnsupported is the internal "this deployment has no answer".
var errUnsupported = accesstokenclient.ErrUnsupported

func (r *Resolver) resolve(ctx context.Context, userID, orgID string) (accesstokenclient.OrgMembership, error) {
	k := key{userID, orgID}
	now := r.now()
	r.mu.Lock()
	if a, ok := r.answers[k]; ok && now.Before(a.expires) {
		r.mu.Unlock()
		return a.m, nil
	}
	r.mu.Unlock()

	m, err := r.src.ResolveOrgMembership(ctx, userID, orgID)
	if err != nil {
		return accesstokenclient.OrgMembership{}, err
	}
	r.remember(k, m)
	return m, nil
}

// remember caches m as the answer for k for one TTL.
func (r *Resolver) remember(k key, m accesstokenclient.OrgMembership) {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	// Expired entries are dropped as they are met, so the map holds at most
	// the (user, org) pairs asked about within one TTL.
	for k2, a := range r.answers {
		if !now.Before(a.expires) {
			delete(r.answers, k2)
		}
	}
	r.answers[k] = answer{m: m, expires: now.Add(r.ttl)}
}

// IsMember reports whether userID is a live member of orgID. ok is false when
// the deployment has no orgs (a nil Resolver, or a control plane without the
// rpc): the caller then applies today's owner-only rule. An error is an
// outage, and the caller fails closed on it.
func (r *Resolver) IsMember(ctx context.Context, userID, orgID string) (member, ok bool, err error) {
	if r == nil || userID == "" || orgID == "" {
		return false, false, nil
	}
	m, err := r.resolve(ctx, userID, orgID)
	if errors.Is(err, errUnsupported) {
		return false, false, nil
	}
	if err != nil {
		return false, true, err
	}
	return m.Member && m.OrgID == orgID, true, nil
}

// Membership is control-plane's whole answer about userID in orgID (member,
// role, when the current membership began, the org's live member count),
// cached like IsMember. ok is false when the deployment has no orgs; err is an
// outage. A caller deciding access from it fails closed on both: a negative,
// an unknown and an error all grant nothing.
func (r *Resolver) Membership(ctx context.Context, userID, orgID string) (m accesstokenclient.OrgMembership, ok bool, err error) {
	if r == nil || userID == "" || orgID == "" {
		return accesstokenclient.OrgMembership{}, false, nil
	}
	m, err = r.resolve(ctx, userID, orgID)
	if errors.Is(err, errUnsupported) {
		return accesstokenclient.OrgMembership{}, false, nil
	}
	if err != nil {
		return accesstokenclient.OrgMembership{}, true, err
	}
	if m.OrgID != orgID {
		// An answer about another org says nothing about this one.
		return accesstokenclient.OrgMembership{OrgID: orgID}, true, nil
	}
	return m, true, nil
}

// The roles control-plane answers (its org_memberships.role). Authority to
// administer an org is RoleOwner or RoleAdmin (docs/design/org-sharing.md
// §3.4 in control-plane).
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Role is userID's role in orgID, cached like membership (CacheTTL): for
// display and other questions a stale answer cannot widen. A decision that
// grants admin authority asks RoleFresh instead.
//
// role is RoleOwner, RoleAdmin or RoleMember for a live member, "" otherwise.
// ok reports whether control-plane gave a definite answer: true for a member
// with a role and for a non-member (role ""); false when the role is unknown
// (a deployment with no orgs, a control plane without the rpc, or one that
// predates the role field and answers a member with none). err is an outage.
// Every caller refuses an admin action unless role is RoleOwner or RoleAdmin,
// so an unknown role and an error both fail closed.
func (r *Resolver) Role(ctx context.Context, userID, orgID string) (role string, ok bool, err error) {
	if r == nil || userID == "" || orgID == "" {
		return "", false, nil
	}
	m, err := r.resolve(ctx, userID, orgID)
	return roleOf(m, orgID, err)
}

// RoleFresh is Role asked of control-plane now, bypassing the cache, so a
// just-demoted admin cannot act on a cached role. The fresh answer replaces
// the cached one (membership included). It is what every admin-only write
// asks (creating an org connection, clearing a share, adopting an
// automation).
func (r *Resolver) RoleFresh(ctx context.Context, userID, orgID string) (role string, ok bool, err error) {
	if r == nil || userID == "" || orgID == "" {
		return "", false, nil
	}
	m, err := r.src.ResolveOrgMembership(ctx, userID, orgID)
	if err == nil {
		r.remember(key{userID, orgID}, m)
	}
	return roleOf(m, orgID, err)
}

// roleOf reads Role's answer from one membership answer about orgID.
func roleOf(m accesstokenclient.OrgMembership, orgID string, err error) (string, bool, error) {
	if errors.Is(err, errUnsupported) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !m.Member || m.OrgID != orgID {
		return "", true, nil
	}
	switch m.Role {
	case RoleOwner, RoleAdmin, RoleMember:
		return m.Role, true, nil
	default:
		// A member with no role (an older control plane) or one this
		// build does not know: unknown, never guessed upward.
		return "", false, nil
	}
}

// DefaultOrg is the org userID acts in when a request names none: "" when
// they have none or the deployment has no orgs.
func (r *Resolver) DefaultOrg(ctx context.Context, userID string) (string, error) {
	if r == nil || userID == "" {
		return "", nil
	}
	m, err := r.resolve(ctx, userID, "")
	if errors.Is(err, errUnsupported) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !m.Member {
		return "", nil
	}
	return m.OrgID, nil
}

// Named is the org a request acts in by name: the credential's org for a
// machine token (which acts in exactly one), else requested once the caller
// is verified a live member of it. "" when nothing is named or the deployment
// has no orgs.
//
// A credential bound to one org naming another is PermissionDenied
// (ReasonOrgMismatch), and a non-member is PermissionDenied
// (ReasonNotOrgMember) whether or not the org exists. An outage is
// Unavailable: a request that names its org is never stored under a guess.
func (r *Resolver) Named(ctx context.Context, userID, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if r == nil {
		return "", nil
	}
	if p, ok := auth.MachineTokenFromContext(ctx); ok && p != nil && p.OrgID != "" {
		if requested != "" && requested != p.OrgID {
			return "", svcerr.WithReason(svcerr.PermissionDenied(
				"this credential acts in organization "+p.OrgID+" only; it cannot act in "+requested), ReasonOrgMismatch)
		}
		// The credential's org, while its acting user is still a member of
		// it: a token outlives a removal no longer than a session does.
		return r.liveMember(ctx, userID, p.OrgID)
	}
	if requested == "" {
		return "", nil
	}
	return r.liveMember(ctx, userID, requested)
}

// liveMember is orgID when userID is a live member of it, "" when the
// deployment has no answer, and PermissionDenied (ReasonNotOrgMember)
// otherwise; an outage is Unavailable.
func (r *Resolver) liveMember(ctx context.Context, userID, orgID string) (string, error) {
	member, ok, err := r.IsMember(ctx, userID, orgID)
	if err != nil {
		return "", LookupFailed(ctx, "could not verify the organization; try again", err)
	}
	if !ok {
		// No orgs here: nothing to store the name under.
		return "", nil
	}
	if !member {
		return "", NotMember(orgID)
	}
	return orgID, nil
}

// LookupFailed is the error of an org lookup that failed: Unavailable with
// detail and err as its cause, unless the request itself ended first. A
// request its client cancelled, or whose deadline passed, failed for that
// reason, not for an outage: it is Canceled / DeadlineExceeded, so a client
// navigating away is never reported, logged or counted as a server error.
func LookupFailed(ctx context.Context, detail string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return svcerr.Wrap(ctxErr)
	}
	return svcerr.WithCause(svcerr.Unavailable(detail), err)
}

// NotMember is the refusal of a request naming an org its caller is not a
// live member of. The same answer for an org that does not exist.
func NotMember(orgID string) error {
	return svcerr.WithReason(svcerr.PermissionDenied("you are not a member of organization "+orgID), ReasonNotOrgMember)
}

// ActingOrg is the org a creating request acts in: Named, else the caller's
// default org. A default that cannot be resolved (an outage) is "" with the
// cause logged: the row is stored with no org, which keeps today's owner-only
// semantics and is stamped with the default org later (lazy backfill), so the
// create never fails for an org the request did not ask for.
func (r *Resolver) ActingOrg(ctx context.Context, userID, requested string) (string, error) {
	named, err := r.Named(ctx, userID, requested)
	if err != nil || named != "" {
		return named, err
	}
	def, err := r.DefaultOrg(ctx, userID)
	if err != nil {
		logging.WarnContext(ctx, "[orgs] could not resolve the default org; creating with none", "user_id", userID, "error", err)
		return "", nil
	}
	return def, nil
}

// Backfill stamps userID's rows that have no org with orgID (their default
// org). *db.Repo's AssignUnassignedOrg is one.
type Backfill func(ctx context.Context, userID, orgID string) error

// ListScope is the org filter of a list request. nil, no filter, when an
// interactive request names no org (every client before orgs) or the
// deployment has none: the list stays "everything the caller owns".
//
// A machine credential acts in exactly one org, so its list is always that
// org's, named or not: omitting org_id never widens it to the acting user's
// other orgs, and naming another org is PermissionDenied (ReasonOrgMismatch).
//
// A named org must be one the caller is a live member of (PermissionDenied
// otherwise), and an outage is Unavailable: the list fails closed rather than
// show the wrong org. When the named org is the caller's default, rows with no
// org yet (written before orgs) list with it, and stamp, when not nil, gives
// them that org for good: the lazy backfill. A failed stamp is logged only;
// the IncludeUnassigned predicate already lists those rows.
func (r *Resolver) ListScope(ctx context.Context, userID, requested string, stamp Backfill) (*core.OrgScope, error) {
	requested = strings.TrimSpace(requested)
	if r == nil {
		return nil, nil
	}
	if p, ok := auth.MachineTokenFromContext(ctx); ok && p != nil && p.OrgID != "" {
		if requested == "" {
			requested = p.OrgID
		}
		if requested != p.OrgID {
			return nil, svcerr.WithReason(svcerr.PermissionDenied(
				"this credential acts in organization "+p.OrgID+" only; it cannot list "+requested), ReasonOrgMismatch)
		}
	} else if requested == "" {
		return nil, nil
	}
	// A named org, or a credential's, lists only while the caller (the
	// credential's acting user) is still a live member of it.
	if named, err := r.liveMember(ctx, userID, requested); err != nil || named == "" {
		return nil, err
	}
	def, err := r.DefaultOrg(ctx, userID)
	if err != nil {
		return nil, LookupFailed(ctx, "could not resolve your default organization; try again", err)
	}
	scope := &core.OrgScope{OrgID: requested, IncludeUnassigned: def == requested}
	if scope.IncludeUnassigned && stamp != nil && r.dueForStamp(userID) {
		if err := stamp(ctx, userID, def); err != nil {
			r.forgetStamp(userID)
			logging.WarnContext(ctx, "[orgs] could not stamp unassigned rows with the default org", "user_id", userID, "org_id", def, "error", err)
		}
	}
	return scope, nil
}

// dueForStamp reports whether userID's rows are due a stamp, and claims it.
func (r *Resolver) dueForStamp(userID string) bool {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if at, ok := r.stamped[userID]; ok && now.Sub(at) < BackfillInterval {
		return false
	}
	for u, at := range r.stamped {
		if now.Sub(at) >= BackfillInterval {
			delete(r.stamped, u)
		}
	}
	r.stamped[userID] = now
	return true
}

func (r *Resolver) forgetStamp(userID string) {
	r.mu.Lock()
	delete(r.stamped, userID)
	r.mu.Unlock()
}

// InProject is the org of a new row that lives in a project (a chat, a
// trigger): the project's when it has one, since a row and its project are in
// one org, so a request naming another org is FailedPrecondition. Otherwise
// the org the request named (Named), else the caller's default (def), else
// none.
func InProject(projectOrg *string, named, def string) (*string, error) {
	if projectOrg != nil && *projectOrg != "" {
		if named != "" && named != *projectOrg {
			return nil, svcerr.FailedPrecondition("the project belongs to another organization: open it from that organization")
		}
		org := *projectOrg
		return &org, nil
	}
	for _, org := range []string{named, def} {
		if org != "" {
			return &org, nil
		}
	}
	return nil, nil
}

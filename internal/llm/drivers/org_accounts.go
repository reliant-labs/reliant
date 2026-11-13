// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"fmt"
	"sync"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// This file is the org half of the one loader (control-plane
// docs/design/org-provider-accounts.md §4.1): which of a person's accounts,
// and which of an org's, a run of that org may draw on, with the tier it is
// routed in and the funding class its usage records.

// OrgMembership is what the loader asks control-plane, through reliant's
// cached resolver (*orgs.Resolver), about the person a run acts for. Both
// questions fail closed: an unknown answer (ok false) or an error leaves the
// org's accounts and the person's seats out, never in.
type OrgMembership interface {
	// Membership is the person's membership of orgID: whether they are a
	// live member, their role, and when their current membership began.
	Membership(ctx context.Context, userID, orgID string) (m accesstokenclient.OrgMembership, ok bool, err error)
	// DefaultOrg is the org the person's unattributed managed Reliant key
	// bills today (§11.2: it counts as org-funded only in that org).
	DefaultOrg(ctx context.Context, userID string) (string, error)
}

var (
	orgMembershipMu sync.RWMutex
	orgMembership   OrgMembership
)

// InitializeOrgMembership wires the membership resolver the loader asks. Until
// it is called (and on a deployment with no control plane, whose resolver
// answers "no org"), no org account or seat is ever a candidate.
func InitializeOrgMembership(m OrgMembership) {
	orgMembershipMu.Lock()
	defer orgMembershipMu.Unlock()
	orgMembership = m
}

func currentOrgMembership() OrgMembership {
	orgMembershipMu.RLock()
	defer orgMembershipMu.RUnlock()
	return orgMembership
}

// candidate is one stored account the scope may draw on, before its secret is
// shaped into a driver config.
type candidate struct {
	acct    *core.AccountWithSecrets
	limit   *core.LimitState
	tier    models.AccountTier
	funding string
	// group orders accounts within a tier: a seat (0) before an org key (1),
	// each then in its owner's priority order (§4.2, D4).
	group int
}

// memberFacts is what the loader learned about the person in the scope's org.
type memberFacts struct {
	// member is true only for a definite answer that the person is a live
	// member: an unknown deployment, a negative and an outage are all false.
	member bool
	m      accesstokenclient.OrgMembership
	// src is who answered, for the one follow-up question (DefaultOrg).
	src OrgMembership
}

// orgMemberFacts asks whether scope's person is a live member of scope's org.
func orgMemberFacts(ctx context.Context, src OrgMembership, scope Scope) memberFacts {
	if scope.Personal() || src == nil {
		return memberFacts{}
	}
	m, ok, err := src.Membership(ctx, scope.UserID, scope.OrgID)
	if err != nil {
		// Fail closed (§8.1 point 4): the org's accounts and the person's
		// seats are left out; their own accounts still serve.
		logging.WarnContext(ctx, "Could not read org membership; leaving the org's accounts and seats out of this resolution",
			"user_id", scope.UserID, "org_id", scope.OrgID, "error", err)
		return memberFacts{}
	}
	if !ok || !m.Member || m.OrgID != scope.OrgID {
		return memberFacts{src: src}
	}
	return memberFacts{member: true, m: m, src: src}
}

// isManagedKey reports whether acct is the managed Reliant key (billed to an
// org's Reliant wallet, never to the person).
func isManagedKey(acct *core.AccountWithSecrets) bool {
	return acct.AuthKind == core.AuthKindManaged || (acct.Provider == "reliant" && acct.IdentityKey == core.ManagedIdentityKey)
}

// personalCandidate classifies one of the person's own rows for scope. ok is
// false for a row the scope may not draw on.
func personalCandidate(ctx context.Context, scope Scope, facts memberFacts, acct *core.AccountWithSecrets) (c candidate, ok bool) {
	c = candidate{acct: acct, tier: models.TierSecond, funding: models.FundingPersonal}
	if isManagedKey(acct) {
		c.funding = models.FundingOrgWallet
	}
	switch attributed := acct.AttributedOrgID; {
	case attributed == "":
		// The person's own, or today's org-less managed key: §4.1 row 1.
		if !scope.OrgFundedOnly() {
			return c, true
		}
		// An unattended run spends only org-funded sources. The org-less
		// managed key bills the person's default org, so it counts only in
		// that org (§11.2 before P10).
		if c.funding != models.FundingOrgWallet || !facts.member {
			return candidate{}, false
		}
		def, err := facts.src.DefaultOrg(ctx, scope.UserID)
		if err != nil {
			logging.WarnContext(ctx, "Could not read the default org; leaving the managed key out of an unattended run",
				"user_id", scope.UserID, "org_id", scope.OrgID, "error", err)
			return candidate{}, false
		}
		return c, def == scope.OrgID
	case scope.Personal() || attributed != scope.OrgID || !facts.member:
		// Attributed to another org, a call that names no org, or a person
		// who is not (or cannot be shown to be) a member of the org paying
		// for it: never a candidate (§3.2, §4.1).
		return candidate{}, false
	case c.funding == models.FundingOrgWallet:
		// The managed key minted in this org (P10). D4: it keeps the place the
		// gateway has today; it is not preferred over the person's accounts.
		return c, true
	default:
		// An org-paid seat. It is the person's login, reserved by the
		// providers for interactive use, so never an unattended run's (I3).
		if scope.OrgFundedOnly() {
			return candidate{}, false
		}
		c.tier, c.funding, c.group = models.TierFirst, models.FundingOrgSeat, 0
		return c, true
	}
}

// orgAccountCandidates loads the scope org's own accounts the person may use
// (§3.3): live member, and the account's audience admits them. The SQL names
// the org (core.OrgAccountOwner), so no other org's account can come back.
func orgAccountCandidates(ctx context.Context, repo OrgAccountStore, scope Scope, facts memberFacts) []candidate {
	if scope.Personal() || !facts.member {
		return nil
	}
	owner := core.OrgAccountOwner(scope.OrgID)
	rows, err := repo.ListOwnedProviderAccountsWithSecrets(ctx, owner)
	if err != nil {
		logging.WarnContext(ctx, "Could not read the org's provider accounts; resolving without them",
			"user_id", scope.UserID, "org_id", scope.OrgID, "error", err)
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	var grants map[string]bool
	limits, err := repo.ListOwnedAccountLimits(ctx, owner)
	if err != nil {
		logging.WarnContext(ctx, "Could not read the org accounts' limit state; treating them as unlimited",
			"org_id", scope.OrgID, "error", err)
	}
	out := make([]candidate, 0, len(rows))
	for i := range rows {
		acct := &rows[i]
		if acct.OwnerKind != core.AccountOwnerOrg || acct.OrgID != scope.OrgID {
			// Belt and braces over the owner-scoped read.
			continue
		}
		admitted := false
		switch acct.OrgAccess {
		case core.OrgAccessMembers:
			admitted = true
		case core.OrgAccessAdmins:
			admitted = facts.m.Role == "owner" || facts.m.Role == "admin"
		case core.OrgAccessSelected:
			if grants == nil {
				grants = memberGrants(ctx, repo, scope, facts)
			}
			admitted = grants[acct.ID]
		}
		if !admitted {
			continue
		}
		c := candidate{acct: acct, tier: models.TierFirst, funding: models.FundingOrgKey, group: 1}
		if l, ok := limits[acct.ID]; ok {
			c.limit = &l
		}
		out = append(out, c)
	}
	return out
}

// memberGrants is the set of the org's accounts granted to the person in their
// CURRENT membership: a grant counts only when made at or after member_since,
// so a person who left and rejoined starts with none, and with no
// member_since (an older control plane) none counts (§3.3, fail closed).
func memberGrants(ctx context.Context, repo OrgAccountStore, scope Scope, facts memberFacts) map[string]bool {
	out := map[string]bool{}
	if facts.m.MemberSince.IsZero() {
		return out
	}
	rows, err := repo.ListMemberAccountGrants(ctx, scope.OrgID, scope.UserID)
	if err != nil {
		logging.WarnContext(ctx, "Could not read the member's account grants; leaving selected-access accounts out",
			"user_id", scope.UserID, "org_id", scope.OrgID, "error", err)
		return out
	}
	for id, grantedAt := range rows {
		if !grantedAt.Before(facts.m.MemberSince) {
			out[id] = true
		}
	}
	return out
}

// driverRank is each driver's best tier over all its accounts, or nil when no
// account is on the first tier: nothing is tiered, so resolution is exactly
// as before. The per-model tie-break is AvailableDrivers.TierFor.
func driverRank(accounts map[models.DriverID][]models.AccountConfig) map[models.DriverID]models.AccountTier {
	rank := make(map[models.DriverID]models.AccountTier, len(accounts))
	tiered := false
	for driverID, list := range accounts {
		if len(list) == 0 {
			continue
		}
		best := models.TierSecond
		for _, a := range list {
			if a.Tier < best {
				best = a.Tier
			}
		}
		rank[driverID] = best
		tiered = tiered || best == models.TierFirst
	}
	if !tiered {
		return nil
	}
	return rank
}

// ErrAccountOutOfScope is the driver build's refusal of an account owned by,
// or attributed to, an org other than the run's (§8.1 point 3).
type ErrAccountOutOfScope struct {
	AccountID, AccountOrg, ScopeOrg string
}

func (e *ErrAccountOutOfScope) Error() string {
	return fmt.Sprintf("provider account %s belongs to org %q, not the run's org %q", e.AccountID, e.AccountOrg, e.ScopeOrg)
}

// CheckAccountScope is the re-check at the driver build (§8.1 point 3): an
// account owned by an org, or attributed to one, serves only that org's runs.
// It catches a stale sticky assignment (a project move, a race with a
// revocation) and any future code that hands the router accounts from another
// scope. The refusal is logged at ERROR with both orgs; the caller re-resolves
// rather than failing the turn.
func CheckAccountScope(ctx context.Context, scope Scope, cfg models.DriverConfig) error {
	for _, org := range []string{cfg.AccountOrgID, cfg.AttributedOrgID} {
		if org == "" || org == scope.OrgID {
			continue
		}
		err := &ErrAccountOutOfScope{AccountID: cfg.AccountID, AccountOrg: org, ScopeOrg: scope.OrgID}
		logging.ErrorContext(ctx, "Refusing to build a driver for an account of another org",
			"account_id", cfg.AccountID, "account_org_id", org, "scope_org_id", scope.OrgID, "user_id", scope.UserID)
		return err
	}
	return nil
}

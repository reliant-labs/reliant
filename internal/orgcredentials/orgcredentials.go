// Copyright (c) 2025 Reliant Labs

// Package orgcredentials keeps the credentials reliant holds for orgs in step
// with control-plane's orgs (control-plane
// docs/design/org-provider-accounts.md §3.3, §6; the P8 row of §14).
// Membership and whether an org exists are control-plane's; reliant asks and
// sweeps, and never copies either.
//
// ── STALE GRANTS ──────────────────────────────────────────────────────
//
// A provider_account_grants row is bound to the membership it was made in: it
// counts only while its member is live AND it was made at or after the
// membership began (member_since). Control-plane cannot delete reliant's rows
// in the transaction that removes a member, so a removed or rejoined member's
// old grants are inert at the read and deleted here, lazily. Every deletion is
// decided by a fresh answer: a grant from before the current membership began,
// or, for a person who is no member, a grant made before the question was
// asked. A grant made after a negative answer may belong to a membership that
// began since, so it waits for the next pass. An unknown member_since (a
// control plane that predates it) and an error delete nothing.
//
// ── GONE ORGS ─────────────────────────────────────────────────────────
//
// An org is gone when control-plane soft-deleted it or its owner's account was
// deleted with no live member left. Nothing reaches a gone org's credentials
// before this runs: nobody is its member, so no org key serves, no connection
// resolves and no admin can act. What remains is the data, and this destroys
// it: the org's provider accounts, grants and workspace bindings, its
// connections, the managed keys minted in it, and its vault tenant's keys (the
// crypto-shred); members' seats it paid for are released. An org is purged
// only GoneOrgGrace after it went, the window obstenant keeps a gone org's
// telemetry for, because the shred cannot be undone and a wrongly detected
// deletion can. A control plane that cannot say (an outage, or one without
// ResolveGoneOrgs) purges nothing.
//
//forge:exclude-contract: one concrete Sweeper over consumer-declared seams (the repo, control-plane's two answers), driven by internal/app's OrgCredentialSweep; no second implementation to substitute
package orgcredentials

import (
	"context"
	"errors"
	"time"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// GoneOrgGrace is how long after an org went its credentials are kept before
// they are destroyed: control-plane obstenant's retention of a gone org's
// telemetry, so a wrongly detected deletion is recoverable in both.
const GoneOrgGrace = 7 * 24 * time.Hour

// Interval is how often Run sweeps. Neither sweep is urgent: nothing serves
// from a stale grant or a gone org's credential in the meantime.
const Interval = time.Hour

// pageSize bounds one read of the store and one ResolveGoneOrgs call.
const pageSize = 200

// Store is the repository slice the sweeps use (*db.Repo).
type Store interface {
	ListProviderAccountGrantMembers(ctx context.Context, after core.GrantMember, limit int) ([]core.GrantMember, error)
	DeleteProviderAccountGrantsBefore(ctx context.Context, orgID, userID string, before time.Time) (int64, error)
	ListOrgCredentialHolders(ctx context.Context, after string, limit int) ([]string, error)
	PurgeOrgCredentials(ctx context.Context, orgID string) (core.OrgCredentialsPurged, error)
}

// Membership is control-plane's answer about one member, asked fresh
// (accesstokenclient.Service; never the cached orgs.Resolver: a cached
// negative could outlive a rejoin).
type Membership interface {
	ResolveOrgMembership(ctx context.Context, userID, orgID string) (accesstokenclient.OrgMembership, error)
}

// GoneOrgs is control-plane's answer about which orgs are gone
// (accesstokenclient.Service).
type GoneOrgs interface {
	ResolveGoneOrgs(ctx context.Context, orgIDs []string) ([]accesstokenclient.GoneOrg, error)
}

// Deps are the Sweeper's collaborators.
type Deps struct {
	Store      Store
	Membership Membership
	GoneOrgs   GoneOrgs
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Sweeper runs the two sweeps.
type Sweeper struct {
	deps Deps
	now  func() time.Time
}

// New builds a Sweeper.
func New(d Deps) *Sweeper {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &Sweeper{deps: d, now: now}
}

// SweepStaleGrants deletes every grant that no longer counts and returns how
// many went. A control plane without the membership rpc deletes nothing; an
// error ends the pass, and the next pass starts over.
func (s *Sweeper) SweepStaleGrants(ctx context.Context) (int64, error) {
	var (
		deleted int64
		after   core.GrantMember
	)
	for {
		page, err := s.deps.Store.ListProviderAccountGrantMembers(ctx, after, pageSize)
		if err != nil {
			return deleted, err
		}
		for _, gm := range page {
			n, err := s.sweepMember(ctx, gm)
			deleted += n
			if errors.Is(err, accesstokenclient.ErrUnsupported) {
				return deleted, nil
			}
			if err != nil {
				return deleted, err
			}
		}
		if len(page) < pageSize {
			return deleted, nil
		}
		after = page[len(page)-1]
	}
}

// sweepMember deletes gm's grants that no longer count, by one fresh answer.
func (s *Sweeper) sweepMember(ctx context.Context, gm core.GrantMember) (int64, error) {
	asked := s.now()
	m, err := s.deps.Membership.ResolveOrgMembership(ctx, gm.UserID, gm.OrgID)
	if err != nil {
		return 0, err
	}
	var before time.Time
	switch {
	case m.OrgID != gm.OrgID && (m.Member || m.OrgID != ""):
		// An answer about another org says nothing about this one. A
		// negative may name no org at all (control-plane answers an
		// unknown or deleted user with an empty answer): that is still
		// "no member" of the org asked about.
		return 0, nil
	case !m.Member:
		// No member: every grant made before the question is from a
		// membership that ended. One made since may be a rejoin's.
		before = asked
	case m.MemberSince.IsZero():
		// A member whose membership start is unknown (an older control
		// plane): the read counts none of their grants, and the sweep
		// cannot tell which are void, so it keeps them.
		return 0, nil
	default:
		before = m.MemberSince
	}
	return s.deps.Store.DeleteProviderAccountGrantsBefore(ctx, gm.OrgID, gm.UserID, before)
}

// SweepGoneOrgs purges the credentials of every org control-plane reports
// gone for at least GoneOrgGrace, and returns what it removed. A control plane
// without ResolveGoneOrgs purges nothing; an error ends the pass.
func (s *Sweeper) SweepGoneOrgs(ctx context.Context) (core.OrgCredentialsPurged, error) {
	var (
		total core.OrgCredentialsPurged
		after string
	)
	for {
		page, err := s.deps.Store.ListOrgCredentialHolders(ctx, after, pageSize)
		if err != nil {
			return total, err
		}
		if len(page) == 0 {
			return total, nil
		}
		gone, err := s.deps.GoneOrgs.ResolveGoneOrgs(ctx, page)
		if errors.Is(err, accesstokenclient.ErrUnsupported) {
			return total, nil
		}
		if err != nil {
			return total, err
		}
		now := s.now()
		for _, g := range gone {
			if now.Sub(g.At) < GoneOrgGrace {
				continue
			}
			purged, err := s.deps.Store.PurgeOrgCredentials(ctx, g.OrgID)
			if err != nil {
				return total, err
			}
			if purged.Total() > 0 {
				logging.InfoContext(ctx, "purged a gone org's credentials",
					"org_id", g.OrgID, "gone_at", g.At,
					"provider_accounts", purged.ProviderAccounts, "grants", purged.Grants,
					"workspaces", purged.Workspaces, "seats_released", purged.Attributions,
					"managed_keys", purged.ManagedKeys, "connections", purged.Connections,
					"vault_keys", purged.VaultKeys)
			}
			add(&total, purged)
		}
		if len(page) < pageSize {
			return total, nil
		}
		after = page[len(page)-1]
	}
}

func add(t *core.OrgCredentialsPurged, p core.OrgCredentialsPurged) {
	t.ProviderAccounts += p.ProviderAccounts
	t.Grants += p.Grants
	t.Workspaces += p.Workspaces
	t.Attributions += p.Attributions
	t.ManagedKeys += p.ManagedKeys
	t.Connections += p.Connections
	t.ConnectionEvents += p.ConnectionEvents
	t.OAuthFlows += p.OAuthFlows
	t.VaultKeys += p.VaultKeys
}

// Run sweeps every interval until ctx ends. A failed sweep is logged and
// retried at the next pass: both are idempotent.
func (s *Sweeper) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if n, err := s.SweepStaleGrants(ctx); err != nil {
			logging.WarnContext(ctx, "sweeping stale provider account grants failed", "deleted", n, "error", err)
		} else if n > 0 {
			logging.InfoContext(ctx, "deleted stale provider account grants", "deleted", n)
		}
		if _, err := s.SweepGoneOrgs(ctx); err != nil {
			logging.WarnContext(ctx, "purging gone orgs' credentials failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

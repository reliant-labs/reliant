// Copyright (c) 2025 Reliant Labs

// Package seatmatch is the binding matcher (control-plane
// docs/design/org-provider-accounts.md §3.4): it makes a member's sign-in in a
// provider workspace their org pays for one of the org's seats
// (provider_accounts.attributed_org_id, source 'workspace'), and makes a seat
// that no binding of its org covers any more the person's own again.
//
// A seat whose holder left the org stays the org's (§6, "Member leaves"): it
// is still the org's seat at the provider, so it serves nothing until the
// holder removes it or an admin releases it.
//
// It runs at the three points the design names: when a person connects an
// account or signs in to it again (settings' sign-in, MatchAccount), and when an
// admin binds a workspace (MatchWorkspace, over every sign-in already in it).
// Unbinding needs no matcher: the store clears the binding's seats in the same
// transaction.
//
// Attributing is gated by ApiServerConfig orgs.provider_workspace_seats_enabled:
// a release before org-provider-accounts P5 ignores attributed_org_id, so a seat
// would serve its holder's other orgs after a rollback to it. Clearing a seat
// only narrows what serves, so it is never gated.
//
// Membership is read live (orgs.Resolver). An outage, or a deployment with no
// orgs, attributes nothing.
package seatmatch

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Service is the binding matcher.
type Service interface {
	// MatchAccount brings one of userID's sign-ins in line with the workspace
	// bindings after it was connected or signed in to again. A sign-in in a
	// bound workspace whose holder is a live member of the binding's org, and
	// that the org has not released, becomes the org's seat. A seat no binding
	// of its org covers (the sign-in moved workspace, or the binding went
	// without clearing it) becomes the person's own again. Any other account
	// (an API key, the managed key, a seat an admin approved) is left alone.
	MatchAccount(ctx context.Context, userID, accountID string) (Outcome, error)
	// MatchWorkspace attributes to w's org every sign-in already in w whose
	// holder is a live member of it, after an admin bound w. It returns how
	// many it attributed; an error is the first failure, after it has tried
	// every sign-in.
	MatchWorkspace(ctx context.Context, w core.OrgProviderWorkspace) (int, error)
}

// Outcome is what MatchAccount did.
type Outcome string

const (
	// Unchanged: the account already matched the bindings, or the matcher
	// could not tell (a membership outage) and attributed nothing.
	Unchanged Outcome = "unchanged"
	// Attributed: the account became a seat the org pays for.
	Attributed Outcome = "attributed"
	// Cleared: the account stopped being a seat and is the person's own again.
	Cleared Outcome = "cleared"
)

// Store is the slice of the repository the matcher reads and writes; *db.Repo
// satisfies it.
type Store interface {
	// ListProviderAccounts is the person's own accounts, metadata only.
	ListProviderAccounts(ctx context.Context, userID string) ([]core.ProviderAccount, error)
	// ProviderWorkspaceBinding is the binding of provider's workspace
	// externalOrgID (one org at most), or nil when it is not bound.
	ProviderWorkspaceBinding(ctx context.Context, provider, externalOrgID string) (*core.OrgProviderWorkspace, error)
	// ListWorkspaceSignIns is every person's sign-in in provider's workspace
	// externalOrgID, metadata only.
	ListWorkspaceSignIns(ctx context.Context, provider, externalOrgID string) ([]core.ProviderAccount, error)
	// AttributeWorkspaceSeat makes accountID w's org's seat (source
	// 'workspace') and reports whether it did. It does nothing when the
	// binding no longer exists, the account is no longer in w or is already
	// attributed, or the org released it.
	AttributeWorkspaceSeat(ctx context.Context, w core.OrgProviderWorkspace, accountID string) (bool, error)
	// ClearWorkspaceSeat makes accountID, a seat orgID's binding matched
	// (source 'workspace'), the person's own again; reports whether it did.
	// It re-decides under the account's lock: a seat a binding of orgID
	// covers by then (a rebind matched it again) is left alone.
	ClearWorkspaceSeat(ctx context.Context, orgID, accountID string) (bool, error)
}

// Members answers whether a person is a live member of an org; *orgs.Resolver
// satisfies it. ok is false when the deployment has no orgs; err is an outage.
type Members interface {
	IsMember(ctx context.Context, userID, orgID string) (member, ok bool, err error)
}

// Rollout is the deployment's org rollout gates; *configv1.OrgsConfig
// satisfies it.
type Rollout interface {
	GetProviderWorkspaceSeatsEnabled() bool
}

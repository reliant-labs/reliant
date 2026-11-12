// Copyright (c) 2025 Reliant Labs

// Package orgtakeover answers the questions the takeover of a departed
// member's automations asks (control-plane docs/design/org-sharing.md §8):
// does this person administer the org, is this person still a live member of
// it, and so has an automation's creator left it.
//
// ── WHO ADMINISTERS ───────────────────────────────────────────────────
//
// An org admin is a person (never a machine or agent credential) whose org
// role is admin or owner, read FRESH from control-plane, so a just-demoted
// admin cannot act on a stale cache (decision D8): authz.RequireOrgAdmin. A
// role that cannot be named (an older control plane, a deployment with no
// orgs) is refused: fail closed.
//
// ── WHO HAS LEFT ──────────────────────────────────────────────────────
//
// Membership is authz's own answer, asked through the trigger rule for a
// row the person owns in the org (the question triggers.CreatorLeftOrg asks
// before a firing), so it reads the process's membership and its cache
// exactly as every other decision does. A deployment with no orgs has no
// answer, and there nobody has ever left.
//
//forge:exclude-contract: functions over authz that the takeover handlers call directly; they are tested through authz's own Membership seam
package orgtakeover

import (
	"context"
	"errors"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// ReasonOrgAdminRequired is the x-forge-error-reason of an admin-only action
// refused because the caller is not an admin or owner of the org, or their
// role could not be confirmed.
const ReasonOrgAdminRequired = "org_admin_required"

// RequireAdmin refuses unless userID, signed in as a person, is an admin or
// owner of orgID by a fresh read of their role (authz.RequireOrgAdmin): a
// member, a non-member and an unknown role are PermissionDenied with
// ReasonOrgAdminRequired, an outage Unavailable.
func RequireAdmin(ctx context.Context, userID, orgID string) error {
	if err := auth.RequirePerson(ctx, "administer an organization's automations"); err != nil {
		return err
	}
	switch err := authz.RequireOrgAdmin(ctx, userID, orgID); {
	case err == nil:
		return nil
	case errors.Is(err, authz.ErrMembershipUnavailable):
		return orgs.LookupFailed(ctx, "could not confirm your role in this organization; try again", err)
	default:
		// ErrNotOrgAdmin, or ErrDenied (a credential bound to another org).
		return svcerr.WithCause(svcerr.WithReason(
			svcerr.PermissionDenied("only an admin or owner of this organization can do this"), ReasonOrgAdminRequired), err)
	}
}

// LiveMember reports whether userID is a live member of orgID, by authz's
// rule (cached, orgs.CacheTTL). A deployment with no orgs answers true. err
// is an outage (authz.ErrMembershipUnavailable): the caller fails closed.
func LiveMember(ctx context.Context, userID, orgID string) (bool, error) {
	if userID == "" || orgID == "" {
		return false, nil
	}
	org := orgID
	switch denial := authz.WriteTriggerDenial(ctx, userID, &core.Trigger{UserID: userID, OrgID: &org}); {
	case denial == nil:
		return true, nil
	case errors.Is(denial, authz.ErrMembershipUnavailable):
		return false, denial
	default:
		// ErrNotOrgMember, or ErrDenied (a credential bound to another org,
		// which reaches nothing of this one).
		return false, nil
	}
}

// CreatorLeft reports whether ownerUserID, the creator of an automation in
// org (nil: none), is no longer a live member of it: whether the automation
// is an orphan. An automation with no org never is.
func CreatorLeft(ctx context.Context, ownerUserID string, org *string) (bool, error) {
	if org == nil || *org == "" {
		return false, nil
	}
	member, err := LiveMember(ctx, ownerUserID, *org)
	if err != nil {
		return false, err
	}
	return !member, nil
}

// Departures memoizes CreatorLeft per (user, org) for one request, so a list
// asks once per creator however many automations they left behind.
type Departures struct {
	left map[[2]string]bool
}

// Left is CreatorLeft, asked at most once per (ownerUserID, org).
func (d *Departures) Left(ctx context.Context, ownerUserID string, org *string) (bool, error) {
	if org == nil || *org == "" {
		return false, nil
	}
	k := [2]string{ownerUserID, *org}
	if v, ok := d.left[k]; ok {
		return v, nil
	}
	v, err := CreatorLeft(ctx, ownerUserID, org)
	if err != nil {
		return false, err
	}
	if d.left == nil {
		d.left = map[[2]string]bool{}
	}
	d.left[k] = v
	return v, nil
}

// Unavailable is the error of a membership read that failed: Unavailable,
// never an answer.
func Unavailable(ctx context.Context, err error) error {
	return orgs.LookupFailed(ctx, "could not check organization membership; try again", err)
}

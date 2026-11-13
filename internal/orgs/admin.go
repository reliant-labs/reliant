// Copyright (c) 2025 Reliant Labs

package orgs

import (
	"context"
	"errors"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
)

// ReasonHumanOnly is the x-forge-error-reason of an org administration
// refused because the request came with a credential an agent or a script can
// hold (a CLI login, an rlat_ access token, an org automation token):
// control-plane's reason for its members rpcs (orgs-and-members.md §2.3), and
// org connections' (connections.ReasonHumanOnly).
const ReasonHumanOnly = "human_only"

// ReasonOrgAdminRequired is the x-forge-error-reason of an admin-only action
// refused because the caller is not an owner or admin of the org, or their
// role could not be confirmed.
const ReasonOrgAdminRequired = "org_admin_required"

// RequireAdmin refuses unless userID, signed in to the app as a person, is an
// owner or admin of orgID by a fresh read of their role
// (authz.RequireOrgAdmin, so a just-demoted admin cannot act on a cached
// role). action names what was refused, for the message ("manage an
// organization's AI accounts"):
//
//   - a credential an agent or a script can hold: PermissionDenied, reason
//     ReasonHumanOnly, whatever the caller's role;
//   - a member, a non-member, an unknown role, or a credential bound to
//     another org: PermissionDenied, reason ReasonOrgAdminRequired;
//   - an outage: Unavailable (never an answer: it is not a removal).
func RequireAdmin(ctx context.Context, userID, orgID, action string) error {
	if err := RequirePerson(ctx, action); err != nil {
		return err
	}
	switch err := authz.RequireOrgAdmin(ctx, userID, orgID); {
	case err == nil:
		return nil
	case errors.Is(err, authz.ErrMembershipUnavailable):
		return LookupFailed(ctx, "could not confirm your role in this organization; try again", err)
	case errors.Is(err, authz.ErrPersonRequired):
		return humanOnly(action, err)
	default:
		// ErrNotOrgAdmin, or ErrDenied (a credential bound to another org).
		return svcerr.WithCause(svcerr.WithReason(
			svcerr.PermissionDenied("only an owner or admin of this organization can "+action), ReasonOrgAdminRequired), err)
	}
}

// RequirePerson refuses, with PermissionDenied and ReasonHumanOnly, a request
// whose credential an agent or a script can hold (auth.RequirePerson's rule).
func RequirePerson(ctx context.Context, action string) error {
	if err := auth.RequirePerson(ctx, action); err != nil {
		return humanOnly(action, err)
	}
	return nil
}

func humanOnly(action string, cause error) error {
	return svcerr.WithCause(svcerr.WithReason(svcerr.PermissionDenied(
		"only a person signed in to the Reliant app can "+action+"; this request was made with a credential "+
			"an agent or a script can hold (such as the CLI login)"), ReasonHumanOnly), cause)
}

// RequireMember refuses unless userID is a live member of orgID (authz's
// rule, cached for CacheTTL): PermissionDenied with ReasonNotOrgMember
// otherwise, the same answer for an org that does not exist; an outage is
// Unavailable.
func RequireMember(ctx context.Context, userID, orgID string) error {
	switch err := authz.OrgMemberDenial(ctx, userID, orgID); {
	case err == nil:
		return nil
	case errors.Is(err, authz.ErrMembershipUnavailable):
		return LookupFailed(ctx, "could not verify the organization; try again", err)
	default:
		return svcerr.WithCause(NotMember(orgID), err)
	}
}

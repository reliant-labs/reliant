// Copyright (c) 2025 Reliant Labs

package authz

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Refusals of a project move (ProjectOrgMove) beyond ErrDenied (the call
// site's NotFound), ErrNotOrgMember (the owner left the project's org),
// ErrPersonRequired, ErrNotOrgAdmin and ErrMembershipUnavailable.
var (
	// ErrMoveNeedsOrgs: the deployment has no organizations (self-hosted),
	// or the owner belongs to none, so there is nothing to move between.
	ErrMoveNeedsOrgs = errors.New("authz: there are no organizations to move the project between")
	// ErrMoveSameOrg: the project is already in the organization named.
	ErrMoveSameOrg = errors.New("authz: the project is already in that organization")
	// ErrNotTargetMember: the caller is not a live member of the
	// organization they asked to move the project to.
	ErrNotTargetMember = errors.New("authz: not a member of the organization to move the project to")
)

// ProjectOrgMove decides userID moving project to toOrgID (control-plane
// docs/design/org-project-move.md §4.1, D3, D4): the org it moves from, or
// why it may not.
//
//   - Only the project's owner, while they may write it (deny: a live member
//     of its org, through a credential that acts there). Anyone else, an
//     admin of its org or a viewer of a shared project included, is ErrDenied:
//     a move is never made for another person, and the call site answers it
//     NotFound, as for an outsider.
//   - Only as a person (ErrPersonRequired): an agent must never move its own
//     project across an org boundary.
//   - The source is the project's org, or, for a project the backfill has
//     not reached, the owner's default org, which the backfill would give it.
//     None (no orgs here) is ErrMoveNeedsOrgs.
//   - The owner holds admin or owner in the source, asked fresh
//     (RequireOrgAdmin): the org owns what is in it, and its admin's role is
//     its consent to the project leaving.
//   - The owner is a live member of the target, any role
//     (ErrNotTargetMember), as creating a project there needs.
//
// It fails closed on every outage (ErrMembershipUnavailable). The commit
// re-checks the source's org under the owner's org lock, and control-plane
// re-checks both memberships and the role when the move is prepared.
func ProjectOrgMove(ctx context.Context, userID string, project *core.Project, toOrgID string) (string, error) {
	r := projectResource(project)
	if err := deny(ctx, userID, r); err != nil {
		return "", err
	}
	if !personCredential(ctx) {
		return "", ErrPersonRequired
	}
	from := r.orgID
	if from == "" {
		defaults, ok := membershipOf(ctx).(defaultOrgs)
		if !ok {
			return "", ErrMoveNeedsOrgs
		}
		def, err := defaults.DefaultOrg(ctx, userID)
		if err != nil {
			logging.WarnContext(ctx, "[authz] could not read the default org; denying a project move", "user_id", userID, "error", err)
			return "", ErrMembershipUnavailable
		}
		if def == "" {
			return "", ErrMoveNeedsOrgs
		}
		from = def
	}
	if toOrgID == from {
		return "", ErrMoveSameOrg
	}
	if err := RequireOrgAdmin(ctx, userID, from); err != nil {
		return "", err
	}
	if err := OrgMemberDenial(ctx, userID, toOrgID); err != nil {
		if errors.Is(err, ErrNotOrgMember) || errors.Is(err, ErrDenied) {
			return "", ErrNotTargetMember
		}
		return "", err
	}
	return from, nil
}

// Copyright (c) 2025 Reliant Labs

package authz

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// A project scheduled for deletion (core.Project.DeleteAfter set) is hidden:
// projectResource reads it as absent, so every decision about it — read,
// write, share — is ErrDenied, the call site's NotFound, exactly as for a
// project that is gone. Only deleting, restoring and purging it decide on the
// project as it was before it was scheduled (ProjectDelete).

// ProjectDeleteAuthority is how a caller may delete or restore a project.
type ProjectDeleteAuthority uint8

const (
	// ProjectDeleteNone is no authority; it is only ever returned with an
	// error.
	ProjectDeleteNone ProjectDeleteAuthority = iota
	// ProjectDeleteOwner is the project's owner, a live member of its org.
	ProjectDeleteOwner
	// ProjectDeleteOrgAdmin is an admin or owner of the project's org
	// acting on the project of a member who has left it.
	ProjectDeleteOrgAdmin
)

// ErrOwnerIsMember refuses an org admin deleting a project whose owner is
// still a live member of its org: it is the owner's work, and only they may
// delete it (control-plane docs/design/unified-shell-and-projects.md §11.4,
// D19). Only an admin of the project's org is ever told it.
var ErrOwnerIsMember = errors.New("authz: the project's owner is a live member of its organization")

// ProjectDelete decides userID deleting, or restoring, project, whether or
// not it is already scheduled for deletion (D19):
//
//   - its owner, while they may write it (a live member of its org, through
//     a credential that acts there): any credential, as every other write
//     of their own project;
//   - an admin or owner of its org, asked fresh, signed in as a person
//     (RequireOrgAdmin), once the owner is no longer a live member of it.
//     The org owns what its departed members left; admins never delete a
//     live member's work (ErrOwnerIsMember).
//
// Anyone else is ErrDenied (the call site's NotFound): an outsider, a member
// who is not an admin, a viewer of a shared project. A departed owner is
// ErrNotOrgMember, also NotFound to them. Every outage is
// ErrMembershipUnavailable: it fails closed and decides nothing.
func ProjectDelete(ctx context.Context, userID string, project *core.Project) (ProjectDeleteAuthority, error) {
	if project == nil || userID == "" {
		return ProjectDeleteNone, ErrDenied
	}
	r := liveProjectResource(project)
	if r.ownerUserID == userID {
		if err := deny(ctx, userID, r); err != nil {
			return ProjectDeleteNone, err
		}
		return ProjectDeleteOwner, nil
	}
	if r.orgID == "" {
		return ProjectDeleteNone, ErrDenied
	}
	// The caller is a live member of the project's org, through a credential
	// that acts there: nobody else learns the project exists.
	if err := OrgMemberDenial(ctx, userID, r.orgID); err != nil {
		if errors.Is(err, ErrMembershipUnavailable) {
			return ProjectDeleteNone, err
		}
		return ProjectDeleteNone, ErrDenied
	}
	switch err := RequireOrgAdmin(ctx, userID, r.orgID); {
	case err == nil:
	case errors.Is(err, ErrMembershipUnavailable), errors.Is(err, ErrPersonRequired):
		return ProjectDeleteNone, err
	default:
		// ErrNotOrgAdmin: a member, who may not learn that another
		// member's project exists.
		return ProjectDeleteNone, ErrDenied
	}
	switch err := OrgMemberDenial(ctx, r.ownerUserID, r.orgID); {
	case err == nil:
		return ProjectDeleteNone, ErrOwnerIsMember
	case errors.Is(err, ErrNotOrgMember):
		return ProjectDeleteOrgAdmin, nil
	case errors.Is(err, ErrMembershipUnavailable):
		return ProjectDeleteNone, err
	default:
		return ProjectDeleteNone, ErrDenied
	}
}

// liveProjectResource is projectResource for the project as it was before
// any scheduled deletion.
func liveProjectResource(p *core.Project) resource {
	if p == nil {
		return resource{}
	}
	return resource{present: true, ownerUserID: p.UserID, orgID: orgOf(p.OrgID), visibility: p.Visibility}
}

package sharing

import (
	"context"
	"errors"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Reasons (x-forge-error-reason) a visibility change is refused for, beyond
// NotFound (no such resource, or one the caller may not read).
const (
	// ReasonNoOrg: the resource belongs to no organization, so there is no
	// one to share it with.
	ReasonNoOrg = "no_org"
	// ReasonOwnerOnly: only the owner can share; a viewer asked to.
	ReasonOwnerOnly = "owner_only"
	// ReasonOrgAdminRequired: a viewer who is not an admin or owner of the
	// resource's org asked to make it private.
	ReasonOrgAdminRequired = "org_admin_required"
)

// visibilityFrom is the visibility a request asks for.
func visibilityFrom(v pb.ResourceVisibility) (core.Visibility, error) {
	switch v {
	case pb.ResourceVisibility_RESOURCE_VISIBILITY_ORG:
		return core.VisibilityOrg, nil
	case pb.ResourceVisibility_RESOURCE_VISIBILITY_PRIVATE:
		return core.VisibilityPrivate, nil
	default:
		return "", svcerr.InvalidArgument("visibility must be ORG or PRIVATE")
	}
}

// shareRefusal is the answer to a visibility change authz refused. kind
// names the resource ("chat", "project") for the messages.
func shareRefusal(ctx context.Context, kind string, err error) error {
	switch {
	case errors.Is(err, authz.ErrMembershipUnavailable):
		return svcerr.WithCause(svcerr.Unavailable("could not verify organization membership; try again"), err)
	case errors.Is(err, authz.ErrPersonRequired):
		// The same refusal, and reason, as every person-only action.
		return auth.RequirePerson(ctx, "change who can read this "+kind)
	case errors.Is(err, authz.ErrShareNeedsOrg):
		return svcerr.WithReason(svcerr.FailedPrecondition(
			"this "+kind+" belongs to no organization, so there is no one to share it with"), ReasonNoOrg)
	case errors.Is(err, authz.ErrOnlyOwnerShares):
		return svcerr.WithReason(svcerr.PermissionDenied("only the "+kind+"'s owner can share it"), ReasonOwnerOnly)
	case errors.Is(err, authz.ErrNotOrgAdmin):
		return svcerr.WithReason(svcerr.PermissionDenied(
			"only an admin or owner of the organization can make another member's "+kind+" private"), ReasonOrgAdminRequired)
	case errors.Is(err, authz.ErrInvalidVisibility):
		return svcerr.InvalidArgument("visibility must be ORG or PRIVATE")
	default:
		return svcerr.NotFound(kind)
	}
}

// sharedOrg is the org a shared list is of: the request's, verified, else
// the caller's default. "" lists nothing (no orgs here, or none for the
// caller).
func (s *Service) sharedOrg(ctx context.Context, userID string, requested *string) (string, error) {
	if s.deps.Orgs == nil {
		return "", nil
	}
	named, err := s.deps.Orgs.Named(ctx, userID, derefString(requested))
	if err != nil || named != "" {
		return named, err
	}
	def, err := s.deps.Orgs.DefaultOrg(ctx, userID)
	if err != nil {
		return "", svcerr.WithCause(svcerr.Unavailable("could not resolve your default organization; try again"), err)
	}
	return def, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// page bounds a shared list's limit (default and cap 100) and offset.
func page(limit, offset int32) (int, int) {
	l := int(limit)
	if l <= 0 || l > 100 {
		l = 100
	}
	o := int(offset)
	if o < 0 {
		o = 0
	}
	return l, o
}

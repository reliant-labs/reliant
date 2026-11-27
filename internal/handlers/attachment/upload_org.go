package attachment

import (
	"context"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/logging"
)

// uploadOrg is the org stamped on a new attachment (attachments.org_id): the
// org the request names, once the caller is verified a live member of it.
// The stamp means that and only that: the owner was a member of the org at
// upload time. It says nothing about which chat the file is for.
//
// It is nil when the request names none (unset or blank), the deployment has
// no orgs, or membership cannot be verified right now (the control plane is
// Unavailable): an upload never waits on the control plane, it is stored with
// no stamp, and an org-scoped read never takes it. An org the caller is not a
// member of, or one that does not exist, is NotFound: it is never stamped,
// nothing is stored, and the answer does not say which.
//
// Nothing defaults here: an org-scoped read (store.GetAttachmentForUserInOrg)
// takes only an attachment stamped with its org, and a guessed stamp would let
// one org's read reach an upload made for another.
func (s *Service) uploadOrg(ctx context.Context, userID string, requested *string) (*string, error) {
	if requested == nil || strings.TrimSpace(*requested) == "" || s.deps.Orgs == nil {
		return nil, nil
	}
	named, err := s.deps.Orgs.Named(ctx, userID, *requested)
	switch {
	case svcerr.IsPermissionDenied(err):
		return nil, svcerr.NotFound("organization")
	case svcerr.IsUnavailable(err):
		logging.WarnContext(ctx, "[attachment] could not verify the upload's organization; storing it with no org",
			"user_id", userID, "org_id", strings.TrimSpace(*requested), "error", err)
		return nil, nil
	case err != nil:
		return nil, svcerr.WrapOr(err, svcerr.Internal("could not verify the organization"))
	}
	if named == "" {
		return nil, nil
	}
	return &named, nil
}

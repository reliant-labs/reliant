package attachment

import (
	"context"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// uploadOrg is the org a new attachment records (attachments.org_id): the
// org the request names, once the caller is verified a live member of it.
// It is nil when the request names none (unset or blank) or the deployment
// has no orgs. An org the caller is not a member of, or one that does not
// exist, is NotFound: it is never recorded, and the answer does not say
// which.
//
// An org-scoped read (store.GetAttachmentForUserInOrg) takes only an
// attachment made in its org, so nothing defaults here: a guessed org would
// let one org's read reach an upload made for another.
func (s *Service) uploadOrg(ctx context.Context, userID string, requested *string) (*string, error) {
	if requested == nil || strings.TrimSpace(*requested) == "" || s.deps.Orgs == nil {
		return nil, nil
	}
	named, err := s.deps.Orgs.Named(ctx, userID, *requested)
	if svcerr.IsPermissionDenied(err) {
		return nil, svcerr.NotFound("organization")
	}
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("could not verify the organization"))
	}
	if named == "" {
		return nil, nil
	}
	return &named, nil
}

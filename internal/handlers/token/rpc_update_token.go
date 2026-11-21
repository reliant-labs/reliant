package token

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
)

// UpdateToken changes one of the caller's tokens' name and/or permissions.
//
// THE SECRET IS NEVER REISSUED. A live daemon's permissions can be widened or
// narrowed without re-registering it — which for a remote daemon would mean a
// browser login it cannot perform — and the change takes effect on the token's
// very next request, because authentication reads the row every time.
//
// SESSION ONLY, like minting. A machine credential editing scopes would be a
// token granting itself authority, which is the one thing the
// no-mint-beyond-your-scopes rule exists to prevent. Renaming is refused on the
// same path rather than carved out: one rule is easier to reason about than a
// field-by-field exception, and headless automation has no reason to rename.
func (s *Service) UpdateToken(
	ctx context.Context, req *connect.Request[reliantv1.UpdateTokenRequest],
) (*connect.Response[reliantv1.UpdateTokenResponse], error) {
	userID, isMachine, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}
	if isMachine {
		return nil, svcerr.PermissionDenied("a machine credential cannot edit a credential; sign in to change a token's permissions")
	}
	id := strings.TrimSpace(req.Msg.GetId())
	if id == "" {
		return nil, svcerr.InvalidArgument("token id is required")
	}

	var name *string
	if req.Msg.Name != nil {
		trimmed := strings.TrimSpace(req.Msg.GetName())
		if trimmed == "" {
			return nil, svcerr.InvalidArgument("name cannot be blank")
		}
		name = &trimmed
	}

	// A nil scope list leaves authority alone; an empty one is refused
	// downstream. The wrapper in the request is what keeps those apart — a
	// bare repeated field would make a rename strip every permission.
	var scopes fat.Set
	if req.Msg.Scopes != nil {
		scopes, err = fat.NewSet(req.Msg.GetScopes().GetScopes())
		if err != nil {
			// fat's refusal names the scope it does not know: the caller's to fix.
			return nil, svcerr.InvalidArgument(err.Error())
		}
	}
	if name == nil && scopes == nil {
		return nil, svcerr.InvalidArgument("nothing to update: set name, or set scopes with the permissions to grant")
	}

	info, err := s.deps.Authority.UpdateForUser(ctx, userID, id, name, scopes)
	if err != nil {
		return nil, authorityError("update", err)
	}
	logging.InfoContext(ctx, "access token updated", "user_id", userID, "token_id", id,
		"renamed", name != nil, "rescoped", scopes != nil)
	return connect.NewResponse(&reliantv1.UpdateTokenResponse{Info: tokenInfoProto(info)}), nil
}

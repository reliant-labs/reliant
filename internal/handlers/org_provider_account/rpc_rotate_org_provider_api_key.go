package org_provider_account

import (
	"context"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// RotateOrgProviderAPIKey replaces an org account's key in place, after validating the new one:
// the account keeps its id, and so its label, order, access, grants, limit state and assigned
// threads; a needs-attention mark is cleared. Never gated: rotating a leaked key must always work.
func (s *Service) RotateOrgProviderAPIKey(
	ctx context.Context,
	req *pb.RotateOrgProviderAPIKeyRequest,
) (*pb.RotateOrgProviderAPIKeyResponse, error) {
	orgID, err := requireOrgID(req.GetOrgId())
	if err != nil {
		return nil, err
	}
	apiKey := strings.TrimSpace(req.GetApiKey())
	if apiKey == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("api_key is required"))
	}
	userID, err := admin(ctx, orgID)
	if err != nil {
		return nil, err
	}
	cur, err := s.find(ctx, orgID, req.GetAccountId())
	if err != nil {
		return nil, err
	}
	if err := s.validKey(ctx, cur.Provider, apiKey); err != nil {
		return nil, err
	}
	owner := core.OrgAccountOwner(orgID)
	acct, err := s.deps.Store.RotateOrgAPIKey(ctx, orgID, cur.ID, core.OrgAPIKey{
		IdentityKey: core.APIKeyIdentityKey(owner.Key(), cur.Provider, apiKey),
		KeyHint:     core.KeyHint(apiKey),
		Secret:      apiKey,
	})
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	s.wakeOrgChats(ctx, orgID, "an org account's key was replaced")
	s.deps.Logger.InfoContext(ctx, "org provider account key rotated", "org_id", orgID, "account_id", acct.ID, "user_id", userID)
	out, err := s.one(ctx, orgID, acct)
	if err != nil {
		return nil, err
	}
	return &pb.RotateOrgProviderAPIKeyResponse{Account: out}, nil
}

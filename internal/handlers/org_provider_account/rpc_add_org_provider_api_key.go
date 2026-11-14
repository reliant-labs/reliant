package org_provider_account

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// AddOrgProviderAPIKey validates a pasted key against its provider and adds it as an account of
// the org, sealed under the org's vault tenant (core.AccountOwner picks it). A key the org already
// holds for the provider is that account: created is false and nothing changes.
func (s *Service) AddOrgProviderAPIKey(
	ctx context.Context,
	req *connect.Request[pb.AddOrgProviderAPIKeyRequest],
) (*connect.Response[pb.AddOrgProviderAPIKeyResponse], error) {
	orgID, err := requireOrgID(req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	provider := strings.TrimSpace(req.Msg.GetProvider())
	if !apiKeyProviders[provider] {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("provider " + provider + " does not take an API key"))
	}
	apiKey := strings.TrimSpace(req.Msg.GetApiKey())
	if apiKey == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("api_key is required"))
	}
	name, err := label(req.Msg.GetLabel())
	if err != nil {
		return nil, err
	}
	access := core.OrgAccessMembers
	if a := req.Msg.GetAccess(); a != pb.OrgProviderAccountAccess_ORG_PROVIDER_ACCOUNT_ACCESS_UNSPECIFIED {
		var ok bool
		if access, ok = accessToCore(a); !ok {
			return nil, svcerr.Wrap(svcerr.InvalidArgument("unknown access"))
		}
	}
	userID, err := admin(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if err := s.gated(); err != nil {
		return nil, err
	}
	if err := s.validKey(ctx, provider, apiKey); err != nil {
		return nil, err
	}

	owner := core.OrgAccountOwner(orgID)
	acct, created, err := s.deps.Store.UpsertProviderAccount(ctx, core.AccountUpsert{
		Owner:           owner,
		OrgAccess:       access,
		CreatedByUserID: userID,
		Provider:        provider,
		AuthKind:        core.AuthKindAPIKey,
		IdentityKey:     core.APIKeyIdentityKey(owner.Key(), provider, apiKey),
		KeyHint:         core.KeyHint(apiKey),
		Secrets:         core.AccountSecrets{Secret: apiKey},
	})
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	if created {
		if acct, err = s.deps.Store.PatchOwnedProviderAccount(ctx, owner, acct.ID, core.AccountPatch{Label: &name}); err != nil {
			return nil, svcerr.Wrap(err)
		}
	} else {
		activity.Skip(ctx) // the org already held this key: nothing was added
	}
	if created {
		s.wakeOrgChats(ctx, orgID, "an org account was added")
	}
	s.deps.Logger.InfoContext(ctx, "org provider account added",
		"org_id", orgID, "account_id", acct.ID, "provider", provider, "created", created, "user_id", userID)
	out, err := s.one(ctx, orgID, acct)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.AddOrgProviderAPIKeyResponse{Account: out, Created: created}), nil
}

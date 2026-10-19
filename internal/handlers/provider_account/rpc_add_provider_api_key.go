package provider_account

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// AddProviderAPIKey validates a pasted key against its provider and adds it as an account. A key
// already stored for the provider is the same account: created is false and nothing is duplicated.
func (s *Service) AddProviderAPIKey(
	ctx context.Context,
	req *connect.Request[pb.AddProviderAPIKeyRequest],
) (*connect.Response[pb.AddProviderAPIKeyResponse], error) {
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	provider := strings.TrimSpace(req.Msg.GetProvider())
	apiKey := strings.TrimSpace(req.Msg.GetApiKey())
	if !apiKeyProviders[provider] {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("provider " + provider + " does not take an API key"))
	}
	if apiKey == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("api_key is required"))
	}
	label := strings.TrimSpace(req.Msg.GetLabel())
	if len([]rune(label)) > maxLabelRunes {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("label is too long"))
	}
	if ok, msg := s.deps.ValidateKey(ctx, models.Family(provider), apiKey); !ok {
		return nil, svcerr.Wrap(svcerr.InvalidArgument(msg))
	}

	acct, created, err := s.deps.Store.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID:      userID,
		Provider:    provider,
		AuthKind:    core.AuthKindAPIKey,
		IdentityKey: core.APIKeyIdentityKey(userID, provider, apiKey),
		KeyHint:     core.KeyHint(apiKey),
		Secrets:     core.AccountSecrets{Secret: apiKey},
	})
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	if label != "" {
		if acct, err = s.deps.Store.PatchProviderAccount(ctx, userID, acct.ID, core.AccountPatch{Label: &label}); err != nil {
			return nil, wrapAccountErr(err)
		}
	}
	s.afterChange(ctx, userID, provider)

	total := 0
	if all, err := s.deps.Store.ListProviderAccounts(ctx, userID); err == nil {
		total = len(all)
	}
	client := analytics.GetClientForUser(ctx, userID)
	client.TrackProviderSettingsUpdated(analytics.ProviderSettingsUpdatedMetrics{Provider: provider, Action: "connected", AuthMethod: "api_key"})
	client.TrackAPIKeyConfigured(analytics.APIKeyConfiguredMetrics{Provider: provider, AuthMethod: "api_key", IsFirstKey: total <= 1, TotalProviders: total})

	limits, err := s.deps.Store.ListAccountLimits(ctx, userID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	return connect.NewResponse(&pb.AddProviderAPIKeyResponse{Account: proto(acct, limits, time.Now()), Created: created}), nil
}

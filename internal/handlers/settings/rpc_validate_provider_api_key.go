package settings

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/drivers/antigravity"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// ValidateProviderAPIKey validates a pasted API key for a provider. Subscription providers have
// no key to validate: they answer whether the user has a working account.
func (s *Service) ValidateProviderAPIKey(ctx context.Context, req *connect.Request[reliantv1.ValidateProviderAPIKeyRequest]) (*connect.Response[reliantv1.ValidateProviderAPIKeyResponse], error) {
	provider := models.Family(req.Msg.Provider)
	userID := auth.MustGetUserID(ctx)

	switch provider {
	case "claude", "codex", "copilot", antigravity.DriverID:
		name := providerName(string(provider))
		accounts, err := s.deps.Accounts.ListProviderAccounts(ctx, userID)
		if err != nil {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: "Failed to load " + name + " credentials"}), nil
		}
		have, usable := false, false
		for _, a := range accounts {
			if a.Provider == string(provider) {
				have = true
				usable = usable || a.NeedsReauthAt == nil
			}
		}
		switch {
		case !have:
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: name + " is not connected"}), nil
		case !usable:
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: name + " session expired. Please reconnect " + name + "."}), nil
		}
		return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: true, Message: "Connected to " + name}), nil
	}

	valid, message := s.deps.ValidateKey(ctx, provider, req.Msg.ApiKey)
	return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: valid, Message: message}), nil
}

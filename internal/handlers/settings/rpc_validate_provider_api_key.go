package settings

import (
	"context"

	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/drivers/antigravity"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// ValidateProviderAPIKey validates a pasted API key for a provider. Subscription providers have
// no key to validate: they answer whether the user has a working account.
func (s *Service) ValidateProviderAPIKey(ctx context.Context, req *reliantv1.ValidateProviderAPIKeyRequest) (*reliantv1.ValidateProviderAPIKeyResponse, error) {
	provider := models.Family(req.Provider)
	userID := auth.MustGetUserID(ctx)

	switch provider {
	case "claude", "codex", "copilot", antigravity.DriverID:
		name := providerName(string(provider))
		accounts, err := s.deps.Repo.ListProviderAccounts(ctx, userID)
		if err != nil {
			return &reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: "Failed to load " + name + " credentials"}, nil
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
			return &reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: name + " is not connected"}, nil
		case !usable:
			return &reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: name + " session expired. Please reconnect " + name + "."}, nil
		}
		return &reliantv1.ValidateProviderAPIKeyResponse{Valid: true, Message: "Connected to " + name}, nil
	}

	valid, message := s.deps.ValidateKey(ctx, provider, req.ApiKey)
	return &reliantv1.ValidateProviderAPIKeyResponse{Valid: valid, Message: message}, nil
}

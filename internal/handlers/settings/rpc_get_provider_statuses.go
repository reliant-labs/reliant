package settings

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/drivers/antigravity"
)

// providerDisplayNames name the providers GetProviderStatuses reports, in display order.
var statusProviders = []struct{ id, name string }{
	{"claude", "Claude subscription"},
	{"codex", "Codex (ChatGPT)"},
	{"copilot", "GitHub Copilot"},
	{antigravity.DriverID, "Antigravity"},
	{"openrouter", "OpenRouter"},
	{"anthropic", "Anthropic API"},
	{"openai", "OpenAI"},
	{"gemini", "Google Gemini"},
	{"reliant", "Reliant"},
}

// GetProviderStatuses reports which providers can serve, derived from the user's accounts: a
// provider is configured when at least one of its accounts is not waiting for sign-in. Masked
// keys and per-account detail live on the accounts (ProviderAccountService).
func (s *Service) GetProviderStatuses(ctx context.Context, req *connect.Request[reliantv1.GetProviderStatusesRequest]) (*connect.Response[reliantv1.GetProviderStatusesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	accounts, err := s.deps.Repo.ListProviderAccounts(ctx, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get provider statuses"))
	}
	total := map[string]int32{}
	usable := map[string]bool{}
	for _, a := range accounts {
		total[a.Provider]++
		if a.NeedsReauthAt == nil {
			usable[a.Provider] = true
		}
	}

	statuses := make([]*reliantv1.ProviderStatus, 0, len(statusProviders))
	for _, p := range statusProviders {
		statuses = append(statuses, &reliantv1.ProviderStatus{
			Provider:         p.id,
			DisplayName:      p.name,
			Configured:       usable[p.id],
			AccountCount:     total[p.id],
			OutputModalities: providerOutputModalities(p.id),
		})
	}
	return connect.NewResponse(&reliantv1.GetProviderStatusesResponse{Providers: statuses}), nil
}

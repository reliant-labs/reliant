package provider_account

import (
	"context"
	"errors"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/accountlimits"
	"github.com/reliant-labs/reliant/internal/logging"
)

// apiKeyProviders are the providers whose accounts are pasted keys. Subscription providers sign
// in through SettingsService.Complete*OAuth, and the managed Reliant key is minted by the server.
var apiKeyProviders = map[string]bool{"anthropic": true, "openai": true, "gemini": true, "openrouter": true}

func callerID(ctx context.Context) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return "", svcerr.Wrap(svcerr.Unauthenticated("user ID not found in context"))
	}
	return userID, nil
}

// wrapAccountErr maps the store's not-found and bad-order errors; another user's account is
// NotFound so ids cannot be enumerated.
func wrapAccountErr(err error) error {
	switch {
	case errors.Is(err, core.ErrProviderAccountNotFound):
		return svcerr.Wrap(svcerr.NotFound("provider account"))
	case errors.Is(err, core.ErrInvalidAccountOrder):
		return svcerr.Wrap(svcerr.InvalidArgument(err.Error()))
	}
	return svcerr.Wrap(err)
}

// proto renders one account with its limit state.
func proto(a core.ProviderAccount, limits map[string]core.LimitState, now time.Time) *pb.ProviderAccount {
	var l *core.LimitState
	if v, ok := limits[a.ID]; ok {
		l = &v
	}
	return accountlimits.ProtoAccount(a, l, now)
}

// protoAll renders accounts, optionally only those of one provider.
func protoAll(accounts []core.ProviderAccount, limits map[string]core.LimitState, provider string, now time.Time) []*pb.ProviderAccount {
	out := make([]*pb.ProviderAccount, 0, len(accounts))
	for _, a := range accounts {
		if provider == "" || a.Provider == provider {
			out = append(out, proto(a, limits, now))
		}
	}
	return out
}

// find returns the user's account with the given id.
func (s *Service) find(ctx context.Context, userID, accountID string) (core.ProviderAccount, error) {
	accounts, err := s.deps.Store.ListProviderAccounts(ctx, userID)
	if err != nil {
		return core.ProviderAccount{}, svcerr.Wrap(err)
	}
	for _, a := range accounts {
		if a.ID == accountID {
			return a, nil
		}
	}
	return core.ProviderAccount{}, wrapAccountErr(core.ErrProviderAccountNotFound)
}

// afterChange tells the user's other windows the provider set moved. A failed notification never
// fails the change: the account store is already correct.
func (s *Service) afterChange(ctx context.Context, userID, provider string) {
	if err := s.deps.Store.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.Warn("Failed to emit config_health refetch after provider account change", "provider", provider, "error", err)
	}
}

func notFoundOr(err error, msg string) error {
	if errors.Is(err, core.ErrChatNotFound) {
		return svcerr.Wrap(svcerr.NotFound(msg))
	}
	return svcerr.Wrap(err)
}

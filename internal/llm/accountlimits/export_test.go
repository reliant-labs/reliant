// Copyright (c) 2025 Reliant Labs

package accountlimits

import (
	"context"
	"errors"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// ErrTokenExpired is what StoredTokens answers for an expired access token: it
// cannot refresh one.
var ErrTokenExpired = errors.New("accountlimits: stored access token has expired")

// StoredTokens is a test TokenSource that reads the stored access token and
// never refreshes it. Production uses drivers.RefreshingTokens, which refreshes
// through the per-account coordinator the LLM drivers share.
type StoredTokens struct {
	Store interface {
		GetProviderAccountWithSecrets(ctx context.Context, userID, accountID string) (*core.AccountWithSecrets, error)
	}
	Now func() time.Time
}

// AccessToken returns the stored access token while it is still valid.
func (s StoredTokens) AccessToken(ctx context.Context, userID, accountID string) (string, error) {
	acct, err := s.Store.GetProviderAccountWithSecrets(ctx, userID, accountID)
	if err != nil {
		return "", err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	if !acct.ExpiresAt.IsZero() && !acct.ExpiresAt.After(now().Add(30*time.Second)) {
		return "", ErrTokenExpired
	}
	return acct.Secrets.Secret, nil
}

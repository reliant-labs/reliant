// Copyright (c) 2025 Reliant Labs
package ghaccess

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/vault"
)

// cpTokens is what DelegatedTokens needs from control-plane.
// *gitcredentialclient.Client satisfies it.
type cpTokens interface {
	UserAccessToken(ctx context.Context, externalUserID, provider string) (gitcredentialclient.Token, error)
}

// DelegatedTokens reads a user's GitHub App user token from control-plane,
// the GitHub token authority in hosted deployments. The user id is the one
// reliant holds (the IdP subject), which control-plane translates.
type DelegatedTokens struct {
	cp cpTokens
}

// NewDelegatedTokens builds the hosted token source.
func NewDelegatedTokens(cp cpTokens) *DelegatedTokens { return &DelegatedTokens{cp: cp} }

// Token implements TokenSource.
func (d *DelegatedTokens) Token(ctx context.Context, userID string) (string, error) {
	tok, err := d.cp.UserAccessToken(ctx, userID, IntegrationID)
	switch {
	case errors.Is(err, gitcredentialclient.ErrNotConnected):
		return "", fmt.Errorf("%w: connect GitHub in Settings", ErrNotConnected)
	case errors.Is(err, gitcredentialclient.ErrNeedsReconnect):
		return "", fmt.Errorf("%w: reconnect GitHub in Settings", ErrNeedsReconnect)
	case err != nil:
		// Transient: control-plane or GitHub unreachable during renewal.
		return "", fmt.Errorf("ghaccess: control-plane token: %w", err)
	}
	var out string
	_ = tok.Use(func(access string) error { out = access; return nil })
	if out == "" {
		return "", fmt.Errorf("%w: control-plane returned no token", ErrNotConnected)
	}
	return out, nil
}

// connectionStore is what SavedTokens reads. connections' store satisfies it.
type connectionStore interface {
	DefaultConnection(ctx context.Context, userID, integrationID string) (*core.Connection, error)
}

// connectionTokens opens a connection's current token.
// *connections.TokenSource satisfies it.
type connectionTokens interface {
	Token(ctx context.Context, userID, connectionID string) (vault.Secret, error)
}

// SavedTokens reads a self-hosted user's token from their default saved
// GitHub connection. Only an oauth2 connection (an App user token) works:
// GitHub answers /user/installations only for App user tokens, so a pasted
// personal access token is refused up front rather than failing at GitHub.
type SavedTokens struct {
	store  connectionStore
	tokens connectionTokens
}

// NewSavedTokens builds the self-hosted token source.
func NewSavedTokens(store connectionStore, tokens connectionTokens) *SavedTokens {
	return &SavedTokens{store: store, tokens: tokens}
}

// Token implements TokenSource.
func (s *SavedTokens) Token(ctx context.Context, userID string) (string, error) {
	conn, err := s.store.DefaultConnection(ctx, userID, IntegrationID)
	if err != nil || conn == nil {
		if err == nil || errors.Is(err, core.ErrConnectionNotFound) || connections.CodeOf(err) == connections.CodeNotFound {
			return "", fmt.Errorf("%w: connect GitHub in Settings", ErrNotConnected)
		}
		return "", fmt.Errorf("ghaccess: load GitHub connection: %w", err)
	}
	if conn.AuthKind != core.ConnectionAuthOAuth2 {
		return "", fmt.Errorf("%w: GitHub triggers need a GitHub App connection (sign in with GitHub), not a %s", ErrUnsupportedToken, conn.AuthKind)
	}
	secret, err := s.tokens.Token(ctx, userID, conn.ID)
	if err != nil {
		switch connections.CodeOf(err) {
		case connections.CodeNeedsReauth:
			return "", fmt.Errorf("%w: reconnect GitHub in Settings", ErrNeedsReconnect)
		case connections.CodeFailedPrecondition, connections.CodeNotFound:
			return "", fmt.Errorf("%w: %v", ErrNotConnected, err)
		}
		return "", fmt.Errorf("ghaccess: GitHub connection token: %w", err)
	}
	var out string
	_ = secret.Use(func(b []byte) error { out = string(b); return nil })
	return out, nil
}

var (
	_ TokenSource = (*DelegatedTokens)(nil)
	_ TokenSource = (*SavedTokens)(nil)
)

// Copyright (c) 2025 Reliant Labs

package connections

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/oauth2"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/vault"
	"golang.org/x/sync/singleflight"
)

// refreshWindow is how close to expiry a token is treated as expired.
const refreshWindow = 60 * time.Second

// tokenStore is what the token source needs from persistence.
type tokenStore interface {
	GetConnection(ctx context.Context, userID, id string) (*core.Connection, error)
	GetSecrets(ctx context.Context, userID, id string) (map[string]core.ConnectionSecret, error)
	WithSecretsLock(ctx context.Context, userID, id string, fn func(core.SecretsTx) error) error
	AppendConnectionEvent(ctx context.Context, ev core.ConnectionEvent) error
}

type cacheKey struct {
	id         string
	generation int64
}

type cachedToken struct {
	secret  vault.Secret
	expires time.Time // zero: non-expiring
}

// TokenSource hands out the current access token for a connection, refreshing
// it when it is about to expire (§4.4).
//
// Two layers keep refresh safe. singleflight collapses concurrent callers in
// this process onto one provider call. A row lock plus the generation counter
// collapses callers across worker processes: providers that rotate refresh
// tokens (GitHub Apps among them) treat a replayed token as theft, so two
// replicas refreshing at once would otherwise knock the connection into
// needs_reauth.
type TokenSource struct {
	store     tokenStore
	vault     Sealer
	providers *Registry
	doer      HTTPDoer
	now       func() time.Time

	flight singleflight.Group

	mu    sync.Mutex
	cache map[cacheKey]cachedToken
}

// NewTokenSource builds a token source. doer may be nil (a bounded default is used).
func NewTokenSource(store tokenStore, v Sealer, providers *Registry, doer HTTPDoer) *TokenSource {
	if doer == nil {
		doer = NewHTTPClient()
	}
	return &TokenSource{store: store, vault: v, providers: providers, doer: doer, now: time.Now, cache: map[cacheKey]cachedToken{}}
}

// Token returns the access token (or, for api_key and basic connections, the
// stored credential) for a connection the user owns.
func (s *TokenSource) Token(ctx context.Context, userID, connectionID string) (vault.Secret, error) {
	conn, err := s.store.GetConnection(ctx, userID, connectionID)
	if err != nil {
		return vault.Secret{}, mapStoreErr(err)
	}
	switch conn.Status {
	case core.ConnectionStatusNeedsReauth:
		return vault.Secret{}, newError(CodeNeedsReauth, "connection %q needs to be reconnected", conn.Name)
	case core.ConnectionStatusActive:
	default:
		return vault.Secret{}, newError(CodeFailedPrecondition, "connection %q is not active", conn.Name)
	}

	if conn.AuthKind == core.ConnectionAuthOAuth2 || conn.AuthKind == core.ConnectionAuthGitHubAppUser {
		return s.oauthToken(ctx, conn)
	}
	secrets, err := s.store.GetSecrets(ctx, userID, connectionID)
	if err != nil {
		return vault.Secret{}, mapStoreErr(err)
	}
	return s.openStatic(ctx, conn, secrets)
}

func (s *TokenSource) openStatic(ctx context.Context, conn *core.Connection, secrets map[string]core.ConnectionSecret) (vault.Secret, error) {
	switch conn.AuthKind {
	case core.ConnectionAuthAPIKey:
		return s.open(ctx, conn, secrets[core.SecretFieldAPIKey])
	case core.ConnectionAuthBasic:
		user, err := s.open(ctx, conn, secrets[core.SecretFieldAPIKey]) // username is stored in the api_key field
		if err != nil {
			return vault.Secret{}, err
		}
		pass, err := s.open(ctx, conn, secrets[core.SecretFieldPassword])
		if err != nil {
			return vault.Secret{}, err
		}
		var joined []byte
		_ = user.Use(func(u []byte) error {
			return pass.Use(func(p []byte) error {
				joined = append(append(append([]byte{}, u...), 0), p...)
				return nil
			})
		})
		defer clear(joined)
		return vault.NewSecret(joined), nil
	default:
		return vault.Secret{}, newError(CodeFailedPrecondition, "auth kind %q holds no static credential", conn.AuthKind)
	}
}

func (s *TokenSource) open(ctx context.Context, conn *core.Connection, sec core.ConnectionSecret) (vault.Secret, error) {
	if len(sec.Ciphertext) == 0 {
		return vault.Secret{}, newError(CodeInternal, "connection %q has no stored credential", conn.Name)
	}
	secret, err := s.vault.OpenSecret(ctx, vault.UserTenant(conn.UserID), sec.Ciphertext, SecretAAD(conn.ID, sec.Field))
	if err != nil {
		return vault.Secret{}, &Error{Code: CodeInternal, Message: "stored credential could not be opened", Err: err}
	}
	return secret, nil
}

func (s *TokenSource) oauthToken(ctx context.Context, conn *core.Connection) (vault.Secret, error) {
	secrets, err := s.store.GetSecrets(ctx, conn.UserID, conn.ID)
	if err != nil {
		return vault.Secret{}, mapStoreErr(err)
	}
	access, ok := secrets[core.SecretFieldAccessToken]
	if !ok {
		return vault.Secret{}, newError(CodeInternal, "connection %q has no access token", conn.Name)
	}
	if tok, ok := s.cached(conn.ID, access.Generation); ok {
		return tok, nil
	}
	if !s.expiring(conn.AccessExpiresAt) {
		tok, err := s.open(ctx, conn, access)
		if err != nil {
			return vault.Secret{}, err
		}
		s.store2cache(conn.ID, access.Generation, tok, conn.AccessExpiresAt)
		return tok, nil
	}
	return s.refresh(ctx, conn, access.Generation)
}

func (s *TokenSource) expiring(exp *time.Time) bool {
	return exp != nil && !s.now().Add(refreshWindow).Before(*exp)
}

func (s *TokenSource) cached(id string, gen int64) (vault.Secret, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cache[cacheKey{id, gen}]
	if !ok {
		return vault.Secret{}, false
	}
	if !c.expires.IsZero() && !s.now().Add(refreshWindow).Before(c.expires) {
		delete(s.cache, cacheKey{id, gen})
		return vault.Secret{}, false
	}
	return c.secret, true
}

func (s *TokenSource) store2cache(id string, gen int64, tok vault.Secret, exp *time.Time) {
	c := cachedToken{secret: tok}
	if exp != nil {
		c.expires = *exp
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.cache {
		if k.id == id && k.generation != gen {
			delete(s.cache, k)
		}
	}
	s.cache[cacheKey{id, gen}] = c
}

// Forget drops cached tokens for a connection (it was deleted or changed).
func (s *TokenSource) Forget(connectionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.cache {
		if k.id == connectionID {
			delete(s.cache, k)
		}
	}
}

func (s *TokenSource) refresh(ctx context.Context, conn *core.Connection, seenGeneration int64) (vault.Secret, error) {
	key := fmt.Sprintf("%s@%d", conn.ID, seenGeneration)
	v, err, _ := s.flight.Do(key, func() (any, error) {
		// The shared call must not die with the first caller's context, or one
		// cancelled caller fails every waiter.
		return s.refreshLocked(context.WithoutCancel(ctx), conn, seenGeneration)
	})
	if err != nil {
		return vault.Secret{}, err
	}
	return v.(vault.Secret), nil
}

func (s *TokenSource) refreshLocked(ctx context.Context, conn *core.Connection, seenGeneration int64) (vault.Secret, error) {
	provider, ok := s.providers.Get(conn.IntegrationID)
	if !ok || !provider.Available() {
		return vault.Secret{}, newError(CodeFailedPrecondition, "integration %q is not configured on this deployment", conn.IntegrationID)
	}
	var result vault.Secret
	var permanent error
	err := s.store.WithSecretsLock(ctx, conn.UserID, conn.ID, func(tx core.SecretsTx) error {
		secrets := tx.Secrets()
		access := secrets[core.SecretFieldAccessToken]
		// Another worker may have refreshed while we waited for the lock: use
		// its token rather than spending (and possibly invalidating) ours.
		if access.Generation != seenGeneration {
			fresh := tx.Connection()
			if !s.expiring(fresh.AccessExpiresAt) {
				tok, err := s.open(ctx, fresh, access)
				if err != nil {
					return err
				}
				result = tok
				s.store2cache(fresh.ID, access.Generation, tok, fresh.AccessExpiresAt)
				return nil
			}
		}
		refreshSecret, ok := secrets[core.SecretFieldRefreshToken]
		if !ok {
			// No refresh token: nothing can renew this. Treat as reauth.
			permanent = s.markNeedsReauth(ctx, tx, conn, "no_refresh_token")
			return nil
		}
		rt, err := s.vault.Open(ctx, vault.UserTenant(conn.UserID), refreshSecret.Ciphertext, SecretAAD(conn.ID, core.SecretFieldRefreshToken))
		if err != nil {
			return &Error{Code: CodeInternal, Message: "stored refresh token could not be opened", Err: err}
		}
		token, err := oauth2.NewRefreshToken(string(rt))
		clear(rt)
		if err != nil {
			permanent = s.markNeedsReauth(ctx, tx, conn, "no_refresh_token")
			return nil
		}
		var clientSecret string
		_ = provider.ClientSecret.Use(func(b []byte) error { clientSecret = string(b); return nil })
		ex := &oauth2.Exchanger{Client: s.doer, UserAgent: "reliant-connections"}
		refreshed, err := ex.Refresh(ctx, oauth2.RefreshRequest{
			Endpoint:     provider.TokenURL,
			ClientID:     provider.ClientID,
			ClientSecret: clientSecret,
			RefreshToken: token,
		})
		if err != nil {
			var oerr *oauth2.Error
			if errors.As(err, &oerr) && isPermanentRefreshFailure(oerr) {
				permanent = s.markNeedsReauth(ctx, tx, conn, "invalid_grant")
				return nil
			}
			// Transient (5xx, timeout, network): leave the status alone and let
			// the caller retry. Only a class is logged: the provider's body
			// could echo a credential.
			slog.Warn("connection token refresh failed", "connection_id", conn.ID, "class", refreshFailureClass(err))
			_ = tx.AppendEvent(ctx, workerEvent(core.ConnectionEventRefreshFailed, conn.UserID))
			return newError(CodeUnavailable, "token refresh for connection %q is temporarily unavailable", conn.Name)
		}

		nextAccess, err := s.vault.Seal(ctx, vault.UserTenant(conn.UserID), []byte(refreshed.Token.AccessToken), SecretAAD(conn.ID, core.SecretFieldAccessToken))
		if err != nil {
			return err
		}
		nextAccessID, err := vault.KeyIDOf(nextAccess)
		if err != nil {
			return err
		}
		updates := []core.ConnectionSecret{{Field: core.SecretFieldAccessToken, VaultKeyID: nextAccessID, Ciphertext: nextAccess}}
		// Refreshed.RefreshToken is always the token to present next: the
		// rotated one when the provider sent one, else the one just used.
		if refreshed.Rotated {
			nextRefresh, err := s.vault.Seal(ctx, vault.UserTenant(conn.UserID), []byte(refreshed.RefreshToken.Secret()), SecretAAD(conn.ID, core.SecretFieldRefreshToken))
			if err != nil {
				return err
			}
			id, err := vault.KeyIDOf(nextRefresh)
			if err != nil {
				return err
			}
			updates = append(updates, core.ConnectionSecret{Field: core.SecretFieldRefreshToken, VaultKeyID: id, Ciphertext: nextRefresh})
		}
		var expires *time.Time
		if exp := refreshed.Token.Expiry(s.now()); !exp.IsZero() {
			expires = &exp
		}
		if err := tx.Replace(ctx, access.Generation, updates, expires); err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, workerEvent(core.ConnectionEventRefreshed, conn.UserID)); err != nil {
			return err
		}
		result = vault.NewSecret([]byte(refreshed.Token.AccessToken))
		s.store2cache(conn.ID, access.Generation+1, result, expires)
		return nil
	})
	if err != nil {
		return vault.Secret{}, err
	}
	if permanent != nil {
		return vault.Secret{}, permanent
	}
	return result, nil
}

// markNeedsReauth records the permanent failure inside the refresh transaction
// and returns the typed error. status_reason is a class, never the provider's body.
func (s *TokenSource) markNeedsReauth(ctx context.Context, tx core.SecretsTx, conn *core.Connection, reason string) error {
	if err := tx.MarkStatus(ctx, core.ConnectionStatusNeedsReauth, reason); err != nil {
		return err
	}
	if err := tx.AppendEvent(ctx, workerEvent(core.ConnectionEventNeedsReauth, conn.UserID)); err != nil {
		return err
	}
	return newError(CodeNeedsReauth, "connection %q needs to be reconnected", conn.Name)
}

// isPermanentRefreshFailure: RFC 6749 §5.2 invalid_grant, plus GitHub's
// bad_refresh_token, mean the grant is dead. Anything else (invalid_client, a
// 5xx) is a deployment or transient problem and must not condemn the user's
// connection.
func isPermanentRefreshFailure(e *oauth2.Error) bool {
	return e.Code == oauth2.ErrCodeInvalidGrant || e.Code == "bad_refresh_token"
}

func refreshFailureClass(err error) string {
	var oerr *oauth2.Error
	if errors.As(err, &oerr) {
		if oerr.Code != "" {
			return oerr.Code
		}
		return fmt.Sprintf("http_%d", oerr.StatusCode)
	}
	return "network"
}

func mapStoreErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, core.ErrConnectionNotFound):
		return newError(CodeNotFound, "connection not found")
	case errors.Is(err, core.ErrConnectionNameTaken):
		return newError(CodeAlreadyExists, "a connection with that name already exists")
	case errors.Is(err, core.ErrGenerationConflict):
		return newError(CodeUnavailable, "connection was updated concurrently; retry")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		var te *Error
		if errors.As(err, &te) {
			return err
		}
		return &Error{Code: CodeInternal, Message: "connection store error", Err: err}
	}
}

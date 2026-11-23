// Copyright (c) 2025 Reliant Labs

// Package automationtokens is the only path to the api_keys table: the
// delegated daemon:resume token an unattended trigger fire uses to wake the one
// daemon its trigger names, one per (user, daemon), sealed under the user's
// vault tenant.
//
// A handler or a worker never reads the table or opens a sealed value: it asks
// this package, which seals on write and opens on read. The sealed column is the
// only source of truth, so a package built without a vault refuses to run.
//
// Built by hand, by internal/app's NewAutomationTokens provider (providers.go):
// never a node compose.go constructs from Deps.
//
// (exp)
package automationtokens

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/vault"
)

// Deps holds dependencies for the automationtokens package.
type Deps struct {
	// DB is the forge ORM handle. A call made with a ctx carrying a transaction
	// joins it.
	DB orm.Context
	// Sealer seals and opens the stored tokens (the credential vault).
	Sealer Sealer
}

//forge:contract
type service struct {
	deps Deps
	now  func() time.Time
}

// New creates the automation-token store. It fails without a vault: a token is
// stored only sealed.
//
// forge:constructor
func New(deps Deps) (Service, error) {
	if deps.DB == nil {
		return nil, errors.New("automationtokens: Deps.DB is required")
	}
	if deps.Sealer == nil {
		return nil, errAPIKeyVaultDisabled
	}
	return &service{deps: deps, now: func() time.Time { return time.Now().UTC() }}, nil
}

var errAPIKeyVaultDisabled = errors.New("api keys require the credential vault; none is configured")

var errEmptyKey = errors.New("user_id and daemon_id cannot be empty")

func provider(daemonID string) string { return core.AutomationProviderPrefix + daemonID }

// aad binds a ciphertext to its user and provider, so a value copied to another
// row or another user's row does not open.
func aad(userID, provider string) []byte {
	return []byte("api_keys\x00" + userID + "\x00" + provider + "\x00api_key")
}

// GetAutomationToken returns the user's stored token for exactly daemonID. A user
// with none gets an error that satisfies errors.Is(err, sql.ErrNoRows) and
// svcerr.IsNotFound; a row that is not sealed is an error, never a downgrade.
func (s *service) GetAutomationToken(ctx context.Context, userID, daemonID string) (string, error) {
	if userID == "" || daemonID == "" {
		return "", errEmptyKey
	}
	p := provider(daemonID)
	sealed, err := store.GetAPIKeySealed(ctx, s.deps.DB, userID, p)
	if err != nil {
		return "", err
	}
	if len(sealed) == 0 {
		return "", fmt.Errorf("api key for provider %q has no sealed value", p)
	}
	pt, err := s.deps.Sealer.Open(ctx, vault.UserTenant(userID), sealed, aad(userID, p))
	if err != nil {
		return "", fmt.Errorf("opening sealed api key for provider %q: %w", p, err)
	}
	defer clear(pt)
	return string(pt), nil
}

// SetAutomationToken seals token under the user's tenant and stores it, replacing
// the one held for the daemon.
func (s *service) SetAutomationToken(ctx context.Context, userID, daemonID, token string) error {
	if userID == "" || daemonID == "" {
		return errEmptyKey
	}
	p := provider(daemonID)
	sealed, err := s.deps.Sealer.Seal(ctx, vault.UserTenant(userID), []byte(token), aad(userID, p))
	if err != nil {
		return fmt.Errorf("sealing automation token for daemon %q: %w", daemonID, err)
	}
	return store.UpsertSealedAPIKey(ctx, s.deps.DB, uuid.New().String(), userID, p, sealed, s.now())
}

// DeleteAutomationToken forgets the user's token for daemonID. A missing one is not
// an error.
func (s *service) DeleteAutomationToken(ctx context.Context, userID, daemonID string) error {
	if userID == "" || daemonID == "" {
		return errEmptyKey
	}
	return store.DeleteAPIKey(ctx, s.deps.DB, userID, provider(daemonID))
}

// PurgeUser deletes every token the user holds and returns how many, for the account purge. It
// joins the transaction ctx carries, so the purge stays all-or-nothing.
func (s *service) PurgeUser(ctx context.Context, userID string) (int64, error) {
	if userID == "" {
		return 0, errEmptyKey
	}
	return store.DeleteUserAPIKeys(ctx, s.deps.DB, userID)
}

// UserHasCredentials reports whether the user holds any token.
func (s *service) UserHasCredentials(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, errEmptyKey
	}
	return store.UserHasAPIKey(ctx, s.deps.DB, userID)
}

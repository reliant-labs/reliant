package db

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

// apiKeyStore seals and opens the api_keys table's automation tokens. Its
// statements are internal/store's (api_key_repo_ext.go); what stays here is
// the vault work around them.
type apiKeyStore struct {
	db     orm.Context
	sealer CredentialSealer
}

func newAPIKeyStore(db orm.Context) *apiKeyStore {
	return &apiKeyStore{db: db}
}

// CredentialSealer is the slice of the credential vault the Repository needs
// for api_keys, provider_accounts and the legacy provider sign-in token
// tables. Declared here, where it is consumed.
type CredentialSealer interface {
	Seal(ctx context.Context, tenant vault.Tenant, plaintext, aad []byte) ([]byte, error)
	Open(ctx context.Context, tenant vault.Tenant, ciphertext, aad []byte) ([]byte, error)
}

// SetSealer enables the credential vault. Sealed columns are the only source
// of truth, so without one the store refuses to read or write provider API
// keys and provider sign-in tokens.
func (s *apiKeyStore) SetSealer(sealer CredentialSealer) { s.sealer = sealer }

var errAPIKeyVaultDisabled = errors.New("api keys require the credential vault; none is configured")

func apiKeyAAD(userID, provider string) []byte {
	return []byte("api_keys\x00" + userID + "\x00" + provider + "\x00api_key")
}

// openAPIKey decrypts the sealed column. There is deliberately no plaintext
// fallback: a row without a sealed value is an error, not a downgrade.
func (s *apiKeyStore) openAPIKey(ctx context.Context, userID, provider string, sealed []byte) (string, error) {
	if s.sealer == nil {
		return "", errAPIKeyVaultDisabled
	}
	if len(sealed) == 0 {
		return "", fmt.Errorf("api key for provider %q has no sealed value", provider)
	}
	pt, err := s.sealer.Open(ctx, vault.UserTenant(userID), sealed, apiKeyAAD(userID, provider))
	if err != nil {
		return "", fmt.Errorf("opening sealed api key for provider %q: %w", provider, err)
	}
	return string(pt), nil
}

// The api_keys table holds only automation credentials now: one daemon:resume token per
// (user, daemon), under the reserved provider name AutomationProviderPrefix+daemonID. LLM
// credentials live in provider_accounts.

func automationProvider(daemonID string) string { return core.AutomationProviderPrefix + daemonID }

func (s *apiKeyStore) GetAutomationToken(ctx context.Context, userID, daemonID string) (string, error) {
	provider := automationProvider(daemonID)
	sealed, err := store.GetAPIKeySealed(ctx, s.db, userID, provider)
	if err != nil {
		return "", err
	}
	return s.openAPIKey(ctx, userID, provider, sealed)
}

// SetAutomationToken writes only the sealed column. The plaintext column is kept (always empty)
// until a later migration drops it.
func (s *apiKeyStore) SetAutomationToken(ctx context.Context, userID, daemonID, token string) error {
	if s.sealer == nil {
		return errAPIKeyVaultDisabled
	}
	provider := automationProvider(daemonID)
	sealed, err := s.sealer.Seal(ctx, vault.UserTenant(userID), []byte(token), apiKeyAAD(userID, provider))
	if err != nil {
		return fmt.Errorf("sealing automation token for daemon %q: %w", daemonID, err)
	}
	return store.UpsertSealedAPIKey(ctx, s.db, uuid.New().String(), userID, provider, sealed, time.Now().UTC())
}

func (s *apiKeyStore) DeleteAutomationToken(ctx context.Context, userID, daemonID string) error {
	return store.DeleteAPIKey(ctx, s.db, userID, automationProvider(daemonID))
}

// CountUnsealedAPIKeys is count(api_key_sealed IS NULL): the number operators
// watch reach zero before the contract step.
func (s *apiKeyStore) CountUnsealedAPIKeys(ctx context.Context) (int64, error) {
	return store.CountUnsealedAPIKeys(ctx, s.db)
}

// ValidateAPIKeysSealedConstraint validates the NOT VALID CHECK the contract
// migration added. Call only after the backfill has sealed every row; it fails
// if any NULL remains. Safe to run repeatedly and from several processes.
func (s *apiKeyStore) ValidateAPIKeysSealedConstraint(ctx context.Context) error {
	return store.ValidateAPIKeysSealedConstraint(ctx, s.db)
}

// BackfillAPIKeys seals rows whose api_key_sealed IS NULL, batch rows at a
// time, until none remain. Idempotent. The UPDATE re-checks the plaintext it
// sealed so a concurrent SetAutomationToken is never overwritten with stale
// data. Returns the number of rows sealed.
func (s *apiKeyStore) BackfillAPIKeys(ctx context.Context, batch int) (int, error) {
	if s.sealer == nil {
		return 0, errors.New("api key backfill requires a vault")
	}
	if batch <= 0 {
		batch = 200
	}
	total := 0
	for {
		work, err := store.ListUnsealedAPIKeys(ctx, s.db, batch)
		if err != nil {
			return total, err
		}
		if len(work) == 0 {
			return total, nil
		}
		progressed := 0
		for _, p := range work {
			sealed, err := s.sealer.Seal(ctx, vault.UserTenant(p.UserID), []byte(p.APIKey), apiKeyAAD(p.UserID, p.Provider))
			if err != nil {
				return total, fmt.Errorf("sealing api key %s: %w", p.ID, err)
			}
			ok, err := store.SealUnsealedAPIKey(ctx, s.db, p.ID, p.APIKey, sealed)
			if err != nil {
				return total, err
			}
			if ok {
				progressed++
			}
		}
		total += progressed
		if progressed == 0 {
			// Every candidate raced with a writer; they will be re-selected
			// sealed or not on the next call. Stop rather than spin.
			return total, nil
		}
	}
}

// oauthTokenAAD is the AAD the legacy provider sign-in tables (claude_auth_tokens, ...) sealed
// under: table, user and column. Only the one-time legacy copy still opens such values.
func oauthTokenAAD(table, userID, column string) []byte {
	return []byte(table + "\x00" + userID + "\x00" + column)
}

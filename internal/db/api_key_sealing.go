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
// for api_keys and provider_accounts. Declared here, where it is consumed.
type CredentialSealer interface {
	Seal(ctx context.Context, tenant vault.Tenant, plaintext, aad []byte) ([]byte, error)
	Open(ctx context.Context, tenant vault.Tenant, ciphertext, aad []byte) ([]byte, error)
}

// SetSealer enables the credential vault. Sealed columns are the only source
// of truth, so without one the store refuses to read or write automation
// tokens.
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

// SetAutomationToken seals the token and writes it.
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

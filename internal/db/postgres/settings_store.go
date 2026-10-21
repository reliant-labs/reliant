package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db/core"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
	"github.com/reliant-labs/reliant/internal/vault"
)

type settingStore struct {
	q      pgdb.Querier
	db     pgdb.DBTX
	bind   func(string) string
	sealer CredentialSealer
}

// NewSettingStore creates the Postgres settings store implementation.
func NewSettingStore(q pgdb.Querier, db pgdb.DBTX, bind func(string) string) core.SettingStore {
	return &settingStore{q: q, db: db, bind: bind}
}

// CredentialSealer is the slice of the credential vault this store needs for
// api_keys and the provider sign-in token tables. Declared here, where it is
// consumed.
type CredentialSealer interface {
	Seal(ctx context.Context, tenant vault.Tenant, plaintext, aad []byte) ([]byte, error)
	Open(ctx context.Context, tenant vault.Tenant, ciphertext, aad []byte) ([]byte, error)
}

// SetSealer enables the credential vault. Sealed columns are the only source
// of truth, so without one the store refuses to read or write provider API
// keys and provider sign-in tokens.
func (s *settingStore) SetSealer(sealer CredentialSealer) { s.sealer = sealer }

var errAPIKeyVaultDisabled = errors.New("api keys require the credential vault; none is configured")

func apiKeyAAD(userID, provider string) []byte {
	return []byte("api_keys\x00" + userID + "\x00" + provider + "\x00api_key")
}

// openAPIKey decrypts the sealed column. There is deliberately no plaintext
// fallback: a row without a sealed value is an error, not a downgrade.
func (s *settingStore) openAPIKey(ctx context.Context, userID, provider string, sealed []byte) (string, error) {
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

func (s *settingStore) GetAutomationToken(ctx context.Context, userID, daemonID string) (string, error) {
	provider := automationProvider(daemonID)
	var sealed []byte
	query := s.bind("SELECT api_key_sealed FROM api_keys WHERE user_id = ? AND provider = ?")
	if err := s.db.QueryRowContext(ctx, query, userID, provider).Scan(&sealed); err != nil {
		return "", err
	}
	return s.openAPIKey(ctx, userID, provider, sealed)
}

// SetAutomationToken writes only the sealed column. The plaintext column is kept (always ”)
// until a later migration drops it.
func (s *settingStore) SetAutomationToken(ctx context.Context, userID, daemonID, token string) error {
	if s.sealer == nil {
		return errAPIKeyVaultDisabled
	}
	provider := automationProvider(daemonID)
	sealed, err := s.sealer.Seal(ctx, vault.UserTenant(userID), []byte(token), apiKeyAAD(userID, provider))
	if err != nil {
		return fmt.Errorf("sealing automation token for daemon %q: %w", daemonID, err)
	}
	now := time.Now().UTC()
	id := uuid.New().String()
	query := s.bind(`INSERT INTO api_keys (id, user_id, provider, api_key, api_key_sealed, created_at, updated_at)
		 VALUES (?, ?, ?, '', ?, ?, ?)
		 ON CONFLICT(user_id, provider) DO UPDATE SET
		   api_key = '',
		   api_key_sealed = excluded.api_key_sealed,
		   updated_at = excluded.updated_at`)
	_, err = s.db.ExecContext(ctx, query, id, userID, provider, sealed, now, now)
	return err
}

func (s *settingStore) DeleteAutomationToken(ctx context.Context, userID, daemonID string) error {
	query := s.bind("DELETE FROM api_keys WHERE user_id = ? AND provider = ?")
	_, err := s.db.ExecContext(ctx, query, userID, automationProvider(daemonID))
	return err
}

// CountUnsealedAPIKeys is count(api_key_sealed IS NULL): the number operators
// watch reach zero before the contract step.
func (s *settingStore) CountUnsealedAPIKeys(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM api_keys WHERE api_key_sealed IS NULL").Scan(&n)
	return n, err
}

// ValidateAPIKeysSealedConstraint validates the NOT VALID CHECK the contract
// migration added. Call only after the backfill has sealed every row; it fails
// if any NULL remains. Safe to run repeatedly and from several processes.
func (s *settingStore) ValidateAPIKeysSealedConstraint(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "ALTER TABLE api_keys VALIDATE CONSTRAINT api_keys_sealed_not_null")
	return err
}

// BackfillAPIKeys seals rows whose api_key_sealed IS NULL, batch rows at a
// time, until none remain. Idempotent. The UPDATE re-checks the plaintext it
// sealed so a concurrent SetAutomationToken is never overwritten with stale
// data. Returns the number of rows sealed.
func (s *settingStore) BackfillAPIKeys(ctx context.Context, batch int) (int, error) {
	if s.sealer == nil {
		return 0, errors.New("api key backfill requires a vault")
	}
	if batch <= 0 {
		batch = 200
	}
	total := 0
	for {
		rows, err := s.db.QueryContext(ctx, s.bind(
			"SELECT id, user_id, provider, api_key FROM api_keys WHERE api_key_sealed IS NULL ORDER BY id LIMIT ?"), batch)
		if err != nil {
			return total, err
		}
		type pending struct{ id, userID, provider, apiKey string }
		var work []pending
		for rows.Next() {
			var p pending
			if err := rows.Scan(&p.id, &p.userID, &p.provider, &p.apiKey); err != nil {
				_ = rows.Close()
				return total, err
			}
			work = append(work, p)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return total, err
		}
		_ = rows.Close()
		if len(work) == 0 {
			return total, nil
		}
		progressed := 0
		for _, p := range work {
			sealed, err := s.sealer.Seal(ctx, vault.UserTenant(p.userID), []byte(p.apiKey), apiKeyAAD(p.userID, p.provider))
			if err != nil {
				return total, fmt.Errorf("sealing api key %s: %w", p.id, err)
			}
			res, err := s.db.ExecContext(ctx, s.bind(
				"UPDATE api_keys SET api_key_sealed = ? WHERE id = ? AND api_key_sealed IS NULL AND api_key = ?"),
				sealed, p.id, p.apiKey)
			if err != nil {
				return total, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
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

package postgres

import (
	"context"
	"crypto/subtle"
	"database/sql"
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

// CreateSetting writes a setting, replacing any existing value for the same
// (user, project, key).
//
// Two queries rather than one because the conflict target differs. A
// user-level setting has project_id NULL, which the table's
// UNIQUE (user_id, project_id, key) cannot match — NULL is never equal to
// NULL — so it resolves against the partial index added in
// 20260826000000_settings_dedupe_and_unique_key.sql. A project-scoped setting
// has a non-NULL project_id and uses the table constraint directly. Postgres
// requires the ON CONFLICT target to name an index that actually covers the
// row, so a single statement cannot serve both.
func (s *settingStore) CreateSetting(ctx context.Context, setting *core.Setting) error {
	if setting.ProjectID != nil {
		return s.q.CreateProjectSetting(ctx, pgdb.CreateProjectSettingParams{
			ID:        setting.ID,
			UserID:    setting.UserID,
			ProjectID: settingPtrToNullString(setting.ProjectID),
			Key:       setting.Key,
			Value:     setting.Value,
			ValueType: setting.ValueType,
			CreatedAt: setting.CreatedAt,
			UpdatedAt: setting.UpdatedAt,
		})
	}

	return s.q.CreateSetting(ctx, pgdb.CreateSettingParams{
		ID:        setting.ID,
		UserID:    setting.UserID,
		ProjectID: settingPtrToNullString(setting.ProjectID),
		Key:       setting.Key,
		Value:     setting.Value,
		ValueType: setting.ValueType,
		CreatedAt: setting.CreatedAt,
		UpdatedAt: setting.UpdatedAt,
	})
}

func (s *settingStore) GetSetting(ctx context.Context, userID string, projectID *string, key string) (*core.Setting, error) {
	row, err := s.q.GetSetting(ctx, pgdb.GetSettingParams{UserID: userID, ProjectID: settingPtrToNullString(projectID), Key: key})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("setting not found: %s", key)
		}
		return nil, fmt.Errorf("failed to get setting: %w", err)
	}
	return &core.Setting{
		ID:        row.ID,
		UserID:    row.UserID,
		ProjectID: settingNullStringToPtr(row.ProjectID),
		Key:       row.Key,
		Value:     row.Value,
		ValueType: row.ValueType,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}, nil
}

func (s *settingStore) ListSettings(ctx context.Context, userID string, projectID *string) ([]*core.Setting, error) {
	rows, err := s.q.ListSettings(ctx, pgdb.ListSettingsParams{UserID: userID, ProjectID: settingPtrToNullString(projectID)})
	if err != nil {
		return nil, fmt.Errorf("failed to list settings: %w", err)
	}
	settings := make([]*core.Setting, len(rows))
	for i, row := range rows {
		settings[i] = &core.Setting{
			ID:        row.ID,
			UserID:    row.UserID,
			ProjectID: settingNullStringToPtr(row.ProjectID),
			Key:       row.Key,
			Value:     row.Value,
			ValueType: row.ValueType,
			CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt,
		}
	}
	return settings, nil
}

func (s *settingStore) ListSettingsByKey(ctx context.Context, userID string, keyPattern string) ([]*core.Setting, error) {
	rows, err := s.q.ListSettingsByKey(ctx, pgdb.ListSettingsByKeyParams{UserID: userID, Key: keyPattern})
	if err != nil {
		return nil, fmt.Errorf("failed to list settings by key: %w", err)
	}
	settings := make([]*core.Setting, len(rows))
	for i, row := range rows {
		settings[i] = &core.Setting{
			ID:        row.ID,
			UserID:    row.UserID,
			ProjectID: settingNullStringToPtr(row.ProjectID),
			Key:       row.Key,
			Value:     row.Value,
			ValueType: row.ValueType,
			CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt,
		}
	}
	return settings, nil
}

func (s *settingStore) UpdateSetting(ctx context.Context, setting *core.Setting) error {
	return s.q.UpdateSetting(ctx, pgdb.UpdateSettingParams{ID: setting.ID, Value: setting.Value, ValueType: setting.ValueType})
}

func (s *settingStore) DeleteSetting(ctx context.Context, id string) error {
	return s.q.DeleteSetting(ctx, id)
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

func (s *settingStore) GetProviderAPIKey(ctx context.Context, userID string, provider string) (string, error) {
	var sealed []byte
	query := s.bind("SELECT api_key_sealed FROM api_keys WHERE user_id = ? AND provider = ?")
	if err := s.db.QueryRowContext(ctx, query, userID, provider).Scan(&sealed); err != nil {
		return "", err
	}
	return s.openAPIKey(ctx, userID, provider, sealed)
}

// SetProviderAPIKey writes only the sealed column. The legacy plaintext column
// is kept (always ”) until a later migration drops it.
func (s *settingStore) SetProviderAPIKey(ctx context.Context, userID string, provider, apiKey string) error {
	if s.sealer == nil {
		return errAPIKeyVaultDisabled
	}
	sealed, err := s.sealer.Seal(ctx, vault.UserTenant(userID), []byte(apiKey), apiKeyAAD(userID, provider))
	if err != nil {
		return fmt.Errorf("sealing api key for provider %q: %w", provider, err)
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

func (s *settingStore) DeleteProviderAPIKey(ctx context.Context, userID string, provider string) error {
	query := s.bind("DELETE FROM api_keys WHERE user_id = ? AND provider = ?")
	_, err := s.db.ExecContext(ctx, query, userID, provider)
	return err
}

func (s *settingStore) GetProviderAPIKeys(ctx context.Context, userID string) (map[string]string, error) {
	// Automation credentials live in this table under a reserved prefix and
	// must never surface as provider keys (settings, LLM driver selection).
	query := s.bind("SELECT provider, api_key_sealed FROM api_keys WHERE user_id = ? AND provider NOT LIKE ?")
	rows, err := s.db.QueryContext(ctx, query, userID, core.AutomationProviderPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var provider string
		var sealed []byte
		if err := rows.Scan(&provider, &sealed); err != nil {
			return nil, err
		}
		opened, err := s.openAPIKey(ctx, userID, provider, sealed)
		if err != nil {
			return nil, err
		}
		result[provider] = opened
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
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
// sealed so a concurrent SetProviderAPIKey is never overwritten with stale
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

// Provider sign-in tokens (Claude, Codex, GitHub Copilot, Antigravity) are
// sealed with the same vault as api_keys. Each column's ciphertext is bound to
// its table, user and column, so a value copied to another row, another user or
// another column does not open. There is no plaintext column and no fallback
// read: a missing row means "not connected", and a row that does not open is
// an error, never a downgrade.
const (
	claudeTokensTable      = "claude_auth_tokens"
	codexTokensTable       = "codex_auth_tokens"
	copilotTokensTable     = "copilot_auth_tokens"
	antigravityTokensTable = "antigravity_auth_tokens"
)

var errOAuthTokenVaultDisabled = errors.New("provider sign-in tokens require the credential vault; none is configured")

func oauthTokenAAD(table, userID, column string) []byte {
	return []byte(table + "\x00" + userID + "\x00" + column)
}

// tokenCodec seals and opens the token columns of one user's row. The first
// failure sticks, so a caller can handle several columns and check once.
type tokenCodec struct {
	ctx    context.Context
	sealer CredentialSealer
	table  string
	userID string
	err    error
}

func (s *settingStore) tokenCodec(ctx context.Context, table, userID string) *tokenCodec {
	c := &tokenCodec{ctx: ctx, sealer: s.sealer, table: table, userID: userID}
	if s.sealer == nil {
		c.err = errOAuthTokenVaultDisabled
	}
	return c
}

func (c *tokenCodec) seal(column, plaintext string) []byte {
	if c.err != nil {
		return nil
	}
	sealed, err := c.sealer.Seal(c.ctx, vault.UserTenant(c.userID), []byte(plaintext), oauthTokenAAD(c.table, c.userID, column))
	if err != nil {
		c.err = fmt.Errorf("sealing %s.%s: %w", c.table, column, err)
		return nil
	}
	return sealed
}

func (c *tokenCodec) open(column string, sealed []byte) string {
	if c.err != nil {
		return ""
	}
	pt, err := c.sealer.Open(c.ctx, vault.UserTenant(c.userID), sealed, oauthTokenAAD(c.table, c.userID, column))
	if err != nil {
		c.err = fmt.Errorf("opening %s.%s: %w", c.table, column, err)
		return ""
	}
	defer clear(pt)
	return string(pt)
}

// maxSealedSwapAttempts bounds how often a compare-and-swap re-reads after
// another writer changed the row between its read and its update.
const maxSealedSwapAttempts = 3

// compareAndSwapSealedRefresh is compare-and-swap over a sealed refresh token.
//
// The comparison cannot happen in SQL: sealing is randomized, so equal
// plaintexts never produce equal ciphertexts. So the stored token is read,
// opened and compared here (in constant time), and update is conditioned on
// the exact ciphertext that was read — the witness. Every write re-seals, so
// the witness changes whenever the row does, which makes "the row still holds
// the witness" atomic with the comparison. If another writer got in between,
// the update matches nothing and the decision is made again against what that
// writer stored.
func (s *settingStore) compareAndSwapSealedRefresh(ctx context.Context, table, column, userID, expected string, update func(witness []byte) (sql.Result, error)) (bool, error) {
	//nolint:gosec // G201: table and column are package constants, never input.
	query := s.bind(fmt.Sprintf("SELECT %s_sealed FROM %s WHERE user_id = ?", column, table))
	for range maxSealedSwapAttempts {
		var witness []byte
		if err := s.db.QueryRowContext(ctx, query, userID).Scan(&witness); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
		c := s.tokenCodec(ctx, table, userID)
		stored := c.open(column, witness)
		if c.err != nil {
			return false, c.err
		}
		if subtle.ConstantTimeCompare([]byte(stored), []byte(expected)) != 1 {
			return false, nil
		}
		res, err := update(witness)
		if err != nil {
			return false, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return false, err
		}
		if affected > 0 {
			return true, nil
		}
	}
	return false, nil
}

func (s *settingStore) GetCodexAuthTokens(ctx context.Context, userID string) (*core.CodexAuthTokens, error) {
	query := s.bind(`SELECT access_token_sealed, refresh_token_sealed, id_token_sealed, account_id
		FROM codex_auth_tokens
		WHERE user_id = ?`)
	row := s.db.QueryRowContext(ctx, query, userID)

	var tokens core.CodexAuthTokens
	var access, refresh, idToken []byte
	if err := row.Scan(&access, &refresh, &idToken, &tokens.AccountID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	c := s.tokenCodec(ctx, codexTokensTable, userID)
	tokens.AccessToken = c.open("access_token", access)
	tokens.RefreshToken = c.open("refresh_token", refresh)
	tokens.IDToken = c.open("id_token", idToken)
	if c.err != nil {
		return nil, c.err
	}
	return &tokens, nil
}

func (s *settingStore) SetCodexAuthTokens(ctx context.Context, userID string, tokens core.CodexAuthTokens) error {
	c := s.tokenCodec(ctx, codexTokensTable, userID)
	access := c.seal("access_token", tokens.AccessToken)
	refresh := c.seal("refresh_token", tokens.RefreshToken)
	idToken := c.seal("id_token", tokens.IDToken)
	if c.err != nil {
		return c.err
	}
	now := time.Now().UTC()
	id := uuid.New().String()
	query := s.bind(`INSERT INTO codex_auth_tokens (id, user_id, access_token_sealed, refresh_token_sealed, id_token_sealed, account_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   access_token_sealed = excluded.access_token_sealed,
		   refresh_token_sealed = excluded.refresh_token_sealed,
		   id_token_sealed = excluded.id_token_sealed,
		   account_id = excluded.account_id,
		   updated_at = excluded.updated_at`)
	_, err := s.db.ExecContext(ctx, query, id, userID, access, refresh, idToken, tokens.AccountID, now, now)
	return err
}

// CompareAndSwapCodexAuthTokens persists tokens only if the stored refresh
// token still equals expectedRefreshToken. See core.SettingStore for semantics
// and compareAndSwapSealedRefresh for why two processes racing to persist a
// rotation cannot both win.
func (s *settingStore) CompareAndSwapCodexAuthTokens(ctx context.Context, userID string, expectedRefreshToken string, tokens core.CodexAuthTokens) (bool, error) {
	c := s.tokenCodec(ctx, codexTokensTable, userID)
	access := c.seal("access_token", tokens.AccessToken)
	refresh := c.seal("refresh_token", tokens.RefreshToken)
	idToken := c.seal("id_token", tokens.IDToken)
	if c.err != nil {
		return false, c.err
	}
	query := s.bind(`UPDATE codex_auth_tokens SET
		   access_token_sealed = ?,
		   refresh_token_sealed = ?,
		   id_token_sealed = ?,
		   account_id = ?,
		   updated_at = ?
		 WHERE user_id = ? AND refresh_token_sealed = ?`)
	return s.compareAndSwapSealedRefresh(ctx, codexTokensTable, "refresh_token", userID, expectedRefreshToken,
		func(witness []byte) (sql.Result, error) {
			return s.db.ExecContext(ctx, query, access, refresh, idToken, tokens.AccountID, time.Now().UTC(), userID, witness)
		})
}

func (s *settingStore) DeleteCodexAuthTokens(ctx context.Context, userID string) error {
	query := s.bind("DELETE FROM codex_auth_tokens WHERE user_id = ?")
	_, err := s.db.ExecContext(ctx, query, userID)
	return err
}

func (s *settingStore) GetCopilotAuthTokens(ctx context.Context, userID string) (*core.CopilotAuthTokens, error) {
	query := s.bind(`SELECT user_id, github_access_token_sealed, github_refresh_token_sealed, tier, created_at, updated_at
		FROM copilot_auth_tokens
		WHERE user_id = ?`)
	row := s.db.QueryRowContext(ctx, query, userID)

	var tokens core.CopilotAuthTokens
	var access, refresh []byte
	if err := row.Scan(&tokens.UserID, &access, &refresh, &tokens.Tier, &tokens.CreatedAt, &tokens.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	c := s.tokenCodec(ctx, copilotTokensTable, userID)
	tokens.GitHubAccessToken = c.open("github_access_token", access)
	tokens.GitHubRefreshToken = c.open("github_refresh_token", refresh)
	if c.err != nil {
		return nil, c.err
	}
	return &tokens, nil
}

func (s *settingStore) SetCopilotAuthTokens(ctx context.Context, userID string, tokens core.CopilotAuthTokens) error {
	c := s.tokenCodec(ctx, copilotTokensTable, userID)
	access := c.seal("github_access_token", tokens.GitHubAccessToken)
	refresh := c.seal("github_refresh_token", tokens.GitHubRefreshToken)
	if c.err != nil {
		return c.err
	}
	now := time.Now().UTC()
	id := uuid.New().String()
	query := s.bind(`INSERT INTO copilot_auth_tokens (id, user_id, github_access_token_sealed, github_refresh_token_sealed, tier, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   github_access_token_sealed = excluded.github_access_token_sealed,
		   github_refresh_token_sealed = excluded.github_refresh_token_sealed,
		   tier = excluded.tier,
		   updated_at = excluded.updated_at`)
	_, err := s.db.ExecContext(ctx, query, id, userID, access, refresh, tokens.Tier, now, now)
	return err
}

func (s *settingStore) DeleteCopilotAuthTokens(ctx context.Context, userID string) error {
	query := s.bind("DELETE FROM copilot_auth_tokens WHERE user_id = ?")
	_, err := s.db.ExecContext(ctx, query, userID)
	return err
}

func (s *settingStore) GetClaudeAuthTokens(ctx context.Context, userID string) (*core.ClaudeAuthTokens, error) {
	query := s.bind(`SELECT access_token_sealed, refresh_token_sealed, expires_at, account_uuid, account_email, organization_uuid, organization_name, scope
		FROM claude_auth_tokens
		WHERE user_id = ?`)
	row := s.db.QueryRowContext(ctx, query, userID)

	var tokens core.ClaudeAuthTokens
	var access, refresh []byte
	if err := row.Scan(&access, &refresh, &tokens.ExpiresAt, &tokens.AccountUUID, &tokens.AccountEmail, &tokens.OrganizationUUID, &tokens.OrganizationName, &tokens.Scope); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	c := s.tokenCodec(ctx, claudeTokensTable, userID)
	tokens.AccessToken = c.open("access_token", access)
	tokens.RefreshToken = c.open("refresh_token", refresh)
	if c.err != nil {
		return nil, c.err
	}
	return &tokens, nil
}

func (s *settingStore) SetClaudeAuthTokens(ctx context.Context, userID string, tokens core.ClaudeAuthTokens) error {
	c := s.tokenCodec(ctx, claudeTokensTable, userID)
	access := c.seal("access_token", tokens.AccessToken)
	refresh := c.seal("refresh_token", tokens.RefreshToken)
	if c.err != nil {
		return c.err
	}
	now := time.Now().UTC()
	id := uuid.New().String()
	query := s.bind(`INSERT INTO claude_auth_tokens (id, user_id, access_token_sealed, refresh_token_sealed, expires_at, account_uuid, account_email, organization_uuid, organization_name, scope, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   access_token_sealed = excluded.access_token_sealed,
		   refresh_token_sealed = excluded.refresh_token_sealed,
		   expires_at = excluded.expires_at,
		   account_uuid = excluded.account_uuid,
		   account_email = excluded.account_email,
		   organization_uuid = excluded.organization_uuid,
		   organization_name = excluded.organization_name,
		   scope = excluded.scope,
		   updated_at = excluded.updated_at`)
	_, err := s.db.ExecContext(ctx, query, id, userID, access, refresh, tokens.ExpiresAt, tokens.AccountUUID, tokens.AccountEmail, tokens.OrganizationUUID, tokens.OrganizationName, tokens.Scope, now, now)
	return err
}

// CompareAndSwapClaudeAuthTokens persists tokens only if the stored refresh
// token still equals expectedRefreshToken. See core.SettingStore for semantics
// and compareAndSwapSealedRefresh for why two processes racing to persist a
// rotation cannot both win.
func (s *settingStore) CompareAndSwapClaudeAuthTokens(ctx context.Context, userID string, expectedRefreshToken string, tokens core.ClaudeAuthTokens) (bool, error) {
	c := s.tokenCodec(ctx, claudeTokensTable, userID)
	access := c.seal("access_token", tokens.AccessToken)
	refresh := c.seal("refresh_token", tokens.RefreshToken)
	if c.err != nil {
		return false, c.err
	}
	query := s.bind(`UPDATE claude_auth_tokens SET
		   access_token_sealed = ?,
		   refresh_token_sealed = ?,
		   expires_at = ?,
		   account_uuid = ?,
		   account_email = ?,
		   organization_uuid = ?,
		   organization_name = ?,
		   scope = ?,
		   updated_at = ?
		 WHERE user_id = ? AND refresh_token_sealed = ?`)
	return s.compareAndSwapSealedRefresh(ctx, claudeTokensTable, "refresh_token", userID, expectedRefreshToken,
		func(witness []byte) (sql.Result, error) {
			return s.db.ExecContext(ctx, query,
				access, refresh, tokens.ExpiresAt,
				tokens.AccountUUID, tokens.AccountEmail, tokens.OrganizationUUID, tokens.OrganizationName, tokens.Scope,
				time.Now().UTC(), userID, witness)
		})
}

func (s *settingStore) DeleteClaudeAuthTokens(ctx context.Context, userID string) error {
	query := s.bind("DELETE FROM claude_auth_tokens WHERE user_id = ?")
	_, err := s.db.ExecContext(ctx, query, userID)
	return err
}

func (s *settingStore) GetAntigravityAuthTokens(ctx context.Context, userID string) (*core.AntigravityAuthTokens, error) {
	query := s.bind(`SELECT access_token_sealed, refresh_token_sealed, expires_at, id_token_sealed, scope
		FROM antigravity_auth_tokens
		WHERE user_id = ?`)
	row := s.db.QueryRowContext(ctx, query, userID)

	var tokens core.AntigravityAuthTokens
	var access, refresh, idToken []byte
	if err := row.Scan(&access, &refresh, &tokens.ExpiresAt, &idToken, &tokens.Scope); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	c := s.tokenCodec(ctx, antigravityTokensTable, userID)
	tokens.AccessToken = c.open("access_token", access)
	tokens.RefreshToken = c.open("refresh_token", refresh)
	tokens.IDToken = c.open("id_token", idToken)
	if c.err != nil {
		return nil, c.err
	}
	return &tokens, nil
}

func (s *settingStore) SetAntigravityAuthTokens(ctx context.Context, userID string, tokens core.AntigravityAuthTokens) error {
	c := s.tokenCodec(ctx, antigravityTokensTable, userID)
	access := c.seal("access_token", tokens.AccessToken)
	refresh := c.seal("refresh_token", tokens.RefreshToken)
	idToken := c.seal("id_token", tokens.IDToken)
	if c.err != nil {
		return c.err
	}
	now := time.Now().UTC()
	id := uuid.New().String()
	query := s.bind(`INSERT INTO antigravity_auth_tokens (id, user_id, access_token_sealed, refresh_token_sealed, expires_at, id_token_sealed, scope, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   access_token_sealed = excluded.access_token_sealed,
		   refresh_token_sealed = excluded.refresh_token_sealed,
		   expires_at = excluded.expires_at,
		   id_token_sealed = excluded.id_token_sealed,
		   scope = excluded.scope,
		   updated_at = excluded.updated_at`)
	_, err := s.db.ExecContext(ctx, query, id, userID, access, refresh, tokens.ExpiresAt, idToken, tokens.Scope, now, now)
	return err
}

// CompareAndSwapAntigravityAuthTokens persists tokens only if the stored
// refresh token still equals expectedRefreshToken. See core.SettingStore for
// semantics and compareAndSwapSealedRefresh for why two processes racing to
// persist a refresh cannot both win.
func (s *settingStore) CompareAndSwapAntigravityAuthTokens(ctx context.Context, userID string, expectedRefreshToken string, tokens core.AntigravityAuthTokens) (bool, error) {
	c := s.tokenCodec(ctx, antigravityTokensTable, userID)
	access := c.seal("access_token", tokens.AccessToken)
	refresh := c.seal("refresh_token", tokens.RefreshToken)
	idToken := c.seal("id_token", tokens.IDToken)
	if c.err != nil {
		return false, c.err
	}
	query := s.bind(`UPDATE antigravity_auth_tokens SET
		   access_token_sealed = ?,
		   refresh_token_sealed = ?,
		   expires_at = ?,
		   id_token_sealed = ?,
		   scope = ?,
		   updated_at = ?
		 WHERE user_id = ? AND refresh_token_sealed = ?`)
	return s.compareAndSwapSealedRefresh(ctx, antigravityTokensTable, "refresh_token", userID, expectedRefreshToken,
		func(witness []byte) (sql.Result, error) {
			return s.db.ExecContext(ctx, query,
				access, refresh, tokens.ExpiresAt, idToken, tokens.Scope,
				time.Now().UTC(), userID, witness)
		})
}

func (s *settingStore) DeleteAntigravityAuthTokens(ctx context.Context, userID string) error {
	query := s.bind("DELETE FROM antigravity_auth_tokens WHERE user_id = ?")
	_, err := s.db.ExecContext(ctx, query, userID)
	return err
}

func (s *settingStore) GetVisibilityOverride(ctx context.Context, userID string, itemType int32, slug string) (*bool, error) {
	isVisible, err := s.q.GetVisibilityOverride(ctx, pgdb.GetVisibilityOverrideParams{UserID: userID, ItemType: itemType, Slug: slug})
	if err != nil {
		return nil, nil
	}
	return &isVisible, nil
}

func (s *settingStore) ListVisibilityOverrides(ctx context.Context, userID string, itemType int32) (map[string]bool, error) {
	rows, err := s.q.ListVisibilityOverrides(ctx, pgdb.ListVisibilityOverridesParams{UserID: userID, ItemType: itemType})
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool)
	for _, row := range rows {
		result[row.Slug] = row.IsVisible
	}
	return result, nil
}

func (s *settingStore) SetVisibilityOverride(ctx context.Context, userID string, itemType int32, slug string, isVisible bool) error {
	return s.q.SetVisibilityOverride(ctx, pgdb.SetVisibilityOverrideParams{
		ID:        uuid.New().String(),
		UserID:    userID,
		ItemType:  itemType,
		Slug:      slug,
		IsVisible: isVisible,
	})
}

func (s *settingStore) DeleteVisibilityOverride(ctx context.Context, userID string, itemType int32, slug string) error {
	return s.q.DeleteVisibilityOverride(ctx, pgdb.DeleteVisibilityOverrideParams{UserID: userID, ItemType: itemType, Slug: slug})
}

func (s *settingStore) GetItemDefault(ctx context.Context, itemType int32, slug string) (*core.ItemDefault, error) {
	row, err := s.q.GetItemDefault(ctx, pgdb.GetItemDefaultParams{ItemType: itemType, Slug: slug})
	if err != nil {
		return nil, err
	}
	var reason *string
	if row.Reason.Valid {
		reason = &row.Reason.String
	}
	return &core.ItemDefault{ItemType: itemType, Slug: slug, IsHidden: row.IsHidden, Reason: reason}, nil
}

func (s *settingStore) ListHiddenItemDefaults(ctx context.Context, itemType int32) ([]string, error) {
	rows, err := s.q.ListHiddenItemDefaults(ctx, itemType)
	if err != nil {
		return nil, err
	}
	slugs := make([]string, len(rows))
	for i, row := range rows {
		slugs[i] = row.Slug
	}
	return slugs, nil
}

func (s *settingStore) GetDefaultPresetAssignments(ctx context.Context, workflowName string) (map[string]string, error) {
	rows, err := s.q.GetDefaultPresetAssignments(ctx, workflowName)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for _, row := range rows {
		result[row.GroupName] = row.PresetSlug
	}
	return result, nil
}

func settingPtrToNullString(s *string) sql.NullString {
	if s != nil {
		return sql.NullString{String: *s, Valid: true}
	}
	return sql.NullString{Valid: false}
}

func settingNullStringToPtr(ns sql.NullString) *string {
	if ns.Valid {
		return &ns.String
	}
	return nil
}

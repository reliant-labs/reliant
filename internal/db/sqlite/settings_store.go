package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db/core"
	sqlitedb "github.com/reliant-labs/reliant/internal/db/sqlite/generated"
)

type settingStore struct {
	q    sqlitedb.Querier
	db   sqlitedb.DBTX
	bind func(string) string
}

// NewSettingStore creates the SQLite settings store implementation.
func NewSettingStore(q sqlitedb.Querier, db sqlitedb.DBTX, bind func(string) string) core.SettingStore {
	return &settingStore{q: q, db: db, bind: bind}
}

func (s *settingStore) CreateSetting(ctx context.Context, setting *core.Setting) error {
	return s.q.CreateSetting(ctx, settingToCreateParams(setting))
}

func (s *settingStore) GetSetting(ctx context.Context, userID string, projectID *string, key string) (*core.Setting, error) {
	sqlcSetting, err := s.q.GetSetting(ctx, sqlitedb.GetSettingParams{
		UserID:    userID,
		ProjectID: settingPtrToNullString(projectID),
		Column3:   settingPtrToNullString(projectID), // Query uses same param twice for NULL check
		Key:       key,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("setting not found: %s", key)
		}
		return nil, fmt.Errorf("failed to get setting: %w", err)
	}
	return settingFromSQLc(sqlcSetting), nil
}

func (s *settingStore) ListSettings(ctx context.Context, userID string, projectID *string) ([]*core.Setting, error) {
	sqlcSettings, err := s.q.ListSettings(ctx, sqlitedb.ListSettingsParams{
		UserID:    userID,
		ProjectID: settingPtrToNullString(projectID),
		Column3:   settingPtrToNullString(projectID), // Query uses same param twice for NULL check
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list settings: %w", err)
	}
	return settingsFromSQLc(sqlcSettings), nil
}

func (s *settingStore) ListSettingsByKey(ctx context.Context, userID string, keyPattern string) ([]*core.Setting, error) {
	sqlcSettings, err := s.q.ListSettingsByKey(ctx, sqlitedb.ListSettingsByKeyParams{
		UserID: userID,
		Key:    keyPattern,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list settings by key: %w", err)
	}
	return settingsFromSQLc(sqlcSettings), nil
}

func (s *settingStore) UpdateSetting(ctx context.Context, setting *core.Setting) error {
	return s.q.UpdateSetting(ctx, sqlitedb.UpdateSettingParams{
		ID:        setting.ID,
		Value:     setting.Value,
		ValueType: setting.ValueType,
	})
}

func (s *settingStore) DeleteSetting(ctx context.Context, id string) error {
	return s.q.DeleteSetting(ctx, id)
}

func (s *settingStore) GetProviderAPIKey(ctx context.Context, userID string, provider string) (string, error) {
	var apiKey string
	query := s.bind("SELECT api_key FROM api_keys WHERE user_id = ? AND provider = ?")
	err := s.db.QueryRowContext(ctx, query, userID, provider).Scan(&apiKey)
	if err != nil {
		return "", err
	}
	return apiKey, nil
}

func (s *settingStore) SetProviderAPIKey(ctx context.Context, userID string, provider, apiKey string) error {
	now := time.Now().UTC()
	id := uuid.New().String()
	query := s.bind(`INSERT INTO api_keys (id, user_id, provider, api_key, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, provider) DO UPDATE SET
		   api_key = excluded.api_key,
		   updated_at = excluded.updated_at`)
	_, err := s.db.ExecContext(ctx, query, id, userID, provider, apiKey, now, now)
	return err
}

func (s *settingStore) DeleteProviderAPIKey(ctx context.Context, userID string, provider string) error {
	query := s.bind("DELETE FROM api_keys WHERE user_id = ? AND provider = ?")
	_, err := s.db.ExecContext(ctx, query, userID, provider)
	return err
}

func (s *settingStore) GetProviderAPIKeys(ctx context.Context, userID string) (map[string]string, error) {
	query := s.bind("SELECT provider, api_key FROM api_keys WHERE user_id = ?")
	rows, err := s.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var provider, apiKey string
		if err := rows.Scan(&provider, &apiKey); err != nil {
			return nil, err
		}
		result[provider] = apiKey
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *settingStore) GetCodexAuthTokens(ctx context.Context, userID string) (*core.CodexAuthTokens, error) {
	query := s.bind(`SELECT access_token, refresh_token, id_token, account_id
		FROM codex_auth_tokens
		WHERE user_id = ?`)
	row := s.db.QueryRowContext(ctx, query, userID)

	var tokens core.CodexAuthTokens
	if err := row.Scan(&tokens.AccessToken, &tokens.RefreshToken, &tokens.IDToken, &tokens.AccountID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &tokens, nil
}

func (s *settingStore) SetCodexAuthTokens(ctx context.Context, userID string, tokens core.CodexAuthTokens) error {
	now := time.Now().UTC()
	id := uuid.New().String()
	query := s.bind(`INSERT INTO codex_auth_tokens (id, user_id, access_token, refresh_token, id_token, account_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   access_token = excluded.access_token,
		   refresh_token = excluded.refresh_token,
		   id_token = excluded.id_token,
		   account_id = excluded.account_id,
		   updated_at = excluded.updated_at`)
	_, err := s.db.ExecContext(ctx, query, id, userID, tokens.AccessToken, tokens.RefreshToken, tokens.IDToken, tokens.AccountID, now, now)
	return err
}

func (s *settingStore) DeleteCodexAuthTokens(ctx context.Context, userID string) error {
	query := s.bind("DELETE FROM codex_auth_tokens WHERE user_id = ?")
	_, err := s.db.ExecContext(ctx, query, userID)
	return err
}

func (s *settingStore) GetClaudeAuthTokens(ctx context.Context, userID string) (*core.ClaudeAuthTokens, error) {
	query := s.bind(`SELECT access_token, refresh_token, expires_at, account_uuid, account_email, organization_uuid, organization_name, scope
		FROM claude_auth_tokens
		WHERE user_id = ?`)
	row := s.db.QueryRowContext(ctx, query, userID)

	var tokens core.ClaudeAuthTokens
	if err := row.Scan(&tokens.AccessToken, &tokens.RefreshToken, &tokens.ExpiresAt, &tokens.AccountUUID, &tokens.AccountEmail, &tokens.OrganizationUUID, &tokens.OrganizationName, &tokens.Scope); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &tokens, nil
}

func (s *settingStore) SetClaudeAuthTokens(ctx context.Context, userID string, tokens core.ClaudeAuthTokens) error {
	now := time.Now().UTC()
	id := uuid.New().String()
	query := s.bind(`INSERT INTO claude_auth_tokens (id, user_id, access_token, refresh_token, expires_at, account_uuid, account_email, organization_uuid, organization_name, scope, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   access_token = excluded.access_token,
		   refresh_token = excluded.refresh_token,
		   expires_at = excluded.expires_at,
		   account_uuid = excluded.account_uuid,
		   account_email = excluded.account_email,
		   organization_uuid = excluded.organization_uuid,
		   organization_name = excluded.organization_name,
		   scope = excluded.scope,
		   updated_at = excluded.updated_at`)
	_, err := s.db.ExecContext(ctx, query, id, userID, tokens.AccessToken, tokens.RefreshToken, tokens.ExpiresAt, tokens.AccountUUID, tokens.AccountEmail, tokens.OrganizationUUID, tokens.OrganizationName, tokens.Scope, now, now)
	return err
}

func (s *settingStore) DeleteClaudeAuthTokens(ctx context.Context, userID string) error {
	query := s.bind("DELETE FROM claude_auth_tokens WHERE user_id = ?")
	_, err := s.db.ExecContext(ctx, query, userID)
	return err
}

func (s *settingStore) GetVisibilityOverride(ctx context.Context, userID string, itemType int32, slug string) (*bool, error) {
	isVisible, err := s.q.GetVisibilityOverride(ctx, sqlitedb.GetVisibilityOverrideParams{
		UserID:   userID,
		ItemType: int64(itemType),
		Slug:     slug,
	})
	if err != nil {
		return nil, nil
	}
	return &isVisible, nil
}

func (s *settingStore) ListVisibilityOverrides(ctx context.Context, userID string, itemType int32) (map[string]bool, error) {
	rows, err := s.q.ListVisibilityOverrides(ctx, sqlitedb.ListVisibilityOverridesParams{UserID: userID, ItemType: int64(itemType)})
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
	return s.q.SetVisibilityOverride(ctx, sqlitedb.SetVisibilityOverrideParams{
		ID:        uuid.New().String(),
		UserID:    userID,
		ItemType:  int64(itemType),
		Slug:      slug,
		IsVisible: isVisible,
	})
}

func (s *settingStore) DeleteVisibilityOverride(ctx context.Context, userID string, itemType int32, slug string) error {
	return s.q.DeleteVisibilityOverride(ctx, sqlitedb.DeleteVisibilityOverrideParams{
		UserID:   userID,
		ItemType: int64(itemType),
		Slug:     slug,
	})
}

func (s *settingStore) GetItemDefault(ctx context.Context, itemType int32, slug string) (*core.ItemDefault, error) {
	row, err := s.q.GetItemDefault(ctx, sqlitedb.GetItemDefaultParams{ItemType: int64(itemType), Slug: slug})
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
	rows, err := s.q.ListHiddenItemDefaults(ctx, int64(itemType))
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

func settingFromSQLc(ss sqlitedb.Setting) *core.Setting {
	return &core.Setting{
		ID:        ss.ID,
		UserID:    ss.UserID,
		ProjectID: settingNullStringToPtr(ss.ProjectID),
		Key:       ss.Key,
		Value:     ss.Value,
		ValueType: ss.ValueType,
		CreatedAt: ss.CreatedAt,
		UpdatedAt: ss.UpdatedAt,
	}
}

func settingsFromSQLc(sqlcSettings []sqlitedb.Setting) []*core.Setting {
	settings := make([]*core.Setting, len(sqlcSettings))
	for i, ss := range sqlcSettings {
		settings[i] = settingFromSQLc(ss)
	}
	return settings
}

func settingToCreateParams(setting *core.Setting) sqlitedb.CreateSettingParams {
	return sqlitedb.CreateSettingParams{
		ID:        setting.ID,
		UserID:    setting.UserID,
		ProjectID: settingPtrToNullString(setting.ProjectID),
		Key:       setting.Key,
		Value:     setting.Value,
		ValueType: setting.ValueType,
		CreatedAt: setting.CreatedAt,
		UpdatedAt: setting.UpdatedAt,
	}
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

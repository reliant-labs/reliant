package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db/core"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

type settingStore struct {
	q    pgdb.Querier
	db   pgdb.DBTX
	bind func(string) string
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

func (s *settingStore) GetCopilotAuthTokens(ctx context.Context, userID string) (*core.CopilotAuthTokens, error) {
	query := s.bind(`SELECT user_id, github_access_token, github_refresh_token, tier, created_at, updated_at
		FROM copilot_auth_tokens
		WHERE user_id = ?`)
	row := s.db.QueryRowContext(ctx, query, userID)

	var tokens core.CopilotAuthTokens
	if err := row.Scan(&tokens.UserID, &tokens.GitHubAccessToken, &tokens.GitHubRefreshToken, &tokens.Tier, &tokens.CreatedAt, &tokens.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &tokens, nil
}

func (s *settingStore) SetCopilotAuthTokens(ctx context.Context, userID string, tokens core.CopilotAuthTokens) error {
	now := time.Now().UTC()
	id := uuid.New().String()
	query := s.bind(`INSERT INTO copilot_auth_tokens (id, user_id, github_access_token, github_refresh_token, tier, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   github_access_token = excluded.github_access_token,
		   github_refresh_token = excluded.github_refresh_token,
		   tier = excluded.tier,
		   updated_at = excluded.updated_at`)
	_, err := s.db.ExecContext(ctx, query, id, userID, tokens.GitHubAccessToken, tokens.GitHubRefreshToken, tokens.Tier, now, now)
	return err
}

func (s *settingStore) DeleteCopilotAuthTokens(ctx context.Context, userID string) error {
	query := s.bind("DELETE FROM copilot_auth_tokens WHERE user_id = ?")
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

// CompareAndSwapClaudeAuthTokens persists tokens only if the stored refresh
// token still equals expectedRefreshToken. See core.SettingStore for semantics.
// The conditional UPDATE is atomic, so two processes racing to persist a
// rotation cannot both win.
func (s *settingStore) CompareAndSwapClaudeAuthTokens(ctx context.Context, userID string, expectedRefreshToken string, tokens core.ClaudeAuthTokens) (bool, error) {
	now := time.Now().UTC()
	query := s.bind(`UPDATE claude_auth_tokens SET
		   access_token = ?,
		   refresh_token = ?,
		   expires_at = ?,
		   account_uuid = ?,
		   account_email = ?,
		   organization_uuid = ?,
		   organization_name = ?,
		   scope = ?,
		   updated_at = ?
		 WHERE user_id = ? AND refresh_token = ?`)
	res, err := s.db.ExecContext(ctx, query,
		tokens.AccessToken, tokens.RefreshToken, tokens.ExpiresAt,
		tokens.AccountUUID, tokens.AccountEmail, tokens.OrganizationUUID, tokens.OrganizationName, tokens.Scope,
		now, userID, expectedRefreshToken)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (s *settingStore) DeleteClaudeAuthTokens(ctx context.Context, userID string) error {
	query := s.bind("DELETE FROM claude_auth_tokens WHERE user_id = ?")
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

-- +goose Up
-- Create dedicated api_keys table for sensitive credentials
-- This removes API keys from the settings table for better security isolation

CREATE TABLE api_keys (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    api_key TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, provider)
);

CREATE INDEX idx_api_keys_user ON api_keys(user_id);
CREATE INDEX idx_api_keys_provider ON api_keys(user_id, provider);

-- Migrate existing API keys from settings table
INSERT INTO api_keys (id, user_id, provider, api_key, created_at, updated_at)
SELECT 
    id,
    user_id,
    SUBSTR(key, 17) as provider,  -- Extract provider from 'provider.apikey.{provider}'
    value as api_key,
    created_at,
    updated_at
FROM settings
WHERE key LIKE 'provider.apikey.%'
  AND user_id IS NOT NULL;

-- Delete migrated API keys from settings table
DELETE FROM settings WHERE key LIKE 'provider.apikey.%';

-- +goose Down
-- Migrate API keys back to settings table
INSERT INTO settings (id, user_id, project_id, key, value, value_type, created_at, updated_at)
SELECT 
    id,
    user_id,
    NULL as project_id,
    'provider.apikey.' || provider as key,
    api_key as value,
    'string' as value_type,
    created_at,
    updated_at
FROM api_keys;

DROP TABLE IF EXISTS api_keys;

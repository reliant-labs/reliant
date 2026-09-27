-- +goose Up
ALTER TABLE daemon_pats ADD COLUMN IF NOT EXISTS daemon_id TEXT;


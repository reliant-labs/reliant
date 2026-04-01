-- +goose Up
-- Fix auto_approve column to be NOT NULL
-- First, update any existing NULL values to FALSE
UPDATE chats SET auto_approve = FALSE WHERE auto_approve IS NULL;

-- Note: SQLite doesn't support modifying column constraints directly
-- We need to recreate the table with the correct constraint
-- But since we already updated 001_initial_schema.sql with NOT NULL,
-- this migration serves as a data migration to ensure existing rows
-- have proper values before the app expects NOT NULL

-- +goose Down
-- No down migration needed - data migration is not reversible

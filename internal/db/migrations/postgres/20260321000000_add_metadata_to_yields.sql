-- +goose Up
-- Add metadata column to yields table for storing arbitrary JSON metadata.
ALTER TABLE yields ADD COLUMN metadata TEXT;

-- +goose Down
ALTER TABLE yields DROP COLUMN metadata;

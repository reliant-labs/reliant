-- +goose Up
-- Add metadata column to yields table for storing arbitrary JSON metadata.
-- IF NOT EXISTS because this file shipped as 20260321000000 in #76 before #82
-- renumbered it here: a database migrated in that window already ran this SQL
-- under the old version, and a bare ADD COLUMN would fail on it.
ALTER TABLE yields ADD COLUMN IF NOT EXISTS metadata TEXT;


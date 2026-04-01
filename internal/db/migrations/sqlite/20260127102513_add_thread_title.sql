-- +goose Up
-- Add title column to threads table for human-readable thread names

ALTER TABLE threads ADD COLUMN title TEXT;

-- +goose Down
-- Remove title column from threads table

ALTER TABLE threads DROP COLUMN title;

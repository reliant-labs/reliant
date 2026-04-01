-- +goose Up
-- Add agent column to chats table
ALTER TABLE chats ADD COLUMN agent TEXT;

-- +goose Down
-- Remove agent column from chats table
ALTER TABLE chats DROP COLUMN agent;

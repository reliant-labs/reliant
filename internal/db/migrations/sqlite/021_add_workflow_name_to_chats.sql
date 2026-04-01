-- +goose Up
ALTER TABLE chats ADD COLUMN workflow_name TEXT;

-- +goose Down
ALTER TABLE chats DROP COLUMN workflow_name;

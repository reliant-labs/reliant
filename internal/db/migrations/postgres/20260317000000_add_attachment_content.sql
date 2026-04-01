-- +goose Up
ALTER TABLE attachments ADD COLUMN content BYTEA;

-- +goose Down
ALTER TABLE attachments DROP COLUMN content;

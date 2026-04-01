-- +goose Up
ALTER TABLE attachments ADD COLUMN content BLOB;

-- +goose Down
ALTER TABLE attachments DROP COLUMN content;

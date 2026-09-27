-- +goose Up
ALTER TABLE attachments ADD COLUMN content BYTEA;


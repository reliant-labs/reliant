-- +goose Up
ALTER TABLE daemons ADD COLUMN daemon_type TEXT;


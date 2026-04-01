-- +goose Up
-- SQLite introduced project_configs with memory columns from day one
-- (20260210000000_add_daemon_registry_and_project_configs.sql).
-- This migration keeps SQLite/Postgres migration version parity.
SELECT 1;

-- +goose Down
SELECT 1;

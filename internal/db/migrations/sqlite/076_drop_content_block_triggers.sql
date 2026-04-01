-- +goose Up
-- Drop content_block triggers that cause duplicate chat_updates
-- 
-- CONTEXT:
-- Migration 056 added chat_updates_content_block_insert and chat_updates_content_block_update triggers
-- Migration 069 dropped these triggers to prevent duplicates
-- However, the triggers may still exist in some databases
--
-- PROBLEM:
-- When save_message.go creates content blocks via CreateContentBlock():
-- 1. If these triggers exist, they fire and create a chat_update for EACH content block
-- 2. Then save_message.go creates ONE enriched chat_update with ALL content blocks (lines 468-610)
-- 3. This causes duplicate tool calls to appear in the UI
--
-- SOLUTION:
-- Drop these triggers permanently. Content block updates are now handled by
-- manual dual-write in save_message.go (internal/workflow/v2/activities/handlers/save_message.go)
-- which creates a SINGLE chat_update with all content blocks embedded.
--
-- This migration ensures these triggers are gone even if:
-- - The database hasn't run migration 069
-- - The triggers were accidentally re-added
-- - A new database is being created from scratch

DROP TRIGGER IF EXISTS chat_updates_content_block_insert;
DROP TRIGGER IF EXISTS chat_updates_content_block_update;

-- +goose Down
-- Do NOT recreate these triggers
-- They cause duplicate chat_updates because save_message.go already does dual-write
-- See: internal/workflow/v2/activities/handlers/save_message.go lines 468-610

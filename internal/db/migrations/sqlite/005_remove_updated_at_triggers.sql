-- +goose Up
-- Remove triggers that automatically set updated_at using CURRENT_TIMESTAMP
-- This causes timestamp format inconsistencies (SQLite format vs RFC3339)
-- All updated_at values should be set explicitly by Go code using time.Now().UTC()

DROP TRIGGER IF EXISTS update_chats_updated_at;
DROP TRIGGER IF EXISTS update_threads_updated_at;
DROP TRIGGER IF EXISTS update_context_windows_updated_at;
DROP TRIGGER IF EXISTS update_messages_updated_at;
DROP TRIGGER IF EXISTS update_message_content_blocks_updated_at;
DROP TRIGGER IF EXISTS update_projects_updated_at;
DROP TRIGGER IF EXISTS update_worktrees_updated_at;
DROP TRIGGER IF EXISTS update_plans_updated_at;
DROP TRIGGER IF EXISTS update_tasks_updated_at;
DROP TRIGGER IF EXISTS update_settings_updated_at;
DROP TRIGGER IF EXISTS update_tool_approvals_updated_at;

-- +goose Down
-- Cannot easily recreate triggers - they use CURRENT_TIMESTAMP which causes the original problem
-- To rollback, restore from backup

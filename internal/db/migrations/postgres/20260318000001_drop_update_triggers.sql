-- +goose Up
-- Drop the chat_updates and user_updates triggers that fire on UPDATE chats.
-- These are replaced by application-level code in the repo layer that calls
-- CreateChatUpdate / CreateUserUpdate explicitly, which also publishes to the
-- streaming UpdateHub for event-driven delivery (no more polling).
--
-- Note: Postgres may not have these triggers if they were never created
-- (triggers were originally SQLite-only). Drop IF EXISTS for safety.

DROP TRIGGER IF EXISTS chat_updates_chat_update ON chats;
DROP TRIGGER IF EXISTS user_updates_chat_config_update ON chats;
DROP FUNCTION IF EXISTS chat_updates_chat_update_fn();
DROP FUNCTION IF EXISTS user_updates_chat_config_update_fn();

-- +goose Down
-- No-op: Postgres never had these triggers in the standard migration path.
-- If needed, recreate them following the SQLite down migration pattern.

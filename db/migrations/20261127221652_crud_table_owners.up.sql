-- forge:backward-compatible — comment-only: declares who scopes the rows of attachments, plans and tasks; no column, constraint or row changes
-- Migration: crud_table_owners
-- Created: 2026-11-27T22:16:52Z
--
-- The schema is the 259 migration(s) in this directory, applied in order (newest: 20261127164356_pipeline_repos_config_pending.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Write your migration SQL below:

-- forge's crud-table-unowned lint (gating from forge v1.12.0) asks each table
-- that generated CRUD reads or writes on an authenticated rpc to say how its
-- rows are scoped. These three are scoped in their handlers, today, and this
-- records that: no generated op changes and no row, column or constraint
-- does. Moving attachments to `forge:owner=user_id` (the TODO(forge#2461) in
-- internal/handlers/attachment/handlers_crud.go) would regenerate its CRUD
-- ops and is a separate change.
COMMENT ON TABLE attachments IS 'forge:unscoped: personal files, scoped to the caller in the handler (ownedBy: user_id) on every generated op';
COMMENT ON TABLE plans IS 'forge:unscoped: scoped through the plan''s chat in the handler (writableChatOfThread: the caller must be able to write the chat) on every generated op';
COMMENT ON TABLE tasks IS 'forge:unscoped: scoped through the task''s plan and its chat in the handler on every generated op: get, create, update and delete need writableChatOfPlan (the caller can write the chat), and ListTasks requires plan_id and checks ReadChatAccess on that plan''s chat';

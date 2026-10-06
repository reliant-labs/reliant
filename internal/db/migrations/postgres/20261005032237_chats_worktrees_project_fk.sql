-- +goose Up

-- chats.project_id and worktrees.project_id had no foreign key, so deleting a
-- project left its chats and worktrees behind: unreachable (every project-scoped
-- read 404s) yet still listed by surfaces that do not join projects. Deleting a
-- project now deletes them in the same statement via ON DELETE CASCADE, matching
-- plans, triggers, repos and project_daemons which already cascade.
--
-- A FK cannot coexist with rows that violate it, so the rows orphaned by past
-- project deletes are removed first (their messages, threads, tool_calls, etc.
-- cascade from chats).
--
-- The chats DELETE cannot be left to cascade alone. messages_thread_id_fkey,
-- tool_calls_thread_id_fkey, approvals_thread_id_fkey and both fork_at_message_id
-- foreign keys are ON DELETE RESTRICT, which (unlike NO ACTION) fires
-- immediately, even when the referencing row is itself about to be cascaded
-- away in the same statement. So the orphaned chats' rows are removed in
-- dependency order first: fork pointers cleared (they only point within the
-- orphaned chat's own history, which is going away), then approvals, tool
-- calls, messages and threads (context_windows cascade from threads).
UPDATE threads SET fork_at_message_id = NULL
WHERE fork_at_message_id IS NOT NULL AND chat_id IN (SELECT c.id FROM chats c
 WHERE NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = c.project_id));

UPDATE context_windows SET fork_at_message_id = NULL
WHERE fork_at_message_id IS NOT NULL AND thread_id IN (SELECT t.id FROM threads t JOIN chats c ON c.id = t.chat_id
 WHERE NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = c.project_id));

DELETE FROM approvals WHERE thread_id IN (SELECT t.id FROM threads t JOIN chats c ON c.id = t.chat_id
 WHERE NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = c.project_id));

DELETE FROM tool_calls WHERE chat_id IN (SELECT c.id FROM chats c
 WHERE NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = c.project_id));

DELETE FROM messages WHERE chat_id IN (SELECT c.id FROM chats c
 WHERE NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = c.project_id));

DELETE FROM threads WHERE chat_id IN (SELECT c.id FROM chats c
 WHERE NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = c.project_id));

DELETE FROM chats c
WHERE NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = c.project_id);

DELETE FROM worktrees w
WHERE NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = w.project_id);

ALTER TABLE chats
    ADD CONSTRAINT chats_project_id_fkey
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE;

ALTER TABLE worktrees
    ADD CONSTRAINT worktrees_project_id_fkey
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE;

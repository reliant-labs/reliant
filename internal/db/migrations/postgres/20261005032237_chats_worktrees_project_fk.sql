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

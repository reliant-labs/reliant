-- forge:backward-compatible — adds a table only this release reads and writes; the previous release never reads it, so after a rollback a chat simply runs in its primary project (chats.project_id) again and its attachments wait unused.

-- Migration: chat_projects
--
-- A chat has one primary project, chats.project_id: its billing dimension,
-- its list grouping, the checkout its branch workspace belongs to, and what
-- every client older than this table reads. A chat may also act on other
-- projects. Each such ATTACHED project is a row here; the primary never is,
-- so the two can never disagree.
--
-- Its tools reach an attached project's main checkout through a `project`
-- argument, resolved on the worker to that project's checkout on the machine
-- the call runs on. For now every attached project must be installed on the
-- chat's machine (one machine per chat). daemon_id records the machine the
-- attachment was checked against, so a later release can route each
-- attachment to its own machine without a schema change.
CREATE TABLE chat_projects (
    chat_id text NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    project_id text NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    daemon_id text REFERENCES daemons (id) ON DELETE SET NULL,
    created_at timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (chat_id, project_id)
);

CREATE INDEX chat_projects_project ON chat_projects (project_id);

COMMENT ON TABLE chat_projects IS
    'A project attached to a chat beside its primary project (chats.project_id, never a row here): the chat''s tools may work in its main checkout.';
COMMENT ON COLUMN chat_projects.daemon_id IS
    'The machine the attachment was checked against (it held an installed checkout when attached); NULL once that machine is deleted.';

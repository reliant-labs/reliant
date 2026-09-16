-- +goose Up
-- Give a run its own identity, independent of the chat it belongs to.
--
-- Today a run has no owner. Every activity that needs to know who it is
-- running for re-reads the chat row and takes user_id off it
-- (call_llm.go, compact.go, load_workflow.go, workflow_status.go). That
-- works only because workflows.chat_id is NOT NULL, so `chats` is doing
-- three jobs at once: the coding product's object, the run container, and
-- the engine's identity carrier.
--
-- The third job is the one that blocks everything else. A trigger-fired or
-- API-started run has no conversation, so it cannot borrow an identity from
-- one — and identity is the single thing a run genuinely cannot do without.
-- Giving it a column of its own is what makes chat_id droppable later.
--
-- NULLABLE ON PURPOSE, and it stays nullable through this migration. The
-- backfill fills every existing row, and the write path starts populating it
-- for new rows, but NOTHING READS IT YET. That ordering is what makes this
-- change provably behavior-preserving: a column nobody reads cannot alter
-- what the product does. The read sites flip in a follow-up, each with a
-- fallback to chat.user_id, so the two sources can be compared before the
-- old one is retired.
--
-- Not a foreign key to users: this database has no users table. user_id is
-- an opaque string everywhere (23 tables carry it the same way), validated
-- against an external JWT rather than a local row. A FK here would be the
-- only one of its kind and would fail on the first row.

ALTER TABLE workflows
    ADD COLUMN owner_user_id text;

-- Backfill from the chat, which is where the value lives today. Rows whose
-- chat has since been deleted keep a NULL owner rather than blocking the
-- migration: they are unreachable runs, and inventing an owner for them
-- would be worse than admitting we do not know.
UPDATE workflows w
SET owner_user_id = c.user_id
FROM chats c
WHERE w.chat_id = c.id
  AND w.owner_user_id IS NULL;

-- Runs are listed and reaped per owner once identity moves here, and a
-- partial index keeps the NULLs (pre-backfill leftovers, and later the
-- chatless runs this enables) out of it.
CREATE INDEX IF NOT EXISTS idx_workflows_owner_user_id
    ON workflows (owner_user_id)
    WHERE owner_user_id IS NOT NULL;

-- +goose Down
-- Lossy, and deliberately so. Rolling back discards each run's own identity
-- and returns it to borrowing one from its chat — which is correct while
-- chat_id is still NOT NULL, because every row can still find its owner that
-- way. It stops being safe once chatless runs exist, and the migration that
-- makes chat_id nullable is the one that has to say so.
DROP INDEX IF EXISTS idx_workflows_owner_user_id;

ALTER TABLE workflows
    DROP COLUMN owner_user_id;

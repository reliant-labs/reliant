-- +goose NO TRANSACTION
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
--
-- NO TRANSACTION, because workflows is the hottest table the engine writes.
-- As one transaction, the ALTER's ACCESS EXCLUSIVE lock is held until the
-- backfill commits — measured on 2M synthetic runs, ~20s during which every
-- SELECT, INSERT and UPDATE on workflows blocked, i.e. every live run stalled.
-- Split up, each step holds the least it can:
--   1. ADD COLUMN of a nullable column with no default is catalog-only: a
--      brief ACCESS EXCLUSIVE, no table rewrite.
--   2. The backfill commits every batch, so it holds only row locks, and only
--      on one batch's rows at a time.
--   3. CREATE INDEX CONCURRENTLY builds without blocking writes.
-- Every step is idempotent, so a deploy that dies halfway re-runs this file
-- from the top and finishes the job: goose records the version only after the
-- last statement succeeds.

ALTER TABLE workflows
    ADD COLUMN IF NOT EXISTS owner_user_id text;

-- Backfill from the chat, which is where the value lives today. Rows whose
-- chat has since been deleted keep a NULL owner rather than blocking the
-- migration: they are unreachable runs, and inventing an owner for them
-- would be worse than admitting we do not know.
--
-- Batched by keyset on the primary key rather than by "owner IS NULL LIMIT n":
-- the orphans stay NULL forever, so a NULL-driven loop would re-scan them on
-- every pass and could never tell "done" from "only orphans left". Walking the
-- key visits each row once. Rows inserted while this runs are written with an
-- owner by the new code, and the owner_user_id IS NULL guard means a row the
-- application already stamped is never overwritten.
-- +goose StatementBegin
DO $$
DECLARE
    batch_size CONSTANT int := 5000;
    last_id text := '';
    batch_last_id text;
BEGIN
    LOOP
        SELECT max(id) INTO batch_last_id
        FROM (
            SELECT id FROM workflows
            WHERE id > last_id
            ORDER BY id
            LIMIT batch_size
        ) batch;

        EXIT WHEN batch_last_id IS NULL;

        UPDATE workflows w
        SET owner_user_id = c.user_id
        FROM chats c
        WHERE w.id > last_id
          AND w.id <= batch_last_id
          AND w.chat_id = c.id
          AND w.owner_user_id IS NULL;

        last_id := batch_last_id;
        COMMIT;
    END LOOP;
END
$$;
-- +goose StatementEnd

-- Runs are listed and reaped per owner once identity moves here, and a
-- partial index keeps the NULLs (pre-backfill leftovers, and later the
-- chatless runs this enables) out of it.
--
-- Dropped first because a CONCURRENTLY build that fails leaves an INVALID
-- index behind, which IF NOT EXISTS would then accept as done — a re-run would
-- "succeed" with an index the planner never uses.
DROP INDEX CONCURRENTLY IF EXISTS idx_workflows_owner_user_id;

CREATE INDEX CONCURRENTLY idx_workflows_owner_user_id
    ON workflows (owner_user_id)
    WHERE owner_user_id IS NOT NULL;

-- +goose Down
-- Lossy, and deliberately so. Rolling back discards each run's own identity
-- and returns it to borrowing one from its chat — which is correct while
-- chat_id is still NOT NULL, because every row can still find its owner that
-- way. It stops being safe once chatless runs exist, and the migration that
-- makes chat_id nullable is the one that has to say so.
DROP INDEX CONCURRENTLY IF EXISTS idx_workflows_owner_user_id;

ALTER TABLE workflows
    DROP COLUMN IF EXISTS owner_user_id;

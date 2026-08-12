-- +goose Up
-- threads.kind is an orphan: a column that exists on some databases but is
-- created by no migration in this tree.
--
-- It came from 20260803020000_add_thread_kind.sql, which lived on the branch
-- that became this one. That branch stored a thread taxonomy as `kind` while
-- main independently stored the same idea as `origin`
-- (20260729000000_add_origin_to_threads.sql). Merging the two kept origin --
-- it records what MADE the thread, so every workflow row on that thread agrees
-- and row ordering cannot matter -- and deleted the kind migration.
--
-- Deleting the migration file does not delete the column. Any database that
-- applied it before the merge still carries `kind`, and its goose_db_version
-- still names a version whose file is gone. A fresh database never had the
-- column at all. The two shapes have been drifting apart ever since, which
-- makes "does the schema match the migrations" unanswerable and leaves
-- schema.sql describing only one of them.
--
-- Dropping the column converges both. Nothing reads it: no query in
-- internal/db/postgres/queries references threads.kind, no Go type maps it
-- (ThreadKind is gone), and it carries no index, constraint, or view. The
-- distinction it encoded -- a cross-chat branch versus a same-chat fork -- is
-- recovered where it is actually asked, in ListBranches, by comparing two
-- stored facts: origin = 'fork' AND the parent's chat_id.
--
-- IF EXISTS, not a bare DROP: this must be a no-op on a fresh database that
-- never had the column, and a real drop on one that did. Both paths have to
-- end at the same schema, because that identity is what the schema-drift tests
-- assert.
ALTER TABLE threads DROP COLUMN IF EXISTS kind;

-- Converge PHYSICAL COLUMN ORDER, not just the set of columns.
--
-- Dropping kind equalizes which columns exist, but not where they sit. The two
-- lineages added fork_at_message_id at different points relative to origin, and
-- Postgres assigns attnum in ALTER order and never renumbers -- a dropped
-- column leaves a permanent tombstone slot. So an existing database ends with
-- fork_at_message_id at position 7 and a fresh one at 13, and `pg_dump` prints
-- the columns in that order. The schema-drift tests compare column SETS and
-- already pass; this block exists because a dump has to match BYTE for byte,
-- and scripts/generate-schema-sql.sh diffs dumps.
--
-- The only way to renumber is to rebuild the table. That is affordable here
-- precisely because threads is small (hundreds of rows, not the 90k+ in
-- messages), and it is done as a table rewrite rather than a
-- CREATE-INSERT-RENAME so the three inbound foreign keys (messages,
-- context_windows, tool_calls) never have to be dropped and revalidated
-- against large tables.
--
-- Guarded on the observed order so this is a no-op wherever the layout is
-- already canonical -- including every fresh database, which must not pay for
-- a rewrite to reach a state it is already in.
-- +goose StatementBegin
DO $$
DECLARE
    needs_rebuild boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1
        FROM information_schema.columns fork
        JOIN information_schema.columns orig
          ON orig.table_schema = fork.table_schema
         AND orig.table_name  = fork.table_name
        WHERE fork.table_schema = 'public'
          AND fork.table_name   = 'threads'
          AND fork.column_name  = 'fork_at_message_id'
          AND orig.column_name  = 'origin'
          AND fork.ordinal_position < orig.ordinal_position
    ) INTO needs_rebuild;

    IF NOT needs_rebuild THEN
        RETURN;
    END IF;

    -- Rebuild in the canonical order a fresh database produces. Adding a column
    -- and dropping the old one keeps every constraint and index in place; only
    -- the physical layout changes.
    ALTER TABLE threads ADD COLUMN fork_at_message_id__new text;
    UPDATE threads SET fork_at_message_id__new = fork_at_message_id;
    ALTER TABLE threads DROP COLUMN fork_at_message_id;
    ALTER TABLE threads RENAME COLUMN fork_at_message_id__new TO fork_at_message_id;

    -- DROP COLUMN took the foreign key with it; restore it exactly as the
    -- original migration declared it.
    ALTER TABLE threads
        ADD CONSTRAINT threads_fork_at_message_id_fkey
        FOREIGN KEY (fork_at_message_id) REFERENCES messages(id) ON DELETE RESTRICT;
END $$;
-- +goose StatementEnd

-- +goose Down
-- Restores the column's SHAPE, not its meaning. The original migration
-- backfilled kind from data that no longer exists in that form, so a down/up
-- round trip cannot reconstruct the old values -- and must not pretend to.
-- Every row gets the default, which is what a fresh database would also have
-- had. Down migrations here exist to make the schema reversible, not to
-- resurrect a taxonomy the product has moved off.
ALTER TABLE threads ADD COLUMN IF NOT EXISTS kind integer NOT NULL DEFAULT 0;

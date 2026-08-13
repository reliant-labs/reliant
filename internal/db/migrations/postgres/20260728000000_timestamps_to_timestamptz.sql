-- +goose Up
-- Every timestamp column becomes `timestamp WITH time zone`.
--
-- The bug this removes: `timestamp without time zone` stores whatever wall
-- clock it is handed and DISCARDS the offset. Correctness therefore depended on
-- every writer remembering to call .UTC() — a convention enforced by nothing
-- but comments. It held in most places and not in the rest, and the failure is
-- silent: step_executions.created_at was written with time.Now() (local) while
-- workflows.created_at was written with time.Now().UTC(), into the same
-- database, so every ordering across the two was wrong by the host's offset
-- with nothing in the data to reveal it. Measured skew: exactly the host's
-- 4-hour EDT offset.
--
-- With timestamptz the driver's offset is preserved and normalized on the way
-- in, so a bare time.Now() and a time.Now().UTC() store the same instant. The
-- bug stops being something a reviewer has to catch, because it stops being
-- representable.
--
-- Two details make this cheap and correct:
--
--   SET LOCAL TimeZone TO 'UTC' — the implicit timestamp→timestamptz cast
--   interprets existing naked values in the SESSION zone, and UTC is the right
--   interpretation because it is what the overwhelming majority of writers
--   stored. It also lets Postgres 12+ skip the table rewrite: the conversion is
--   then binary-compatible, so the ALTER is a catalog update rather than a full
--   rewrite of every table under ACCESS EXCLUSIVE.
--
--   No USING clause — an explicit USING expression forces the rewrite that the
--   line above exists to avoid.
--
-- Rows written in local time before this migration stay off by the offset in
-- effect when they were written. That offset is not recoverable from the data
-- (the value carries no zone, and the database server's zone is not the
-- application's), so nothing here pretends to recover it.
--
-- Columns and views are both discovered from the catalog rather than listed.
-- internal/db/postgres/schema.sql — the sqlc input — has drifted from the
-- migrations: it is missing tables that exist (provider_backoff,
-- daemon_attachment, daemon_pats, claude_auth_tokens) and still carries a
-- chats_with_activity definition referencing a `yields` table that no longer
-- exists. A migration written from that file would convert whichever columns
-- happened to be listed there and would recreate the view wrong.

-- +goose StatementBegin
DO $$
DECLARE
    r RECORD;
    v RECORD;
    view_names TEXT[] := ARRAY[]::TEXT[];
    view_defs TEXT[] := ARRAY[]::TEXT[];
    converted INT := 0;
BEGIN
    PERFORM set_config('TimeZone', 'UTC', true);

    -- A view that selects a column blocks ALTER COLUMN TYPE on it, so every
    -- view is captured as it currently stands, dropped, and rebuilt verbatim
    -- afterwards.
    --
    -- EXTENSION-OWNED OBJECTS ARE EXCLUDED (the pg_depend deptype='e' check).
    -- `public` is not necessarily ours alone: a managed Postgres installs its
    -- own extensions there, and Postgres REFUSES to drop an object an
    -- extension owns —
    --   ERROR: cannot drop view g_agg_stat_statements because extension
    --          google_columnar_engine requires it (SQLSTATE 2BP01)
    -- — which aborts the whole DO block and fails the migration before a
    -- single column is converted. Observed on AlloyDB, whose
    -- google_columnar_engine extension owns eleven `g_columnar_*` views in
    -- public; the same applies to any managed provider that does this.
    --
    -- Excluding them is correct, not a workaround: those views select from
    -- the extension's own catalogs, never from our tables, so they cannot
    -- block a column conversion. We must not drop objects we do not own, and
    -- we do not need to.
    FOR v IN
        SELECT c.relname AS name, pg_get_viewdef(c.oid, true) AS def
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public' AND c.relkind = 'v'
          AND NOT EXISTS (
              SELECT 1 FROM pg_depend d
              WHERE d.objid = c.oid AND d.deptype = 'e'
          )
        ORDER BY c.relname
    LOOP
        view_names := view_names || v.name;
        view_defs := view_defs || v.def;
        EXECUTE format('DROP VIEW public.%I', v.name);
    END LOOP;

    -- Extension-owned TABLES are excluded for the same reason: altering a
    -- column of a table an extension owns is not ours to do, and a managed
    -- provider's bookkeeping tables have no bearing on our timestamp bug.
    FOR r IN
        SELECT c.table_name, c.column_name
        FROM information_schema.columns c
        JOIN information_schema.tables t
          ON t.table_schema = c.table_schema
         AND t.table_name = c.table_name
        WHERE c.table_schema = 'public'
          AND t.table_type = 'BASE TABLE'
          AND c.data_type = 'timestamp without time zone'
          AND NOT EXISTS (
              SELECT 1
              FROM pg_class pc
              JOIN pg_namespace pn ON pn.oid = pc.relnamespace
              JOIN pg_depend d ON d.objid = pc.oid AND d.deptype = 'e'
              WHERE pn.nspname = c.table_schema AND pc.relname = c.table_name
          )
        ORDER BY c.table_name, c.column_name
    LOOP
        EXECUTE format(
            'ALTER TABLE public.%I ALTER COLUMN %I TYPE timestamptz',
            r.table_name, r.column_name);
        converted := converted + 1;
    END LOOP;

    FOR i IN 1 .. COALESCE(array_length(view_names, 1), 0) LOOP
        EXECUTE format('CREATE VIEW public.%I AS %s', view_names[i], view_defs[i]);
    END LOOP;

    RAISE NOTICE 'converted % timestamp columns to timestamptz', converted;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
DECLARE
    r RECORD;
    v RECORD;
    view_names TEXT[] := ARRAY[]::TEXT[];
    view_defs TEXT[] := ARRAY[]::TEXT[];
BEGIN
    PERFORM set_config('TimeZone', 'UTC', true);

    -- Extension-owned views/tables excluded — see the Up block for why
    -- (a managed provider owns objects in `public` that Postgres refuses to
    -- drop, which aborts the whole DO block).
    FOR v IN
        SELECT c.relname AS name, pg_get_viewdef(c.oid, true) AS def
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public' AND c.relkind = 'v'
          AND NOT EXISTS (
              SELECT 1 FROM pg_depend d
              WHERE d.objid = c.oid AND d.deptype = 'e'
          )
        ORDER BY c.relname
    LOOP
        view_names := view_names || v.name;
        view_defs := view_defs || v.def;
        EXECUTE format('DROP VIEW public.%I', v.name);
    END LOOP;

    FOR r IN
        SELECT c.table_name, c.column_name
        FROM information_schema.columns c
        JOIN information_schema.tables t
          ON t.table_schema = c.table_schema
         AND t.table_name = c.table_name
        WHERE c.table_schema = 'public'
          AND t.table_type = 'BASE TABLE'
          AND c.data_type = 'timestamp with time zone'
          AND NOT EXISTS (
              SELECT 1
              FROM pg_class pc
              JOIN pg_namespace pn ON pn.oid = pc.relnamespace
              JOIN pg_depend d ON d.objid = pc.oid AND d.deptype = 'e'
              WHERE pn.nspname = c.table_schema AND pc.relname = c.table_name
          )
        ORDER BY c.table_name, c.column_name
    LOOP
        EXECUTE format(
            'ALTER TABLE public.%I ALTER COLUMN %I TYPE timestamp',
            r.table_name, r.column_name);
    END LOOP;

    FOR i IN 1 .. COALESCE(array_length(view_names, 1), 0) LOOP
        EXECUTE format('CREATE VIEW public.%I AS %s', view_names[i], view_defs[i]);
    END LOOP;
END $$;
-- +goose StatementEnd

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
    FOR v IN
        SELECT c.relname AS name, pg_get_viewdef(c.oid, true) AS def
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public' AND c.relkind = 'v'
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
          AND c.data_type = 'timestamp without time zone'
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

    FOR v IN
        SELECT c.relname AS name, pg_get_viewdef(c.oid, true) AS def
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public' AND c.relkind = 'v'
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

-- forge:backward-compatible — a session default for this role in this database; no schema object changes, and every release's statements run unchanged with it.

-- Migration: chat_db_role_jit_off
--
-- Turn Postgres's JIT compilation off for the role that runs reliant, in
-- reliant's database.
--
-- JIT compiles a query's expressions to machine code when the planner's
-- cost estimate passes jit_above_cost (100,000). The chat reads run
-- chats_with_activity's per-chat subqueries for every row they return, and
-- the planner prices each of those pessimistically, so listing 300 chats was
-- estimated well past the threshold while executing in ~35ms. Postgres then
-- compiled ~290 functions on every execution, and threw them away: on a
-- scratch Postgres 16 seeded to prod scale, ListChats across all projects
-- for a 2,000-chat user spent 210ms of its 250ms in JIT (Generation 17ms,
-- Optimization 13ms, Emission 181ms). JIT is never cached between
-- executions, so a prepared statement pays it every time. It pays off for
-- long analytic scans, which this database does not run.
--
-- Scoped to CURRENT_USER IN DATABASE current_database(): an ordinary role
-- may set its own defaults, so this needs no superuser, and it leaves every
-- other database and role on a shared server alone. It takes effect for
-- sessions opened after it commits: the pools' connections pick it up as
-- they are recycled (ConnMaxLifetime, PgBouncer's server_lifetime).
DO $$
BEGIN
    EXECUTE format('ALTER ROLE CURRENT_USER IN DATABASE %I SET jit = off', current_database());
END
$$;

---
name: dashboards
description: Dashboards over your database — the forge_dash role and its lifecycle (ManagedDatabase.dashboards in KCL turns its login on), granting it exactly the columns a panel needs with an ordinary migration, revoking in a later one, the migrator's read-only check, the recorded limits (row cap, timeout, concurrency), kubedial credentials by role, and why the allowlist is Postgres privileges rather than an SQL parser.
---

# Dashboards over your database

Live dashboard panels (a spend chart, a cohort table) can query your application's own Postgres as one role, `forge_dash`. forge owns the role's **lifecycle**; **you** decide what it reads, with ordinary migrations. The summary is in the `db` skill; this is the depth.

| forge does | you do |
|---|---|
| creates `forge_dash` (NOLOGIN) in the cluster before any migration runs | write a migration that GRANTs it the columns a panel needs |
| lets it log in, with a password Secret and a connection limit, when the database enables dashboards | write a later migration that REVOKEs what a panel no longer needs |
| fails the migration run if the role can do anything but read | keep PII and secrets out of every GRANT |

## 1. The database turns dashboards on (KCL)

```python
forge.ManagedDatabase {
    name = "orders"
    dashboards = forge.DatabaseDashboards {
        enabled = True          # default False
        row_cap = 1000          # 1-10000: most rows one panel query returns
        timeout = "5s"          # "1s".."10s": statement timeout of one panel query
        max_concurrency = 2     # 1-4: panel queries at once
    }
}
```

Enabled, the ManagedDatabase renders `forge_dash` as a CNPG managed role that can log in (no role membership, connection limit `max_concurrency + 1`), with its password in the `<name>-forge-dash` Secret, minted the way `<name>-forge-ro` is. Absent or `enabled = False`, the role still renders, as `ensure: present` with `login: false` and `disablePassword: true`: it cannot log in and has no password.

**Off means NOLOGIN, and your grants stay.** Nothing revokes them: they are your migrations. They are unusable while the role cannot log in, and they serve again the moment it can. Turning dashboards back on flips the same entry, and the `-forge-dash` Secret was kept, so the password does not rotate. It is deliberately not `ensure: absent`: CNPG removes a role with a plain `DROP ROLE`, which Postgres refuses (`2BP01`) once the role holds a grant, so the role would keep its login and its password.

A value out of range fails the render naming the field, and `dashboards` set on both the wrapper and its `spec` is refused. The limits are recorded in the release shape (`databases[].dashboards`, bundle feature `shape-database-dashboards`) and the control plane enforces them from there; an env that records nothing reads as **disabled**, never open.

## 2. Grant a panel its columns (a migration)

```bash
forge db migration new dashboard_spend_by_model
```

```sql
-- forge:backward-compatible — grants only; no schema change
GRANT SELECT (id, created_at, model, cost_cents) ON messages TO forge_dash;
```

That is the whole mechanism. The migration applies in dev, in tests, in CI and in production alike, with dashboards on or off, because the migrator has created the role before it runs. Follow these rules for every GRANT:

1. **Grant exactly the columns the panel's query reads.** Write the panel's SQL first, list every column it touches (in `SELECT`, `WHERE`, `GROUP BY`, `JOIN ... ON`, `ORDER BY`), and grant those. A query naming a column the role lacks fails with `permission denied for table`, which is how you find the one you missed. `SELECT *` fails on any table the role cannot read whole, so do not write panels with it.
2. **Prefer a column list to a whole table.** `GRANT SELECT ON messages TO forge_dash` covers every column the table will EVER have, including a secret one added next quarter. A column list does not grow. Grant a whole table only when every current column is fine to show and the table cannot plausibly gain a sensitive one (a lookup table of plan names).
3. **Never grant PII or secrets.** Not emails, names, phone numbers, addresses, IPs, free text a user typed (titles, notes, prompts, messages), and not tokens, keys, password or token hashes, ciphertext, or anything `forge:secret`. When in doubt, check the table's row in `db/grants/readonly/<schema>.<table>_gen.sql`: the columns listed there as withheld (`forge:secret`, or credential-shaped and undecided) are never for a panel. A panel that needs a person groups by an opaque id (`user_id`), not an email.
4. **Grant base-table columns, not views.** A view reads its tables with its owner's privileges, so granting a view grants every column it selects, whatever the column grants say. If a panel needs an aggregate, write it in the panel's SQL over the granted columns.
5. **Another schema needs USAGE too.** `public` is usable by every role by default; for a table in another schema add `GRANT USAGE ON SCHEMA billing TO forge_dash;`. On Postgres 14, and on a database upgraded from it, PUBLIC can also CREATE in `public`, which the check below refuses: `REVOKE CREATE ON SCHEMA public FROM PUBLIC;` (Postgres 15's default), and run it as the owner of `public` (on Postgres 14, the bootstrap superuser `postgres`): any other role's REVOKE "succeeds" with only `WARNING: no privileges could be revoked`.
6. **Grant nothing else, to it or to PUBLIC.** Only `SELECT` (on tables, columns and sequences), schema `USAGE` and database `CONNECT` are allowed; see section 4. What PUBLIC holds, every role holds, `forge_dash` included.

### Removing access is a later migration

Never edit or delete the migration that granted it: it has run in every environment. Revoke in a new one:

```sql
-- forge:backward-compatible — revokes a dashboard grant; no schema change
REVOKE SELECT (cost_cents) ON messages FROM forge_dash;   -- one column
REVOKE ALL ON messages FROM forge_dash;                   -- the whole table, column grants included
```

A column a migration drops takes its grant with it. Renaming a column keeps its grant under the new name.

## 3. The role exists before your first migration

Every migration run (`db migrate up`, the server's boot-time AutoMigrate, a test database) first creates `forge_dash` as `NOLOGIN NOINHERIT` when the cluster has none, and otherwise leaves the existing role exactly as it is, whatever its login and password.

**The role is cluster-wide.** A Postgres role belongs to the server, not to a database. On a server several databases share (a Cloud SQL instance serving two apps, a dev Postgres), there is one `forge_dash`: every database's migrations grant to the same role, PUBLIC's default `CONNECT` lets it connect to all of them, and one leaked credential reaches what every database granted. Give each app its own server when that matters. On a ManagedDatabase, CNPG declares the role too and **adopts** the one the migrator created: its reconciler finds a declared role in the catalog and ALTERs it to the declared attributes (login, password, connection limit, comment) instead of creating it.

One role may not create it: a ManagedDatabase's own application role has no `CREATEROLE`, and CNPG's managed roles can reconcile after the first migration Job starts. The run then continues if no pending migration names `forge_dash`. If one does, the run refuses **before applying anything** (`role forge_dash does not exist and the migrating role may not create it … Nothing was applied`), so the schema is not left dirty; the Job's retry, after CNPG has created the role, succeeds. On a database you run yourself with a migrating role that lacks `CREATEROLE`, have an administrator run `CREATE ROLE forge_dash NOLOGIN` once.

## 4. The migrator's one check: the role can only read

After the migrations, every run asks Postgres what `forge_dash` can **effectively** do (`has_table_privilege` and its siblings, which count what it holds through PUBLIC as well as what was granted to it) and **fails** if it can do anything but read. The error names each object and privilege, with the statement that takes it away:

- `INSERT`, `UPDATE`, `DELETE`, `TRUNCATE`, `REFERENCES`, `TRIGGER` (and `MAINTAIN` on Postgres 17) on any table, view or foreign table, whole or one column;
- a grant option on anything, read privileges included (table or sequence `SELECT`, schema `USAGE`, database `CONNECT`): the role could hand them on;
- `SELECT` granted to PUBLIC on a table or a large object: every role reads it whatever your migrations granted (`REVOKE SELECT ON t FROM PUBLIC`, then grant the roles that need it);
- `USAGE` or `UPDATE` on a sequence (`nextval`, `setval`);
- `CREATE` on any schema (Postgres 14's `public` included: `REVOKE CREATE ON SCHEMA public FROM PUBLIC`, which only the schema's owner can run), and `CREATE` on any database of the cluster;
- `USAGE` on a foreign-data wrapper (`CREATE SERVER`) or a foreign server (`CREATE USER MAPPING`);
- owning anything: a table, schema, function, type, large object or database, in any database. An owner holds every privilege with no grant to see;
- `UPDATE` on a large object, and `SET` or `ALTER SYSTEM` on a parameter (Postgres 15+), granted to the role or to PUBLIC: `ALTER SYSTEM SET archive_command` runs an OS command;
- an `ALTER DEFAULT PRIVILEGES ... TO forge_dash` entry, which would hand it every table created later instead of the columns you named, and one `TO PUBLIC` granting more than `SELECT` (Postgres's own `EXECUTE` on functions and `USAGE` on types are fine): a table your application creates between runs, a partition, would be born writable;
- membership in any role (`pg_read_all_data` would make every column grant moot), and the `SUPERUSER`, `CREATEROLE`, `CREATEDB`, `REPLICATION` and `BYPASSRLS` attributes.

forge never revokes anything itself: PUBLIC's privileges are your schema's, and the fix is a migration you write.

**It detects; it does not prevent.** The offending migration has already committed when the check runs, so every run fails until a new migration revokes it, and the release that over-granted never serves. In between, the previous release keeps serving and a role that can log in holds the privilege. `task test` runs the same check on test databases (`testkit.NewMigratedPostgresDB`, `app.NewMigratedTestDB`, whose databases revoke Postgres 14's `CREATE` on `public` to match 15+), so it fails before review, not in production. That one difference cuts the other way on Postgres 14: `task test` passes while production fails until the owner of `public` has run the REVOKE.

**Allowed, by choice:** `TEMP` on the database (PUBLIC's default) lets a dashboard session create temporary tables, which live in that session alone and cannot touch your data; a `READ ONLY` transaction still lets a session write its own temporary tables. `EXECUTE` on functions is PUBLIC's default and is not refused; see SECURITY DEFINER below.

**Not checked:** objects in another database of the same cluster (that database's own migrator checks them; roles and `CREATE` on databases are checked cluster-wide), and `USAGE` on tablespaces, types and languages, which let the role read nothing.

To see what a panel could read, log in as the role in a test against a migrated database (or `SET ROLE forge_dash;`) and run the panel's SQL.

### SECURITY DEFINER functions are a way around the grants

The boundary holds for every expression the **role** evaluates. A `SECURITY DEFINER` function evaluates its body as its **owner**, so through one the role may execute it reads columns it has no `SELECT` on and, unless the caller's transaction is `READ ONLY`, writes. `pkg/dbquery` runs every panel `READ ONLY`, which blocks the writes; a leaked `forge_dash` password used from `psql` is not, and writes through the function. Postgres grants `EXECUTE` on every new function to `PUBLIC` by default. `forge_ro` has the same exposure.

forge generates no such function, but it is real. Every migration run lists the `SECURITY DEFINER` functions `forge_dash` or `forge_ro` can execute and logs a warning naming them, whether or not dashboards are on, so a dev or test run shows it (it never fails the run). Fix it at the function: `REVOKE EXECUTE ON FUNCTION f(...) FROM PUBLIC;` (`forge db squash` keeps that revoke), or do not write a definer function over a sensitive table.

### Not bounded by the privileges

The role cannot set per-role GUCs, and inside a read-only transaction `set_config('work_mem', ...)` and `set_config('statement_timeout', '0', true)` still succeed, so the privileges alone do not bound a query's memory. `dbquery`'s guard refuses `SET`, `RESET`, `set_config()` and the functions that run SQL text they are handed (`ts_stat`, `ts_rewrite`, `query_to_xml`, `dblink`, `crosstab`, ...), and `pkg/dbquery`, which runs panels, sets its timeouts for the panel's own transaction and keeps a client-side deadline behind them. It refuses a denied name wherever it appears (attribute notation calls a function without parentheses) and every `U&"..."` identifier. A denylist over SQL is a speed bump, not a boundary: a cluster-level `temp_file_limit` is the real backstop for a panel's memory, so set one. The caller's explicit `READ ONLY` transaction is what keeps a panel from writing, beside the privileges.

## Why the allowlist is Postgres privileges, not an SQL parser

A panel runs arbitrary SQL, so something must decide which tables and columns it may touch. A parser-based allowlist (extract the table names, check them, forward the query) is a second SQL grammar that disagrees with Postgres's about subqueries, CTEs, `select *`, `row_to_json(t)`, `upper(col)`, set-returning functions and whatever the next release adds; every disagreement is a hole, and the parser can only ever be as complete as the day it was written. A column the role has no `SELECT` privilege on is refused by the server however the SQL reaches it, which is a check that cannot be out-parsed. So the parser (if any) is a convenience for good error messages and the privileges are the boundary, and the same reasoning keeps the role out of views and out of every role membership.

Per-query bounds (row cap, statement timeout, concurrency) are the caller's: whatever connects as `forge_dash` (`pkg/dbquery` over `pkg/dbquery/kubedial`, `Client.CredentialsFor(ctx, db, kubedial.RoleDashboard)`) applies them from the recorded shape. `kubedial.PolicyRulesFor(kubedial.RoleDashboard, dbs...)` is the Kubernetes Role such a caller needs, and it names `<name>-forge-dash` and not `<name>-forge-ro`: a leaked panel runner reaches the narrower role only.

## Checklist: adding a panel

1. Write the panel's SQL. List every column it reads.
2. Drop any column that is PII, free text or a secret; change the panel to group by an id instead.
3. `forge db migration new dashboard_<panel>`; write one `GRANT SELECT (<columns>) ON <table> TO forge_dash;` per table, with `-- forge:backward-compatible — grants only`.
4. `task test:short -- ./internal/<pkg>/...` (or any test that migrates a database): the migrator's read-only check runs there.
5. Turn dashboards on for the database in KCL if they are not already.

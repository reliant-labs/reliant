---
name: query
description: Read an environment's app data with forge db query <env> — one read-only statement, the transports (direct, port-forward, control plane, bring-your-own `--addr`), the env's data_access policy (open, audited, deny), the forge_ro role that cannot read forge:secret columns, the JSON document and exit codes.
---

# Reading an env's data: `forge db query`

`forge db query <env> "<sql>"` runs ONE read-only statement against an environment's app database and prints the rows. It is the database leg of the debug loop: a trace or an issue says what failed, and the rows say what state caused it.

```bash
forge db query dev "select count(*) from jobs where crew_id is null"
forge db query dev -f report.sql --format csv > report.csv
forge db query dev "select * from invoices order by created_at desc limit 5" --json
forge db query preview --database ledger "select id, status from entries limit 20"
forge db query staging "select status, count(*) from jobs group by 1" --confirm-env staging --reason "ISS-7f3a"
```

**The env is required, dev included.** A query names the database it reads; there is no default env to read by accident.

## What a statement may do

Every query runs in a `READ ONLY` transaction with a statement timeout and a lock timeout, as one statement over the extended protocol, then rolls back. Writes, `nextval()`, DDL and a second statement are refused. A lexical guard refuses them first so the message is readable; Postgres refuses them anyway. `SELECT`, `WITH`, `TABLE`, `VALUES`, `SHOW` and `EXPLAIN` lead a statement. `EXPLAIN ANALYZE` is allowed only over a read: a `SELECT`, `TABLE`, `VALUES`, or a `WITH` with no data-modifying CTE (no `INSERT`, `UPDATE`, `DELETE` or `MERGE` statement in it), and not `FOR UPDATE`. A column or table named `update` or `delete` is fine. `SET`, `RESET`, `set_config()` and the functions that run SQL text or reach the server's administration (`ts_stat`, `ts_rewrite`, `query_to_xml`, `dblink`, advisory locks, large objects, `pg_terminate_backend`, ...) are refused by name.

| Limit | Default | Flag | Maximum |
|---|---|---|---|
| rows | 200 | `--max-rows` | 10,000 |
| statement timeout | 5s | `--timeout` | 30s |
| one cell | 4 KiB, then cut with a marker | — | — |
| whole response | 4 MiB | — | — |

A cap that cuts the result sets `truncated` (and `truncated_by`: `rows` or `bytes`). Narrow the query rather than raising the cap.

## Which database, and how forge reaches it

The workload decides (`--workload`, default `api`):

| Workload runs | Transport | Connects as |
|---|---|---|
| OnHost / OnCompose | **direct**: the workload's resolved `DATABASE_URL`, the same value `forge env exec` gives the process | whatever that URL names |
| OnCluster, in an env declared `lifecycle = "local"` or `"ephemeral"` | **port-forward**: a pods/portforward stream through your kube context's API server (needs `pods/portforward` create, `endpointslices` list and `secrets` get on the two database Secrets in the env's namespace) to the env's ManagedDatabase (`<name>-ro` when it runs standbys, else `<name>-rw`) | `forge_ro` |
| OnHosted | **control plane**: `AppDatabaseService.QueryDatabase` runs the same read-only core inside the platform (`<name>-ro` when there are replicas) and writes an audit row and an activity event, with your credential (under Reliant, the helper) | `forge_ro` |
| OnCluster in a real env, its ManagedDatabase | not reachable from forge yet: exit 2, unless you pass `--addr` (below) | — |
| any env, a declared `forge.ExternalDatabase` | **port-forward** to a ready pod behind its `service` (below), any lifecycle | the role it declares |

`--database <name>` picks the database when the env declares several (ManagedDatabases and ExternalDatabases share one namespace of names).

## A database forge does not run: `forge.ExternalDatabase`

Cloud SQL, RDS, a database behind a pooler: forge renders nothing for it, but declaring it on the env (`Bundle.external_databases`) gives `forge db query` a route to it, on a real env too. Declaring it is the opt-in:

```kcl
external_databases = [forge.ExternalDatabase {
    name = "ledger"
    service = "pgbouncer"           # the in-cluster Service in front of it (its 5432, or its only port)
    database = "ledger_prod"        # the database, or the pooler's pool, to open
    username = "ledger_ro"          # make it a read-only role
    password_secret = "ledger-ro"   # a Secret in the namespace; key `password_key` (default "password")
}]
```

```bash
forge db query prod "select count(*) from entries" --database ledger --confirm-env prod --reason "ISS-7f3a"
```

- **Where**: `runtime = forge.OnCluster {target}` names the cluster (kube context) and namespace of `service`; unset, the env's primary cluster target (`namespace` overrides it).
- **Transport**: the same pods/portforward stream as a ManagedDatabase's, with your kube context (`pods/portforward` create, `endpointslices` list, `secrets` get on `password_secret`). A port-forward enters the pod, so the service's NetworkPolicy does not stand in the way. No `--workload` is needed: `--database` names it, or it is the env's only database.
- **Policy** is the env's `data_access`, as everywhere: a real env is `audited`, judged before the password Secret is read.
- **Read-only**: every statement runs in a READ ONLY transaction Postgres enforces, so a write is refused even from a role that may write. On a real env forge checks the role (below): a `username` that can write is refused. Point `username` at `forge_ro` and it is also what keeps a query from reading `forge:secret` columns. The migrator creates `forge_ro` NOLOGIN on every run whose migrating role may create roles, and converges its generated column grants (`db/grants/readonly/`), so the role and its grants already exist on the external database. What stays yours is its login: `ALTER ROLE forge_ro LOGIN PASSWORD … CONNECTION LIMIT …` from a deploy step, the password in `password_secret`, and an entry in the pooler's user list.
- **TLS** is `sslmode`: `require` (default: encrypted, not verified, since the tunnel ends at a pod) or `disable`. `--addr` does not apply.

### Checking `forge_ro` after the first migration, and undoing it

The migration log says what the run did: `read-only role created (NOLOGIN) and its grants applied` (it created the role), `read-only grants applied` (the role already existed and was left as it was), or `read-only role not provisioned … may not create it` (the migrating role lacks CREATEROLE: nothing was created or granted). Then check the catalog, connected to the external database as an administrator:

```sql
-- The role: a created one is rolcanlogin = false, rolinherit = false, and no superuser, createrole, createdb, replication or bypassrls.
-- A role that existed before keeps exactly the attributes it had: compare with what you recorded before the deploy.
SELECT rolname, rolcanlogin, rolinherit, rolsuper, rolcreaterole, rolcreatedb, rolreplication, rolbypassrls, rolconnlimit
  FROM pg_roles WHERE rolname = 'forge_ro';

-- Its privileges: column SELECT only, and no write, no table-level grant, no sequence.
SELECT table_schema, table_name, privilege_type, count(*) AS columns
  FROM information_schema.column_privileges WHERE grantee = 'forge_ro' GROUP BY 1, 2, 3 ORDER BY 1, 2;
SELECT table_schema, table_name, privilege_type
  FROM information_schema.table_privileges WHERE grantee = 'forge_ro';   -- expect no rows
SELECT has_column_privilege('forge_ro', '<table>', '<secret column>', 'SELECT');  -- expect false for every forge:secret column
```

Once the deploy has given the role LOGIN, check the boundary as the reader itself, through `forge db query`: `select count(*) from <table>` answers, `select <secret column> from <table>` is refused (`permission_denied`, exit 1), and `delete from <table>` is refused (read-only, exit 1).

**Undoing it.** A NOLOGIN role with column SELECT grants lets nobody in, so leaving it is safe; while this release migrates the database, a dropped `forge_ro` is created again on the next run. In order, as far as you need to go:

1. **Stop reading as it**: `ALTER ROLE forge_ro NOLOGIN;` then end its sessions with `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = 'forge_ro';`, and remove it from the pooler's user list and `password_secret`. Nothing forge runs logs in as `forge_ro`, so the application is unaffected.
2. **Stop forge creating it**: run the migrations as a role without CREATEROLE (the run then reports the role missing and applies no grants), or pin the previous forge release, which applies grants only to a `forge_ro` that already exists.
3. **Revoke its privileges, never its objects.** Do not run `DROP OWNED BY forge_ro`: it drops every object the role owns, data included, not only its grants, and a `forge_ro` the run adopted (the log said `read-only grants applied`) is whatever role was there before and may own tables. Revoke instead, in your database, connected as the owner of its tables, for each schema the grants cover: `REVOKE ALL ON ALL TABLES IN SCHEMA <schema> FROM forge_ro; REVOKE ALL ON ALL SEQUENCES IN SCHEMA <schema> FROM forge_ro; REVOKE ALL ON SCHEMA <schema> FROM forge_ro;` (a table-level REVOKE removes its column grants too). If the role was adopted, the run's grants replaced the ones it had in this database: re-grant what you recorded before the deploy, and stop here.
4. **Drop it** only when the run created it (the log said `read-only role created`). A role is cluster-wide, and on a shared instance another database may grant to it too (another forge project's migrations do). First check it owns nothing anywhere: `SELECT d.datname, s.classid::regclass, s.objid FROM pg_shdepend s LEFT JOIN pg_database d ON d.oid = s.dbid WHERE s.refobjid = 'forge_ro'::regrole AND s.deptype = 'o';` must return no rows; if it returns any, keep the role and stop at step 3. Then find every database that still grants to it: `SELECT DISTINCT d.datname FROM pg_shdepend s JOIN pg_database d ON d.oid = s.dbid WHERE s.refobjid = 'forge_ro'::regrole;`. If another database still needs it, stop at step 3. Otherwise revoke as in step 3 in each of them, then `DROP ROLE forge_ro;` once. `DROP ROLE` refuses while the role still owns an object or holds a privilege anywhere, so it cannot take data with it.

## Bring your own route: `--addr host:port`

For a database forge cannot route to itself (a real, non-scratch cluster env, or one behind a VPN or bastion) **you supply the address and the network path**; forge builds no fallback transport. `--addr` makes forge dial exactly that address instead of its own transport (compose/host direct, the kube port-forward, or the control plane):

```bash
kubectl -n app-prod port-forward svc/ledger-rw 5432:5432 &
forge db query prod "select status, count(*) from jobs group by 1" \
  --addr localhost:5432 --confirm-env prod --reason "ISS-7f3a"
```

Everything else is unchanged: the same read-only guard, row/byte caps, timeout, formats and `data_access` policy (a real env is `audited`, so `--confirm-env` and `--reason` still apply). The JSON `scope.transport` is `"addr"`.

- **Credentials** come from the same place the other transports use them. A cluster database's `forge_ro` password and CA are read from `<name>-forge-ro` and `<name>-ca` through your kube context; an OnHost / OnCompose workload keeps its `DATABASE_URL` (user, password, database, `sslmode`) and only its host and port are replaced. There is no `--password` flag: a password never goes on the command line.
- **TLS** on a cluster database is `verify-full` against the cluster CA, with server name `<name>-rw.<namespace>.svc` (a name CloudNativePG's certificate carries). That is why it verifies through a tunnel to `localhost`. `--sslmode require` (encrypt, no verification) or `disable` overrides it. A DATABASE_URL workload takes `sslmode` from its URL, and `--sslmode` is refused there.
- **A hosted env refuses `--addr`** (exit 1): the control plane executes those queries as the env's read-only role and audits them, and you hold no credentials for it.
- A connection that fails (the route is down) is exit 2, naming the address.

There is no per-env config key: the address belongs to your machine and your route, not to the env's declaration, so it is a flag only.

## The read-only role

Every ManagedDatabase carries `forge_ro`, a role forge declares on the CNPG Cluster. Its password is in the `<name>-forge-ro` Secret. `forge generate` projects its grants from the applied schema into `db/grants/readonly_gen.sql` and one row file per table under `db/grants/readonly/`, and the migrator applies them after every run. The role may SELECT the columns somebody **decided** it may read, and nothing else: no writes, no sequences, no views.

The boundary is Postgres, not a redactor. `select upper(api_key) from …` is refused (`permission_denied`, exit 1) just like `select api_key`. A column added later is unreadable until a migration converges the grants. Don't grant the role more by hand; the next migration revokes it.

`// forge:secret` on a proto field keeps the column settable on Create/Update and drops only the read path (`<entity>ToProto`). Its second effect is the one Postgres enforces, below: `forge generate` leaves the column out of its table's row in `db/grants/readonly/`, and the migrator applies the grants after every `db migrate up`, so `forge_ro` is refused it through any expression. The role holds column-level `SELECT` only: no write privilege, no table-level `SELECT`, so a column added later is unreadable until `forge generate` has seen whether it is secret.

### Safe by construction: who decides a column

A column is **withheld** when it is declared `forge:secret`, and also when it is **credential-shaped and undecided**. Everything else is granted. So a project cannot expose a credential by forgetting to mark it, and a table nobody thought about is still queryable.

| Declaration | Where | Effect |
|---|---|---|
| `forge:secret` | `// forge:secret` on the entity's proto field, **or** `COMMENT ON COLUMN t.c IS 'forge:secret'` in a migration | withheld. The SQL spelling is the one for a column with no wire field, which is most credential tables |
| `forge:readable <reason>` | `COMMENT ON COLUMN t.c IS 'forge:readable — SHA-256 of the prompt, not of a credential'` | granted although the name looks like a credential. The reason is mandatory; `forge generate` rejects a bare marker |
| neither, name looks like a credential | | withheld **and** `forge lint --db-grants` fails, naming `table.column` and both markers |

`forge:secret` wins if a column declares both. `COMMENT ON` replaces the whole comment, so keep an existing `forge:immutable` etc. in the same string.

**Credential-shaped** is judged on the column's name and type, by `dbgrants.CredentialShaped`:

- *Stems*, matched anywhere in the name with underscores ignored: `password`, `passwd`, `passphrase`, `secret`, `credential`, `token`, `bearer`, `ciphertext`, `plaintext`, `encrypted`, `sealed`, `apikey`, `accesskey`, `secretkey`, `privatekey`, `signingkey`, `encryptionkey`, `authkey`, `masterkey`, `licensekey`, `sessionkey`, `recoverycode`, `backupcode`, `resetcode`, `verificationcode`, `authcode`.
- *Whole words* between underscores (too short to match inside other words): `hash`, `hashed`, `hashes`, `salt`, `jwt`, `otp`, `totp`, `pin`, `pwd`, `cred`, `creds`, `cipher`, `cookie`. `hash` is here for token and password hashes, which are offline-guessing targets; a content hash that is not one takes a `forge:readable`.
- *Not* credential-shaped: a name whose last word describes a credential rather than holding it (`token_id`, `secret_name`, `credential_type`, `api_key_status`, `…_count`, `…_version`, `…_provider`, `…_scope`), and a number, boolean or timestamp column (`input_tokens bigint`, `token_expires_at timestamptz`).
- A **`bytea` column** needs a decision whatever it is called: it is opaque by construction.

The list over-approximates on purpose: a false positive costs one `forge:readable <reason>`, a false negative ships a credential. What a name cannot reveal (`value`, `data`, `config` holding a credential) you mark `forge:secret` yourself. `forge project annotations --kind column` prints both markers.

`forge lint --db-grants` is in the default lint run and gates it:

```
✗ [forge-db-grants-undecided] db/migrations/0003_git.up.sql:7
    git_credentials.access_token looks like a credential and nobody decided it: forge_ro cannot read it
    → in a new migration, declare one: COMMENT ON COLUMN git_credentials.access_token IS 'forge:secret'; …
```

## Who may query: `data_access`

The env declares its policy in KCL, and forge enforces it before connecting:

```
data_access = forge.DataAccess {db_query = "audited"}
```

| `db_query` | Who | Needs |
|---|---|---|
| `open` | anyone with the env | nothing. The default for `lifecycle` local or ephemeral |
| `audited` | a person | `--confirm-env <env>` (the env name again) and `--reason "…"` (at most 500 characters). The default for every other env |
| `deny` | nobody | — |

The policy is recorded in the release shape. For a hosted env the control plane enforces the **recorded** one (not this checkout's KCL) and audits every call; a client flag cannot weaken it. On a hosted audited env it is people only (an org automation token is refused) and needs the `db:query` grant (owners and admins hold it); a machine token on an open env needs the `db:query` scope. One query per person per env at a time, 30 a minute.

On every other transport the policy is judged by the `forge` process you run, so it guards against mistakes, not against someone holding the role's password. What bounds that person is the role, hence the next two rules.

## A real env is read as a role that cannot write

The READ ONLY transaction binds one transaction, not the role: the same role can `pg_terminate_backend` the app's sessions, and in a session of its own (psql with the same password) change anything. So on a real env (`lifecycle` neither local nor ephemeral) forge asks the server what the role it signed in as can do (`dbquery.InspectRole`: the catalogs, not a declaration) and refuses, exit 1, a role with any write grant, an owned table, view or sequence (or SET ROLE to its owner), USAGE or UPDATE on a sequence, EXECUTE on a SECURITY DEFINER function (functions are executable by PUBLIC unless revoked), CREATE on a schema or the database (PUBLIC holds CREATE on `public` before Postgres 15), a role attribute (`SUPERUSER`, `CREATEROLE`, `CREATEDB`, `REPLICATION`, `BYPASSRLS`), or a predefined role such as `pg_signal_backend`, `pg_write_all_data` or `pg_read_server_files`:

| Transport | Role | On a real env |
|---|---|---|
| ManagedDatabase, port-forward or `--addr` | `forge_ro` | passes |
| `forge.ExternalDatabase` | its `username` | passes when SELECT-only; point it at one |
| OnHost / OnCompose `DATABASE_URL`, with or without `--addr` | the app's own role | refused |

`--allow-writable-role` reads as such a role anyway, still in a READ ONLY transaction; the audit record carries `allow_writable_role` and what the role can do (`role_writes`). A scratch env is not checked.

## Every query is recorded

A hosted env's query is recorded by the control plane (its audit log and activity feed). Every query forge runs itself, on the direct, port-forward and `--addr` transports, is recorded where it runs, the refused ones included:

- one JSON line appended to `.forge/audit/<env>.jsonl` in the project (`schema` `forge.db.query.audit/v1`: `id`, `at`, `env`, `transport`, `database`, `policy`, `who` {`user`, `host`, `kube_context`}, `reason`, `role`, `role_writes`, `sql`, `sql_sha256`, `outcome`, `error`, `row_count`, `duration_ms`);
- one structured log line on stderr naming the record (the statement itself stays in the file);
- the record's id in the connection's `application_name` (`forge db query dbq_…`), so the database's own logs and `pg_stat_activity` name it.

A record that cannot be written withholds the rows (exit 2). It is a record, not a control: for a log nobody at the keyboard can edit, turn on the database's own audit logging (pgaudit, or the managed service's data-access logs) for the read-only role.

## Output for agents

`--json` (the same as `--format json`) prints one document to stdout:

```json
{"ok": true, "exit_code": 0, "schema": "forge.db.query/v1",
 "scope": {"env": "dev", "transport": "direct", "workload": "api", "policy": "open"},
 "columns": [{"name": "n", "pg_type": "int8"}], "rows": [[3]],
 "row_count": 1, "truncated": false, "duration_ms": 4, "bytes": 1}
```

- `ok` and `exit_code` are computed from the same error the process exits with.
- On failure `error` carries the message, and `reason` carries a stable subcode such as `multiple_statements` or `statement_timeout`.
- Cells are JSON values. A bigint or numeric beyond 15 digits is a string, so no reader rounds it. Dates, uuids and arrays are Postgres's text. `json`/`jsonb` is inline JSON.
- `scope.database` is present on the port-forward and control-plane transports. On the control-plane transport `scope.policy` is absent (the control plane applied the recorded one) and `audit` carries `id` (the audit_log row), `sql_sha256` and `replica` (`ro` or `rw`). On the others `audit` carries `id`, `sql_sha256` and `record` (the `.forge/audit/<env>.jsonl` file). The table footer names the audit id.
- A hosted refusal keeps the control plane's `reason`: `confirmation_required` (pass `--confirm-env` and `--reason`), `credential_missing` (the env's `forge_ro` is not provisioned yet), `rate_limited`, `query_in_flight`, or the executor's subcode.
- `release` is the release live in the env when the query ran (`version`, `promotion_id`, `promoted_at`, and `unsettled` when its rollout hadn't finished), read from the env's release ledger. It's absent when nothing was promoted yet; an unreadable ledger warns on stderr and never fails the query. `forge env releases at <env> <time>` answers the same question for any moment.

| Exit | Meaning |
|---|---|
| 0 | rows returned |
| 1 | the statement or the request is wrong: the guard, the read-only transaction, a SQL error, a permission error, the env's policy, a writable role on a real env |
| 2 | forge could not look: no database reachable, the tunnel, a timeout, a lost connection, the audit record not written; hosted: the control plane unreachable, signed out, rate limited, or the read-only role not provisioned |

Treat exit 2 as "not answered", never as "no rows".

## Rules

- Never point the query at a database by URL to dodge the policy. `forge db query` is the audited path.
- Never ask for a write through it. Data fixes are migrations or application code, reviewed like any change.
- Prefer aggregates and `limit` to dumping tables: the caps exist so an agent's loop cannot scan a production database.

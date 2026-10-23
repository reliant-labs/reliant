---
name: query
description: Read an environment's app data with forge db query <env> — one read-only statement, the transports (direct, port-forward), the env's data_access policy (open, audited, deny), the forge_ro role that cannot read forge:secret columns, the JSON document and exit codes.
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

Every query runs in a `READ ONLY` transaction with a statement timeout and a lock timeout, as one statement over the extended protocol, then rolls back. Writes, `nextval()`, DDL and a second statement are refused. A lexical guard refuses them first so the message is readable; Postgres refuses them anyway. `SELECT`, `WITH`, `TABLE`, `VALUES`, `SHOW` and `EXPLAIN` lead a statement. `EXPLAIN ANALYZE` is allowed only over a `SELECT`.

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
| OnCluster in a real env | not reachable from forge yet: exit 2 | — |

`--database <name>` picks the ManagedDatabase when a cluster env declares several.

## The read-only role

Every ManagedDatabase carries `forge_ro`, a role forge declares on the CNPG Cluster. Its password is in the `<name>-forge-ro` Secret. `forge generate` projects its grants from the applied schema into `db/grants/readonly_gen.sql`, and the migrator applies them after every run. The role may SELECT the columns somebody **decided** it may read, and nothing else: no writes, no sequences, no views.

The boundary is Postgres, not a redactor. `select upper(api_key) from …` is refused (`permission_denied`, exit 1) just like `select api_key`. A column added later is unreadable until a migration converges the grants. Don't grant the role more by hand; the next migration revokes it.

`// forge:secret` on a proto field keeps the column settable on Create/Update and drops only the read path (`<entity>ToProto`). Its second effect is the one Postgres enforces, below: `forge generate` leaves the column out of `db/grants/readonly_gen.sql`, and the migrator applies that file after every `db migrate up`, so `forge_ro` is refused it through any expression. The role holds column-level `SELECT` only: no write privilege, no table-level `SELECT`, so a column added later is unreadable until `forge generate` has seen whether it is secret.

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
- `scope.database` is present on the port-forward and control-plane transports. On the control-plane transport `scope.policy` is absent (the control plane applied the recorded one) and `audit` carries `id` (the audit_log row), `sql_sha256` and `replica` (`ro` or `rw`); the table footer names the audit id.
- A hosted refusal keeps the control plane's `reason`: `confirmation_required` (pass `--confirm-env` and `--reason`), `credential_missing` (the env's `forge_ro` is not provisioned yet), `rate_limited`, `query_in_flight`, or the executor's subcode.
- `release` is the release live in the env when the query ran (`version`, `promotion_id`, `promoted_at`, and `unsettled` when its rollout hadn't finished), read from the env's release ledger. It's absent when nothing was promoted yet; an unreadable ledger warns on stderr and never fails the query. `forge env releases at <env> <time>` answers the same question for any moment.

| Exit | Meaning |
|---|---|
| 0 | rows returned |
| 1 | the statement or the request is wrong: the guard, the read-only transaction, a SQL error, a permission error, the env's policy |
| 2 | forge could not look: no database reachable, the tunnel, a timeout, a lost connection; hosted: the control plane unreachable, signed out, rate limited, or the read-only role not provisioned |

Treat exit 2 as "not answered", never as "no rows".

## Rules

- Never point the query at a database by URL to dodge the policy. `forge db query` is the audited path.
- Never ask for a write through it. Data fixes are migrations or application code, reviewed like any change.
- Prefer aggregates and `limit` to dumping tables: the caps exist so an agent's loop cannot scan a production database.

---
name: adopt/linters
description: forge's linters on a repo that is not a forge project — which lanes run standalone (migration safety on db/migrations/*.up.sql; in a Go module tracing, time-bucketing, slow tests; go test -json readers), what each flags, the per-file waivers, scoping legacy migrations, and the CI step to add beside the user's own jobs.
emit: both
applies: any
---

# Adopt: forge's linters on your repo

The cheapest proof that forge is worth having: findings on code and migrations
the repo already has, each with `file:line`, the bug class it prevents and the
fix. No forge.yaml, no generated code, any language for the SQL lane. Part of
`adopt` (the menu of entry points); load that for the survey and the estimate.

**Estimate.** ~5 min to first findings; +5 for the CI step.

## Get forge

Any of: `npx -y @reliantlabs/forge <command>` (no install; downloads the
matching binary on first run), `curl -fsSL
https://raw.githubusercontent.com/reliant-labs/forge/main/install.sh | sh`, or
inside Reliant `reliant forge <command>`.

## The lanes that run standalone

Run these by name, from the repo root. Each was run in a repo with no
forge.yaml.

| Command | Needs | Flags |
|---|---|---|
| `forge lint --migration-safety` | SQL in `db/migrations/*.up.sql` | `destructive-change` (DROP COLUMN / TABLE, TRUNCATE), `unsafe-add-not-null-column` (NOT NULL with no default), `non-transactional-statement` (`CREATE INDEX CONCURRENTLY` beside other statements), and `no-down-migration` on every `*.down.sql` (forge rolls forward) |
| `forge lint --tracing` | a Go module | `forge-tracing-log-without-ctx` (`slog.Info` with a ctx in scope; `--fix` rewrites to `InfoContext`), `forge-tracing-raw-http-client` (an `http.Client` with no traced transport) and the other tracing rules (`observability/tracing-lint`) |
| `forge lint --time-bucketing` | a Go module, and `db/migrations` to read column types | two-argument `date_trunc` over a `TIMESTAMPTZ` (a warning; does not fail the run) |
| `forge lint --slow-tests` | a Go module | a test that execs a tool (`git`, `go`, `docker`, …) or dials the network without `testkit.Slow` or a `testing.Short()` skip |
| `go test -short -json ./... \| forge ci test-budget` | a Go module | unmarked tests over the fast-tier budget (2s) |
| `go test -json ./... \| forge ci verify-test-run` | a Go module | packages that skipped so much their pass proves nothing |

**Limits, say them before quoting.**

- Migration safety reads **only** `db/migrations/*.up.sql`. A `migrations/`
  directory, or goose-style `NNN_name.sql` files, are not read, and the lane
  passes having checked nothing. Do not move the user's files to make it look;
  tell them, and offer it only if their layout matches.
- `forge ci migration-safety` and `forge ci vuln-scan` need a forge.yaml; use
  the `forge lint` lanes above.
- A bare `forge lint` also runs golangci-lint (forge's pinned build) in a Go
  module, and lanes meant for forge projects. Prefer the named lanes above. If
  a bare run fails a lane with "could not apply db/migrations to a shadow
  postgres", that lane judges forge's generated database role: ignore it here
  and run the named lanes.

## Waivers: per file, with a reason

A forge.yaml cannot help here: rule dials (`lint.rules`) need a forge project,
and a forge.yaml with only `name:` turns the migrations feature **off**
(`--migration-safety` then reports `feature-disabled`). Do not write one for
this. The waivers are comments in the migration, beside the SQL they cover:

```sql
-- forge:allow-destructive: the column was never read; removed after the 2026-09 release
ALTER TABLE users DROP COLUMN legacy_flag;

-- forge:allow-unsafe-not-null: users is empty in every environment
ALTER TABLE users ADD COLUMN name text NOT NULL;

-- forge:allow-in-transaction: postgres runs this file as one transaction anyway
```

There is no waiver for `no-down-migration`. For a repo that keeps `.down.sql`
files (golang-migrate, dbmate), lint the **up** files a change adds, which is
also what protects them going forward:

```bash
new=$(git diff --name-only --diff-filter=A origin/main -- 'db/migrations/*.up.sql')
forge lint --migration-safety $(printf -- '--scope %s ' $new)
```

With no file to scope, that command lints every migration; guard it in CI (the
snippet below does). `forge lint --migration-safety --json` prints every
finding with `rule`, `file`, `line` and `fix_hint` for a machine to read.

## Steps

1. **Run each lane that fits the survey** and read the output with the user in
   mind. Legacy files produce noise; never edit an applied migration to silence
   it.
2. **Show the user one real finding** and its fix hint. That is the value.
3. **Add a CI step beside theirs** (do not replace their workflow). GitHub
   Actions; translate for GitLab, Jenkins or Buildkite (one binary, one exit
   code):

   ```yaml
   - name: Migration safety (new migrations)
     run: |
       files=$(git diff --name-only --diff-filter=A origin/${{ github.base_ref }} -- 'db/migrations/*.up.sql')
       [ -z "$files" ] && exit 0
       npx -y @reliantlabs/forge lint --migration-safety $(printf -- '--scope %s ' $files)
   - name: Tracing lint
     run: npx -y @reliantlabs/forge lint --tracing
   ```

   The checkout needs the base branch (`fetch-depth: 0`). Pin a forge version
   in CI (`npx -y @reliantlabs/forge@<version>`) so a new release cannot change
   the verdict under them.

## Verify

The lane prints its findings, or a clean verdict as the last line. Then, on a
scratch branch, add `ALTER TABLE t DROP COLUMN c;` as a new
`db/migrations/<n>_x.up.sql` and confirm the lane fails with the fix hint.

## What lights up next

`adopt/monitoring` (the hook), or `adopt/deploy` when the user wants forge to
deploy. In a Go module, `adopt/libraries` gives the fix for each tracing finding
(`observe.NewHTTPClient`, `...Context` logging).

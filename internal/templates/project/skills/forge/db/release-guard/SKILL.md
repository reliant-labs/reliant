---
name: db/release-guard
description: The release-time migration-order guard — what migration manifest a release records, why `forge env build --release` and `forge env deploy` refuse a release an environment's schema has already passed (or, for a shared env, one carrying unmerged migrations), the two waiver flags, and the backfill for releases cut before manifests existed.
---

# The release-time migration-order guard

The migrator's refusal of a passed migration (`*migratekit.MissingMigrationError`)
is the last line of defence, and the worst place to meet it: after the build, in
the target environment, with the new pods crash-looping. So a release records
the migrations it carries, and the cut and the deploy refuse it first.

It happened exactly so to reliant on 2026-10-24: v1.7.52 shipped migration
`20261024035148` to prod, main then merged a LOWER `20261024024221` past a merge
gate nothing ran, and v1.7.54's api-server crash-looped on prod's refusal.

## What a release records

`forge env build <env> --release <v>` records a migration manifest on the
release (`release.Release.Migrations`): every migrations directory its artifacts
embed — the project's own `db/migrations` read from disk (go:embed embeds the
working tree), plus each sibling checkout a ShellBuild ran in that is a forge
project, read from git at the commit the build recorded. Each directory is keyed
by `<repo>/<dir>`, e.g. `github.com/reliant-labs/reliant/db/migrations`, so
releases cut on any machine compare. It is evidence, not identity:
`Release.SameContent` ignores it.

## Refusal 1 — passed migrations

Before the build, at the cut, and at every `forge env deploy <env> <v>`, forge
reads `<env>`'s promotion history, takes the manifests of the releases it
applied (a promotion whose apply FAILED applied nothing and is left out), and
refuses a release that carries a file **none of them carried** at a version
**at or below the highest one any of them did**. That is exactly the file the
env's migrator would refuse. Exit 4, naming the file, the head and the release
that applied it, and the fix:

- not yet merged: `forge db migration rebase <file>`;
- already merged: replace it with a re-runnable copy at a new version (`db/versioning`).

`--allow-passed-migrations` waives it, for the one honest case: the SQL was
applied to the env's database by hand and recorded in `schema_migrations_applied`.

## Refusal 2 — unmerged migrations, for an env on a control plane

A release for an env whose ledger is a control plane (prod, staging) that
carries one of the project's own migrations not on the default branch is
refused (exit 4). An env that applies such a release has a schema ahead of
`main`, and `main`'s next migration can then land below it while passing every
merge-time check — those compare against `main`. Merge the migration first (a
hotfix branch can cherry-pick the merged file). `--allow-unmerged-migrations`
waives it for a hotfix that cannot wait; refusal 1 still guards the next
release. An env on this machine's ledger is exempt: deploying a feature branch
to it is the dev loop.

## What is never a refusal

A ledger that cannot list history, a release cut before manifests existed, a
checkout with no default branch: each is a printed note, and the migrator
remains the backstop.

Releases cut before manifests existed are given one by:

```bash
forge env releases backfill-migrations <env> --since <v> --dry-run   # print what it would record
forge env releases backfill-migrations <env> --since <v>
```

It reads the project's migrations at each release's commit, and a sibling's at
the commit the project's go.mod pinned then (fetch both first). It never
rewrites a recorded manifest; the ledger refuses a different one.

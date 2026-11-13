---
name: db/versioning
description: Migration version numbers — why they are UTC timestamps, the two lint rules that enforce it, and what to do when a migration is refused because the schema already passed its version.
---

# Migration versioning

`forge db migration new <name>` names the file `<YYYYMMDDHHMMSS>_<name>.up.sql`, using UTC. **Never hand-type a version number.**

## Why timestamps and not max+1

A sequential allocator picks `max+1` by reading the migrations directory. That is collision-free on one checkout and collision-prone across branches: two branches cut from the same commit both read the same highest number and both pick the same next one, and neither sees the other until merge.

Measured on one repository: **ten version numbers were each claimed by two different migrations**, and one number was claimed on eight separate branches. Every one had to be renumbered by hand.

The damage is worse than a merge conflict, because a version is the schema's only identity. golang-migrate records one integer — "the schema is at 91" cannot say *which* 91. So whichever file reached a shared database first left the other permanently unapplied, while every later `up` reported the schema current. The first symptom is a query for a column that does not exist.

A UTC timestamp removes the shared counter: two branches allocating at different instants cannot pick the same value without coordinating. Versions become sparse, which nothing depends on — the migrator orders by version and never assumes `version+1` exists.

## Existing sequential migrations are never renamed

A 14-digit timestamp exceeds any 5-digit number by nine orders of magnitude, so **every new migration sorts after every old one** with no renaming, no backfill and no flag day. A project mid-adoption holds both spellings and orders correctly. Adopting this costs nothing.

## The two lint rules

Both are errors in `forge lint`:

| Rule | Fires when |
|---|---|
| `duplicate-migration-version` | two files claim one version. Reported on *every* claimant, so a reviewer reading one file learns it is contested and by which file. Needs no git. |
| `non-timestamp-migration-version` | a **new** migration is not a 14-digit UTC timestamp. |
| `migration-version-below-merged-head` | a migration on this branch is numbered at or below the highest version on your default branch, so it can never be applied. Needs git. |

"New" means *added since the merge-base with your default branch* — exactly the set a pull request can still fix — so pre-existing sequential migrations are never flagged and nobody is asked to renumber history. This rule is what catches an out-of-order version at review time, before a deploy can refuse.

Without git (a shallow clone, a tarball, no default branch) newness is unknowable, and the rule narrows to what is wrong regardless: a 14-or-more-digit version that is not a real calendar instant. `99999999999999` cannot be a sequence number and is not a date, so it is hand-typed and would sort ahead of every genuine timestamp forever.

The timestamp check is a **calendar** check, not a digit count, for that same reason.

### The silent one: `migration-version-below-merged-head`

This is the only version failure that produces **no error anywhere**, which is why it is a lint and not something you can catch by reading a diff.

golang-migrate records one current version and applies only what is **above** it. So a migration on your branch numbered at or below the highest version already on the default branch is not pending — it is invisible. On every database that has applied that head it is silently skipped: `up` reports the schema current, CI passes (a fresh test database applies everything in version order, where being low is harmless), and the first symptom is a production query against a table that was never created.

Neither other rule sees it: the versions are distinct, so there is no duplicate, and the version is a well-formed UTC timestamp.

It becomes reachable whenever a merged migration is **future-dated** — a hand-typed version, or a clock-skewed one. One measured case had a hand-zeroed version about four hours ahead of real time, so every migration created in that window landed underneath it. `forge db migration new` will no longer allocate such a version (the allocator clears the directory's highest version as well as avoiding exact collisions), but a hand-typed version or a file predating that fix still can.

The fix is the same as any stale version, and it is safe because the file has not run anywhere yet:

```bash
forge db migration rebase db/migrations/<file>.up.sql
```

"New" is decided by whether the default branch has a **file** at that version, not by comparing version numbers — a migration that is below the head and genuinely merged is history and is never flagged. Without git the rule goes silent rather than guess.

## When a migration is refused because the schema passed it

golang-migrate applies only migrations **above** the recorded version, so one numbered below it can never run. With timestamps that happens exactly one way: branches A and B are cut, A allocates the earlier timestamp, and **B merges and deploys first**. Main now holds A's migration at a version the database has already passed.

The migrator refuses with `*migratekit.MissingMigrationError` and applies nothing. The fix gives the file a version newer than the schema's, and **how depends on whether the file has merged to your default branch** — the one fact the binary cannot see, so the error states both.

**Not yet merged** — it has run nowhere, so re-version it:

```bash
forge db migration rebase db/migrations/20260101120000_add_users.up.sql
forge db migration rebase --all-pending   # every migration added on this branch
```

**Already merged** — the usual case, because the refusal is usually first seen by the release that carries the merged file (reliant's `20261024024221` merged below a version main and prod already held; the first symptom was the release's crash-looping migrator). Every database created from main since the merge has run it at its old version, so a rename would re-run SQL they already hold, and rebase refuses it. **Replace it**: delete the file and add the same SQL as a new migration (`forge db migration new <name>`), written to be re-runnable — `CREATE … IF NOT EXISTS`, `ADD COLUMN IF NOT EXISTS`, `CREATE INDEX CONCURRENTLY IF NOT EXISTS`. A database that ran the old version runs the copy as a no-op; the database that passed it runs it for the first time. The record the deleted version leaves behind is harmless: the migrator checks only versions the binary embeds. (`TestUpProceedsOnceAMergedMissingMigrationIsReplaced` pins both databases.)

Do not keep a neutralized copy of the old file at its old version: it is still embedded, still below the schema's version, and still refused.

Rebase keeps the name stem, allocates a version above both the directory's max and the default branch's (so a squashed-away migration still on main is accounted for), `git mv`s tracked files, and prints `old → new`.

It **refuses** a migration that is already on the default branch **under the same filename**, with no override. That one has been recorded as applied under its current filename by every database that ran it, so renaming it would leave those databases with a recorded version whose file does not exist — worse than the problem. A migration that has merged keeps its version; repair it with a new forward migration — or, when a database has already passed its version, replace it as above.

The match is on the **filename**, not the number, because what a database recorded is one specific migration under the name it ran. Those come apart in the most common collision there is: two branches allocate in the same second and one merges, so the other holds a *different* migration under a version that is also on main. That file has never been applied anywhere — it is exactly what `duplicate-migration-version` tells you to rebase, and rebase will do it.

Forge deliberately does not apply it out of order. That would make the schema depend on merge order, so A-then-B and B-then-A would produce different databases from the same commit with nothing reporting which one you got — a `DROP COLUMN` landing after the migration that reads the column is a different schema from the reverse. For the least reversible thing that ships, a loud refusal beats a quiet guess.

See `db/deploy-migrations` for the full record model (`schema_migrations_applied`, the mismatch check, and the one-time bootstrap that lets an existing database adopt the check without refusing).

## A baseline that replaces another tool's history

A baseline that squashes goose's history gets an ordinary fresh timestamp from `reliant forge db migration new baseline`. Its version has no relationship to goose's versions, and none is needed: goose's versions live in the baseline's `-- forge:adopts goose …` lines and in `goose_db_version`, never in `schema_migrations`. It must be the **first** (lowest-versioned) migration in the directory. `forge lint`'s `invalid-adopts-directive` rule reports a declaration anywhere else. Everything after it is versioned exactly as above. See `db/deploy-migrations`, "Adopting a database another tool migrated".

A baseline that squashes a FORGE history (`forge db squash`, which writes `-- forge:adopts forge …`) shares `schema_migrations` with the history it replaces, so its number matters: give it a version ABOVE every squashed one (`--to <fresh timestamp>_baseline`). Never one of theirs — the migrator refuses a baseline listing a version it carries. Below them (`00001_baseline`) adoption still works, but the release before the squash can no longer roll back onto an adopted database. See `db/deploy-migrations`, "Squashing a forge history".

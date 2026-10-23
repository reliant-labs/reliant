---
name: recovery
description: Recover a wedged dev database without hand-editing schema_migrations or dropping it — a migration marked dirty (repair, then forge db migrate force), a version behind disk, stale dev rows (forge db seed reset), and why force never verifies what it asserts.
---

# Recovering a wedged dev database

Never hand-edit `schema_migrations` or `DROP DATABASE` — measured, two runs did exactly that, having missed these commands. Match the symptom:

| Symptom | Do this |
|---|---|
| `migration N failed part-way and is marked dirty` — nothing will migrate until the flag clears, and `forge db migrate up` refuses too | repair the schema, then `forge db migrate force N`, then `forge db migrate up` |
| `applied version N is behind latest on disk M` — nothing is broken | `forge db migrate up` |
| dev rows are stale, wrong, or hand-mangled | `forge db seed reset` |

`forge db migrate force` **asserts** a version is applied without verifying it — forge cannot know how much of the failed migration landed. Inspect with `forge db introspect` and finish or undo the partial migration **first**; forcing past SQL that never ran leaves the schema permanently behind what forge thinks is applied. Use it only for a migration that genuinely failed mid-flight, never to skip one.

`seed reset` fixes *data*, not migration state: on a dirty database it refuses just like `seed apply`. Clear the migration state first.

**Correcting a born schema** (an enum gained a value, a constraint was wrong) is a *migration*, not a recovery: write one and `forge generate`.

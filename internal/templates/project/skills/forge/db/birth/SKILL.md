---
name: db/birth
description: Birthing an entity's table from its proto message — `forge scaffold entity --from-proto`, one-time birth, no import path, the birth markers (append-only, secret, read-only, generated, soft-delete) baked into your owned migration, and the proto → SQL column mapping.
---

# Birthing an entity

## Birthing one named entity

Bare `forge scaffold` births every marked message. To birth one — or to birth a message whose CRUD quintet already exists but whose table does not — name it:

```bash
forge scaffold entity order --from-proto tasks
forge scaffold entity --from-proto tasks.Order
forge scaffold entity --from-proto tasks         # batch: quintets with no applied table
```

The `--from-proto <svc>` batch form sweeps `// forge:entity`-marked messages too. Birth is **one-time**: the migration is yours the moment it is written, and forge never re-reads the proto to regenerate or edit it.

**There is no import path** (`--from-schema` is refused). To put a wire contract on a table that already exists, hand-write the matching proto message and run `forge generate` — it projects the ORM and CRUD wiring from the applied schema without writing a migration.

### Birth markers — schema hardening baked into your owned migration

Everything a marker adds lands in the **user-owned birth migration**, never a forge-owned `_gen` file. Entity-level markers tablize the message, so a marked message needs no separate `// forge:entity` line. Run `forge project annotations --kind entity` (and `--kind field`) for the full list, placement and effect. The storage-side nuances:

- **`// forge:append-only`** — a real **DB trigger** in the birth migration raises on any `UPDATE`/`DELETE`, so no bug or compromised caller can rewrite history. The CRUD quintet completes to Create/Get/List only.
- **`// forge:secret`** — stripped from reads, and withheld from the read-only `forge_ro` role by Postgres; credential columns need a decision: `db/query`.
- **`// forge:read-only`** — the input-side mirror: readable but not client-writable (status, computed price, a lifecycle timestamp). The born Create omits it; the generated Update preserves it on a full replace and refuses an `update_mask` naming it (every `forge generate`; vs `forge:immutable`: `db/write-policy`). Your RPCs write it via `db.Update<Entity>Masked`; give the column a DB `DEFAULT` or the first insert lands the zero. Mark it up front: the born `new/page.tsx`, edit form and `handlers_crud_test.go` leave it out.
- **`// forge:generated <expr>`** — births `<col> <type> NOT NULL GENERATED ALWAYS AS (<expr>) STORED`, the rest of the comment line copied verbatim (NOT NULL unless `optional`; implies `forge:read-only`). Birth tries it against the shadow database first and refuses, at the marker's file and line, an expression postgres rejects. See `db/derived-values`.
- **`// forge:soft-delete`** — **OPT-IN**: unmarked entities get no `deleted_at` and hard-delete. Beyond the read filter, `ListAll<Entities>` returns tombstones. The `--soft-delete` flag and a message already carrying a `deleted_at` field do the same thing.

Proto → SQL, in both birth forms: scalars to `NOT NULL` columns with zero defaults, `optional` to nullable, enums to `TEXT` + `CHECK (col IN (...))` (DEFAULT = first non-`_UNSPECIFIED` member), repeated scalars to arrays, maps/nested messages/`Struct`/`ListValue`/`Value` to `JSONB`, an `*_id` string whose stem resolves to a real entity to `TEXT` + an applied `REFERENCES` and index, and a `// forge:generated <expr>` field to a `GENERATED ALWAYS … STORED` column (above). oneof/`Any`/cross-package fields become TODO comment lines, never silently dropped. Envelopes — Request/Response messages, anything carrying `page_size`/`page_token`/`update_mask` — are refused. An enum over an `INTEGER` column you declared (any width, or `INTEGER[]`) also projects: it stores the wire number, and a `CHECK (col IN (1, 2, …))` gives the seeder its vocabulary.

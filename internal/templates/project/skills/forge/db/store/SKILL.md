---
name: store
description: Which tables (and opted-in views) get a generated store and how it is shaped — every applied table is projected whatever its rpcs, the forge:no-entity opt-out, entity names derived from the table, and where CRUD wiring differs.
---

# The generated store

`forge generate` projects the **data layer from the applied schema**, not from
the wire. Every applied BASE TABLE gets:

- an entity struct (`<entity>_orm_gen.go`), its `<E>Field*` / `<E>Columns`
  constants and constraint names;
- package-level delegates (`Create<E>`, `Get<E>ByID`, `List<E>`, `Count<E>`,
  `Update<E>`, `Update<E>Masked`, `Delete<E>`);
- an `<E>Store` interface in `<entity>_store_gen.go`, which also declares the
  aggregate's accessor for it (`Store` itself, a struct, is in `store_gen.go`);
- a scaffold-once `<entity>_repo_ext.go` seam for hand-written queries.

Two answers every store gives the same way. A by-key method (`Get<E>ByID`,
`Get<E>ByPK`, `Update<E>`, `Update<E>Masked`, `Delete<E>`, `Delete<E>ByPK`)
answers a missing row with `svcerr.NotFound("<entity>")`, which is also
`errors.Is(err, orm.ErrNoRows)`. `List<E>` with no `orm.WithLimit` returns every
matching row, uncapped: bound a read yourself, and walk a table that grows
without bound a page at a time (`WhereGt` on the key + `WithOrderBy` +
`WithLimit`).

whether or not any service declares rpcs for the table — a table whose
CRUD-named rpcs are all `// forge:custom`, or that no rpc names at all, still
has its store. **CRUD wiring** (handlers, pages, seed roots) is the separate,
wire-driven projection described in `db`.

## Naming

A table is named from itself: the singular, camel-cased the way protoc-gen-go
spells a message (`message_content_blocks` → `MessageContentBlock`,
`daemon_attachment` → `DaemonAttachment`). `COMMENT ON TABLE t IS
'forge:entity-name=<Name>'` declares another name for a table whose name
derives to the wrong thing. Two tables that project to one name fail
generate, naming both.

A CRUD rpc's entity `<E>` finds its table by that name, then by the plural
(`bookmarks`) and the singular (`daemon_attachment`) of `<E>`; the claimed
table keeps the wire message's name, so the store and the wiring agree. A
unary, unmarked CRUD-named rpc whose entity has a wire message but matches no
table, and whose only implementation is forge's unwired stub (or none), fails
generate naming the rpc and the tables it looked for: write the migration,
declare `forge:entity-name`, or mark the rpc `// forge:custom`.

## Opting out

- One table: `COMMENT ON TABLE t IS 'forge:no-entity'` in a migration. No
  store, and no CRUD wiring over it either (a CRUD-named rpc over it is custom).
- The whole data layer: `//forge:no-orm: <why>` in `internal/db`'s package doc
  (`db/orm-and-safety`).

## Keys

A single-column primary key is the `id` the delegates take: `Get<E>ByID`,
`Delete<E>`… A **composite primary key** gets a store addressed by the
whole key — `Get<E>ByPK(ctx, db, k1, k2)` and `Delete<E>ByPK` (one parameter
per key column), `Upsert<E>` (conflict on every key column), plus
Create/List/Count/Update/UpdateMasked addressing the row by the struct's key
fields — but no CRUD wiring, which keys on one id, and no ULID fill or cursor
pagination. A table with no primary key is reported and skipped.

## Views

A VIEW (regular or materialized) is projected only when it opts in —
`COMMENT ON VIEW v IS 'forge:entity'` (`key=<column>` to name its key, else
`id`) — as a read-only entity: the row struct, `List<E>` / `Count<E>` and
`Get<E>ByID` when it has a key, and a read-only `<E>Store`. Nothing writes. Its
non-key columns are pointers, because the catalog cannot say a view column is
NOT NULL.

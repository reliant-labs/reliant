---
name: authoring
description: Authoring a service proto — mark entities with `// forge:entity` and let scaffold inject their CRUD, declare every custom rpc yourself (CRUD is decided by the exact rpc name; `// forge:custom`), declare the authoritative route when two FKs reach one parent (`forge:ref`), and the shape in four lines (SQL is schema truth, proto is wire truth, an entity is both halves).
---

# Authoring a service proto

## Authoring a service proto

Three rules cover what agents get wrong.

### 1. Mark entities, and let scaffold inject their CRUD

Give the message a leading `// forge:entity` comment. Bare `forge scaffold`
injects the missing CRUD quintet (Create/Get/Update/Delete/List) into the
service proto, writes the create-table migration pair, and declares the managed
`id` / `created_at` / `updated_at` fields.

```protobuf
// forge:entity
message Customer {
  string name = 2;
  string email = 3 [(buf.validate.field).string.email = true];
}
```

There is no CLI field grammar — enums, `optional` (nullable), `buf.validate`
rules, `forge:read-only` / `forge:secret` / `forge:append-only`, a same-row
derived column (`forge:generated <sql expr>`) and foreign-key references are
all spelled in the message. Full vocabulary: `forge project annotations`.
Which marker keeps clients off a column your code owns (`forge:read-only`,
`forge:computed`, `forge:generated`, `forge:guards`, `forge:immutable`): the
decision table in `db/write-policy`.

`protovalidate` vendors itself: add `import "buf/validate/validate.proto";` and
`forge generate` pulls the protos into your `proto/` tree on the first run that
imports them. Fixed-length codes take an exact rule — `string.len = N` (ISO
currency 3, country 2) or `string.const` — because `buf lint` rejects
`min_len == max_len`.

### 2. Every custom rpc is yours to declare

**Forge injects CRUD rpcs only.** Authoring a `FooRequest`/`FooResponse` message
pair does not create an rpc — nothing scans for orphan message pairs. If the
`service {}` block does not name it, the rpc does not exist, and you find out at
`go build`.

Declare it in the block yourself:

```protobuf
service EstimateService {
  // CRUD quintet injected here by `forge scaffold`
  rpc RecalculateEstimate(RecalculateEstimateRequest) returns (RecalculateEstimateResponse);
}
```

Then `forge scaffold` emits the pb-through stub (a method on `*Service`
returning Unimplemented) in `internal/handlers/<svc>/rpc_<name>.go`.
`forge scaffold rpc <svc> <Name>` does the same for one rpc, and prints a proto
snippet to paste when the rpc is not in the proto yet.

**What makes an rpc CRUD is its exact name.** A unary rpc named `Create<E>`,
`Get<E>`, `Update<E>`, `Delete<E>` or `List<Es>`, where `<E>` has a table AND a
proto message of that name, gets the CRUD wiring. Anything else is custom:
`UpdateJobStatus`, `CreateInvoiceFromEstimate` and `ListJobsByCrew` get a stub
and no page, and a custom mutation's hook invalidates every query on its
service (a CRUD mutation's, only its entity's). Name rpcs for the domain; never
rename one to dodge CRUD detection. An rpc that is CRUD in name only says so:

```protobuf
// forge:custom — creating a task also wires its dependencies.
rpc CreateTask(CreateTaskRequest) returns (CreateTaskResponse);
```

It is then custom everywhere (stub, test row, no shim, no page). Mark every
CRUD-named rpc of a table and the table gets no CRUD wiring, page or seed root.
It keeps its store: the ORM projects every applied table, rpcs or not; keep a
table off it with `COMMENT ON TABLE t IS 'forge:no-entity'`.

Every rpc is closed to unauthenticated callers unless it carries
`auth_required: false` (`proto`, `auth`).

### 3. Declare the authoritative route when two FKs reach one parent

When a table reaches the same parent two ways — `estimates.customer_id` directly
**and** `estimates.job_id → jobs.customer_id` — both foreign keys are valid and
the two routes can name different rows. Nothing in the schema says which wins,
so `forge db seed` refuses to write those rows until you declare it.

This shapes your entity model, so decide it while authoring the proto, not when
the seeder complains. Declare it as a comment on the FK constraint, in a
migration:

```sql
COMMENT ON CONSTRAINT estimates_customer_id_fkey ON estimates IS
    'forge:ref derived-from=job_id';
```

| declaration | meaning |
|---|---|
| `forge:ref derived-from=<column>` | the route leaving by `<column>` decides; this column is seeded from it |
| `forge:ref authoritative` | this column is the truth; the other edge is narrowed to agree |
| `forge:ref independent` | genuinely unrelated facts |

`authoritative` is usually right: the record states its own subject and the
longer route is a back-reference. Depth, and the cases forge resolves silently:
`db/seeding`.

## The shape, in four lines

- **SQL is the schema truth.** `db/migrations/*.up.sql` is the single source; entity structs, the ORM, CRUD wiring and frontend pages are PROJECTIONS of the applied schema. Do not declare schema in proto.
- **Proto is the wire truth.** Services, rpcs, messages. The two evolve on independent clocks; generated conversions map their intersection by name.
- **An entity is both halves**: CRUD rpcs in the proto AND the matching table. One half alone generates honest nothing.
- **Draft, then `forge generate`.** It is a fast, exact oracle that fails loudly. Never reverse-engineer forge's `internal/**` for syntax.

The application lives under `internal/`, nested by role: `internal/handlers/<svc>/`
(one service — owned and generated files in one directory), `internal/workers/<name>/`,
`internal/operators/<name>/`, `internal/app/` (owned `providers.go` + owned,
reconciled `compose.go`), `internal/db/`. `pkg/` is the generated substrate the app imports;
`cmd/` is entrypoints only. Full tree and the composition root: `architecture`.

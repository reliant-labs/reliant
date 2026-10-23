---
name: proto
description: Proto file reference — annotations, CRUD conventions, field rules, and common mistakes.
---

# Proto File Reference

Annotation + naming reference. Structural conventions (one service per file,
CRUD method shapes) are enforced by `forge lint --conventions`.

Proto is the **wire truth**: services, RPCs, messages. It is NOT the schema
language — the schema lives in `db/migrations/` (see `db`).

## Where Proto Files Live

```
proto/services/<service>/v1/<service>.proto    # Service definition (RPCs + messages)
proto/forge/v1/forge.proto                     # Forge annotations (vendored, never edited)
proto/shared/v1/types.proto                    # Cross-service shared messages (optional)
```

`proto/forge/v1/forge.proto` is a copy of forge's own file. `forge generate`
vendors it (and `buf/validate/validate.proto`) the first time a proto imports
it and the project has none, and never rewrites an existing copy:
`forge lint --vendored-protos` reports a stale one. It is an import only — its
Go is `forge/pkg/forgepb` — so a Go buf template must keep it out of its
targets (`inputs: [{directory: proto, exclude_paths: [proto/forge/v1,
proto/buf/validate]}]`, as the scaffolded `buf.gen.yaml` does); `forge generate`
refuses a template that would generate it, because a second Go copy panics
every binary at init.

One service per `.proto` file (`forge lint --conventions` →
`forgeconv-one-service-per-file`). Cross-service references go through
`proto/shared/v1/types.proto`; never import another service's proto.

**The proto package `forge scaffold service` writes follows the project.**
In order: forge.yaml `api.proto_package` (a fixed package like
`controlplane.v1`, or a pattern like `acme.{service}.v1`); else the
convention a strict majority of the existing service protos share (an
outlier is named in the scaffold output, never silently overruled); else
`services.<service>.v1`, which mirrors the path and satisfies buf
`STANDARD` as-is. `go_package` stays `gen/services/<service>/v1` either way.
A package shared across `services/*/v1/` directories needs buf.yaml
`lint.except: [PACKAGE_DIRECTORY_MATCH, PACKAGE_SAME_DIRECTORY,
PACKAGE_SAME_GO_PACKAGE]`, and message names must be unique across the
package.

## After Editing Protos

```bash
forge generate && forge lint && forge build
```

Rebuilds `gen/` (Go stubs, TypeScript clients, mocks, wiring) and verifies the
build. Generated code is overwritten — fix issues in `.proto`, never in `gen/`.

`forge generate` is the canonical entry, never `buf generate` alone: buf emits
only the `.pb.go` / `_pb.ts` stubs, while the descriptors, schema-driven ORM,
mocks, frontend hooks and CRUD wiring come from forge's post-buf passes (it runs
`buf generate` internally).

## No Backwards Compatibility

Proto files in Forge projects are not published APIs with external consumers.
Edit freely — add, rename, remove fields, RPCs and messages. Mark removed fields
as `reserved`; never reuse field numbers.

## Annotation Reference

```proto
import "forge/v1/forge.proto";
```

### Service / Method Annotations

```proto
service TaskService {
  option (forge.v1.service) = { name: "tasks" version: "v1" };

  rpc CreateTask(CreateTaskRequest) returns (CreateTaskResponse) {
    option (forge.v1.method) = {
      auth_required: true
      idempotency_key: true
    };
  }
}
```

`forge scaffold entity` writes exactly this onto every scaffolded CRUD RPC.

**Don't guess the option surface — dump it.** `forge project annotations` emits
the authoritative spec — every `forge:*` marker, the proto→column mapping, the
projected `buf.validate` rules, and every `(forge.v1.service)` /
`(forge.v1.method)` option with its type, effect and default — from forge's
compiled descriptors, so it cannot drift:

```bash
forge project annotations --kind method          # readable table
forge project annotations --kind service --json  # machine-readable
forge project annotations --json                 # everything
```

The whole method surface is five options — there are no others, and a name
forge does not know is a **hard `buf generate` failure**, not a warning:

| option | type | what it does |
|---|---|---|
| `auth_required` | bool | whether the caller must be authenticated (see below) |
| `idempotent` | bool | marks the method safe to retry |
| `idempotency_key` | bool | advisory marker for callers; forge does not enforce it |
| `timeout` | Duration | per-method server timeout — `timeout: { seconds: 30 }` |
| `errors` | repeated string | declared error codes, e.g. `errors: ["NotFound"]` |

There is **no `description`** — the prose that documents an rpc is the
ordinary `//` comment above it. Writing one inside the option block fails
with `field description not found` and takes the whole generate down with it.

`auth_required` is **fail-closed and per-rpc**: auth is required even when the
option is unset, and `auth_required: false` publishes *that one rpc* to
unauthenticated callers while its siblings stay closed. There is no service-wide
posture to set. It declares only *whether a caller must be authenticated*; what
an authenticated caller may then do is handler logic against
`middleware.GetUser(ctx)`, checked against the row's own owner column (`auth`,
and `db` for where that column comes from).

### No schema annotations

There is no proto-side schema declaration: entities are projections of the
applied `db/migrations/` schema. `(forge.v1.entity)` and `(forge.v1.field)` are
ignored — delete any that survive in your protos, along with a `proto/db/`
directory if you have one. A column's default is likewise plain SQL (`NOT NULL
DEFAULT <expr>`) in the birth migration, not an annotation.

### Birth markers — the `// forge:*` comments

You author an entity's proto message and birth it with `forge scaffold` (or
`forge scaffold entity <name> --from-proto <svc>` for one named message) — the
only birth path forge has. **Comment markers** shape what birth emits. They are
ordinary `//` comments, not `(forge.v1.*)` options, read by the raw-proto
scanner at birth only and inert once the table exists. Message-level markers go
in the leading comment; field-level markers take a leading comment **or** a
trailing one after the field's `;` (inline `[...]` options in between are fine).

**Managed fields are declared, not assumed.** Birth writes `id`, `created_at`
and `updated_at` (plus `deleted_at` under `forge:soft-delete`) into your message
at field numbers above its high-water mark, one time, and they are yours
thereafter. Declare them yourself if you prefer — birth leaves a field the
message already has alone. Either way the message describes its own shape, which
is what the ORM row type, the CRUD envelopes and the generated edit pages
project.

Run `forge project annotations --kind entity` / `--kind field` for the current
marker list and what each emits. In practice: `forge:entity` tablizes a message;
`forge:soft-delete` and `forge:append-only` tablize *and* shape the lifecycle
(opt-in soft delete; an immutable ledger with no Update/Delete);
`forge:read-only` and `forge:secret` trim a field off the born write and read
sides respectively; `forge:generated <expr>` births a column postgres computes
from the same row (and is read-only too).

`forge:read-only` (and `forge:computed` / `forge:generated`, which imply it) is also read on every
`forge generate`, because the AIP-134 Update request wraps the whole entity and
so still carries the field. The generated Update refuses an `update_mask` naming
it (`InvalidArgument`, reason `unknown_field`) and keeps it out of a full replace,
so a client can neither set it nor reset it by leaving it out. Your own RPCs write
it with `db.Update<Entity>Masked`. This is stricter than AIP-203, which asks a
server to silently ignore output-only mask paths: a caller who names `status`
believes it is changing status, and a 200 that changed nothing is the silent
non-write forge refuses to ship. Read-only vs `forge:computed` vs
`forge:generated` vs `forge:guards` vs `forge:immutable`, by who writes the
value: `db/write-policy`.

```proto
// forge:soft-delete
message Product {
  string name = 1;
  double price = 2 [(buf.validate.field).double.gte = 0];  // forge:read-only
  string api_token = 3;      // forge:secret
  bool requires_prescription = 4;
  int64 stock = 5;
  int64 reserved = 6;
  int64 available = 7;       // forge:generated stock - reserved
  // birth appends: id, created_at, updated_at, deleted_at
}
```

`forge:generated` takes the **rest of the line** as SQL, verbatim — it becomes
`available BIGINT NOT NULL GENERATED ALWAYS AS (stock - reserved) STORED`, NOT
NULL unless the field is `optional`. Birth tries the migration against postgres
before writing anything, so a typo'd column or a non-`IMMUTABLE` function
refuses the birth at the marker's line rather than surfacing at the next
`forge generate`. Prose explaining the derivation goes on its own comment line.

Storage-side semantics (what each marker adds to the owned migration, the
proto→column type mapping, and the entity-shaping flags `--soft-delete` /
`--no-timestamps` / `--table`) live in `db`.

#### Guarded columns and marker lint

Put `// forge:guards <table>.<column>` on a custom RPC request field when that
RPC owns a transition. It disables the column on scaffolded edit pages, not
Create or direct API writes; add `forge:read-only` on the entity field when the
API must refuse writes too. Pages are owned and never regenerated: run
`forge lint --guarded-fields` after adding guards. Misspelled markers are inert
comments; `forge lint --proto-markers` warns about them. Full examples and
limitations: `forge skill load proto/guards`.

### Getting annotation syntax right: run `forge generate`

It's a fast, exact oracle. Draft the proto (for a new entity, mark it
`// forge:entity` and let `forge scaffold` write the migration), run it, read
the error, fix, repeat. It fails **loudly** on a bad List filter (a field naming
no real column), a missing import, a malformed message, or a migration postgres
rejects. Don't plan the annotation surface up front.

## CRUD RPC Naming Convention — the wire half of an entity

An entity needs both a service proto declaring the CRUD RPCs below and a
matching table in the applied schema (pluralized snake_case of the entity
name); see `db` for the storage half. `forge scaffold entity` writes both in one
step, and the messages and RPCs it puts in the service proto are yours
afterwards — the wire contract evolves independently of the schema.

Use these exact prefixes. For matching methods (with the matching table), forge
generates the per-RPC op constructors and the `<entity>ToProto` /
`<entity>FromProto` conversions in `handlers_crud_ops_gen.go` (Tier-1,
regenerated every run) and scaffolds a thin delegation into the user-owned
`handlers_crud.go` (`return crud.HandleCreate(s.crudCreateItemOp())(ctx, req)`):

| RPC Name | Generated behavior |
|----------|--------------------|
| `Create<Entity>` | Insert via ORM |
| `Get<Entity>` | Select by ID |
| `List<Entities>` | Paginated list with filters |
| `Update<Entity>` | Update via ORM |
| `Delete<Entity>` | Delete (or soft-delete when the table has `deleted_at`) |

**The rule is exact, and it is the same on both sides of the wire.** An rpc is
CRUD when it is unary, its name is one of those prefixes followed by an entity
name (`List` takes the plural, or the name as written), and that entity has
both a table in the applied schema and a proto message of that name. Every
other rpc is custom, including ones that start with a CRUD verb:

| rpc | Is it CRUD? |
|---|---|
| `UpdateJob`, `ListJobs` (table `jobs`, message `Job`) | yes |
| `UpdateJobStatus` | no — `JobStatus` is an enum, not a table + message |
| `CreateInvoiceFromEstimate`, `ListJobsByCrew` | no — no `InvoiceFromEstimate` / `JobsByCrew` entity |

A custom rpc gets a pb-through stub in `rpc_<name>.go`, no scaffolded page, and
a mutation hook that invalidates every query on its service (a CRUD mutation
invalidates only its entity's). So name rpcs for the domain operation; renaming
`UpdateJobStatus` to `ChangeJobStatus` buys nothing. The one real collision is
an rpc whose remainder IS another entity's name — `UpdateJobNote` where
`JobNote` is an entity is that entity's Update.

The generated conversions map the **intersection** of wire fields and
columns by name: a wire-only field never reaches the DB, a column-only
field never leaks onto the wire. Add wire-only fields freely.

Hand-written handler methods always win — the generator skips anything you've
already implemented. To customize a generated CRUD RPC, replace the delegation
body in `handlers_crud.go` (the file is yours; new CRUD RPCs are appended,
existing content never modified).

When a request/response shape deliberately deviates (a list keyed by
`ticker`+`limit` instead of AIP-158, say), forge scaffolds an Unimplemented stub
into `handlers_crud.go` carrying a `forge:custom-read-shape: <reason>` comment
with the observed field list. That is the system working, not an error — the
body is yours to implement, composing the `pkg/crud`/`pkg/orm` helpers (cursor
encode/decode, `WhereEq`/`WhereILikeAny` filters, column-allowlisted order-by).
`forge generate` prints one warning line per stub, and `forge project audit`
reports each under `crud_stubs` until the body lands; the RPC returns
`CodeUnimplemented` until then.

### List Request Conventions (AIP-158)

```proto
message ListTasksRequest {
  int32 page_size = 1;
  string page_token = 2;
  optional string search = 3;   // ILIKE across the table's text columns
  optional bool done = 4;       // Exact-match filter — must name a real column
  string order_by = 5;
  bool descending = 6;
}

message ListTasksResponse {
  repeated Task tasks = 1;
  string next_page_token = 2;
  int32 total_count = 3;
}
```

Filter fields **must** be `optional` — otherwise generated code can't
distinguish "not set" from zero values (a non-optional `bool`/enum filter is
always applied, so omitting it returns only the `false`/UNSPECIFIED rows).
`forge lint --conventions` enforces this as `forgeconv-list-filter-optional`
on any `List<X>Request` (or request of a `List<X>` rpc). Pagination and
ordering controls (`page_size`, `page_token`, `order_by`, `descending`,
`cursor`, `limit`, `offset`, …) are exempt, as are message-typed fields
(they already have presence). A field that is a required parameter rather
than a filter — an org or account scope the handler insists on — says so with
`[(buf.validate.field).required = true]` instead of `optional`.

`search` / `query` / `q` are the fuzzy-search filters: they span the
table's non-PK text columns via `orm.WhereILikeAny`. Any other filter
field must name a real column of the entity's table — `forge generate`
fails loudly otherwise. A user-supplied `order_by` is validated against
the table's declared-column allowlist (`<Entity>Columns`); an undeclared
column returns `InvalidArgument`, not a silent no-op.

**The request's fields ARE the filter surface.** To filter or scope a list by
anything else — an enum `status`, a foreign key, an owner `<owner>_id` — add
that field to the List request naming the real column, and forge wires it end
to end:

```proto
// added by hand to the scaffolded ListPrescriptionsRequest:
optional string status = 3;      // enum facet — exact-match on the status column
optional string patient_id = 4;  // FK / owner scope — exact-match on patient_id
```

Never fetch a page and filter client-side — that truncates past the page cap.
Birth writes `page_size`/`page_token`/`order_by`/`descending`, `search` when
the entity has text, and one exact-match facet per bool, enum and `<stem>_id`
reference — including `optional` ones, so a nullable `crew_id` is filterable
like a required `customer_id`. A facet is `col = $1`, so rows where the column
is NULL never match it; "jobs with no crew" is not a facet — add a request
field for it and filter in the op. Owner scopes and anything else you add by
hand.

## Enum Conventions

```proto
enum TaskStatus {
  TASK_STATUS_UNSPECIFIED = 0;   // Always required — prefix with enum name
  TASK_STATUS_PENDING = 1;
  TASK_STATUS_ACTIVE = 2;
}
```

- First value **must** be `0` and named `<ENUM_NAME>_UNSPECIFIED`.
- All values **must** be prefixed with the enum name in UPPER_SNAKE_CASE.
- Proto fields stay `snake_case` (`created_at`, `org_id`); messages / RPCs / services stay `PascalCase`. Full casing table: **Naming conventions** in `architecture`.

## RPCs owned by another service/repo — import, don't re-scaffold

For an RPC owned by *another* service or repo you are a client, not its
implementer: the remote proto is the source of truth — **import it (pinned
`task protos` copy or a buf BSR dep) and generate only a CLIENT.** Never
hand-copy it into `proto/services/` and scaffold a server: every method
`CodeUnimplemented` and a `Deps` holding only `Logger`/`Config` is the tell.

## Services forge does not manage (yet) — `// forge:unmanaged`

A service this project declares and SERVES, but whose handlers live outside
forge's `internal/handlers/<svc>/` model — the in-place adoption case
(`migration`). Mark it with a full-line comment above the declaration (only
comment and blank lines may sit between):

```proto
// forge:unmanaged — handlers still in internal/grpc/services.
service ChatService { ... }
```

buf still generates its code and its rpcs' `auth_required` still projects into
the open-procedure set; forge generates nothing service-scoped for it (no
handler stubs, mocks, CRUD, mounts, compose entry, subcommand, frontend hooks).
Delete the line to adopt it. It is a whole-service switch: on an rpc or a
message, or misspelled, it fails `forge generate`. `forge project annotations
--kind service` has the full effect list.

Not the same as the section above: an rpc owned by ANOTHER repo is imported
and generates only a client; an unmanaged service is yours, served by code
forge has not taken over yet.

## Common Mistakes

1. **Missing forge import** — Every proto using `(forge.v1.method)` / `(forge.v1.service)` needs `import "forge/v1/forge.proto";`.
2. **Enum without UNSPECIFIED=0** — Proto3 requires the zero value. Name it `<ENUM>_UNSPECIFIED`.
3. **Enum values without prefix** — Use `TASK_STATUS_ACTIVE`, not `ACTIVE`. Proto enums share a namespace.
4. **Non-optional filter fields** — List request filter fields must be `optional` (lint-rejected: `forgeconv-list-filter-optional`).
5. **Reusing field numbers** — Mark removed fields as `reserved`, never reuse.
6. **Multiple services per file** — Lint-rejected (`forgeconv-one-service-per-file`). Use `proto-split`.
7. **Cross-service proto imports** — Hoist shared messages into `proto/shared/v1/types.proto`.
8. **Declaring schema in proto** — There is no entity *annotation*; the `// forge:entity` marker is a birth instruction, not a schema declaration. Columns come from `db/migrations/`; if a CRUD RPC has no matching table, nothing is generated. Mark the message and run `forge scaffold` (or write the migration) first.
9. **`min_len == max_len` on a fixed-length code** — `buf lint` rejects it (exit 100). A fixed length takes `string.len = N` (ISO currency 3, country 2, NPI 10), which still projects to `CHECK (char_length(col) = N)` at birth; a single fixed literal takes `string.const = "X"`, which is wire-only. Reserve `min_len`/`max_len` for a genuine range.

## Rules

- **Draft, then `forge generate`** — the fast, exact oracle; read its error and fix. Never reverse-engineer forge's source for syntax.
- Proto declares the wire; `db/migrations/` declares the schema. Entity = CRUD RPCs + matching table.
- RPCs owned by another repo: **import the upstream proto and generate a client** — never hand-copy it and scaffold a server you won't implement.
- Run `forge generate && forge lint` after every proto edit; fix issues in proto, not in `gen/` — generated code is overwritten.

## When this skill is not enough

- Splitting a multi-service file — `proto-split`.
- Designing the Go service surface behind the proto — `service-layer`.
- Handler implementation patterns — `api`.
- DB schema lifecycle, and the schema as the entity truth — `db`.

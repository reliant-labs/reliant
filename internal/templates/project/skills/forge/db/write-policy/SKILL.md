---
name: write-policy
description: Who may write a column, and which marker says so — the decision table for forge:read-only vs forge:computed vs forge:generated vs forge:guards vs forge:immutable, row-ownership columns, forge:immutable against accidental full-replace overwrites, forge:version optimistic concurrency, and how a NOT NULL column's DEFAULT applies to an unset field (pointer or ,nullzero).
---

# Column write policy

A column's write policy — may a full-replace UPDATE touch it? — is schema
truth, so it is declared where the column is, as a `COMMENT ON COLUMN` in the
migration that owns it. `forge generate` introspects those comments via
`col_description()` and projects them onto the generated ORM. Whether a CLIENT
may write it is a statement about the API, so that half is a `// forge:*`
comment on the proto field. Pick by asking who writes the value.

## Which marker?

| Who writes the value | Declare | What forge then does |
|---|---|---|
| A custom rpc's state machine — `status`, `scheduled_start`, `paid_at` | `// forge:read-only` on the entity field, **and** `// forge:guards <table>.<column>` on a field of that rpc's request | Read-only: off the born Create; an `update_mask` naming it is `InvalidArgument` (`unknown_field`); a full replace keeps the stored value. Guards: the scaffolded edit page shows a disabled row naming the rpc |
| Postgres, from the same row — `line_total = quantity * unit_price` | `// forge:generated <sql expr>` at birth (implies read-only) | Births `GENERATED ALWAYS AS (<expr>) STORED`; the ORM never writes it and reads it back after every write. Depth: `db/derived-values` |
| Your Go code, from OTHER rows — `amount_paid` summing payments | `// forge:computed` | Everything read-only does, and `forge lint` fails while no non-generated Go file assigns it (a warning while the writing rpc is still forge's unwired stub) |
| A trigger, or a GENERATED column added after birth | the SQL in a migration, plus `// forge:read-only` | Read-only keeps clients off it. Not `forge:computed`: no Go assigns it, so that lint would fail |
| The server once, then nobody — an owner id, a Stripe customer id | `COMMENT ON COLUMN … IS 'forge:immutable'`, usually with `// forge:read-only` too | `,skipupdate`: no full replace rewrites it; a masked write naming it still does (below) |
| Nobody outside the app may READ it through the database's read-only `forge_ro` role — a sealed token, ciphertext, a token hash | `COMMENT ON COLUMN … IS 'forge:secret'` (or `// forge:secret` on a wire field) | Postgres refuses the column to `forge_ro` however it is reached. The SQL spelling is for a column with no wire field. Not a write policy: the app still reads and writes it. A credential-shaped name with no decision is withheld too and fails `forge lint --db-grants`; `COMMENT ON COLUMN … IS 'forge:readable <reason>'` grants one that is not a credential (`db/query`) |
| The client at Create; every later change goes through an rpc | `// forge:guards` alone | The edit page stops writing it and names the rpc. **The API still accepts it** through `Update<Entity>`; if clients must not bypass the rpc, override the Update shim in `handlers_crud.go` (`db/crud-overrides`). `forge:read-only` would also drop it from Create |

Since the generated Update enforces `forge:read-only` (and `forge:computed` /
`forge:generated`, which imply it) itself, guards adds no protection to a
read-only column. What it still adds is discoverability: read-only alone drops
the field from the edit page without a word, while read-only plus guards
renders it disabled, reading "Changed through `ChangeJobStatus`", at the moment
a user is looking for how to change it. Guards alone is the weaker choice: it
only stops forge's own form from writing the column. Syntax, and the
`--guarded-fields` lint for pages scaffolded before the marker: `proto`.
Unwritten read-only columns: `forge lint --read-only-fields`.

## If rows belong to someone, that is a column

Forge stores no ownership of its own — no implicit tenant, no ambient owner. If
rows in a table belong to a user, an org or an account, the only place that fact
can live is a **column you write in the birth migration**: `auth_required` says
only whether a caller must be signed in (`proto`), and forge ships no
authorization (`auth`). Adding the column later is a migration plus a backfill,
so decide now.

Keeping it off the wire is the usual shape, since a client that could propose an
owner could propose someone else's — but a full-replace Update builds its entity
from the request, so a request that never carried the column arrives with it
zero-valued and overwrites the stored value. `forge:immutable` stops that. The
queries and checks around it are yours; `db/crud-overrides` covers the seams.

**Say so with `forge:owner`, and forge will hold you to it.** That column is also
the one fact forge needs in order to tell "unscoped" apart from "unsafe", so
declare it:

```sql
COMMENT ON COLUMN crews.company_id IS 'forge:owner';
```

This changes no codegen. Forge still stores no ownership of its own and will not
inject a `WHERE` clause for you, because only your app knows how a caller's
claims map onto this column's values. What it changes is what forge lets ship: it
arms the `unscoped_auth` audit gate for this table, so an authenticated RPC over
`crews` whose handler never resolves the caller becomes an `error` rather than a
warning. A column can carry both markers — `'forge:owner forge:immutable'` — and
usually should, since an owner column is exactly the kind a full-replace Update
must not zero. `auth/authorization` has the gate's semantics and a worked example
of scoping the generated ops.

## `forge:immutable`

```sql
-- up
COMMENT ON COLUMN crews.company_id IS 'forge:immutable';

-- down
COMMENT ON COLUMN crews.company_id IS NULL;
```

projects onto the generated Bun struct tag:

```go
CompanyId string `bun:"company_id,notnull,skipupdate"`
```

Bun's `,skipupdate` omits the column from a full-replace UPDATE's `SET` clause —
the stored value survives untouched. It is still writable on `INSERT`, and a
**masked** Update naming the column explicitly still writes it: that is the
caller asserting a value on purpose.

Reach for it on any column a full-replace Update could zero out by accident: a
server-assigned owner id, an externally-issued identifier (a Stripe customer ID,
an OAuth subject), an audit stamp set once at creation.

### `forge:immutable` or `forge:read-only`?

They answer different questions, and choosing the wrong one either leaves a
hole or takes a write away from code that needs it.

| | `forge:immutable` (column comment) | `// forge:read-only` (proto field) |
|---|---|---|
| Question | may ANY full replace rewrite this column? | may a CLIENT write it through the API? |
| Client full-replace Update | leaves it as stored | leaves it as stored |
| Client `update_mask` naming it | **writes it** | `InvalidArgument` (`unknown_field`) |
| App code `db.Update<Entity>` | leaves it as stored | **writes it** |
| App code `db.Update<Entity>Masked` naming it | writes it | writes it |

A lifecycle column — `status`, `shipped_at`, a balance — is `forge:read-only`:
clients must not touch it, and the RPC that owns the state machine writes it.
`forge:immutable` alone would still let a client set it through a mask naming
it. An owner id that nobody should ever rewrite, including your own full
replaces, is `forge:immutable`, often with `forge:read-only` on its wire field
as well. Both hold on every `forge generate`; `forge:read-only` needs no
migration because it is a statement about the API, not about storage.

Every Update also reads back the columns it did not write (`RETURNING`), so the
response and the entity you hold afterwards report the stored values of an
immutable, read-only or GENERATED column rather than the ones the request
carried.

A write whose caller discards the entity — a worker stamping `dispatched_at`
on a wide row — does not need that read-back, and on a one-column write it is
most of the cost (a 12-column row: 93 → 39 allocations). Pass
`crud.NoReadBack()` to make one `Update` or `UpdateMasked` exec-only: no
`RETURNING`, and the entity keeps what you put in it (plus the stamped
`updated_at` and advanced `forge:version`). From `_repo_ext.go`, call the
entity's repo directly: `<entity>Repo.UpdateMasked(ctx, db, e,
[]string{"dispatched_at"}, crud.NoReadBack())`.

**DB-only columns are not automatically protected.** A column with no matching
proto field says nothing about whether it may be rewritten — an internal rating
or a denormalized cache is absent from the API and mutable by design. Only
`forge:immutable` makes a column un-rewritable; absence from the wire never does.

## `forge:version` — concurrent writes to the same row

By default the last writer wins, silently: two clients that each change a
different field produce one surviving version and no error anywhere. That is
right for a settings row or a status flag, and wrong for anything where a lost
edit is a real loss — a document body, an inventory count, a balance.

Opting a table in takes **both halves** — the column, and a field on the wire
message so the caller can hand back what it read:

```sql
-- up
ALTER TABLE crews ADD COLUMN version BIGINT NOT NULL DEFAULT 0;
COMMENT ON COLUMN crews.version IS 'forge:version';

-- down
ALTER TABLE crews DROP COLUMN version;
```

```protobuf
message Crew {
  // ...
  int64 version = 12;  // forge:read-only
}
```

The wire field is required, not cosmetic: a version the caller cannot read and
return is always the zero value, so shipping the column alone makes every update
after a row's first one fail forever. `forge generate` refuses that and names the
field to add. `forge:read-only` is its right shape — readable on Get/List and on
the entity an Update carries, absent from the Create/Update request messages,
since the repository owns the increment.

Every `Update` and masked Update then carries the caller's version into its
`WHERE` clause and increments it in the same statement — one atomic
compare-and-swap, no explicit transaction and no raised isolation level. A stale
writer matches no row and gets **`Aborted`** (reason `version_conflict`) rather
than a silent overwrite. `Aborted` names a mechanical recovery: **re-read the
row, re-apply the change to the fresh values, write again** — retrying the
identical request fails the same way. It stays distinct from `NotFound`, so a UI
can tell "somebody beat you to it" from "this no longer exists."

The column is **never client-settable** — not on Create, not through an
`update_mask` naming it (that is an `unknown_field` error). A client able to
choose its own version could satisfy its own concurrency check. Entities with no
`forge:version` column are unaffected: no predicate, no extra query.

See also: `db` for the schema/ORM half, `db/seeding` for `forge:ref` (the same
`COMMENT ON` vocabulary applied to a foreign-key constraint).

## Unset columns and DEFAULTs

A `NOT NULL` column with a `DEFAULT` says "unset" with its TYPE, so the
DEFAULT applies when you do not set the field and an explicit zero is still
stored:

- A scalar whose DEFAULT is not its Go zero (`status INTEGER NOT NULL DEFAULT
  2`, `active BOOLEAN NOT NULL DEFAULT true`, `kind TEXT NOT NULL DEFAULT
  'llm'`, any expression) is a **pointer** field: `nil` is unset, `&false` is
  false.
- A column whose zero it can never hold keeps its type and takes `,nullzero`:
  a json document (`""` is not JSON), an array with a non-empty DEFAULT, a
  scalar a CHECK keeps from its zero (every born enum column).
- A DEFAULT equal to the zero (`0`, `false`, `''`, `'{}'`) changes nothing.

Unset means one thing on every write: `Create` omits the column so the DEFAULT
fires, a full-replace `Update` and `Upsert`'s conflict branch keep the stored
value, and a masked update naming an unset column sets it back to its DEFAULT.
Reads always fill the pointer. `forge:read-only` columns the create request
does not carry are left unset the same way, so their DEFAULT is the row's
initial state.

Managed timestamps follow the same rule. `Create` and `Upsert` stamp
`created_at` and `updated_at` with now only when the field is unset (zero
time, `nil`, `""`) and keep a value you set, so an imported or replayed row
keeps its own times; `Upsert` writes that one `updated_at` on either branch.
`Update` and `UpdateMasked` always re-stamp `updated_at`: an entity read back
and written again still carries its old value, which is not a decision. Keep
a client off a wire `created_at`/`updated_at` with `forge:read-only`.

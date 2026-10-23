---
name: crud-overrides
description: Diverge from generated CRUD without forking the projection — override op fields in your owned handlers_crud.go, the per-op seams (op.Filters, op.Fetch, op.Persist) a per-caller policy attaches to, and what the Update op already does for read-only fields.
---

# Diverging From Generated CRUD

The per-entity CRUD wiring (`handlers_crud_ops_gen.go`, including
`<entity>ToProto`/`<entity>FromProto`) is Tier-1 and keeps regenerating. When
one RPC needs custom behavior — extra wire-only response fields, a different
persist path — do NOT disown the ops file. Your **owned** `handlers_crud.go`
shim takes the generated op and overrides the exported field it needs before
delegating:

```go
// handlers_crud.go (yours) — diverge one op without forking the projection:
func (s *Service) CreateInvoice(ctx context.Context, req *connect.Request[pb.CreateInvoiceRequest]) (*connect.Response[pb.CreateInvoiceResponse], error) {
    op := s.crudCreateInvoiceOp() // generated wiring, still regenerated
    op.Pack = func(e *db.Invoice) (*pb.CreateInvoiceResponse, error) {
        m, err := invoiceToProto(e) // returns an error on a corrupt stored enum
        if err != nil {
            return nil, err
        }
        return &pb.CreateInvoiceResponse{Invoice: m}, nil // + custom wire-only fields
    }
    return crud.HandleCreate(op)(ctx, req)
}
```

Every exported op field (`Entity`, `Pack`, `Persist`, `Mask`, ...) is
overridable the same way; the lifecycle (persist → pack, error mapping) stays
in `forge/pkg/crud`, and schema/proto changes keep flowing through the
regenerated op underneath your override.

## Which file, and when forge emits an op

`handlers_crud.go` is where forge *appends* shims; it is not where a CRUD
method has to *stay*. Move a shim — delegating, overridden, or scoped — to
any file of the handler package and the next `forge generate` treats it the
same. Per CRUD rpc, over every file of the package:

- **no method declares the rpc** → forge appends a delegating shim to
  `handlers_crud.go` and emits `crud<Rpc>Op`;
- **a method declares it and anything in the package calls
  `s.crud<Rpc>Op`** → the op is emitted (and no shim is appended);
- **a method declares it and nothing calls the op** → the rpc is
  implemented by hand: no op is emitted, and the op's own checks (a list
  filter naming no column, an Update on an append-only table) do not apply.
  This is the escape hatch those errors point you at.

`<entity>ToProto` / `<entity>FromProto` stay emitted while anything in the
package calls them — a custom rpc projecting rows keeps its helper even when
every CRUD rpc of that entity is hand-written. Declare your own function of
that name and forge stops emitting it.

## Where a per-caller policy attaches

Forge enforces no policy of its own, so if rows are restricted to some callers,
these are the seams that restriction attaches to. What the rule should be is
yours; these are the mechanics of each op, and the constraints are not obvious
from the struct:

Every request-projecting closure takes a `context.Context`, so the caller's
claims are reachable from all of them. If a table declares `forge:owner`, forge
scaffolds these overrides for you — see `auth/authorization`; what follows is
the mechanics for everything else.

- **List** — `op.Filters`, signature
  `func(ctx, *Req) ([]orm.QueryOption, error)`. The generated closure is **nil
  unless the RPC declares filter fields**, so capture and nil-check it before
  calling, or you silently drop the per-field filters. Valid column names are
  exactly `db.<Entity>Columns`. A predicate belongs in the query, not after it —
  the response's `total_count` comes from a COUNT over the same filters, so
  post-filtering a page leaves the total reporting rows the caller cannot see.
  The `error` return is there so a closure that cannot determine the scope can
  **fail closed**: returning no filter is not "no restriction", it is every row.
- **Get / Update / Delete** — `op.Fetch`, `op.Persist` and `op.PersistMasked`
  take trailing `opts ...orm.QueryOption`. Append your predicate and the
  generated shim composes it into the query, so the check is one statement, not
  a fetch-then-compare. On Update the predicate is evaluated against the
  **stored** row, which is what stops a caller reassigning someone else's row by
  rewriting the owner column in their request.
- **Anything reachable only via a join** — `orm.QueryOption` is
  `func(*bun.SelectQuery)` and orm ships no join helper, so drop to raw bun:
  `q.Where("id IN (SELECT intake_id FROM assignments WHERE provider_id = ?)", claims.UserID)`.
  For anything the op cannot express, hand-roll against `s.deps.DB`.
- **Create** — `op.Entity`, signature `func(ctx, *Req) (Ent, error)`. Stamp an
  owner column here, from the claims on `ctx` and never from a wire field: a
  create that took the owner off the request would let anyone file a row under
  anyone else's name.

### A column that is not on the wire, and full-replace Update

A column added by migration but absent from the proto is **not** written by
`<entity>FromProto`, so a maskless Update builds an entity carrying its zero
value — and that zero lands on the row, overwriting what was stored. Declare
the column to opt out:

```sql
COMMENT ON COLUMN crews.company_id IS 'forge:immutable';
```

`forge generate` projects that into the Bun tag (`,skipupdate`) and Bun omits
the column from the full-replace `SET` clause, so an override does not need to
re-stamp it. A **masked** Update naming the column still writes it: that is a
caller asserting a value on purpose, not a request built without knowledge of
the column.

Note what this does and does not cover. `forge:immutable` decides what the
framework may **write**; it says nothing about **who** may write the row. If
that second question matters here, `op.Persist` / `op.PersistMasked` are where
it gets answered, and answering it means reading the stored row rather than
trusting the submitted entity.

### An override that reads, checks, then writes

When a `Persist` / `PersistMasked` override (or any custom RPC) loads the
stored row, decides whether the change is allowed, and then writes, wrap the
three steps in `s.deps.DB.RunTx(ctx, orm.Serializable, …)` and use the `ctx`
it hands you for all of them:

```go
// (This op carries no per-caller predicate; if yours does, the read must honor
// opts — see "Where a per-caller policy attaches" above.)
op.PersistMasked = func(ctx context.Context, e *db.Job, fields []string, _ ...orm.QueryOption) error {
    return s.deps.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
        stored, err := db.GetJobByID(ctx, s.deps.DB, e.ID) // inside the transaction
        if err != nil {
            return err
        }
        if stored.Status == "COMPLETED" {
            return svcerr.FailedPrecondition("a completed job cannot be edited")
        }
        return db.UpdateJobMasked(ctx, s.deps.DB, e, fields)
    })
}
```

No `SELECT … FOR UPDATE`: the generated delegates join the transaction
carried by `ctx`, and `orm.Serializable`'s SERIALIZABLE-plus-retry turns a
concurrent writer into a re-run that sees the committed row. The closure may
run more than once, so keep side effects out of it (`orm.AfterCommit`). The
level is the argument, so name it: the same body at `orm.ReadCommitted` lets
both writers commit, and adding a `FOR UPDATE` to `orm.Serializable` makes the
waiter fail `40001` (`forgeconv-tx-lock-under-serializable`).
`service-layer/transactions` explains why this is safe.

### Read-only fields are already handled — do not overlay them

The generated Update op carries the entity's `// forge:read-only` and
`// forge:computed` columns twice, and both halves survive an override that
wraps rather than replaces:

```go
// handlers_crud_ops_gen.go (generated)
ReadOnly: []string{"status", "shipped_at"},       // HandleUpdate refuses a mask naming one
Persist: ... db.UpdateOrder(ctx, s.deps.DB, entity,
        crud.Preserve("status", "shipped_at")) ... // a full replace leaves them as stored
```

So there is no need to reload the stored row and copy the editable fields onto
it, or to filter lifecycle paths out of the mask — the op does both, and the
response reports the stored values (every Update reads back the columns it did
not write). Keep an override for what is genuinely yours: a business rule, a
cross-entity check, scoping.

Two things to know when overriding:

- **Replacing `op.Persist` outright** (not wrapping it) drops `crud.Preserve`
  with it. Pass it yourself, from the op so the list cannot drift:
  `db.UpdateOrder(ctx, s.deps.DB, e, crud.Preserve(op.ReadOnly...))`.
- **A derived value is not written by the Update op.** Setting `row.Total` in
  an Update `op.Entity` hook has no effect on a read-only column — that is the
  guarantee, not a bug. Same-row derivations belong in a `GENERATED` column;
  cross-row ones are written by the code that changes their inputs, with
  `db.Update<Entity>Masked(ctx, s.deps.DB, e, []string{"total_cents"})`.

AIP-203 asks servers to *ignore* output-only `update_mask` paths; forge refuses
them instead, because a 200 that silently changed nothing is the failure this
exists to prevent. An override that wants the AIP behaviour sets
`op.ReadOnly = nil` and filters the paths in `op.PersistMasked`.

## Hand SQL: two placeholder dialects on one handle

An override, a custom rpc or a `_repo_ext.go` query that drops to hand SQL
reaches it through the same `orm.Context`, and the two paths take
**different placeholders**:

| Path | Placeholders | Who binds the arguments |
|---|---|---|
| `db.Exec` / `db.Query` / `db.QueryRow` (raw passthrough) | postgres `$1`, `$2`, … | the server (a real bind parameter) |
| `db.Bun().NewRaw(…)`, every builder method (`Where`, `Set`, `ColumnExpr`, `OrderExpr`, …), `bun.SafeQuery` | bun `?` (in order) or `?0`, `?1`, … (0-based, reusable) | the server: forge sends bun's statement with its values as bind parameters (below) |
| `db.Bun().ExecContext` / `QueryContext` / `QueryRowContext` | bun `?` | bun, interpolating the values into the SQL text |

```go
// raw passthrough: native $N
db.Exec(ctx, `UPDATE run_events SET dispatched_at = now() WHERE id = $1`, id)

// bun: ? / ?N — a ported sqlc query has its $N rewritten to ?N-1
db.Bun().NewRaw(`INSERT INTO messages (id, chat_id) VALUES (?0, ?1)`, id, chatID).Exec(ctx)
```

In bun-formatted SQL **every unescaped `?` is a placeholder** — one in a
`-- comment`, one inside a `'string literal'`, and the jsonb operators `?`,
`?|` and `?&`. Each consumes the next argument and shifts the rest onto the
wrong placeholders. Escape a literal one as `\?` (`"\\?"` in an interpreted Go
string, `\?` in a raw one) — `payload \? 'tool'`, `tags \?| ?` — or use the
function forms `jsonb_exists`, `jsonb_exists_any`, `jsonb_exists_all`.
Conversely, a `$1` left in bun-formatted SQL reaches postgres literally with
nothing bound (`there is no parameter $1`).

`forge lint` catches both in string literals and named constants:
`forgeconv-bun-dollar-placeholder` and `forgeconv-bun-literal-question-mark`.
A query assembled at run time is not judged — keep hand SQL in a constant.

### What a bun statement binds

A statement run on `db.Bun()` (a `Client`'s, a `RunTx` ctx's, or an explicit
`Tx`'s) reaches postgres as one text per statement *shape*, its values bound,
so pgx prepares it once per connection instead of once per value:

- **Bound:** every column value of a model (INSERT values, UPDATE SET,
  `WherePK`), numbers included; every string, time, `[]byte` and JSON `?`
  argument; anything wrapped in `orm.Arg(v)`, and each element of
  `orm.ArgList(slice)` (bun.In, bound).
- **Not bound:** an integer or float `?` argument (bun formats it without
  asking the dialect) — wrap it in `orm.Arg` on a hot statement; NULL,
  TRUE/FALSE, DEFAULT; and whatever you write into the SQL text itself.

```go
q.Where("chat_id = ?", chatID).Where("seq > ?", orm.Arg(since)).Where("kind IN (?)", orm.ArgList(kinds))
```

**Slices: an array, a document, or a list — say which.**

| You pass | It binds as |
|---|---|
| `orm.Arg(slice)` (or a pointer to one) | a postgres **array**, as pgx encodes `[]T`: `kind = ANY(?)`, `tags @> ?` |
| `orm.JSON(v)` | `v`'s JSON text, for a `json`/`jsonb` operand: `labels @> ?` |
| `orm.ArgList(slice)` | one parameter per element, for `IN (?)` |
| `orm.Arg([]byte)` / a bare `[]byte` | `bytea` |
| a **bare** slice `?` argument | bun's default: **JSON** — wrap it in one of the above |

`pgdialect.Array(v)` still works and means the same as `orm.Arg(v)`.

A value stays a literal wherever a parameter would not mean what it meant:
a positional `ORDER BY ?`, a typed literal (`INTERVAL ?`), a bare argument of a
function such as `jsonb_build_object(?, …)` (no type for `$1`), `? IS NULL`,
and an `ON CONFLICT … DO` target. Anything forge cannot prove is bun's
statement with values turned into parameters runs exactly as bun rendered it.

**A value the planner must see goes in the text.** A generic plan cannot prove
a partial index's predicate from a parameter: write that constant into the SQL,
or pass `orm.Literal(v)`. `orm.InlineValues(ctx)` turns binding off for every
statement run with that ctx.

## Errors from an override must be classified

Return `svcerr` sentinels (or a `*connect.Error`) from any op closure —
`svcerr.NotFound("order")`, `svcerr.InvalidArgument("...")`. `pkg/crud` reads
the classification and preserves your code and message.

An error that carries **no** classification is treated as a server fault: it
becomes `Internal`, its text is replaced with a safe message, and the original
is kept as a server-side cause for the logs. That is right for a driver or SDK
error nobody triaged, and wrong for a decision you made — so make the decision
explicit rather than returning a bare `errors.New`.

```go
op.Entity = func(ctx context.Context, req *pb.CreateJobRequest) (*db.Job, error) {
    e, err := build(ctx, req)
    if err != nil {
        return nil, err
    }
    if !s.ownsCustomer(ctx, e.CustomerId, claims.OrgID) {
        return nil, svcerr.InvalidArgument("customer_id does not name a customer of this org")
    }
    return e, nil
}
```

See also: `db` for the schema/ORM half, `service-layer` for where business
logic belongs.

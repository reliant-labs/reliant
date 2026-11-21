---
name: crud-overrides
description: Declared row ownership (`forge:owner=` on a table, through FK paths or user-OR-org, enforced by the generated ops) and diverging from generated CRUD without forking the projection — override op fields in your owned handlers_crud.go, the per-op seams (op.Filters, op.Fetch, op.Persist) a per-caller policy attaches to, what the Update op already does for read-only fields, and the one set-based UPDATE that replaces a List + per-row update loop.
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

## Declared ownership: `forge:owner=` on the table

When a table's rows belong to someone, declare whose in the migration and the
generated ops enforce it. No override to write, none to forget:

```sql
COMMENT ON TABLE folders  IS 'forge:owner=owner_id';                  -- a column of this table
COMMENT ON TABLE notes    IS 'forge:owner=folder_id>owner_id';        -- through a FK: notes.folder_id → folders.owner_id
COMMENT ON TABLE plans    IS 'forge:owner=thread_id>chat_id>user_id'; -- any number of hops
COMMENT ON TABLE accounts IS 'forge:owner=user:user_id,org:org_id';   -- alternatives: the user OR any of their orgs
```

The value is a comma-separated list of alternative owners, and a row is the
caller's when ANY of them matches. Each one is `[<kind>:]<col>[><col>…]`.
`kind` defaults to `user`. Every column except the last must be a single-column
FOREIGN KEY, and the next column is read on the table it references. A
declaration that does not resolve against the applied schema (an unknown
column, a hop through a column that is not a foreign key) fails `forge
generate`. A misdeclared owner must never quietly become an unscoped table.

**Who the caller is comes from you.** Forge does not know your org model. The
service gains a `Owners crud.OwnerResolver` dep, and compose fills it by type
from a provider in `internal/app/providers.go`. `forge generate` fails, naming
the missing provider and printing this one, until you add it:

```go
type OwnersDeps struct{}

func NewOwners(OwnersDeps) (crud.OwnerResolver, error) {
	return crud.OwnerKinds{
		"user": func(ctx context.Context) ([]string, error) {
			claims, err := middleware.GetUser(ctx)
			if err != nil {
				return nil, crud.ErrNoCaller
			}
			return []string{claims.UserID}, nil
		},
		"org": func(ctx context.Context) ([]string, error) { /* the caller's org ids */ },
	}, nil
}
```

In tests, the harness stubs `Owners` with one that errors until you program
it, so pass `With<Svc>Deps(Deps{Owners: …})`.

A kind the resolver does not know is an Internal error, never an empty scope.

**What each op does** (`forge/pkg/crud`'s `Owned*` helpers):

| op | behavior |
|---|---|
| Get | one SELECT with the PK and the owner predicate; another tenant's row is **NotFound**, the same answer as an unknown id |
| List | the predicate is appended in the query, so `total_count` counts only rows the caller can see |
| Update | looks up the STORED row under the scope (**NotFound** otherwise), writes it, then looks it up again. If the caller would no longer own the written row it answers **PermissionDenied** and the write rolls back. All of this runs in one SERIALIZABLE transaction |
| Delete | scoped lookup then delete, in one SERIALIZABLE transaction; outside the scope it answers **NotFound** |
| Create | inserts, then checks that the caller owns the new row: a plan under someone else's thread is **PermissionDenied** and rolled back. A parent that does not exist gets the same answer, because the path's first FK violation is mapped, so the answer reveals nothing about which ids exist. A direct owner column the row leaves empty is stamped from the caller's one value for the first kind, so the client never sends its own user id |
| no caller | **Unauthenticated**, before any statement runs. A resolver that was never wired is Internal. Either way the op fails closed, `auth_required: false` rpcs included |

The predicate is nested `IN (SELECT …)` subqueries, one per hop. A
path follows the reference, not the parent's liveness: a soft-deleted thread
still owns its plans for scoping purposes.

**Overrides still compose.** The op's `Fetch` / `Persist` / `PersistMasked`
closures still take trailing `opts ...orm.QueryOption`, and `Filters` still
runs first. A shim that appends a predicate (below) narrows the declared scope
further and cannot widen it. Replacing a closure wholesale, without calling the
generated one, is the escape hatch, and it drops the declared scope with it.

**Tables with no owner.** A table with CRUD on an authenticated rpc and no
owner declaration is flagged by the `crud-table-unowned` lint (it warns for one
release, then gates). Answer it by declaring an owner, or by saying why the
table has none:

```sql
COMMENT ON TABLE plan_catalog IS 'forge:public: the pricing page lists every plan';
COMMENT ON TABLE feature_flags IS 'forge:unscoped: admin service only; scoped in its handler';
```

The reason is required. These markers change no codegen. Forge writes none
for you, scaffolded tables included.

The column form, `COMMENT ON COLUMN t.c IS 'forge:owner'`, is unchanged. It
arms the `unscoped_auth` audit and scaffolds a wrapper you finish by hand
(`auth/authorization`). A table that declares the table form does not get that
wrapper, because the op already enforces the scope.

## Where a per-caller policy attaches

Forge enforces no policy beyond a declared `forge:owner=` (above), so for any
other restriction, these are the seams it attaches to. What the rule should be
is yours. These are the mechanics of each op, and the constraints are not
obvious from the struct.

Every request-projecting closure takes a `context.Context`, so the caller's
claims are reachable from all of them. If a table declares the `forge:owner`
COLUMN marker, forge scaffolds these overrides for you (see
`auth/authorization`). What follows is the mechanics for everything else.

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
- **Anything reachable only via a join** — declare it as a path
  (`forge:owner=fk>…>col`, above) when it is ownership. Otherwise
  `orm.QueryOption` is `func(*bun.SelectQuery)`, so drop to raw bun:
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
Unmaskable: []string{"customer_id"},              // forge:immutable / no wire field: refused too
Persist: ... db.UpdateOrder(ctx, s.deps.DB, entity,
        crud.Preserve("status", "shipped_at")) ... // a full replace leaves them as stored
```

`Unmaskable` lists the `forge:immutable` columns and the columns with no wire
field. It only gates the mask: pass `op.ReadOnly`, never `op.Unmaskable`, to
`crud.Preserve` — a column with no wire field is yours to set from an
`op.Entity` hook, and Preserve would silently drop that write.

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
`op.ReadOnly = nil` and filters the paths in `op.PersistMasked`. An Update a
client may legitimately use to set an immutable column (an admin reassigning an
owner) removes it from `op.Unmaskable` in that override, and nowhere else.

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

The raw passthrough never rewrites `?`: there it is postgres's jsonb operator
(`payload ? $1` works as written). A raw statement that has arguments, no
`$N`, and a `?` outside literals and comments is bun-style SQL on the wrong
path, so `db.Exec` / `db.Query` refuse it with `orm.ErrQuestionPlaceholders`
(and `db.QueryRow` returns it from `Scan`) instead of sending it to fail as
`syntax error at or near ","` (SQLSTATE 42601). Number the parameters `$1..$N`,
or move the statement to `db.Bun().NewRaw`.

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

## A write over many rows is one statement, not a loop

The generated writes are single-row (`Update<Entity>Masked(ctx, db, e, fields)`).
Asked to change every row matching a condition — archive a customer's open
orders — do **not** `List<Entities>` and call `Update<Entity>Masked` per row:
that is N round trips inside one transaction, and at 150,000 rows it hit the
30s rpc deadline. The generated `Update<Entity>Where` is one statement over
List's own filters, and returns how many rows it changed:

```go
n, err := db.UpdateOrderWhere(ctx, s.deps.DB, &db.Order{Status: "archived"}, []string{"status"},
	orm.WhereEq("customer_id", customerID), orm.WhereNotEq("status", "archived"))
```

It stamps `updated_at`, never writes a soft-deleted row, and refuses a call
with no filter (`orm.WhereIsNotNull` on the key means every row);
`orm.WithOrderBy` + `orm.WithLimit` write a bounded batch. An entity with a
`forge:version` column gets no `Update<Entity>Where` (and `crud.Repo.UpdateWhere`
refuses it): each of its writes is a compare-and-swap against the version the
caller read, and a set-based write has no version of each row to compare, so it
would overwrite the concurrent writes the column exists to catch. Write such rows
per row with `Update<Entity>Masked`, or — only when overwriting is the intent — a
raw `UPDATE` that also sets `version = version + 1`.
For a write it cannot express, write one set-based `UPDATE` in the entity's
`internal/db/<entity>_repo_ext.go` (the seam's header carries an example for
your entity), and call it from the handler:

```go
// internal/db/order_repo_ext.go (yours)
func ArchiveOpenOrders(ctx context.Context, db orm.Context, customerID string) (int64, error) {
	res, err := db.Bun().NewUpdate().Model((*Order)(nil)).
		Set("status = ?", "archived").
		Set("updated_at = now()"). // a builder write bypasses the generated stamping
		Where("customer_id = ?", customerID).
		Where("status <> ?", "archived").
		Exec(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
```

- Columns are named in the SQL text and values passed as `?` arguments — there
  is no `orm.Ident` or column-builder helper to look for. The column names are
  exactly `db.<Entity>Columns`.
- A `nil` model pointer (`(*Order)(nil)`) gives bun the table without loading
  a row. On a soft-delete entity bun adds `deleted_at IS NULL` itself, so a
  deleted row is not rewritten (`WhereAllWithDeleted()` opts out).
- It joins the caller's transaction like any `_repo_ext.go` query: call it with
  the ctx `s.deps.DB.RunTx` hands its fn.
- Nothing generated runs per row, so nothing per row happens: no
  `forge:read-only` / `forge:immutable` enforcement, no per-row hook. The
  handler owns which columns a set-based write may touch.
- `RowsAffected()` is the count to return (or to compare against an expected
  count before committing).

A loop over a handful of rows is fine; the rule is about writes whose row
count grows with the data.

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

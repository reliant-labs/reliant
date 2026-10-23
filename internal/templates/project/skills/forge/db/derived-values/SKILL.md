---
name: derived-values
description: Columns whose value is computed rather than entered — a same-row derivation is a GENERATED column declared at birth with `// forge:generated <expr>`, an other-rows rollup is a plain `forge:computed` column the owning RPC writes — plus the postgres rules a generated expression must meet.
---

# Derived values

A column whose value follows from other data is one of two kinds, and the
kind decides everything: who computes it, when, and what forge generates.

| derived from | example | column | who writes it |
|---|---|---|---|
| the SAME row | `line_total = quantity * unit_price` | `GENERATED ALWAYS AS (…) STORED` | postgres, on every write |
| OTHER rows | `amount_paid = sum(payments.amount)` | plain, `// forge:computed` | the RPC that owns the change |

## A value derived from the SAME row is a generated column

`total = subtotal + tax`, `line_total = quantity * unit_price`, `balance =
amount - amount_paid`. Declare the derivation; do not assert it and then
maintain it by hand:

```sql
-- YES: the database computes it, always, for every writer.
total_cents BIGINT GENERATED ALWAYS AS (subtotal_cents + tax_cents) STORED

-- NO: states the rule without implementing it. Nothing computes the column,
-- so it sits at its DEFAULT and every INSERT violates the CHECK.
total_cents BIGINT NOT NULL DEFAULT 0,
CONSTRAINT total_is_subtotal_plus_tax CHECK (total_cents = subtotal_cents + tax_cents)
```

The CHECK version costs you the whole chain: forge's write envelopes exclude
the column (correctly — no client should assert it), so nothing writes it;
`forge db seed` warns it cannot place the value; and the fix people reach for
is hand-written CRUD overrides that every future entity needs too. The
generated column needs none of that, and no CHECK — the equality is true by
construction.

### Say it at birth, in the proto

```proto
// forge:entity
message LineItem {
  double quantity = 1;
  int64 unit_price_cents = 2;
  // What the customer pays for this line, rounded to the cent.
  int64 line_total_cents = 3; // forge:generated round(quantity * unit_price_cents)::BIGINT
}
```

births

```sql
-- line_total_cents: forge:generated — postgres computes it; no INSERT or UPDATE may write it.
line_total_cents BIGINT NOT NULL GENERATED ALWAYS AS (round(quantity * unit_price_cents)::BIGINT) STORED
```

so the owned migration is right from its first byte and nobody hand-edits it
afterwards. What the marker means:

- **The expression is the rest of the comment line, verbatim.** Forge never
  parses it. Prose explaining it goes on its own line above, as here.
- **NOT NULL unless the field is `optional`** (a Timestamp is always
  nullable). That is the rule every born column follows — wire presence IS
  the column's nullability — so an expression that yields NULL for a plain
  field fails that write loudly instead of storing a value the wire field
  cannot carry. Mark the field `optional` when NULL is a real answer.
- **No DEFAULT** (postgres refuses one on a generated column). An enum keeps
  its vocabulary CHECK and `buf.validate` rules still project to CHECKs, so
  the expression cannot produce a value the domain does not have.
- **Read-only.** The field leaves the born Create request and the generated
  Update refuses an `update_mask` naming it. The ORM projects the column with
  `forge:"generated"` on its struct tag: never named in an INSERT or SET,
  read back (`RETURNING`) after every write, so the entity you hold after
  `Create`/`Update` carries the computed value.
- **Single-valued scalar, enum and Timestamp fields.** A repeated, map, oneof
  or nested-message field is refused; write its generated form in the
  migration by hand.
- **Inert after birth**, like every birth marker. To change the expression,
  write a new migration: `ALTER TABLE … ALTER COLUMN … SET EXPRESSION AS (…)`
  on postgres 17+, drop and re-add the column before that.

### Birth asks postgres before it writes anything

Birth applies the rendered migration to the shadow database (the same server
and replay of `db/migrations` that `forge generate` uses) inside a transaction
it always rolls back. If postgres rejects it, the birth is refused — no
migration, and your proto untouched — naming the marker's file and line with
postgres's own message:

```
⚠️  LineItem — postgres rejected the forge:generated expression on message LineItem (nothing was written):
    jobs.proto:49: int64 line_total_cents = 4; // forge:generated round(quantityx * unit_price_cents)::BIGINT
      postgres: pq: column "quantityx" does not exist at position 11:65 (42703)
```

The rules it most often catches:

- **Every function must be `IMMUTABLE`.** `now()` is not — a value that
  depends on when it was computed is not derived from the row. Two-argument
  `date_trunc('day', ts)` over a TIMESTAMPTZ is only `STABLE` (it reads the
  session timezone); the three-argument `date_trunc('day', ts, 'UTC')` is
  `IMMUTABLE` and is the one you want anyway (see `db/time-buckets`).
- **A generated column cannot read another generated column.** Spell the
  shared part out: `total = subtotal + (subtotal * tax_rate_bps + 5000) /
  10000`, not `subtotal + tax` when `tax` is itself generated. Birth names
  both markers when this is the problem.
- **Types must line up.** `round(double * bigint)` is a `double precision`;
  cast it (`::BIGINT`) to land in a BIGINT column.

## A value derived from OTHER ROWS is app-written

No generated column can express it. An invoice's `amount_paid` summing a
`payments` table, a job's cost rolling up its materials: postgres cannot reach
another table from a generated column. Keep the column plain, mark the proto
field `// forge:computed`, and write it from the RPC that owns the change (no
CRUD Update writes it) — `forge lint --computed-fields` then holds you to it
and fails if nothing assigns it. Do that write inside the same
`s.deps.DB.RunTx(ctx, orm.Serializable, …)` as the change it summarizes, so the rollup and its inputs
commit together.

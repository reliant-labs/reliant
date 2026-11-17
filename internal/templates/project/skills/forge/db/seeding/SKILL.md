---
name: seeding
description: Development seed data — the runtime seeder derived from the applied schema, `forge db seed` commands, domain vocabulary in db/seeds/vocab.yaml, and hand-authored demo data in db/seeds/custom/.
---

# Seeding Development Data

Seed data is not a file in your project — it materializes at runtime,
deterministically, from the applied schema: FK-coherent, ~20 rows/table,
idempotent.

## Do this first: fill in `db/seeds/vocab.yaml`

**Write the vocabulary BEFORE the first seed, not after you dislike the rows.**
It takes about two minutes, it is the step that decides whether seeded data
reads like your product, and it is the step every hurried run skips — then
spends longer working around. The full reference is `db/seeding/vocab`; the shape is:

```yaml
pools:
  product_names: [Chemex Pour-Over, Aeropress Go, Hario Skerton Grinder]
columns:
  products.name: {pool: product_names}
  products.currency: [USD]                              # see the traps in db/seeding/vocab
  products.price_cents: {min: 1200, max: 24900, step: 100}
  orders.placed_at: {from: -90d, to: now}               # relative to today
```

Two defaults produce schema-valid data that still breaks a page, and both are
invisible until you look at the UI:

- **A 3-char `currency` column synthesizes as `"sa5"`** — right length, and a
  code no currency table recognises. `Intl.NumberFormat` *throws* on it, and
  the generated product page dies through its error boundary. Pin it:
  `products.currency: [USD]`.
- **An undescribed numeric column seeds as the ROW INDEX** (`1, 2, 3 …`), so
  money renders as fractions of a cent and two numeric columns on one table
  come out perfectly correlated. Give it a range.

```
forge db seed apply    # introspect db/migrations, INSERT deterministic rows (dev only)
forge db seed status   # per-table seeded-row counts vs the seed model
forge db seed reset    # wipe seeded tables and re-seed (dev only)
```

`-C` from anywhere, a throwaway postgres (any loopback `--dsn`), or a
`seeded nothing` error: load `db/seeding/targets`.

`forge env up` auto-seed a fresh dev database on first boot
(every in-scope table empty): the synthesized rows, then `db/seeds/custom/`. `apply`/`reset` refuse any non-dev environment,
and the applier is never compiled into your server binary.

**What auto-seed writes to.** Only the tables behind your CRUD entities (a service
with `Create<X>`/`List<Xs>`/… RPCs over a matching table), plus any table those
reach through a NOT NULL foreign key. Every other table is plain schema your own
code owns — a ledger, an idempotency log, a payments table — and synthesized rows
there are fabricated facts, not demo data. An explicit `forge db seed apply`
seeds every table.

**There is no seed configuration in `forge.yaml`**: 20 rows per table, auto-seed on
for a fresh dev database, scoped as above. Skip it for one run with
`forge env up --no-seed`; for different rows, write the dataset under
`db/seeds/custom/` and `db/seeds/vocab.yaml`.

Keep seed data out of migrations: migrations define schema, seeds populate it.
Mixing them makes both harder to reason about.

## Where the rest lives

| You need… | Load |
|---|---|
| the full `db/seeds/vocab.yaml` reference: pools, time, booleans and NULL, numeric ranges, independent columns, pinning an exact value | `db/seeding/vocab` |
| which CHECK, UNIQUE and lifecycle constraints the seeder can satisfy, and how to write a status-lifecycle rule it can seed | `db/seeding/constraints` |
| a composite foreign key | `db/seeding/composite-fks` |
| which files and which database `forge db seed` acts on | `db/seeding/targets` |

## Two paths to one parent (`forge:ref`)

When an entity reaches the same parent **two ways** — `orders.patient_id` and
`orders.prescription_id → prescriptions.patient_id` — every foreign key is
individually valid and the two routes still name different rows. Those rows are
fixtures a correct implementation of the rule must *reject*, so **`forge db
seed` refuses to write them** and prints the relationship, the disagreeing
rows, and the statements to choose between.

Which route is authoritative is a fact about your domain, not something forge
can read off the schema, so you declare it — as a `COMMENT` on the foreign-key
constraint, in a migration:

```sql
COMMENT ON CONSTRAINT orders_patient_id_fkey ON orders IS
    'forge:ref derived-from=prescription_id';
```

| declaration | meaning |
| --- | --- |
| `forge:ref derived-from=<column>` | the route leaving by `<column>` decides; this column is seeded from it |
| `forge:ref authoritative` | this column is the truth; the other edge is narrowed to agree with it |
| `forge:ref independent` | genuinely unrelated facts (an order shipped to someone else's address) |

**`authoritative` is usually the right answer.** The record states its own
subject; the longer route is a back-reference. Deriving the other way can
disagree with a column that already scopes the row — a `company_id` stamped
from the caller, say — and then the derived value points at a parent outside
that scope. The rows still satisfy every foreign key, so nothing rejects them:
that class of bug is found by auditing the data, not by a failing test.

**One shape is not a decision, and forge no longer asks about it:** a NOT NULL
direct column whose indirect route leaves by a NULLABLE one. A row must always
name its parent directly and may carry nothing at all on the other route, so
the optional edge cannot be what determines a column that is never absent —
`derived-from` would have to invent a parent for every row whose route is NULL.
Forge resolves those as `authoritative` and stays quiet. What it still refuses
is the symmetric case, where **both** routes are required and either could
honestly be the truth. On a wide schema that is the difference between a page
of refusals and the handful that are real questions.

It lives on the constraint, not in a forge file, because it is a **schema-level
domain fact** — validation and your own business rules need the same sentence —
and because plain SQL lands in the postgres catalog where forge, `psql \d+` and
anything else can read it. Prose in the same comment is fine; only the
`forge:ref ` clause is read.

A composite foreign key needs no declaration: load `db/seeding/composite-fks`.

### Sweep the schema for diamonds yourself

Declare a diamond when you add the second reference rather than waiting for a
refusal. The seeder only reports a pair whose two routes ACTUALLY disagree in
the rows it just planned, so a domain with several diamonds surfaces them a few
at a time over successive runs. This query reports every one at once:

```sql
-- every child that reaches one parent BOTH directly and through a sibling FK,
-- with each route's nullability and whatever is already declared on it
WITH fks AS (
  SELECT c.conname, c.conrelid::regclass::text AS child,
         a.attname AS col, c.confrelid::regclass::text AS parent,
         a.attnotnull AS required
  FROM pg_constraint c
  JOIN unnest(c.conkey) AS k(attnum) ON true
  JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
  WHERE c.contype = 'f'
)
SELECT d.child, d.col AS direct_col, d.parent,
       s.col AS via_col, s.parent AS via_table,
       d.required AS direct_required, s.required AS via_required,
       COALESCE(obj_description(pc.oid, 'pg_constraint'), '(undeclared)') AS declaration
FROM fks d
JOIN fks s ON s.child = d.child AND s.col <> d.col
JOIN fks t ON t.child = s.parent AND t.parent = d.parent
JOIN pg_constraint pc ON pc.conname = d.conname AND pc.conrelid = d.child::regclass
ORDER BY 1, 2;
```

`forge env config <env>` prints the DSN to run it against (see `dev`).

One rule usually settles every row it returns: when the intermediate link is
REQUIRED and determines the parent (a payment's payer follows from its
invoice), the parent is `derived-from` that link; when the link is OPTIONAL
provenance (an estimate records the lead it came from), the row's own column is
`authoritative`.

## Hand-authored demo data (`db/seeds/custom/`)

Domain-flavored demo rows that the generic synthesizer can't invent go in
`db/seeds/custom/`. These are applied **after** the runtime dataset, so
`ON CONFLICT` overrides of seeded rows are sanctioned there.

See also: `db` for the schema/ORM half.

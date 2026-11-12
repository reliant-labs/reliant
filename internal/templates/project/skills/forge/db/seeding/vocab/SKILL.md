---
name: vocab
description: The db/seeds/vocab.yaml reference — pools and per-column vocabularies, time ranges relative to today, booleans and NULL, numeric ranges with step, why every column is drawn independently (so rows are not coherent), and pinning an exact value.
---

# Domain vocabulary (`db/seeds/vocab.yaml`)

**This file is the only place your domain's words come from.** Nothing else
supplies them, and forge does not invent them.

forge derives everything your schema *declares* — a column's type, its enum or
CHECK vocabulary, a regex `CHECK (col ~ '...')`, `char_length` / `varchar`
caps, numeric ranges, NOT NULL, UNIQUE, every foreign key. It does not derive
what a column *means*, because that is nowhere in the schema: nothing in
`price_cents BIGINT` says money, and nothing in `name TEXT` says whether the
row is a person or a company. forge will not read it off the column's name
either — a name guess is right for the vocabulary it was written against and
silently wrong everywhere else.

So a column nothing describes seeds an obviously-invented value:
`sample_<column>_<row>`, type-correct and inside every constraint. **A database
full of `sample_*` is not a bug — it is forge reporting that this file is
empty.**

`db/seeds/vocab.yaml` — yours, scaffolded once — teaches it your domain's
vocabulary. **Write it right after authoring migrations, while the domain
context is fresh:**

```yaml
pools:                              # shared pools, referenced by name
  city_names: [Lisbon, Osaka, Nairobi, Montreal, Reykjavik]
columns:                            # "table.column": inline list, {pool: name},
                                    # {type: name}, {min: n, max: n},
                                    # or {from: offset, to: offset}
  warehouses.city: {pool: city_names}
  suppliers.city: {pool: city_names}
  carriers.name: [Northwind Freight, Cordon Logistics, Alto Shipping]
  shipments.weight_grams: {min: 100, max: 25000, step: 50}
  carriers.rating: {min: 1.0, max: 5.0, step: 0.5, decimals: 1}
  carriers.active: [true, true, false]
  shipments.delivered_at: [null, -1d, -3d, -7d]
```

**A repeated entry weights the draw**, whatever the value's kind.

## Time, booleans and NULL

Time columns (`TIMESTAMPTZ`, `DATE`, …) are written **relative to now**, the
start of today (UTC): `{from: -90d, to: +30d}` spreads across a range (optional
`step:`; default 1m, 1d for `DATE`); `[now, -3d, +1w]` or ISO dates list exact
instants. Units: `m` `h` `d` `w`. Undescribed, one lands in the last four
weeks; describe any whose dates *mean* something.

That "now" is today only when seeding a live database. `forge generate` renders
the same plan into committed files (the frontend `src/mocks/*_gen.ts` fixtures,
entity factories), and those anchor "now" at a fixed 2024-01-29. Committed
output must not change with the calendar, so the mocks' dates differ from the
seeded rows' even though every other value agrees.

`BOOLEAN` takes `true`/`false` (undescribed: a coin flip). `null` is a list value
for a **nullable** column; on a NOT NULL column it refuses the seed.

## Numeric columns take a range

`{min, max}` (optionally `step`, and `decimals` for a fractional column)
expands to a numeric pool and is drawn from exactly like an authored list.
**Describe every numeric column that means something.** A numeric column with
no CHECK and no vocabulary synthesizes as the row index — `1, 2, 3 …` — which
satisfies the schema, is never warned about, and is wrong in two ways that
only show up in the UI: a money column renders as a fraction of a cent, and
any two numeric columns on the same table are perfectly correlated (row 7 is
`price_cents = 7` *and* `stock_quantity = 7`).

A numeric column that already carries a CHECK — a range (`price_cents >= 0`)
or an IN-list (`level IN (10, 20, 30)`) — is read off the schema and needs no
entry here; the range is for the unconstrained ones.

Matched columns draw from your pools (deterministically, like everything
else); unmatched columns keep built-in synthesis. forge validates every value
**you supply here** against the applied schema — CHECK vocabularies,
varchar/char_length caps, numeric ranges, and the canonical spelling of a
`uuid` column — skipping invalid ones with a warning and falling back to
built-in synthesis for that column. Generated CRUD lifecycle-test fixtures
reuse the same seed plan, so test parent rows carry the same vocabulary.

## Every column is drawn INDEPENDENTLY — rows will not be coherent

This is the thing that surprises people, and it is visible the moment the data
reaches a UI. Each column is a separate deterministic pick, so filling three
pools on one table gives you three unrelated draws per row, not a sensible
row:

```
name: "Bluetooth Turntable"   category: PRODUCT_CATEGORY_HOME   emoji: 🧦
```

Every value is schema-valid and no warning fires — it is a sock-emoji
turntable filed under home goods. Nothing in vocab.yaml can tie those columns
together; there is no archetype or row-template concept, by design (the
column-local hash is what makes a draw stable when you edit a different
column's pool).

That is fine for what seeds are FOR — FK-coherent volume to exercise CRUD,
pagination, and constraints, where nobody reads the rows. It is not fine for a
screenshot, a sales demo, or a dogfooding pass, where incoherent rows read as
an unfinished product.

**When rows have to make sense, hand-write them in `db/seeds/custom/`** — it is
applied after the generated dataset and is exactly the seam for this. A dozen
coherent rows on top of the synthesized volume gets you both.

## Pinning an exact value

A pool of **one** is a pin — the way to state a value forge could not invent:

```yaml
columns:
  users.email: ["dev@example.com"]                              # every row
  orders.currency: ["USD"]                                      # which currency is YOUR decision
  users.external_id: ["3f2504e0-4f89-41d3-9a0c-0305e82c3301"]   # uuid, shape-checked
```

**Keys are not pinnable.** An entry on a primary-key or foreign-key column is
refused with a warning: referential coherence is what the seeder guarantees by
construction, and a hand-written key is a reference it cannot prove resolves.
So you cannot declare the id of a particular row, and you cannot write "this
order belongs to that user" here — both halves are keys.

What you get instead is the **row-0 spine**: row 0 of every table references
row 0 of every table it points at, so one fully-connected dataset always
exists. Ids come from forge's key function and are stable for a given salt, so
querying the database once tells you an id that will still be there next seed.
If a value must exist *before* the seed runs — a principal id baked into a
token your issuer mints — write that row yourself in `db/seeds/custom/`, which
is applied after the generated dataset and is where you own the INSERT and
therefore the key.

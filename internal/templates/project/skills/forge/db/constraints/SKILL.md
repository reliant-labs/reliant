---
name: db/constraints
description: Schema declarations a migration makes beyond columns — the column write policy (ownership, forge:immutable, forge:version), append-only tables, the authoritative route when two FKs reach one parent (forge:ref), CHECKs that mirror a Go set (forge:closed-set), derived values (generated vs computed), and status-lifecycle CHECKs as implications.
---

# Schema declarations in the migration

## Column write policy — ownership, immutability, concurrency

Whether a full-replace UPDATE may touch a column is schema truth, declared as a
`COMMENT ON COLUMN` in the migration that owns it and introspected by `forge
generate`. Three decisions live there, all in `db/write-policy`:

- **Row ownership** — forge stores none of its own. If rows belong to a user, an
  org or an account, that is a column you write in the birth migration; adding
  it later is a migration plus a backfill.
- **`forge:immutable`** — keeps a full-replace Update from zeroing a column the
  request never carried (a server-assigned owner id, a Stripe customer ID, a
  creation stamp).
- **`forge:version`** — opt-in optimistic concurrency. Without it the last
  writer wins silently.

Table policy: `COMMENT ON TABLE payments IS 'forge:append-only';` makes a
ledger immutable in both stores and free-function delegates: no Update/Delete.
The birth marker writes the trigger AND comment; on later generates, applied
schema (not the marker) decides. Contradictory RPCs are refused. Correct a row
by appending its reversal. Load `forge skill load db/append-only` for the
compile-time surface and recognition of existing guard triggers.

## Two routes to the same parent

When a table reaches the same parent two ways — `estimates.customer_id`
directly, and `estimates.job_id → jobs.customer_id` — both foreign keys are
individually valid and the two routes can still name different rows. Declare
which one wins with a `COMMENT ON CONSTRAINT ... 'forge:ref ...'` in a
migration, the same `COMMENT ON` vocabulary as `forge:immutable` above on a
constraint instead of a column. `forge db seed` refuses to write rows where an
undeclared pair disagrees. The vocabulary, the sweep query that finds every
diamond at once, and which route to pick are in `db/seeding`.

## A CHECK that mirrors a Go set

A CHECK listing a vocabulary Go also owns (access-token scopes) declares it:
`COMMENT ON CONSTRAINT … IS 'forge:closed-set <set>'`, and `forge lint` keeps
the two equal. Set names and the fix migration: `db/closed-sets`.

## A derived value is generated, or written by its owner

Derived from the SAME row (`line_total = quantity * unit_price`) → a `GENERATED ALWAYS AS (…) STORED` column, declared at birth with `// forge:generated <expr>`; never a plain column plus a CHECK asserting the equality, which nothing ever satisfies. Derived from OTHER rows (`amount_paid` summing `payments`) → a plain column marked `// forge:computed`, written by the RPC that owns the change. Load `db/derived-values` for the postgres rules a generated expression must meet (birth checks them) and worked examples.

## A status-lifecycle rule is an implication, not a biconditional

"An approved estimate has an approval timestamp" seeds cleanly one way only:

```sql
-- YES: one-way implication. Any number of these over `status` seed fine.
CONSTRAINT estimates_approved_has_stamp
    CHECK (status <> 'ESTIMATE_STATUS_APPROVED' OR approved_at IS NOT NULL)

-- NO: biconditional. `forge db seed apply` cannot place it, nor any guard on
-- `status` beside it, and apply is one transaction: the whole dev dataset.
CONSTRAINT estimates_approved_iff
    CHECK ((status = 'ESTIMATE_STATUS_APPROVED') = (approved_at IS NOT NULL))
```

Enforce the mirror half (not approved ⇒ NULL stamp) in the RPC that owns the
transition. Why, and the mixing rule: `db/seeding/constraints`.

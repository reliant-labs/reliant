---
name: constraints
description: Which constraints forge db seed can satisfy — CHECKs over one or several columns, UNIQUE, ranked bounds, and status-lifecycle rules as one-way implications or two-way biconditionals, with what the seeder does with a guard it cannot place.
---

# Constraints

Values **forge** synthesizes are satisfied by construction, not validated after
the fact — forge derived the CHECK from your proto field rules, so it generates
inside it. Synthesized strings are fitted to `char_length` / `varchar(n)` caps,
numbers to range CHECKs, enum columns draw from the CHECK vocabulary, and a
single-column UNIQUE column draws **without replacement** so it never collides
(a deeper value pool does not fix a colliding draw — drawing without
replacement does).

A column with a **regex** CHECK — the one a `string.email` or a `string.pattern`
field rule projects into — is seeded from the pattern itself, so the value
matches whatever you declared and does so whatever the column is called:

```sql
CREATE TABLE accounts (
    contact TEXT NOT NULL CHECK (contact ~ '^[^@\s]+@[^@\s]+\.[^@\s]+$'),
    reference TEXT NOT NULL CHECK (reference ~ '^SKU-[0-9]{4}$')
);
-- seeds  contact = 'user1@example.com', …   reference = 'SKU-0001', 'SKU-0002', …
```

The email CHECK is the one pattern seeded by its meaning rather than its
shape: the shape alone gives `1@1.1`, which satisfies the CHECK and fails the
form's email validator, so a seeded row could not be saved from its own
edit page. `example.com` is reserved for exactly this.

An **array** column whose CHECK confines its elements to a set — the shape a
closed-set array takes — draws its elements from that set, one per row unless a
`cardinality(col)` or `array_length(col, 1)` bound asks for more (or fewer):

```sql
CREATE TABLE access_tokens (
    scopes TEXT[] NOT NULL,
    CONSTRAINT access_tokens_scopes CHECK (
        cardinality(scopes) > 0 AND scopes <@ ARRAY['deploy:read', 'token:read'])
);
-- seeds  scopes = ARRAY['token:read'], ARRAY['deploy:read'], …
```

The set may be `ARRAY[…]` of strings, numbers or booleans, or a `'{a,b}'`
literal, on any element type. An array with no such set gets two invented
elements, fitted to any cardinality bound. `db/seeds/vocab.yaml` does not
describe array columns.

Anything genuinely unsatisfiable is resolved at **plan** time, before a single
INSERT: a UNIQUE column backed by a 5-value CHECK vocabulary can never carry 20
rows, so its table caps at 5 and the plan says so — the same cap a 1-1 foreign
key already gets.

A **cross-column ordering** CHECK (`expires_at > issued_at`) is placed: the pass
ranks the columns and assigns each a value above its lower bound, so the rows
satisfy it by arithmetic rather than by luck.

A **discriminated union** — a top-level `OR` of `AND`-groups, each pinning a
discriminator column and constraining its siblings from there — is placed too,
by picking one branch per row and satisfying it whole:

```sql
CHECK (
    (kind = 'wallet_credit'    AND amount_cents    IS NOT NULL AND amount_cents > 0
                               AND compute_minutes IS NULL)
 OR (kind = 'compute_minutes'  AND compute_minutes IS NOT NULL AND compute_minutes > 0
                               AND amount_cents    IS NULL)
);
-- seeds  row 0: kind='wallet_credit',   amount_cents=1,    compute_minutes=NULL
--        row 1: kind='compute_minutes', amount_cents=NULL, compute_minutes=2  …
```

Branches are taken **round-robin by row**, so every arm of the union appears in
the dataset — a table with only `wallet_credit` rows cannot exercise the other
redemption path at all. The matcher is narrow on purpose: the terms it reads are
`col = <literal>`, `col IS [NOT] NULL`, `col <op> <number>` and a non-blank
requirement, `length(col) > 0` (also `char_length`, `length(trim(col))`, `>= 1`).
A nested `OR`, a `NOT`, any other function call, or a comparison between two
columns is refused by name,
as is a union whose columns another mechanism owns (a key, a foreign key, a
UNIQUE column) or that a second multi-column constraint also spans.

## Status-lifecycle rules: one-way or two-way, both seed

"An approved estimate has an approval timestamp" has two spellings in SQL, and
forge seeds both. Write the one that states your rule; never weaken a
constraint to suit the seeder.

```sql
-- One-way implications: "this status requires the column".
CONSTRAINT estimates_sent_has_stamp
    CHECK (status <> 'ESTIMATE_STATUS_SENT'     OR sent_at     IS NOT NULL),
CONSTRAINT estimates_rejected_has_stamp
    CHECK (status <> 'ESTIMATE_STATUS_REJECTED' OR rejected_at IS NOT NULL),

-- A biconditional: "this status, and only this status, sets the column".
CONSTRAINT estimates_approved_iff
    CHECK ((status = 'ESTIMATE_STATUS_APPROVED') = (approved_at IS NOT NULL))
```

The implication is a **status guard**: an exempting arm (`<>`, `NOT IN`, or the
positive `IN (…)` complement) followed by what must hold when the row is *not*
exempt. Forge reads the guarded column's vocabulary from its single-column
`CHECK (status IN (…))`, rewrites each guard into positive branches, and MERGES
every guard over that column into one union with a branch per status value
carrying the consequents of every guard that value triggers. Because they are
merged rather than placed one at a time, they are not rivals to each other and
the count does not matter — three over one column seed exactly as cleanly as
one.

A **biconditional** `(P) = (Q)` is the union `(P AND Q) OR (NOT P AND NOT Q)`,
and its xor `(P) <> (Q)` is `(P AND NOT Q) OR (NOT P AND Q)`. Each side may be
`col = <literal>`, `col <> <literal>`, `col [NOT] IN (…)` or `col IS [NOT] NULL`.
When one side tests a column with a vocabulary and the other a presence, the
biconditional is a PAIR of guards over that column ("these values require Q",
"the rest require NOT Q") and merges with its one-way guards above: the
`estimates` table seeds DRAFT rows with no stamps, APPROVED rows with only
`approved_at`, and so on — every status, so both truth values appear. Other
shapes (two presence tests, two value tests) are placed as their own union.

A presence side on a nullable foreign key works too — reliant's

```sql
CHECK ((kind = 'workspace') = (daemon_id IS NOT NULL))
```

seeds workspace grants that reference a daemon and forge grants whose
`daemon_id` is NULL: the reference path declines the edge on exactly those rows.

The guarded column must carry a readable vocabulary — a single-column
`CHECK (status IN (…))` — for forge to write the rows where a value test is
*false* ("some other status"). Entities born from proto enums get this
automatically (`TEXT` + `CHECK (col IN (...))`). Without one, a one-way guard is
refused by name, and a biconditional is seeded on the side it can write (every
row `status = 'APPROVED'` with a stamp) with a warning naming the CHECK to add.

A biconditional whose side forge cannot read (`(char_length(code) > 3) = …`, a
function call, a comparison of two columns) keeps the refusal it always had, and
its rows satisfy it only by chance.

**A `BYTEA` column varies per row** (`sample_<column>_<row>`, hex-encoded), so a
`key_hash BYTEA NOT NULL UNIQUE` carries the full row target instead of capping
its table at one row. A `length(col) = N` CHECK is honored, and the row
discriminator survives the truncation.

**A partial unique index is read as the weaker statement it is.**
`UNIQUE (user_id) WHERE revoked_at IS NULL` means one *active* key per user, not
one key per user, so it does not cap the table — as long as forge can show the
predicate false of every row it writes. A predicate it cannot read keeps the
strict reading (the column is treated as plain `UNIQUE`), which is the safe
direction.

A **derivation** (`total_cents = subtotal_cents + tax_cents`) is not placeable
and the plan says so by name, because three independently synthesized values
make an equality true only by coincidence. It is not a seeding problem to work
around — the schema is asserting a rule it never implements. Declare it instead
(`GENERATED ALWAYS AS (…) STORED`, see `db/constraints`) and the column stops
being seeded at all, because postgres owns it. **Do not pin the columns to
`{min: 0, max: 0}` in `vocab.yaml` to force the CHECK to hold** — that trades a
warning for a demo database where every money column reads zero.

`apply` runs as one transaction: it seeds every table or leaves the database
untouched, so a failed run is always safe to retry and never leaves a
half-populated dev database behind.

**The same planner builds your test factories — in MINIMAL mode.** The dev
dataset fills every column and rotates through every union branch; the
regenerated `New<Entity>` / `New<CreateRequest>` factories (see `testing`) write
only what the schema requires — keys, NOT NULL columns without a usable
DEFAULT, NOT NULL references — leave the rest to DEFAULT or NULL, and take the
guard branch that keeps the status at its DEFAULT. So the implications and
biconditionals above, and a column made `GENERATED`, are safe to add to the birth migration right
after scaffolding: the next `forge generate` re-derives every test row.

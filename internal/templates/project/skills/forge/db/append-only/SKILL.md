---
name: append-only
description: Immutable ledgers — declare append-only tables in migrations so the generated stores and delegates cannot update or delete them.
---

# Append-only tables

Declare table write policy in the migration that owns it, alongside column
write policies (`forge skill load db/write-policy`).

One decision lives on the TABLE rather than a column, same `COMMENT ON`
vocabulary one level up:

- **`forge:append-only`** — `COMMENT ON TABLE payments IS 'forge:append-only';`
  declares an immutable ledger. Rewriting or erasing a row is a COMPILE error
  rather than a runtime rejection, because forge omits the mutators from BOTH
  routes to a write: the `PaymentStore` interface and its adapter lose
  `UpdatePayment` / `UpdatePaymentMasked` / `DeletePayment`, and so does the
  package-level delegate set in the entity's generated ORM file —
  `db.DeletePayment(ctx, tx, id)` does not resolve. The generated test factory also takes no overrides — applying one
  would UPDATE the row it just inserted. `forge scaffold entity --from-proto`
  writes this alongside the guard trigger when the message carries
  `// forge:append-only`; declare it by hand on a table a hand-written migration
  made append-only. Correcting a bad row means appending its reversal.

  Reads and `Create` are untouched, as is `WithTx`: append-only means immutable,
  not invisible.

  Because there is no delegate to call, an `Update`/`Delete` **RPC** declared
  against an append-only table cannot be wired, and `forge generate` refuses it
  by name rather than emitting an op that fails as `undefined: db.UpdatePayment`
  in a forge-owned file. That is a contradiction between two things you own —
  the RPC in the proto and the comment in the migration — so resolve it in
  whichever one is wrong: drop the RPC, or drop the declaration in a migration.

  The proto marker alone is not enough, and this is the trap: it is a
  BIRTH-TIME instruction. It writes the trigger and omits the Update/Delete
  RPCs, and then nothing carries it forward — every later pass reads the
  applied schema. Without the table comment an append-only ledger generates a
  store exposing `UpdateX`/`DeleteX` that type-check fine and fail only when
  postgres refuses them.

  A table born BEFORE forge wrote this declaration needs nothing from you.
  Forge installed a `<table>_append_only` guard trigger long before it wrote
  the comment, and detection reads that trigger as a declaration of the same
  fact — so an existing ledger keeps generating an immutable store with no
  migration and no action. Matching is by forge's exact trigger name, never by
  "some trigger fires on UPDATE": an `updated_at` stamper is also `BEFORE
  UPDATE` and means the opposite. A hand-written guard under a different name
  declares itself with the comment.

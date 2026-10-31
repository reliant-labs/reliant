# Plan: a smaller activation-approval fingerprint (not implemented)

Status: proposal from the 2026-10-28 audit (item S2). The current code ships
unchanged; this records why the simplification was not done in place and what
doing it takes.

## What exists

An interactive agent's `create/update/enable/pause_trigger_activation` call
shows the owner an approval card and applies only if nothing changed since the
card (`internal/handlers/trigger/activation_agent.go`):

- `activationSnapshot` (`activation_snapshot.go`, ~490 lines) walks the whole
  executable closure through the validator's loaders: every referenced child
  workflow (spawn, inline, failure branches), every preset, the effective
  inputs, the user's `tool.bindings.*` settings and the connection. It caps the
  walk at 64 dependencies and 2 MiB.
- The canonical bytes of all of it, the definition, the saved row and the card
  title are hashed in memory; the hash rides on the approval.
- `ManageActivation` re-prepares inside one `orm.Serializable` transaction after
  `LockActivationDependencies` takes `FOR SHARE` row locks on projects,
  project configs, drafts, presets, connections, tool-binding settings, daemons
  and project daemons, then compares hashes and calls the app RPC.

## Why it was not simplified in place

The audit proposed `(trigger revision, sha256(root workflow + presets))`
compared under the trigger row lock. That is not a contained change, because
the closure walk does two jobs:

1. **Fingerprint.** Replaceable by a revision plus a content hash.
2. **Disclosure.** `snap.Summary` is the privilege section of the card: the
   agent tier, the allowed tool privileges, fixed bindings and their target
   scope, and the count of executable node kinds per workflow dependency
   (e5717552f, a reviewed security change). It needs the same walk over child
   workflows and presets.

So deleting the snapshot either removes the disclosure the security review
asked for, or keeps the walk, and with it most of the 490 lines. Dropping the
SERIALIZABLE transaction and the dependency locks reopens the
check-then-write race those locks closed. The audit judged that race
acceptable, because later workflow edits stay live anyway. That is a
security-posture decision for the owner, not a refactor.

## Proposed design, if the owner wants it

1. **Revision instead of row snapshots.** Add `triggers.revision bigint` (bumped
   by every write) and `workflows`/`presets` content hashes computed on save. The
   approval stores `trigger_revision` plus `sha256(canonical root workflow ‖
   sorted (name, hash) of every resolved child workflow and preset ‖ connection
   id ‖ tool-binding settings hash)`.
2. **Keep one walk, shared by hash and card.** The disclosure walk stays. It emits
   the summary lines and the list of `(ref, content hash)` it visited, and drops
   the byte accounting (`maxApprovalSnapshotBytes`) and the in-memory canonical
   copies.
3. **Apply under the trigger row lock only.** `SELECT … FROM triggers WHERE id=$1
   FOR UPDATE`. Then recompute the visited hashes in a `ReadCommitted`
   transaction and compare. The `Serializable` transaction and
   `LockActivationDependencies` are deleted. An edit to a child workflow that
   lands between the compare and the write applies to future runs, which is
   already true for any edit made after approval.
4. **Tests.** Port `activation_security_test.go`. A changed child workflow, preset,
   binding or connection must change the hash. An unchanged closure must keep
   it. A concurrent trigger edit must be refused by the row lock.

Expected size: about −300 lines net, one migration (revision column plus
backfill), 1–2 days with tests.

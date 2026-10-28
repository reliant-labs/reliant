# Generic trigger capacity

Opt-in YAML:

```yaml
concurrency:
  max_running: 6
```

A pool is scoped to user, project and workflow reference. Trigger events queue
FIFO in the existing `trigger_events` table. Schedule overlap skipping is
replaced by queueing when a cap is configured. Manual trigger fires also obey
the cap. This does not yet cap arbitrary interactive chat starts or bounded
parallel loop iterations.

A paused or uncertain execution retains its slot. Only a matching immutable
Temporal workflow ID / FirstRunId chain with an observed terminal status frees
capacity. The chain includes Continue-As-New, retries, resets and cron. UI/chat
status is not release evidence. An outage holds capacity; a bound chain Temporal
reports NotFound has closed and aged past retention (Temporal deletes only closed
histories), so it is released. A row that cannot be reconciled is logged and keeps
its slot; it never fails the fire, so one bad row cannot stop a scope.

The launch transaction atomically claims capacity and creates the chat. A
Temporal memo carries the event ID to recover an accepted start whose binding
reply was lost; admitted launches reject duplicate workflow-ID reuse. Queued
intents expire after the same 24h give-up as any undelivered event (a queue that
never drains surfaces as failed events); a launched start awaiting its binding is
still redriven. Existing redrive
retries pending work, including scheduled intents and missing launch bindings.

No PR-specific subjects, verdicts, author registries or review tables are used.

## Verification and remaining acceptance

- Canonical pinned forge generation passed; whole-tree validation was skipped,
  with affected packages compiled by scoped tests instead.
- Scoped short tests passed for triggers, launch, store, workersetup, YAML and
  workflow validation.
- A real Firer → Launcher → PostgreSQL fixture proves queue order, a strict
  one-run cap, post-accept binding recovery and terminal-only release. It passed
  three repeats. Temporal transport is deterministic in this fixture, not a
  live-server proof.
- Store tests cover concurrent workers, transaction rollback, acknowledged
  claim retries, paused/deleted chats and wrong-chain release refusal.
- Boundary tests reject running, continued, missing/mismatched chain and API
  outage as release evidence.
- A real Temporal dev-server test passed: an actual Continue-As-New successor
  keeps the initial execution's slot, then a matching terminal successor releases
  it. That test uses a deterministic storage boundary; the separate FIFO launch
  test uses real PostgreSQL.
- Scoped convention and migration-safety lint passed.

## Fenced operator recovery (not an automatic expiry)

If a start is permanently unverifiable (unbound, with an uncertain root), capacity
stays occupied. Never release merely because time elapsed or the UI says done.
An authorized operator must first fence launch retries (the root must no longer
be PENDING), terminate/verify absence of the external execution, and record
that evidence. `ReleaseTriggerCapacityByOperator` requires actor and note,
conditionally releases one launched unreleased row with a non-pending/missing
root AND a durable start-attempt stamp older than `core.StartAttemptBound`
(currently five minutes). Both checks are part of the UPDATE, not just a prior
operator observation. It emits a warning with the actor. It does not perform
the external fence for the operator.

Operator ordering is mandatory: FIRST wait until the database reports the
last attempt older than `core.StartAttemptBound`; THEN fence/terminate and
Describe the external execution; FINALLY run the guarded UPDATE using that
same observed stamp. If any retry changes the stamp, repeat verification.

Equivalent audited SQL AFTER that ordering and external fencing/verification:

```sql
UPDATE trigger_events event
SET capacity_released = true, capacity_released_at = now(),
    capacity_release_reason = 'operator:<actor>:<external-fence-evidence>'
WHERE event.id = '<event-id>' AND event.capacity_order IS NOT NULL
  AND event.outcome = 'launched' AND NOT event.capacity_released
  AND event.capacity_workflow_id IS NOT NULL
  -- Must match core.StartAttemptBound; never remove this in-flight fence.
  AND event.capacity_start_attempt_at < now() - interval '5 minutes'
  AND event.capacity_start_attempt_at = '<observed-aged-stamp>'::timestamptz
  AND NOT EXISTS (SELECT 1 FROM workflows root
                  WHERE root.id = event.capacity_workflow_id AND root.state = 1);
```

Automatic unbound-start recovery uses the same durable root fence, then a
SECOND Temporal Describe before releasing a still-absent start. A late accepted
execution retains its slot and is memo-bound on a later pass. A pending retry
that wins the failed-start CAS prevents release. Namespace/transport errors
never count as absence.

Shipped in #1734; the expiry, NotFound and per-row fixes followed the 2026-10-28
audit (research/2026-10-28-audit-reliant.md B1/B2).

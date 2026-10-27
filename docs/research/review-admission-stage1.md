# Private automation admission: Stage 1 review handoff

Base: `8c0018501b03b574620ac90253162aacd0581672`. Draft PR #1710; no queue, activation, merge or release.

## Contract

```yaml
automation_only: true
concurrency:
  max_running: 6
  key: "trigger.payload.attributes.repository + ':' + string(trigger.payload.data.number)"
  supersede: latest
```

Scope is owner + project + normalized workflow **ref**, across declarations/activations. Display titles do not change scope. Ref/slug renames change scope; this is an operator automation policy, not a security quota. Existing global/tenant quotas are neither replaced nor raised. Stage 1 rejects scheduled declarations; inbound integration/webhook/workflow-event and manual activation intake use the queue. Builder test is not a production automation activation and is outside this pool; publication must not be wired to builder tests.

`latest` is intake order, NOT provider chronological order. Provider-normalized observed SHA is only exact-head identity. It does not claim current provider head. Private PR workflows must remain unactivated until authoritative-head admission/publication guards exist; a late old delivery must not displace a valid current review. This slice never requests cancellation and never releases on a cancellation request.

## Persistence and recovery

Existing trigger_events are queue and audit history. A short scoped advisory-lock transaction enqueues/coalesces and assigns a FIFO identity ordinal. One reserved event and one queued event per scope/key are partial unique constraints. Replacement preserves queued key's ordinal. Exact known head repeats attach to an existing queued/reserved/released intent. Unknown heads use ordinary delivery dedupe only.

Server `RELIANT_ADMISSION_PENDING_LIMIT` has proposed configurable default 1000 distinct queued keys per scope (not an owner-fixed product contract). At overflow, event remains durably failed with explicit admission overload detail. Existing ERROR-log metrics pipeline receives an overload record without payload/key; downstream alert delivery is **not yet verified**. Every acknowledged intake has durable storage.

Reservation is acquired within the launch transaction, atomically with chat/event claim. Half-start holds capacity. `Spec.OnStarted` binds original execution ID before run-recorder update. Temporal start memo contains only immutable admission event ID, used to recover acceptance-before-callback crash. Known IDs are described exactly; unknown run ID can bind only if root ID and typed memo generation match. Missing/different memo and NotFound hold capacity, never guess. The shared `runcontinuity` resolver proves authenticated internal Continue-As-New edges from that immutable original execution (maximum 64 hops, ten-second deadline). A live successor retains the permit; only a verified logical terminal result releases through CAS against the ORIGINAL event/root/run. Original admission identity is never overwritten by the successor. Retry/reset/unrelated followup, unavailable history, cycles, missing reader and exceeded bounds retain capacity for investigation. Production wiring supplies the raw Temporal history service and trusted dial namespace explicitly. Paused/waiting and cancelling remain reserved.

Existing redriver polls admission events independently of ordinary pending-event 24h expiry. Each bounded keyset sweep captures a fixed high-water ordinal and resets at that boundary, so continuous new intake cannot starve old reservations or queued replacements. The cursor is not ownership and loses no durable intent on restart. Deleted activations' queued orphan events settle durably failed under a queued-only CAS; reserved orphans retain capacity. `admission_workflow_id` persists the immutable Temporal root independently of the deletable chat FK, atomically with launch attachment, so hard chat deletion cannot leak a permit or change exact-execution reconciliation. No new bus or long-lived Temporal coordinator. Periodic reconciliation supplies recovery; terminal wakeup optimization deliberately deferred.

## Files and generation

Migration created by pinned `task db:migration NAME=trigger_admission`, version `20261027051513`. Proto input `workflow_v2.proto` field 20; no trigger.proto or identity edits. YAML parser + validator, trigger admission/intake/fire/redriver, narrow launcher hook/memo, provider observed-head adapter, store/repo methods. Generated ORM/Go/TS/descriptor/grants from pinned `task generate:forge`. Do not hand-edit generated outputs.

## Verification

Initial scoped suite passed for store, triggers/triggerspec, workflow YAML/validation, webhook and launch. New store tests use `storetest.New`, each creating a fresh migrated test database through pgtest (not port 5434). Additional final focused tests are being run before freezing a review commit. No whole-repo gates.

## Review focus / remaining work

Review transactional reserve/claim atomicity, exact run/memo fencing, recovery continuation and source trust distinction first. Stage 1 does not implement cancellation or publication; Chat A and publisher work own those. No private review workflow should be activated based on this primitive alone. Downstream overload alert routing requires confirmation, not a claim based on an ERROR log alone.

# Generic trigger capacity — draft, not released

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
status is not release evidence. An outage or missing chain holds capacity.

The launch transaction atomically claims capacity and creates the chat. A
Temporal memo carries the event ID to recover an accepted start whose binding
reply was lost; admitted launches reject duplicate workflow-ID reuse. Queued
intents do not expire under the inbound delivery age limit. Existing redrive
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
- Scoped convention and migration-safety lint passed.

Draft PR #1734 is not activated, merged or released. Independent review,
current-main integration, actual Temporal restart/chain tests, complete manual
start/alias semantics and retention/deletion audit remain acceptance work. Do
not call this finished or enable the workflow cap before those checks.

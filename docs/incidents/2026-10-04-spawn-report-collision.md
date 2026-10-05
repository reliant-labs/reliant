# 2026-10-04 — background spawn report collides with a reconciler placeholder (23505)

## Symptom

A detached spawn's `EnqueueAgentMessage` activity fails all three attempts with:

```
failed to enqueue agent message: ERROR: duplicate key value violates unique
constraint "idx_agent_messages_one_terminal_report_per_spawn" (SQLSTATE 23505)
```

The goroutine logs a Warn and moves on, so the sub-agent's real result is
discarded. The parent instead reads the reconciler's synthesized placeholder
("Sub-agent finished while its result was lost in transit…").

## Evidence

Temporal history (both runs read with `temporal workflow show`):

| chat | run | collisions | at (Z) |
|---|---|---|---|
| `34538e7f` | `3bba2409` | 1 (event 30518) | 2026-10-04 02:40:18 |
| `8bb0a875` | `7fce466e` | 6 | 2026-10-03 05:43:41 |

Timeline for `toolu_01BLyChUMEe9APM2sLVqQB3b` (chat `34538e7f`):

| time (Z) | what |
|---|---|
| 02:38:13.205 | child finishes; `runSpawnInlineChild` schedules `WorkflowStatus completed` for the child row **before** the report |
| 02:38:23 → 02:39:52 | root workflow task times out at 10s, three times in a row (history: 40,090 events / 62 MB) |
| 02:38:30.52 | reconciler pass sees child `state=3`, tool call `status=6`, no report ⇒ writes placeholder via `EnqueueAgentMessageIfAbsent` |
| 02:40:07.805 | goroutine resumes and schedules `EnqueueAgentMessage` — a plain INSERT |
| 02:40:18.263 | third attempt fails with 23505; placeholder delivered 30ms later |

Workflow-task durations in the affected runs: 8 timeouts each, and completed
tasks taking up to 17.9s (`34538e7f`) and 42.5s (`8bb0a875`). A smaller history
of the same chat (31 MB) peaked at 0.34s.

## Root cause

Two defects compound:

1. **Ordering.** `ListStrandedBackgroundSpawnToolCalls` defines "stranded" as
   *terminal child + backgrounded call + no terminal report*. The live path
   produces exactly that state on every normal completion, because it marks the
   child terminal before writing the report. Normally the window is
   milliseconds; a stalled root workflow task stretches it to minutes, and the
   30s reconciler poll lands inside it.
2. **No supersede.** The live enqueue is a plain INSERT against the one-report-
   per-spawn unique index, so once a placeholder holds the slot the real report
   can never land. Nothing distinguishes a synthesized placeholder from a real
   report, so nothing can replace one with the other. This was already listed as
   an open follow-up in `2026-09-29-reconciler-false-wedge.md`.

The 10s workflow task timeout is what turned the window from milliseconds into
minutes.

## Decisions

1. **Report before terminal status.** A detached spawn writes its mailbox report
   first, then the child's `WorkflowStatus` and `EmitToolCallStatus`. "Terminal
   child with no report" then only occurs when a report really was lost, which is
   the state the sweep assumes. Gated by `workflow.GetVersion` so in-flight
   histories replay their recorded order.
2. **A real report supersedes a placeholder.** `agent_messages.synthesized`
   marks reconciler-written rows (backfilled by their known body texts). The live
   enqueue becomes an upsert on the one-report slot that replaces a synthesized
   row whatever its status and re-queues it, so the parent always receives the
   real outcome. A slot already held by a real report is an idempotent no-op,
   not an error. That also covers an activity retry after a committed insert
   whose response was lost.

   The superseded row takes a **new id**. A drain lists queued rows outside its
   transaction and claims them by id. If the id were kept, a drain that listed
   the placeholder just before the supersede would claim the real report under
   the old id, write the stale placeholder text, and mark the real report
   delivered unseen. With a new id, the stale claim matches nothing, and the
   drain's existing partial-claim path re-reads the batch at the next boundary.
   This is pinned by `TestEnqueueAgentMessage_SupersedeDefeatsAStaleDrainClaim`.
3. **Workflow task timeout 10s → 60s** on every `DynamicWorkflow` start.
   Continue-as-new and reset carry it forward. 60s covers the worst observed
   task (42.5s) with margin and stays under Temporal's 120s ceiling. It is not
   the fix: the underlying cost is history size, which continue-as-new is meant
   to bound.

## Not done here

- Why these runs reached 40k events / 60 MB before continuing as new.

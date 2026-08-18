# Pause and resume

Status: design settled. Implementation in progress (runs-lifecycle service).

**This document is about PAUSE ONLY.** Queueing is a separate problem with its
own design (`specs/queueing-design.md`) and that design is fine. Do not merge
these two — conflating them is what produced the mess described below.

## Where we went wrong

Pause was assumed to need a **durable record of position**. That assumption
produced a `workflow_positions` stack: a per-frame table, written by every
executor at every depth, with `in_loop` / `memo` / `kind` columns and a content
hash. It shipped broken — created with `workflow_name NOT NULL` while the writer
never set it, so every position write failed at runtime with SQLSTATE 23502
while the build stayed green — and was reverted the same day.

The assumption was wrong. Nothing about pause needs new state.

## The design

**Position already lives in exactly two places, both Temporal's.**

- **Process alive** — position IS the goroutine stack. Executors block in place
  on `CanceledError` and re-dispatch the cancelled step, rather than unwinding to
  the top-level `retryLoop` and re-entering the node. Nothing is written down
  because nothing is lost. (This is the shipped nested-pause fix; it is correct
  and stays. Before it, unwinding re-ran node ENTRY work — re-forking threads and
  re-injecting seed messages, which is what duplicated an implementer's opening
  message 30 minutes later on chat 4d92f694.)
- **Process dead** — position IS Temporal history. Reset-and-replay rebuilds the
  nested engine stack. `continueAsNew` keeps history under the cap (hands off at
  40,000 events; reset only refuses at 50,700) and carries
  `Resume{NodeID, LoopIteration}` forward in the continuation's own input,
  atomically with the handoff — which a separate table write can only lag or
  fail behind.

**So pause is: signal, block in place, resume in place.** No position stack, no
frames, no hash. The flat `workflow_checkpoints` row survives only as the coarse
hint for the one path that can re-enter a top-level node, which is all it was
ever for.

## Terminate, stated once

- No UI control calls it. The stop button calls `pauseChat`
  (`ChatContainer.tsx:232`).
- One operator CLI command does: `reliant workflow terminate`
  (`cmd/reliant/commands/workflow_supervise.go:1245`), documented as terminal
  and DROPPING the resume checkpoint. Use `workflow pause` to stop while
  preserving the ability to resume.

Both are true at once: absent as a USER verb, alive as an OPERATOR verb. It gets
no UI, and `workflow cancel` is intentionally not kept as an alias. The CLI trio
(pause / resume / terminate) documents exactly the model this spec describes.

## The real work: abstraction

The reason the position stack looked necessary is that **no component owned the
question "where does this run continue from,"** so a table was invented to answer
it. With one owner, the answer is "ask Temporal," and there is nothing to persist.

### The leak

`ResumeChat` (`internal/grpc/services/chat_crud.go:1176`) is a gRPC handler that
knows, inline:

- the chat/workflow tables and `db.Failed()`
- how to query Temporal for execution state (`getTemporalWorkflowState`)
- what "stuck" means (DB Failed AND Temporal running → refuse, tell user to
  branch)
- that resume may RESET and mint a new run id, so the run id must be re-read and
  written back (`updateWorkflowRunIDs`)
- that `ErrWorkflowNotFound` maps to a needs-recovery response

The same policy is duplicated in `internal/grpc/services/chat_send.go` (~695,
~918) with its own stuck check. Two handlers, one decision, already drifting.

### The boundary

A run-lifecycle service owning "make this run stop / execute again," following
the `internal/threads` house pattern:

    runs.Pause(ctx, chatID) error
    runs.Resume(ctx, chatID) (ResumeOutcome, error)

`ResumeOutcome` carries only what a caller must RENDER, never how it was
achieved:

    Resumed        (+ authoritative run id)
    NeedsRecovery  (workflow lost — the branch/new-conversation prompt)
    Unresumable    (stuck — "use branch")

Inside, and only inside: workflow-id lookup, the stuck check, live-vs-dead
classification, signal-vs-reset choice, run-id refresh and write-back, and error
→ outcome mapping. `PauseService` is two-thirds of this already; the problem is
that it is not the only door, so it is wrapped or absorbed — but there is exactly
one door afterward.

### Acceptance test for the boundary

After the extraction, NO caller outside the runs service may reference
`db.Failed()` for a lifecycle decision, `getTemporalWorkflowState`,
`updateWorkflowRunIDs`, or `workflow.ErrWorkflowNotFound`. That is grep-able and
should be checked, not assumed.

A handler becomes: authenticate → call the service → map outcome to proto.

## Rules this design is held to

- Pause adds NO new persisted state. If a change to pause requires a new table
  or column, that is the signal the design has gone wrong again.
- Queueing never inspects pause state; pause never inspects the mailbox.
- No parallel record of anything Temporal already owns.
- No back-compat: superseded code is deleted, not deprecated.

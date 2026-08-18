# Workflow lifecycle refactor: position, stop/resume, and statuses

Status: phases 0, 1, 4 and 5 SHIPPED. Phase 2 (the gate) is next and needs
production data. Phases 3 and 6 are blocked on the gate.
Related: `specs/thread-interrupt.md` (interrupt + the nested-pause fix this builds on)

## Shipped so far

| Phase | Outcome |
|---|---|
| 0 — injection idempotency | Shipped. Inject keyed by graph position, not RunID. Fixes the 4d92f694 duplicate seed. |
| 1 — position stack | Shipped. `workflow_positions` REPLACES `workflow_checkpoints`; 8 activity dispatches for a 3-level run; resume behavior unchanged (frame 0 is a drop-in for the flat row). |
| 4 — hard-stop verb | Resolved as analysis, no code. Cancel stays; the CLI already implements this model and the subtree cascade is load-bearing. |
| 5 — status collapse | Shipped. 3 states + reason, one representation everywhere, EXPIRED deleted as vestigial. |
| 2 — THE GATE | **Next.** Needs `reliant_workflow_resume_outcome_total` data from real usage. |
| 3 — resume from the stack | Blocked on the gate, deliberately. |
| 6 — workflow switching | Blocked on the gate; the hash stamp is its prerequisite. |

Two live bugs were found and fixed as side effects, neither of which this spec
predicted:

- `ResumeChat` gated its post-reset run-ID refresh on `isExpired`, which was
  never true — so after a reset produced a new Temporal run, the chat kept a
  **stale run ID**. Deleting vestigial EXPIRED exposed it.
- The inject key could be spoofed through separator collision, because node IDs
  come from user-authored YAML and could contain the join character. Components
  are now length-prefixed.

Three divergent local copies of "which statuses count as still working"
(`spawn_status.go`, `cleanup.go`, `chat_send.go`) collapsed into one `Live()`
predicate — the drift a shared predicate exists to prevent.

## Ground rule: full replace, no versioning

**No old code path survives a phase.** The product has not launched, so there is
no back-compat requirement, no live fleet to migrate gracefully, and no reason to
keep two representations alive at once. Concretely, for every phase here:

- The new representation REPLACES the old one. Never dual-write, never keep a
  translating shim, never gate new behavior behind a flag or a
  "if new available, else old" branch.
- Readers migrate in the SAME change that introduces the new representation. A
  half-migrated system held together by a compatibility layer is the outcome
  this rule exists to prevent.
- Superseded tables, columns, enum values, helpers and activity shapes are
  DELETED, not deprecated. No commented-out code, no "remove later" TODOs.
- Workflow-sandbox changes regenerate replay fixtures (`make replay-fixtures`)
  rather than adding a `workflow.GetVersion` gate. See
  `internal/workflow/runtime/replaytest/fixtures/README.md`, which now documents
  this as the default and explicitly argues against gating.

Sequencing work for REVIEWABILITY is still encouraged — land the reconciler
last, keep each commit building and green, stop and report partial progress. The
rule forbids shipping a system that understands two representations; it does not
forbid doing the migration in a sane order.

Historical migration FILES are the one exception: they are an append-only record.
Superseding a table means writing a new migration that drops it, never editing
the migration that created it.

## The thesis

**Position is never discarded.** Every run that stops — paused, failed, expired,
killed mid-deploy — keeps a precise record of where it was, and resuming means
continuing from exactly there. There is no "start over" disposition, because
starting over is a different user action (a new chat, or an explicit restart),
not a consequence of how the run happened to stop.

That single rule collapses the five-branch resume switch in `SendMessage` and
makes failure recovery ordinary rather than special.

**Two corrections after review, both of which narrow this document's ambition:**

1. Cancel does not go away — it has a real, documented CLI consumer. It changes
   shape. See below.
2. The position stack is NOT the primary resume mechanism. Temporal
   reset-and-replay already rebuilds nested state and does it better (it
   restores in-memory node outputs too). The stack is a fallback for the cases
   replay structurally cannot serve, and Phase 2 is an explicit gate that may
   conclude we should not build it at all.

Read the "Adversarial review" section before the design sections; it is the
honest framing that the rest of the document is now scoped by.

## Why cancel changes shape (originally: "goes away")

**Correction to an earlier draft of this spec.** I claimed cancel was
"effectively dead — no consumer." That was wrong, and it was the kind of claim
that would have justified deleting a working feature on a false premise. The UI
part holds: no chat control calls it, and the stop button calls `pauseChat`
(`ChatContainer.tsx:232`). But there IS a deliberate non-UI consumer:
`reliant workflow cancel <execution-id>`
(`cmd/reliant/commands/workflow_supervise.go:1245`), whose help text already
documents exactly the semantics this spec proposes to remove:

> "Cancel is terminal and DROPS the resume checkpoint — the workflow cannot be
> resumed afterward (start a new run instead). Use 'workflow pause' to stop
> while preserving the ability to resume."

So cancel is not vestigial drift; it is an intentional, documented operator verb
that already sits in a pause/cancel pair. That materially weakens the case for
deleting it and strengthens the case for **keeping a hard-stop capability** —
the question becomes what to call it and whether the terminal `TerminateWorkflow`
is the right implementation, not whether the capability should exist.

Revised position: cancel's *user-facing semantics* ("terminal, drops position")
survive as the `Restart`/hard-stop path described below. What this refactor
removes is the idea that stopping and discarding position are the same act by
default. An operator hard-stop remains reachable, and the CLI keeps working —
possibly renamed, but not deleted.

**Second correction, larger than the first.** The CLI does not merely *use*
cancel — it already implements the exact lifecycle model this spec proposes,
as a documented trio (`workflow_supervise.go:1245-1310`):

- `workflow pause` — "Pause PRESERVES the resume checkpoint … can be continued
  later with 'workflow resume'."
- `workflow resume` — "continuing from its preserved checkpoint."
- `workflow cancel` — "terminal and DROPS the resume checkpoint … Contrast
  'workflow pause'."

That is *stop-and-keep-position* versus *stop-and-discard-position*, already
separated, already named, already explained to operators. The design this spec
presents as new is, at the CLI layer, the design that exists.

What this means for scope: the lifecycle refactor is much less about inventing
semantics and much more about making the **rest of the system** honor the
semantics the CLI already documents — chiefly that "preserved checkpoint" is
today a flat row that cannot express nested position, so `workflow resume` does
not actually resume where a nested run left off. The verbs are right. The
position record underneath them is not.

This is a good outcome for the proposal's credibility and a bad one for its
novelty. Both should be stated plainly rather than the second being quietly
dropped.

What cancel does today: void the pending question, `TerminateWorkflow` (a HARD
kill, chosen deliberately because a run parked on a signal `Await` never observes
a cooperative cancel), CAS the row to CANCELLED, then **manually cascade the
subtree** — a step that exists only to compensate for the hard kill skipping the
workflow's own completion handler. That compensation is worth revisiting even if
the verb stays.

The two things cancel provides get re-homed:

- "stop this, I don't want it running" → park, and never resume.
- "start over" → an explicit `Restart` that clears position deliberately, or a
  new chat. Never a side effect of stopping.

`COMPLETED` stays distinct, but not as a disposition: a finished run simply has
no position to resume to, so the next message starts a new run. That falls out of
the rule rather than being a special case in it.

## The hard part: resuming INTO a frame, not re-entering it

A checkpoint that records "you were at node `content_pass`" is not enough,
because `content_pass` is itself a sub-workflow whose implementer is four frames
down:

    outer loop (iteration 2)
      └─ content_pass (workflow node)
          └─ get-it-right's loop (iteration 1)
              └─ implement (workflow node, forked thread)
                  └─ two live spawns

Chat 4d92f694 recorded `content_pass` / iteration 0 for exactly that stack.
Re-entering the top frame re-ran its on-entry work — the thread fork and
`thread.inject` — which re-seeded `## Get It Right — Attempt 1 of 4` verbatim
thirty minutes later.

So the requirement is not "record where we were" but **"re-enter every frame in
the path without re-running what each frame already did on entry, and stop
descending at the deepest recorded frame."**

### What makes this tractable

Thread identity is already deterministic (`ExecutionContext.ForChild`,
`context.go:317`):

    DeterministicThread(workflowID, "<stepID>:fork:<parentThread>[:iter:N]")

A frame's thread is therefore a pure function of the path that produced it. The
stack does not need to store thread IDs — re-deriving the path re-derives the
thread, so a resumed implementer lands on the SAME forked thread with its full
context, not a fresh one. This is what makes "resume where you left off" preserve
the agent's context rather than just its coordinates.

**Validated empirically** (`tmp/spechecks/`, gitignored scratch): rebuilding the
four-frame landing-page path twice yields the identical thread UUID, and a
mutation that rebuilds it at the wrong iteration produces a different one — the
exact failure the per-frame iteration must prevent.

Two sharp edges the experiments surfaced, both of which shape the table:

- **The `memo` flag is part of the path.** `ForChild` keys on iteration only
  when `memo == false` (`context.go:319-321`); a memoized fork deliberately
  collapses to one thread across iterations. Since the two modes disagree at the
  same iteration, `memo` is NOT derivable from
  `(stepID, parentThread, iteration)` and must be recorded per frame.
- **Loop presence is part of the path, and "iteration 0" is not a safe default.**
  I assumed a loop frame at iteration 0 and a non-loop frame would resolve
  identically (both dropping the suffix). They do not — the key carries
  `:iter:0` in the first case and nothing in the second, so the UUIDs differ. A
  frame recorded without its loop context and replayed with one silently
  resolves a different thread. Loop presence must be explicit, not inferred
  from `iteration == 0`.

### What does NOT survive, and why that is acceptable

`nodeOutputs` are in-memory only (`NewNodeOutputStore`, `workflow.go:762`).
A rebuilt run cannot restore the outputs of nodes that already completed, which
matters for CEL edge conditions referencing `nodes.X.field` from an earlier
frame.

Mitigation, in order of preference:

1. **Thread history is the real state.** For agent nodes, the conversation is on
   the thread and is durable. Most `nodes.X` references in practice read the
   node that just ran, which the resumed frame re-produces.
2. **Persist the outputs of completed frames alongside the stack.** They are
   already JSON-serializable (they cross the activity boundary today). This is
   the honest fix and is scoped to the checkpoint table.

Option 2 should be the plan; option 1 is why the current coarse restart mostly
works and why this is not an emergency.

### Entry side effects must become idempotent

Re-entering a frame re-runs its entry work. Two kinds:

- **Thread fork** — safe. Deterministic ID means the same thread is resolved.
- **`thread.inject`** — NOT safe. This is the duplicated seed.

Fix (SHIPPED): injection is keyed by graph position —
`(childWorkflowID, childThreadID, nodeID, loop presence + iteration)` — and the
existing `checkExistingMessage` dedup then suppresses the duplicate. The bug was
never missing dedup; it was that the key included the Temporal **RunID**
(`handlers/save_message.go`), so a resumed run computed a different key and
missed it.

Two corrections to this spec, both found during implementation:

- **`memo` is NOT needed as an explicit key component.** This document claimed it
  was not derivable from the rest of the path. It is — transitively and exactly:
  `ForChild` (`context.go:317-321`) folds the iteration into the thread ID *only
  when memo is false*, so `childThreadID` already encodes memo. A tripwire test
  pins the dependency, so if `ForChild` ever stops folding memo in, the test
  fails and memo must become explicit. A redundant component would have implied
  a guarantee it did not add.
  (The position-stack table still records `memo` — it stores raw frame data
  rather than a derived thread ID, so it needs the input.)
- **Separator injection was a real second bug.** Node IDs come from
  user-authored YAML and may contain the join character, so plain `|` joining
  let one frame impersonate another (`implement|review` on `thread` collides
  with `review` on `thread|implement`) and silently dedupe away a legitimate
  injection. Components are length-prefixed.

The RunID-scoped derivation is unchanged for every other `SaveMessage` caller,
which is correct: per-run scoping is what ordinary assistant and tool messages
want.

## How the claims here were checked

The empirical claims in this spec were validated against the real code in
`tmp/spechecks/` (gitignored, not part of the suite — delete or promote when the
spec is settled). Each was mutation-checked: broken deliberately to confirm it
fails, then restored.

| Claim | Result |
|---|---|
| Thread identity is a pure function of the path | Holds; stable UUID across rebuilds |
| Wrong iteration → different thread | Holds (this is the danger the stack prevents) |
| A branch cannot collide with its source's threads | Holds; scoped by workflow ID |
| Node IDs diverge across workflows | Holds — `structured-agent` has `max_turns_notification`, `agent_loop/remind_response` that `agent` does not |
| An edited workflow can invalidate a recorded position | Holds; motivates the hash stamp |
| ~~iteration 0 ≡ no loop~~ | **FALSE** — corrected above; they differ |

## Adversarial review: what the position stack is actually for

A devil's-advocate review made an argument that survives scrutiny and changes how
this should be framed:

> Temporal reset-and-replay ALREADY rebuilds nested state correctly. Replay
> re-executes the recorded history, which reconstructs the entire inline engine
> stack — that is exactly what a position stack is being proposed to do. The
> stack therefore optimizes rare fallback cases while adding cost to every
> execution.

Verified, and largely correct. `resetInterruptedForResume`
(`pause_service.go:442`) resets a closed execution and replay rebuilds the nested
frames for free. When it works, it is strictly better than any checkpoint we
could write, because it restores in-memory node outputs too — the gap this spec
otherwise has to paper over with persisted jsonb.

**So the stack is NOT the primary resume mechanism. Reset-and-replay is.** The
spec is reframed accordingly: the stack is the fallback for the cases replay
provably cannot serve, and its value is measured by how often those occur.

Where replay does not reach (`pause_service.go:457-465`):

- Eligible statuses are FAILED / TERMINATED / TIMED_OUT only. A **live paused
  run** is explicitly not reset-resumed — it is signalled. That is correct and
  is what the shipped in-place pause fix now handles, without a stack.
- `ErrHistoryLimitExceeded` — a run at the history cap cannot be rescued by
  reset, because the reset point lives inside the oversized history
  (`pause_service.go:474`). This is the one case where replay is structurally
  unable to help and a checkpoint genuinely wins.
- `ErrNoReplayableHistory` — ghost executions, past retention.
- `ErrResetAttemptsExhausted` — the bounded guard gave up.

Only the last three fall back to the coarse restart that loses nested position.
**The honest question this spec must answer before Phase 3 is: how often?** If
history-limit and ghost cases are rare, the correct outcome is to keep
reset-and-replay as the mechanism, fix its fallbacks, and not build the stack at
all.

That is now an explicit gate (see Sequencing): Phase 1 ships write-only and
**measures**. If the fallback paths are rare in practice, we stop there and the
stack becomes diagnostic data rather than a resume mechanism.

The review also noted the spec conflated "make nested pause work" (shipped, and
solved WITHOUT a stack by never unwinding) with "add position tracking" (this
proposal). That conflation is removed: the nested-pause fix stands on its own and
is not evidence for the stack.

## Complication 1: branching

Branching is already compatible, and the spec must not break it.

`BranchChat` creates a NEW chat with a NEW root workflow at `PENDING` with **no
checkpoint** (`chat_branch.go:217`). It inherits *messages* through the context
window chain, not *position*. A branch therefore starts at graph entry by
construction, which is correct: the user is starting a new line of work from a
point in the conversation, not resuming someone else's execution.

Two rules follow:

- **The checkpoint stack is per-workflow and never copied on branch.** A branch's
  absent stack is what makes it start fresh — the same rule as everything else
  ("no position → graph entry"), not an exception to it.
- **Thread determinism is scoped by workflow ID.** `DeterministicThread` takes
  `workflowID`, and a branch has a new one, so a branch's forks cannot collide
  with its source's. This already holds; the refactor must preserve it.

One real interaction: a branch taken from a chat whose run is mid-flight inherits
messages that may include an assistant turn whose tool results never landed.
`convertAndRepairMessages` already handles that (it is the documented "fork
inherited the results but cut before the call" case). The stack changes nothing
here.

## Complication 2: changing workflow mid-chat

Today this is refused unless the root workflow is `PENDING`
(`chat_send.go:1048`) — "cannot change workflow after chat has started - use
Branch". Enabling it is the stated goal, and it interacts with position directly:
**a position recorded against workflow A is meaningless in workflow B.** Node
IDs, loop structure, and nesting all differ.

Design rule: **the stack is stamped with the workflow name and version it was
recorded against, and a mismatch invalidates it.**

    workflow_checkpoint.workflow_name  -- e.g. "builtin://get-it-right"
    workflow_checkpoint.workflow_hash  -- content hash of the resolved definition

On a workflow switch:

- Stack does not match the new workflow → discard it, start at graph entry. The
  conversation is preserved (it lives on threads); only the execution position
  is dropped. That is the honest outcome — there is nowhere in workflow B that
  corresponds to "iteration 1 of get-it-right's loop."
- This makes switching a **position-clearing** operation, which is exactly the
  `Restart` verb described above. Switching workflows *is* restarting, with a
  different graph.

The `workflow_hash` matters beyond switching: a builtin workflow edited between
a run stopping and resuming (we ship YAML changes continuously) has the same
name and different structure. Without the hash, a resumed run would re-enter a
node ID that has moved or no longer exists. With it, the run degrades safely to
graph entry instead of landing somewhere wrong.

This is the single most valuable thing in the design that is not in the current
system, and it is cheap.

## The shape

### Position record

    workflow_positions
      workflow_id     text        -- PK part
      depth           int         -- PK part; 0 = outermost
      node_id         text
      -- Loop identity. in_loop distinguishes "no loop frame" from "loop at
      -- iteration 0"; they resolve DIFFERENT threads (validated), so this
      -- cannot be collapsed to iteration=0.
      in_loop         bool
      iteration       bigint
      -- memo participates in thread derivation and is not implied by the rest
      -- of the path (validated); a memoized fork drops iteration deliberately.
      memo            bool
      kind            int         -- loop | workflow-node | router
      entry_applied   bool        -- entry side effects already run for this frame
      node_outputs    jsonb       -- completed sibling outputs at this depth (see above)
      -- stamped once per workflow, denormalized onto every frame for simple reads:
      workflow_name   text
      workflow_hash   text
      updated_at      timestamptz

Thread IDs are deliberately absent — re-derived from
`(workflow_id, node_id, parent thread, in_loop, iteration, memo)`, which the
experiments confirm is the complete key.

### Writes

Each executor pushes its frame on entry and pops on exit, via the
`iterationCheckpoint` callback that already exists on `InlineLoopExecutor`
(`loop_executor.go:81`) but is currently wired only for the top-level loop and
carries only a flat `(nodeID, iteration)`. Generalize it to a stack handle
threaded through `PauseController` — already the one object passed to every
executor at every depth, so no new plumbing path is invented.

**Cost discipline:** write the WHOLE stack in ONE activity call, only when it
changes. A per-frame write would multiply activity dispatches per iteration.
Must be measured on a real nested run.

### Resume

    if stack exists AND workflow_hash matches -> re-enter each frame in order,
                                                 skipping entry work where
                                                 entry_applied, stopping at the
                                                 deepest frame
    otherwise                                 -> graph entry

One path, one input. The five-branch switch is deleted.

## Statuses

With position always preserved, the status enum is nearly vestigial. Today:
`PENDING, RUNNING, COMPLETED, FAILED, CANCELLED, PAUSED, EXPIRED` (161
non-test call sites, 46 in the reconciler alone).

`EXPIRED` turns out to be worse than redundant — it is **vestigial**, and this is
measured rather than argued:

- The product database has 1,077 `workflows` rows and **zero** at `EXPIRED`.
- Every non-test reference to `WorkflowStatusExpired` is a READ or a declaration
  (`chat_send.go:845`, `chat_crud.go:1217`, `spawn_status.go:504`,
  `pause_service.go:314`, plus the two declarations). **No writer exists.**
- Temporal's `TIMED_OUT` is deliberately mapped to `FAILED` in both mappers
  (`chat_helpers.go:300-307`, `pause_service.go:197-201`), with comments saying
  so.

So `EXPIRED` is deleted outright rather than carried forward as a stop reason,
and the dead `case WorkflowStatusExpired` resume branch in `SendMessage` goes
with it — which removes one of the five resume branches before Phase 3 even
starts. `CANCELLED` survives as a reason (cancel stays; see above).

Proposal:

    state:  PENDING | ACTIVE | STOPPED
    reason: (STOPPED only) COMPLETED | FAILED | PAUSED | CANCELLED

(no EXPIRED — deleted as vestigial, see above)

with predicates replacing scattered enum comparisons:

    Live(state, reason)       -- PENDING or ACTIVE, or STOPPED+PAUSED
    Executable(state, reason) -- not STOPPED (PAUSED included)
    Resumable(state, reason)  -- STOPPED and reason != COMPLETED
                                 (i.e. "stopped with a position to return to")

**`Executable` is a THIRD question, and leaving it out is a real hole.** It was
added after a live failure, not designed in.

`Live` and `Executable` give OPPOSITE answers for PAUSED, and both are right,
because they answer different questions:

- `Live` — "will this run produce another turn?" A paused run will, once
  resumed, so PAUSED is alive. This is the QUEUEING question: work queued for a
  paused run IS eventually drained, and treating it as dead would drop a
  message that would in fact have been delivered.
- `Executable` — "may work run this instant?" A paused run must answer NO;
  stopping work is the entire point of pausing.

Conflating them is not hypothetical. With only `Live` available, a paused run
kept issuing LLM calls: on chat 128cf4f5 a retry-exhaustion self-pause resumed
at 17:41:51, re-ran the same failing step, and failed identically at 17:42:08,
with the workflow row at STOPPED/PAUSED throughout. Every one of those turns was
work issued by a run that was not running.

`Executable` now exists on `core.WorkflowStatus` (`internal/db/core/chat.go`),
alongside `Live` and `Resumable`, and is pinned by a test that fails if anyone
redefines it as `Live()`.

### Where the rule lives: `internal/workflow/lifecycle`

The "nothing runs for a stopped run" rule is owned by a package, not scattered
across callers. `internal/workflow/lifecycle` exposes ONE question:

    lifecycle.MayExecute(ctx, reader, workflowID) Decision
    lifecycle.MayExecuteWork(ctx, reader, workflowID, managesLifecycle) Decision

The package owns what counts as stopped, which work is exempt, and the lookup
budget. A dispatcher says what KIND of work it is dispatching and gets an
answer; it does not encode which kinds are special.

This guard first landed inline in `ActivityWrapper.Execute`, which made the
generic activity dispatcher know about workflow status, stop reasons, pause
semantics and lifecycle exemptions — exactly the leak this refactor exists to
remove, and an extension of the `managesLifecycle` leak already there. It was
extracted; the registry's involvement is now a single call.

`WorkflowReader` is a one-method interface (`GetWorkflow`) rather than
`db.Repository`, so the rule can be tested with an in-memory stub and no
database.

**Best-effort by design.** An unreadable status, nil reader or empty id all
ALLOW. It is a guard, not a gate: blocking real work because a bookkeeping
lookup failed is a worse failure than the one being prevented.

**The exemption is load-bearing.** Lifecycle work must run for a stopped run —
it is how the run reports, repairs and un-stops itself. Without it the guard is
self-sealing: a paused run could never write the status that un-pauses it.

### Still open: the guard treats a symptom

The stopped-run guard stops the bleeding. It does NOT fix the root cause:
`loop_executor.go`'s retry-exhaustion branch resumes and blindly re-runs the
step that just failed. If the underlying condition has not changed, that step
fails identically and the cycle repeats — a self-sustaining loop that exists
independently of where the guard lives. Fix that separately; do not let the
guard be mistaken for the fix.

`Resumable` becomes almost trivial under the thesis: it is "does a valid stack
exist," which the position record answers directly.

161 call sites is a lot of mechanical edits, but per the ground rule it is still
a REPLACE, not a widening: the migration converts the column (or adds, backfills
and drops in the same migration), every reader moves in the same change, and the
retired enum values are deleted from proto rather than left as deprecated
tombstones. Sequence the work for reviewability — reconciler last, each commit
building and green — but never ship a tree that understands both
representations.

## Sequencing

Revised after adversarial review. The order now front-loads the two items that
pay off regardless of whether the stack is ever built, and puts an explicit
**gate** before the expensive part.

0. **Injection idempotency.** Keyed by the full derivation key. This alone would
   have prevented the chat 4d92f694 defect. Independently testable, no
   dependencies, valuable even if everything below is abandoned. **Do this
   first.**
1. **Position stack REPLACES `workflow_checkpoints`, plus instrumentation.**
   Per the ground rule, this is a full replace, not an addition: the flat row is
   simply depth 0 of the stack, so its readers (`chat_send.go`'s
   `resumeInputForInterruptedRun`, `chat_crud.go`'s delete-on-cancel, the db
   store, `workflow_ps`, `wf-supervise`) migrate in the same change and the old
   table is dropped by a new migration. "Write-only" means no NEW read semantics
   land yet — resume behavior is unchanged — not that two tables coexist.
   Verifiable by "does the recorded stack match the real nesting," extending
   `nested_pause_resume_test.go` to three levels. Simultaneously count how often
   resume actually falls back past reset-and-replay
   (`ErrHistoryLimitExceeded` / `ErrNoReplayableHistory` /
   `ErrResetAttemptsExhausted`).
2. **GATE — decide with the numbers.** If the fallback paths are rare, STOP:
   keep reset-and-replay as the resume mechanism, and either delete the stack or
   keep it as diagnostics. Only proceed if the data justifies it. Writing this
   gate down is the point; it is the difference between a measured decision and
   a rationalized one.
3. **Resume from the stack** (only past the gate). Replace the five-branch switch
   for the cases replay cannot serve. Reset-and-replay REMAINS the primary path.
4. **Hard-stop verb — RESOLVED, no code change.** Investigated and closed. Cancel
   stays exactly as it is: the CLI trio already implements this spec's model
   (`workflow_supervise.go:1245-1310`), and the manual subtree cascade in
   `CancelChat` is load-bearing, not vestigial — it compensates for
   `TerminateWorkflow` skipping the workflow's completion handler and is
   documented against a real incident. Removing it would strand every descendant
   at running/paused forever.
5. **Status collapse.** 161 call sites, one representation at a time per the
   ground rule (no dual-write). Independent of everything above; lands as its
   own PR with the reconciler migrated last.
6. **Workflow switching mid-chat.** A product feature, not a prerequisite. The
   hash stamp is what unblocks it, and the hash stamp is worth landing early on
   its own merits — a run resuming into a YAML that changed under it is a live
   correctness bug today, independent of this refactor.

0 and 1 are independent and can run in parallel. Nothing after 2 starts without
the gate.

## Risks

- **Replay compatibility.** Phases 1 and 3 change the workflow command sequence.
  Per `replaytest/fixtures/README.md` we cut to the new code and regenerate
  fixtures; in-flight runs wedge on deploy and are recovered by the reconciler.
  State it in each PR rather than discovering it.
- **Checkpoint write cost.** Measured, not assumed.
- **Lost node outputs.** The known gap; option 2 above is the fix and must be
  decided in Phase 1, since it shapes the table.
- **Sandbox code.** A mistake wedges live runs. Every phase lands with tests and
  its own verification pass.

## Not in scope

The graph engine, CEL evaluation, the inline execution model, and the
spawn/mailbox design. They carry real complexity for real reasons. This refactor
is lifecycle only: position, stop/resume, statuses.

# Design: Spawn-aware Continue-as-New to Defeat History-Cap Terminations

**Status:** Design investigation (no implementation).

**Context:** Chat `0dc5167e-b268-4661-a863-5a120f43b78e` was terminated at 51,199 events because `readyToContinueAsNew` (continue_as_new.go:102) requires BOTH ≥40k events AND quiescence (zero live detached spawns). For a continuously-spawning parent, this gate is unsatisfiable; the run rides its history into the hard cap, killing all spawns instantly with no repair path.

**Question:** Can spawns be carried forward across the continue-as-new boundary, turning quiescence from a hard precondition into an optimization?

**Revised Recommendation:** **Only if tool-call idempotency is implemented first.** Carrying spawns without it risks double-execution of non-idempotent side effects (shell commands, file writes, git operations). See "In-Flight Work at Handoff" section for the critical risk and alternatives.

---

## Proposal: Carry Spawns Across the Handoff

When `historyNeedsContinueAsNew()` is true but spawns are live, instead of blocking until quiescence:

1. **At handoff (parent execution ~40k events):**
   - Capture `childTracker.listLiveDetachedSpawns()` — every spawn currently registered (workflow.go:218-225)
   - Write them into the continuation's `WorkflowInput` as a new field (e.g., `CarriedSpawns`)
   - Proceed with `newContinueAsNewError()` (continue_as_new.go:163)

2. **At successor start (new execution, fresh history):**
   - Retrieve `CarriedSpawns` from input
   - For each record, re-dispatch as a resumption via `parseSpawnToolCall(..., agent_id=ChildThread)`
   - Use the existing `isResumption` path (workflow.go:2610) which calls `initChildWorkflow(..., ThreadModeInherit)`
   - The spawn's agent loop resumes at iteration 0 (no position checkpoint exists; see "Checkpoint Decision" below)
   - Spawn completes normally, reports to parent mailbox, parent drains and moves on

3. **Version gate (required):**
   - Add `workflow.GetVersion(ctx, "spawn-continue-as-new", ..., 1)` before carrying spawns
   - Old histories that never wrote `CarriedSpawns` must not see a continuation with an empty array and wedge trying to re-dispatch nonexistent spawns
   - Pattern matches `continueAsNewVersionGate` and `drainAgentMessagesVersionGate` (workflow.go/agent_mailbox.go)

---

## Risk Assessment

### 1. **Replay / Determinism — MITIGATED**

**Question:** Re-dispatch happens at successor start; what ordering guarantees are needed?

**Finding:** The proposal maintains deterministic ordering because:

- **`listLiveDetachedSpawns()` is already sorted** (workflow.go:223): `sort.Slice(records, func(i, j int) bool { return records[i].ToolCallID < records[j].ToolCallID })`
  - Sorted by ToolCallID, which is stable across restarts
  - This ordering is VERIFIED as the existing pattern for replay determinism (comment at line 215-217)

- **Re-dispatch loop is deterministic:** iteration order over the sorted slice is fixed
  - Each re-dispatch is a synchronous `parseSpawnToolCall()` call, then `initChildWorkflow()` 
  - No randomness introduced; order is ToolCallID-ascending, same on every replay

- **Version gate required:** `GetVersion("spawn-continue-as-new", ...)` gates the entire re-dispatch logic
  - Replaying pre-continuation histories skips it (old version, no `CarriedSpawns` field)
  - New histories that carry spawns see the new code path
  - This matches the established pattern; see `continueAsNewVersionGate` (continue_as_new.go:17-18)

**Recommendation:** SAFE. The sorting and version-gating patterns are already proven.

---

### 2. **In-Flight Work at Handoff — CRITICAL UNMITIGATED RISK**

**Question:** A spawn mid-`ExecuteTools` or mid-`CallLLM` when the parent hands off — what happens to that activity? Is there a double-execution risk?

**The Correct Analysis — Three Cases (Not Two):**

The critical issue: `ExecuteTools` runs **non-transactional side effects** before persisting results. When the parent execution ends, a spawn's in-flight activity can be in one of three states:

1. **Activity never started** → Safe. Spawn resumes, never ran the tool.
2. **Activity completed AND result persisted to DB** → Safe. Spawn resumes and sees the persisted result.
3. **Activity executed side effects BUT result never persisted** → **DOUBLE-EXECUTION RISK.** The tool's side effects already happened; resumption re-runs the tool.

**Evidence This Risk Is Real:**

From `internal/workflow/runtime/activities/handlers/execute_tools_idempotency_test.go`:

- Line ~224: *"Current implementation doesn't have full idempotency caching / This test documents the current behavior - tool WILL be re-executed"*
- Line ~245: `t.Skip("Skipping until idempotency is implemented - current implementation does not prevent re-execution")`
- Test `TestExecuteToolsActivity_NoReExecutionOnRetry` is SKIPPED because idempotency does not yet exist

From `execute_tools.go:237-243`:

- Activity collects `activityID`, `workflowRunID`, `attemptNumber` "for idempotency tracking"
- These identifiers are gathered but **no dedup gate is built on them**
- `workflowRunID` CHANGES across a continue-as-new boundary (new execution → new runID), so any future dedup keyed on it would not protect this path

From `execute_tools.go` execution order:

```
1. a.toolExecutor.ExecuteTool(ctx, ...) ← tool runs, side effects happen
2. a.handleToolExecutionResult(...) ← processes result
3. a.upsertToolCallResult(...) ← persists result to DB
```

If the parent execution ends between steps 1 and 3, the tool's side effects are durable (shell commands ran, files written, git push committed) but the result row is not. When the successor re-dispatches this spawn as a resumption:

- The spawn's agent loop loads from thread history (last persisted message)
- It never sees the tool's output (result was not persisted)
- It has no way to know the tool already ran
- It calls `ExecuteTools` again
- **The tool runs twice.**

**Concrete Example — `git push` (non-idempotent):**

```
Timeline:
T0:  Parent at 39.9k events; spawn mid-ExecuteTools
T1:  ExecuteTool("git push") runs successfully, commits pushed
T2:  handleToolExecutionResult starts, about to call upsertToolCallResult
T3:  Parent emits CONTINUE_AS_NEW at 40k events
T4:  Temporal ends parent execution
T5:  upsertToolCallResult call is abandoned (activity context closed)
     Result row never written
T6:  New parent execution starts, re-dispatches spawn
T7:  Spawn's agent loop resumes, has no persisted result
T8:  Agent loop calls ExecuteTools again
T9:  ExecuteTool("git push") runs again
     → Branch already exists, push succeeds, same commits pushed again or force-push happens
```

Result: **git push ran twice.** If idempotent (commits already there, push succeeds silently), it's benign. If the retry changes semantics (force-push, squash, rebase), data loss.

**The Mitigation Claim in the Document Was Wrong:**

I previously claimed:

> "Solution: The spawn re-dispatches at successor start as a resumption (agent_id = ChildThread). `parseSpawnToolCall()` treats this as `isResumption=true` and does NOT re-execute the spawn tool itself."

This is FALSE. `isResumption=true` means the spawn thread already exists and the agent loop resumes from its thread history. **It does not prevent re-execution of tools the LLM calls inside that loop.** The agent loop will see no result for the lost tool and re-call it.

**Residual Risk: UNACCEPTABLE without idempotency.**

---

### 3. **The Missing Checkpoint — KEEP EXISTING, ADD NEW PATH**

**Question:** Spawn threads have NO position checkpoint — `workflow_checkpoints` is keyed by `workflow_id`, only the root run writes one. A re-dispatched spawn restarts its agent loop at iteration 0 with full thread history intact. Is per-thread checkpointing the right fix, and what would it cost?

**Finding:**

**Current state (VERIFIED from incident data):**
- `workflow_checkpoints` table: `(workflow_id PK, chat_id, node_id, loop_iteration, updated_at)`
- Only ROOT workflow writes checkpoints (notifyWorkflowCheckpoint, workflow.go:3708)
- Spawn threads never write to this table
- Incident chat: 12 spawn threads, 1 checkpoint row (the parent at agent_loop, iteration 126)

**Cost of current approach:**
- When a spawn resumes (any resumption, including our new continue-as-new path), it has NO position record
- `resolveResumeTarget()` (workflow.go:3752) falls back to:
  1. Workflow-level `resume_node` override
  2. Single top-level loop node (heuristic)
  3. Graph start
- For a spawn with a main loop, it resumes at the loop's iteration 0, not wherever it was interrupted
- The agent loop re-executes all of its CEL nodes and activity calls
- **Trade-off:** Agent loop iterations are cheap (~37 events each, median; see incident brief); re-executing one iteration costs nothing
- For position-safety, a spawn at iteration 50 that crashes would restart at iteration 50 (not 0) if checkpoints were per-spawn
- **But:** spawns are INLINE in the parent; they don't survive the parent's termination anyway (that's the whole problem we're solving)
- **So:** once the parent continues-as-new, the old spawn goroutines are dead; the re-dispatch starts fresh as a resumption

**Proposal: Keep the existing checkpoint structure for now.**

1. **DON'T add per-spawn checkpoints YET.** Rationale:
   - Spawns are already transient within the parent's execution
   - Re-dispatching as a resumption on continue-as-new is the recovery vector
   - The agent loop restart cost (one iteration re-executed) is acceptable vs. the checkpoint maintenance cost
   - The checkpoint table is already small (incident: 128 checkpoint operations for 1,350 agent turns = 1.5% overhead)

2. **IF evidence emerges that spawns need finer position recovery:**
   - e.g., a spawn running for 200 iterations has a higher cost to re-do
   - Solution: add `workflow_id + spawn_thread_id` composite key to checkpoints
   - Write spawn checkpoints at the same boundaries (node-entry, loop-iteration)
   - This is future-safe because spawn threads already have workflow_id

3. **Confirm in review:** Measurement from incident shows the 1.5% cost of root checkpoints is negligible; spawns would be similar small load if added later.

**Recommendation:** KEEP the current structure. Document that spawn position recovery is a future optimization (not urgent based on cost/benefit).

---

### 4. **Signal Loss — ACCEPTED RESIDUAL RISK**

**Question:** Does carrying spawns widen the existing signal-loss window (continue_as_new.go:91-101)?

**Current accepted window (VERIFIED from code):**
- After CONTINUE_AS_NEW command is emitted but before new run starts
- `quiescentForContinueAsNew()` guarantees no live spawns at this point (line 114-121)
- So `cancel_thread` signals cannot be lost (no spawn to cancel)
- Questions and conversation state are reconstructible (already carried/queried on replay)
- The window is microseconds and unavoidable (Temporal's design)

**With spawn carrying:**
- The moment we emit CONTINUE_AS_NEW, live spawns are about to be re-dispatched
- A `cancel_thread` signal for spawn X delivered during this window would be lost
- Spawn X starts on a fresh thread in the successor, doesn't see the cancel
- **But:** this is no worse than today's behavior where the spawn goroutine is killed outright
- And it's strictly better than the current alternative: chat wedged at history cap

**Signal sources that survive:**
- **Questions:** queried on replay via `QuestionCreate(..., AlreadyResolved)` (inherited behavior, works today)
- **Conversation state:** thread history (inherited behavior, works today)
- **input.Inputs:** carried explicitly in continuation (existing pattern, already in code)
- **cancel_thread:** per-spawn, transient, and quiescence guarantees none when we hand off (this is NEW but NARROWER than status quo)

**Recommendation:** ACCEPTABLE. The window widens by roughly the spawn-re-dispatch duration, but:
- It's still microseconds (re-dispatch is synchronous, no activity)
- It's better than the alternative (chat completely stuck)
- It mirrors the existing handled-signal-loss risk at continue_as_new.go:91-101

---

## Alternatives Considered

### Alternative A: Kill spawns gracefully when history cap approaches

**Description:** Monitor history length; when history reaches 35k events, kill all live spawns with a "chat suspended" message, then hand off.

**Trade-off:**
- ✓ No double-execution risk (spawns are explicitly terminated)
- ✓ No uncertainty about tool idempotency
- ✓ User is told (chat message), can resume after cap release
- ✗ User loses in-flight work (spawns + parent chat interrupted mid-flow)
- ✗ UX: "your spawned agents were killed"

**Verdict:** Better risk profile than carrying spawns without idempotency. The risk of silent double-execution (git push twice, migration twice) is worse than explicit termination with user notification.

---

### Alternative B: Lower the handoff threshold

**Description:** Hand off at 25k events instead of 40k, leaving more headroom for quiescence waits.

**Trade-off:**
- ✓ More time for spawns to finish
- ✗ More frequent handoffs → more rebuild cost for every user
- ✗ Doesn't actually *solve* the problem for continuously-spawning parents (just delays cap)
- ✗ Converts a rare recovery (one handoff for a 200-turn conversation) into a frequent one (every ~60 turns)

**Verdict:** Band-aid. Doesn't solve the fundamental issue and hurts common case.

---

### Alternative C: Cap concurrent spawns at time-of-spawn

**Description:** Reject spawn requests when history > 35k events, or when ≥5 spawns already live.

**Trade-off:**
- ✓ Prevents unbounded spawn accumulation
- ✗ Prevents the user's intended workflow (multi-agent coordination)
- ✗ "your spawn was rejected because history is full" is worse UX than "we'll continue the chat"
- ✗ Doesn't recover already-live spawns

**Verdict:** Punitive, not protective. Carrying spawns (if idempotency exists) is better.

---

### Alternative D: Do nothing; accept chat termination

**Description:** The current state. Cap hit → chat dies.

**Trade-off:**
- ✓ No implementation
- ✗ Chat is lost; user gets an error; spawns report nowhere
- ✗ Unacceptable (see Gap 1-3 in incident brief: repair cannot run)

**Verdict:** Unacceptable.

---

## Revised Recommendation

**Implement spawn-aware continue-as-new ONLY AS A FOLLOW-UP to implementing tool-call idempotency.**

**The prerequisites:**

1. **Tool-call idempotency is REQUIRED.** The double-execution risk (Section 2) is real and unmitigated by the current resumption path. Before carrying spawns, implement dedup at the execute-tools activity level:
   - Check `tool_call_results` table for existing result before re-executing (workflow.go:801 already upserts here)
   - Use `tool_call_id` as the dedup key (survives continue-as-new boundary, unlike workflowRunID)
   - Document which tools are known-idempotent vs. idempotent-with-caveats (git push vs. file write)

2. **Until then, use Alternative A.** Kill spawns at 35k events with explicit user message. Risk profile:
   - User is told explicitly (recoverable, intentional)
   - No tool double-execution (safe)
   - UX: "chat suspended; resuming now" vs. silent tool re-run

**Why this recommendation:**

1. **Sound architecture** — Carrying spawns is the right long-term answer (spawns are inline, resurrection via resumption is proven)
2. **But not sound **right now**** — Double-execution of non-idempotent side effects is worse than an explicit pause
3. **Implementation order** — Idempotency (a few days of work) before spawn-carry (a week of careful workflow choreography)
4. **The honest risk** — You found a real gap (skipped test, missing dedup, workflowRunID changes). Fixing it properly is better than shipping with a residual risk.

---

## What Changed From Initial Draft

1. **Section 2 (In-Flight Work)** — Corrected the three-case analysis. Case 3 (side effects ran, result lost) is real and undefended.
2. **Checkpoint decision (Section 3)** — Unchanged; decision still sound.
3. **Signal loss (Section 4)** — Unchanged; decision still sound.
4. **Alternative A** — Upgraded from "worse UX" to "better risk profile until idempotency exists."
5. **Recommendation** — Changed from "implement now" to "implement as follow-up to idempotency."

---

## Implementation Checklist (For Idempotency FIRST)

**Phase 1 — Tool-Call Idempotency (Prerequisite)**

- [ ] At ExecuteTools activity entry, query `tool_call_results` by `tool_call_id`
- [ ] If result exists, return it (skip tool execution)
- [ ] If not, proceed with execution and upsert result
- [ ] Use `tool_call_id` (not `workflowRunID`) as dedup key
- [ ] Test: verify a re-executed activity returns cached result, tool does not re-run
- [ ] Test with git, file operations, network calls to confirm side-effect dedup
- [ ] Measurement: confirm query overhead is negligible (~1-2 ms per tool)
- [ ] Document which tools are known idempotent (git add/commit) vs. conditional (git push)

**Phase 2 — Spawn-Aware Continue-as-New (After Idempotency)**

- [ ] Add `CarriedSpawns []*detachedSpawnRecord` to `WorkflowInput`
- [ ] Modify `newContinueAsNewError()` to capture and carry spawns
- [ ] Add version gate `spawn-continue-as-new` in the re-dispatch logic
- [ ] At successor start, iterate `CarriedSpawns` and re-dispatch each as resumption
- [ ] Test: verify old histories (without `CarriedSpawns`) still replay cleanly
- [ ] Test: verify spawns resume and complete after carry-forward
- [ ] Test: verify determinism (sorted order preserved on replay)
- [ ] Measurement: confirm spawn re-dispatch adds <100 events (should be <10 for prep resumption)

---

## References

- **Incident brief:** `docs/incidents/2026-08-12-spawn-history-cap.md`
- **Idempotency test (SKIPPED):** `internal/workflow/runtime/activities/handlers/execute_tools_idempotency_test.go:245`
- **Idempotency tracking (incomplete):** `internal/workflow/runtime/activities/handlers/execute_tools.go:237-243`
- **Tool result upsert:** `internal/db/repository_tool_calls.go` / `repo_tool_calls_test.go:TestUpsertToolCallResultIsIdempotent`
- **Code locations:**
  - `readyToContinueAsNew()` / `quiescentForContinueAsNew()`: continue_as_new.go:102, 113
  - `ChildWorkflowTracker.listLiveDetachedSpawns()`: workflow.go:218-225
  - `newContinueAsNewError()`: continue_as_new.go:163-173
  - `dispatchSpawnBackground()`: workflow.go:2910-2980
  - `parseSpawnToolCall()`: workflow.go:2428-2530 (isResumption at line 2472)
  - `initChildWorkflow()`: workflow.go:2624 (ThreadModeInherit for resumption)
  - `executeSingleTool()` / `handleToolExecutionResult()`: execute_tools.go (execution order: tool → result handling → DB upsert)
  - `workflow_checkpoints` schema: migrations/postgres/20260712000000_add_workflow_checkpoints.sql

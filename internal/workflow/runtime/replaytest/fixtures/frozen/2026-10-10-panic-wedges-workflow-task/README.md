# Frozen: a run wedged by a panic before the fix, healed by the fixed worker

`workflow_panic_healed.json` is one run across two builds, on one Temporal dev
server:

1. Events 1–33, recorded by the worker at `60d63966` (origin/main before the
   panic fix), running `TestGenerateFixture_WorkflowPanic`'s scenario (a
   `replay-panic` draft; `workflowPanicInjector` panics where the run schedules
   its first `CallLLM`). The panic unwound into the completion bookkeeping,
   whose first blocking call raised the SDK's `yield during panic unwinding`
   panic in its place, so the workflow task failed
   (`WorkflowWorkerUnhandledFailure`) and retried: events 30 and 33. Temporal
   said RUNNING and the workflow row said ACTIVE — the chat showed as working,
   forever. Nothing of the bookkeeping was ever recorded.
2. Events 34–63, recorded by the fixed worker, which picked up the next retry:
   it replayed to the panic, recovered it, recorded the
   `workflow-panic-fails-run` marker (event 42), ran `Cleanup`, showed the
   panic (`WorkflowError`), recorded the run failed (`WorkflowStatus`) and
   failed the run with `panic: replay fixture: injected workflow panic`.

This is the shape of every run a panic wedged before the fix, once the fixed
worker has run it — and a closed run is still replayed (queries,
reset-and-replay resume), so it must keep replaying. Verified both ways: it
replays on the fix, and on `60d63966` it fails with `yield during panic
unwinding`.

Recorded with a scratch harness change (both builds pointed at one external
dev server and one task queue); the scenario and injector are the ones checked
in. What keeps it replaying: `panicFailsRunChangeID`
(`internal/workflow/runtime/workflow_panic.go`). A history recorded before the
fix has no marker anywhere, and its panic is in its live task, where the
marker is recorded — so it takes the new path, as this one did.

Retire this set only together with the gate.

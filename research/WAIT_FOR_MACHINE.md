# wait_for_machine — letting an agent wait out a machine disconnect

## Problem

When a run's machine (daemon) disconnects mid-turn, every machine tool call
returns `Failed to execute tool on daemon: unavailable: no daemon connected for user`
(`toolexec.RemoteExecutor.executeOnDaemon`, ErrorCode `DAEMON_EXECUTION_ERROR`).
The model reads that as "I cannot continue" and ends its turn. When the machine
reconnects nothing resumes the run: the chat sits idle until the user notices.

The daemon-offline circuit breaker (`internal/workflow/runtime/daemon_offline_tracker.go`)
pauses a workflow after 3 consecutive offline steps spanning >= 2 minutes. That
halt STAYS. It is unchanged by this work, apart from one classification rule
below.

## Design (settled — do not re-derive)

1. **The error still reaches the model.** A machine tool that cannot reach the
   machine still returns its error. What changes: when the result is a
   daemon-offline error (`daemonoffline.IsToolResultContent(content)`) AND the
   turn's capabilities offer `wait_for_machine`, execute_tools APPENDS a hint
   telling the model to call `wait_for_machine` instead of ending its turn or
   retrying in a loop. The "no daemon connected" substring must survive
   (the breaker counts it) — append, never replace.

2. **New tool `wait_for_machine`** (`tools.ToolWaitForMachine`,
   `names.ToolWaitForMachine`, both already declared):
   - PlacementServer (runs in the worker), `Preemptable: true`, tags
     `TagReadOnly, TagCodingPlan, TagCodingDefault`. Preemptable tools MUST be
     TagReadOnly (`TestEveryPreemptableToolIsReadOnly`).
   - Classified in `machineBoundTools` (internal/llm/tools/machine.go) with the
     reason "waits for the run's machine; a run with no machine has none" — so
     `NeedsMachine(wait_for_machine)` is true and no-machine runs never get it.
   - Reads state through `tools.MachineObserver` (internal/llm/tools/machine_observer.go,
     already written): `MachineStatus(ctx, userID) (MachineStatus, error)`. The
     run's daemon selector rides on ctx (`executeOnServer` sets
     `toolexec.WithDaemonSelector`). userID from `auth.GetUserIDFromContext`.
   - Never wakes a machine. Tool-time traffic does not wake machines (#406);
     only preflight and the attended send do.
   - Semantics: poll every 2s (preflight's cadence) for up to
     `MaxBlockingToolWait` (20m, clamp `timeout_seconds` to it, like shell_wait).
     Return as soon as `Connected` ("back — retry the call that failed");
     return at once on `Removed` / `Failed` (terminal: tell the user, end the
     turn; include StatusMessage). A failed status read is transient — keep
     waiting. Budget elapsed → SUCCESSFUL "still not connected, call again"
     result naming the last state seen (suspended → "asleep; this run cannot
     wake it — the user can start it from the machines page or by sending a
     message"; starting → "on its way up"; plain disconnected → "may be
     restarting or reconnecting"). On ctx.Done (preempted by a message, or
     interrupted) take one last look on a detached 5s ctx, like
     `shell_wait.stoppedEarly`, and answer with a real result, not an error.
   - Its text must NEVER contain "no daemon connected" (the breaker and the
     hint key on that substring).
   - Refuses in a no-machine run (`nomachine.Is(ctx)`), and reports
     "unavailable here" when the factory has no observer.
   - Metadata: `{connected, state, waited_ms, timed_out, stopped_early}`.

3. **Preloaded whenever the run's machine is remote.** "Remote" = the tools
   factory has a `MachineObserver` (`(*ToolsFactory).ObservesMachine()`, already
   written). Only the worker wires one (internal/serverworker/run.go), because
   that is where tools reach the machine over NATS. In call_llm's
   `resolveAvailableTools`, beside the existing no-machine narrowing:

   ```go
   if noMachine {
       access = withoutMachineTools(access)
   } else if projectScopedToolsFactory.ObservesMachine() {
       access = withWaitForMachine(access)
   }
   ```

   `withWaitForMachine` (new, handlers/remote_machine.go, mirroring
   `withRequestMachine` in handlers/no_machine.go) adds the tool to
   `access.Preloaded` when the node can reach any machine tool:
   `access.LoadableAll || slices.ContainsFunc(access.Preloaded, tools.NeedsMachine) || slices.ContainsFunc(access.Loadable, tools.NeedsMachine)`.
   A node that reaches no machine tool can never see a daemon-offline error,
   so it does not need the tool. Idempotent (no duplicate).

4. **toolexec implements the observer.** `(*toolexec.RunMachine).MachineStatus`:
   `IsDaemonOnline(ctx, userID, DaemonSelectorFromContext(ctx))`; online →
   `Connected`. Otherwise, when the router has `MachineState` (only
   `*NATSDaemonRouter`), map it: `!Exists → Removed`, `Failed`, plus NEW
   `MachineState.Suspended` / `MachineState.Starting` fields derived from
   `daemonRecord.state` (`recordSuspended` / `recordStarting`). Wire the SAME
   `RunMachine` value into both `ToolsOptions.RunMachine` and
   `ToolsOptions.MachineObserver` in serverworker/run.go.

5. **Breaker classification.** In `classifyStepEvent`, a tool result whose
   `name` is `names.ToolWaitForMachine` is NEUTRAL — skipped entirely. Its
   "still waiting" answer is not daemon-offline evidence (it would otherwise
   never let the breaker trip... or reset it falsely: today ANY non-error
   result counts as "daemon alive"). A machine tool succeeding after the wait
   is what resets the streak. Verify the `name` key is present in the
   tool_results maps the breaker sees (ToolResultMsg.name, proto field 2).

6. **Web**: `wait_for_machine` gets the execution category and a formatter
   whose summary reads like a wait ("machine"), next to shell_wait's.

## Out of scope (note, do not build)

- Auto-resuming a breaker-PAUSED workflow when the machine reconnects
  (queueddelivery does this for preflight terminal failures only).
- Waking a suspended machine from tool time.

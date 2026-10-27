# Cloud CI machine took over existing chats — 2026-10-26

## Production evidence (read-only, collected 2026-10-27)

Owner observed tools moving from `ws-ws-2aab1465` to the new CI-only
`ws-ws-e164d47d`, then back, without requesting a move. A child reported work
remaining on the other machine. These observations are the incident report;
we did not independently inspect or mutate either machine's worktrees.

Commands used:

- `kubectl --context gke_reliant-labs-475814_us-central1_prod -n control-plane-prod get deploy -o custom-columns=N:.metadata.name,I:.spec.template.spec.containers[0].image`
- `gcloud artifacts docker tags list us-central1-docker.pkg.dev/reliant-labs-475814/reliant-prod/reliant --filter='version~7adeb2b6'`
- `kubectl --context gke_reliant-labs-475814_us-central1_prod -n control-plane-prod logs deployment/reliant-temporal-worker --since-time=2026-10-26T22:55:00Z --tail=-1`

Facts:

- Worker/API/gateway image digest: `sha256:7adeb2b60be7ffa15c157d7a1fa2c06faf3dbcc35a9f7e244d25a1563848a66f`, image tag `v1.7.71`.
- Worker log BuildID: **`1.8.4-dev+3e6a0e9d`**. The tag/service.version is not the product commit: use BuildID.
- `3e6a0e9d` is Oct 25. `git merge-base --is-ancestor cedbabff 3e6a0e9d` exits 1: it does **not** include #1630's launch-time chat pinning.
- At **23:13:20.104–.105 UTC**, three worker messages say `[DaemonRouter] daemon stream dropped under a tool request; waiting for its result to be redelivered`, with daemon_id `e164d47d-1288-468d-ad0b-c487af2da1c4`.
- At **23:14:50.106 UTC**, those requests report `[DaemonRouter] gave up waiting for a redelivered tool result: the machine did not reconnect` for that same id.
- At **23:53:24.689 UTC**, a worker tool-log message names daemon_id `2aab1465-f76b-4d95-b61b-480d9ba070e6`.

These logs corroborate requests reaching the new daemon and later activity on
the old daemon. They do not contain the nil selector or an audited chat move,
and do not prove which particular chat each dropped request belonged to. No
claim that a heartbeat alone changed the ordering is needed.

## Concrete selection path

Verified on the incident BuildID and current main:

1. A default-machine chat could have NULL `core.Chat.ActiveDaemonID` before
   #1630. `launch.InjectSessionDaemonID` only writes `session_daemon_id` when
   the chat has a pin. Runtime step 6.065 only sets a selector from a nonempty
   session id; otherwise it stays nil.
2. ExecuteTools routes explicit workflow/node selector > worktree owner >
   default. Main-checkout chats, and old ownerless worktrees, have no owner.
   Tools and `ensureBatchWorkspace` do **not** otherwise consult the chat pin.
3. Worker `NATSDaemonRouter.resolveDaemonID` uses registry records when there
   is no matching gateway-local resolver. `lookupDaemonRecord` calls
   `ListDaemonsByUserID`, whose SQL is **`ORDER BY updated_at DESC`**, then
   returns the first attached record matching the selector. Nil matches all.
4. Gateway registration/reconnect calls `Repo.UpsertDaemon` and maintains the
   attachment lease. A newly registered cloud daemon can therefore become
   the first attached default. Disconnect/reconnect or a later metadata
   upsert can change the answer again. This is a per-request, user-wide
   default, not a durable project-to-machine binding; the router does not
   check project installation.
5. Worktrees live on a daemon's disk. Resolving another default cannot move
   their uncommitted files, credentials, or checkout to that machine.

Changing router ordering would only hide the problem. The binding must be a
chat fact, not whichever daemon happens to win default resolution today.

## Existing fix and the remaining gap

#1630 (`cedbabff`, merged Oct 26 11:55 UTC) already pins new launches and
backfills unambiguous old chats. It is absent from the observed prod build.
See `CHAT_MACHINE_PLACEMENT.md` for that fix and its project-install filter.

However a running Temporal workflow retains its original inputs. Even when
the migration or a later send pins its chat, it still carries no session id.
Before this follow-up, activities loaded that chat but ignored its pin, so
those live workflows could keep switching machines until a new run started.

This follow-up fills a **missing** activity selector from the durable chat pin
at tool execution, workspace ensure, LLM tool/MCP discovery, preflight wake,
run-node commands and log writes, resume wake, create_worktree and git_publish. Explicit workflow/node selectors are unchanged;
a spawned thread's own worktree still beats an inherited chat selector. No
router ordering, lifecycle, schema, default-machine policy or new infrastructure.

## Regression

`TestExecuteTools_LiveChatStaysOnMachineWhenSecondCloudRegisters` runs a real
ExecuteTools activity with no snapshotted selector, using a DB-pinned chat and
the NATS router's real registry resolution (no live NATS needed to resolve).
First only the original cloud daemon is registered/attached. Then a second is
registered later. The test proves nil/default resolution changes to the new
machine, but both workspace checks and tool requests keep selecting the old
machine, and the stored chat pin remains unchanged.

Initial reproduction with the original routing behavior failed both workspace
check and tool dispatch: expected `ws-ws-2aab1465`, got `ws-ws-e164d47d`.
Unit cases also preserve explicit selectors, thread worktree precedence and
no-machine behavior.

Independent review of f3793533 correctly found run nodes still ignored the pin.
`TestRunStep_DurableMachineAcrossRegistrationOfflineAndReconnect` reproduced it
through the real ExecuteRunStep activity, RemoteRunExecutor, RemoteExecutor and
NATS registry path: expected original, got cicd after registration. The repair
also fills the missing selector in MachineResumeWake, not just command dispatch.
Run-node precedence is explicit selector > chat pin > chat worktree owner.

The recovery regression uses an in-process NATS server with gateway replies and
a real test database. It checks a second attached CI machine, explicit override
without rewriting the pin, original suspended (run transport error, no command
verdict), no tool/workspace/run/MCP delivery to CI, and resume wake not mistaking
CI for the original. It then starts a run while original is disconnected, waits
for the router's exact-id reconnect observer, reconnects original and verifies
that same pending command completes there. Workspace/MCP and resume checks work
again. A foreign-user persisted pin is refused without contacting that user or
falling back to CI. The pin is checked unchanged throughout. This does not claim
atomic moves across separately loaded batch requests.

## Deployment implication

Both #1630 and this follow-up must be in the worker/API build to cover new and
already-running chats. No release/deploy or production data repair was done
here. A still-unpinned ambiguous historical chat cannot have its original disk
inferred safely; it needs an explicit selection (or other durable ownership
evidence), not a guess at the newest machine. This patch deliberately does
not invent an origin for such rows.

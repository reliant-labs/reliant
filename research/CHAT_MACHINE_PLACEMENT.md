# A chat's machine is decided once

Status: implemented on fix/chat-machine-pin. Revises research/NO_MACHINE_CHATS.md
§6.5's "An unpinned chat is not tool-less … nothing binds it, and nothing needs
to."

## The bug (owner report, 2026-10)

The user had a cloud machine (managed VM) and several chats running on it. They
added their laptop (a self-hosted daemon). **Every existing chat instantly
switched to the laptop.**

## Root cause (verified — do not re-derive)

A chat started with the default picker value (`DEFAULT_MACHINE`, web
`lib/chatMachine.ts`) sends no `daemon_id`, so `chats.active_daemon_id` stays
NULL. Nothing ever writes it later. Every tool call of such a chat then routes
by **default resolution, re-evaluated per call**:

- `launch.InjectSessionDaemonID` (`internal/launch/session.go`) injects
  `session_daemon_id` only when `ActiveDaemonID` is set; otherwise the runtime's
  `execCtx.DaemonSelector` is nil (`internal/workflow/runtime/workflow.go`
  STEP 6.065).
- `toolDaemonSelector` (`activities/handlers/call_llm.go`) then yields nil unless
  the chat's worktree has an owner (`worktrees.daemon_id`). Main-checkout chats
  have none.
- `NATSDaemonRouter.resolveDaemonID` (`internal/toolexec/daemon_router_nats.go`)
  with a nil selector **prefers a connected `local` daemon**. The laptop is
  local, the VM is cloud → every unpinned chat's next tool call lands on the
  laptop.
- Default resolution is also **project-blind**: the laptop usually does not
  have the project checked out at all (no `project_daemons` row in state
  `installed`), while `daemonchoice.Validate` refuses exactly that choice when a
  user makes it explicitly.

Same defect, same path: `placeNewWorktree` (`internal/handlers/worktree/
worktree_placement.go`) falls back to default resolution for an unpinned chat,
so a new branch workspace for such a chat is created on the laptop too.

## The rule

1. **A chat that runs on a machine is pinned to one from its first run.** When a
   run starts for a chat with `active_daemon_id IS NULL AND NOT no_machine`, the
   server picks the machine once and writes it to the chat row before the run
   starts. From then on the chat moves only when the user moves it
   (`SetChatDaemon`) or its machine is removed (RemoveDaemon already releases
   pins; the next run re-pins).
2. **The pick** (`daemonplacement.PinTarget`):
   1. `daemonplacement.ChatDaemonID` — the existing pin or the worktree owner.
   2. Otherwise `ProjectDefaultDaemonID`: among the machines the project is
      `installed` on (`ListProjectDaemonsForProject`, rows in state
      `installed`, daemon owned by the chat's user), the one default resolution
      would choose — the router's default when it is in that set; else the first
      install (oldest first) the router can place by id; else the first install
      whatever its state. A project with no installed machine (none recorded, or
      only `installing` / `failed` rows) has nothing to narrow to, so plain
      default resolution answers.
   3. The router side is `NATSDaemonRouter.PlacementDaemonID`: routing's
      resolution, except that a machine that is starting or suspended answers
      with its record id instead of `ErrDaemonPending`/`ErrDaemonSuspended`
      (the id is known; the run's preflight waits, the attended wake resumes).
      Only "no machine at all" (`ErrNoDaemon`) leaves the chat unpinned.
3. **Best-effort, never blocking a send.** A resolution or write failure is
   logged and the run starts unpinned (today's behaviour). Conversations always
   continue.
4. **Race-safe write.** `Repo.PinChatDaemonIfUnset` (`store.PinChatDaemonIfUnset`):
   one `UPDATE … WHERE active_daemon_id IS NULL AND NOT no_machine RETURNING`,
   then, when it matched nothing, a read of what the row holds. The caller uses
   the returned value, so two concurrent sends agree on one machine. The write
   that wins also emits the `chat_config_changed` update `SetChatDaemon` sends,
   so open clients show the pin; it moves neither `updated_at` nor
   `last_active` (choosing a machine is not something the user did to the
   chat).
5. **Existing chats** are backfilled by migration
   `20261026034904_pin_unpinned_chats_to_machine` from evidence only: the
   worktree's owner, else the project's single `installed` daemon, and only a
   daemon the chat's own user owns. Anything ambiguous stays NULL and is pinned
   at its next run. It is data-only (`forge:backward-compatible`) and tested by
   running the shipped file over seeded rows.

## Where runs start (every one pins first)

All callers of `launch.InjectSessionDaemonID` (gopls-verified):
`Launcher.start` (`launchNew`, `startRecorded`), and in `internal/handlers/chat`:
`SendMessage`, `restartAtCheckpoint`, `startRunResumingStoppedAgent`,
`resurrectGhostWorkflow`, `startRunForQueuedRow`. `run.StartRun` and async
answers reach `SendMessage`. Triggers/automations reach the Launcher.

One helper serves all of them, `daemonplacement.Pinner` (`Pin` for a chat that
exists, `Target` for one that does not yet):

- The **Launcher** takes it as `launch.WithChatPinner`. A new chat gets its
  `Target` onto the row before creation (so it is created pinned, in the same
  insert); `start` calls `Pin` for the chats that reach it unpinned (a branch's
  first send, a trigger-recorded chat) — it never overwrites an existing pin.
  Wired in `internal/app/providers_worker.go` (`NewRunLauncher`, which now takes
  the worker's router) and `internal/handlers/chat/service.go`.
- The five **chat-handler** sites call `s.chatPinner().Pin(ctx, chat)` directly
  before `InjectSessionDaemonID`.
- A pinner whose router cannot place (`Router` nil or a test double without
  `PlacementDaemonID`) still places by the chat's pin and its worktree's owner.

StartChat (`chat_crud.go`) wakes the machine BEFORE `Launch`, and a new chat has
no row yet: it wakes the machine `Pinner.Target` names for the chat it is about
to be, so the wake and the pin agree. `wakeDaemonForAttendedTurn` takes the
same `Target` for an existing chat.

`placeNewWorktree` (the workspace for an unpinned chat, or with no chat) falls
back to `ProjectDefaultDaemonID` instead of plain default resolution, keeping
its error mapping; a router that cannot place keeps plain resolution.

## Web

`defaultMachineDaemon` (`lib/chatMachine.ts`) mirrors the server's default to
NAME the machine a new chat will land on. It takes the project's installed
daemon ids (`installedDaemonIds`, built from `useProjectDaemonInstalls`) as a
parameter — it stays pure — and narrows to them exactly as the server does;
when none of the listed machines has the project it narrows nothing. Callers:
`NewChatView` and `MobileNewChat` (the picker's label), `ComposerWakeStatus`
and `ChatPresenter` via `resolveChatMachine` (a chat whose first run has not
pinned it yet). For an existing chat the UI reads `activeDaemonId`, which is now
set. One known approximation: the server breaks a tie between installed
machines it cannot rank by oldest install; the web, which does not see install
age, ranks by its default order, so they differ only between machines that
order treats alike.

## Not changed

- The router's default preference itself (local first) — with the project
  filter and the pin it only decides where a NEW chat goes, which the picker
  shows and the user can change.
- Project-level surfaces without a chat (Files tab of the main checkout) still
  use plain default resolution.

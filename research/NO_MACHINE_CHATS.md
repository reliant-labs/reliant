# No-machine chats: UI, upgrade path, and code reading without a checkout

Status: design accepted by the user (2026-10). Two streams build it. Stream A
does the chat UI and the upgrade/branch path. Stream B does GitHub code reading.
Stream A is built: §6 is its as-built record, including where it deviates from
§2–3 and why. Read §6 before relying on §2–3 for stream A's behaviour.
A sibling stream, C (research/TOOL_CAPABILITIES.md), refactors how tool
restrictions are enforced. It does not change any behaviour described here.

Related: research/DAEMONLESS_RUNS.md (H #478/#480). That work built the backend
for runs with no machine. This doc covers the product surface on top of it.

## 1. What already exists (verified on main f7f5c57f; do not re-derive)

- `chats.no_machine` (core.Chat.NoMachine, proto `Chat.no_machine` = 36) is
  set at launch from `StartChatRequest.no_machine` (field 20). See
  `internal/grpc/services/chat_crud.go:102`, and `internal/launch/launcher.go:227`,
  which calls `validateNoMachine` in `launch/workflows.go:489`.
- Enforcement is complete and durable, read from the chat row:
  - **Tool menu.** `call_llm` narrows the menu and load_tool's reach via
    `withoutMachineTools` / `withoutMachineToolNames`
    (`internal/workflow/runtime/activities/handlers/no_machine.go`). It skips MCP,
    adds `noMachineSystemNote`, and sets `nomachine.With(ctx)` at about
    `call_llm.go:295`.
  - **Execution.** `execute_tools.go:582` refuses `tools.NeedsMachine` tools
    with `nomachine.Refusal`.
  - **Routing.** The router never wakes a daemon (`toolexec/daemon_router_nats.go`
    returns `nomachine.ErrNoMachine`). `chat_wake.go:27` skips the attended-turn
    wake.
  - **Inheritance.** Sub-agents run on the same chat under a new thread, so they
    inherit no-machine automatically. `start_run` and `activate_trigger` copy it
    explicitly.
- `tools.NeedsMachine(name)` (`internal/llm/tools/machine.go:53`) is the single
  classifier. "Needs a machine" is NOT the same as daemon placement: `view` and
  `write` execute on the server but read and write the user's disk.
- **Gaps this work closes** (the first three are closed by stream A, §6):
  - `SetChatDaemon` (`chat_crud.go`, proto `chat.proto:266`) sets
    ActiveDaemonID but does NOT clear `no_machine`.
  - `BranchChatRequest` (`chat.proto:838`) has no `no_machine`.
  - The web chat UI does not read `noMachine` anywhere. Only Automations/triggers
    do (`web/src/components/Automations/daemonChoices.ts` exports `NO_MACHINE`).
  - **Integration tools that need a connection are never offered to agents.**
    `tools.ConnectionAvailable` (`internal/llm/tools/integration_tools.go:28`)
    returns false for any manifest with required auth, because "the tool list is
    built without an owner today". So GitHub, Slack, Gmail and Twilio actions only
    work as workflow action nodes. A no-machine chat today gets HTTP and web tools
    but no GitHub tools at all. Stream B fixes this.

## 2. Product rules (accepted)

1. **The default is always the user's machine.**
   - A new chat falls back to no-machine only when the user has no machine at
     all, e.g. a web signup with no daemon. **Revised 2026-10-10 (§6.5):** a
     machine still being provisioned, restarting, reconnecting or failed is NOT
     a fallback case; the new chat waits for it.
   - An asleep or suspended machine keeps today's "Waking…" behaviour. It does
     NOT fall back.
   - "No machine" is also an explicit option in the machine picker when starting a
     chat.
2. **Projects.** A no-machine chat stays in its project. The header says plainly:
   "No machine: can't read or change files in this project". When the project has
   a GitHub remote and the user has GitHub connected, the model can read the
   repository through GitHub (stream B). A project-less space is deferred.
3. **Upgrade is one-way.**
   - "Connect a machine" moves a no-machine chat onto a machine. It goes through
     `SetChatDaemon` and clears `no_machine` in the same write. The next turn's
     `call_llm` re-reads the chat row and gets the full tool set.
   - A chat on a machine can NEVER become no-machine; `SetChatDaemon` with an
     empty daemon must not set it.
   - "Continue without machine" creates a BRANCH instead: a new no-machine chat
     carrying the conversation, leaving the original intact. Offer it where the
     machine is unavailable, e.g. the waking/offline composer state.
   - A no-machine branch must not carry a worktree, because worktrees live on a
     machine and route to `worktree.DaemonID`.
4. **UI.**
   - A header pill: "No machine · web & integrations".
   - A one-line hint in the composer.
   - An inline "Connect a machine" card when the model needs a machine (see §3).
5. **Mobile** (`/m/*`, `web/src/lib/surface.ts`). Keep the machine picker, with
   "No machine" pre-selected when none of the user's machines is awake. The user
   can still pick an asleep machine, which wakes it. Desktop keeps rule 1.

## 3. The "needs a machine" signal: a tool, not text-sniffing

`noMachineSystemNote` tells the model to say so when part of a task needs the
user's computer. Detecting that from prose is brittle. Instead, add a
server-safe tool, offered ONLY in no-machine runs:

- `request_machine(reason)`. The model calls it when the task genuinely needs
  the user's computer. The tool returns immediately, telling the model the user
  has been offered the choice and it should stop and wait. The web renders the
  tool call as an inline card: "This needs a machine: <reason>" with a
  **Connect a machine** button. That button opens the machine picker and calls
  `SetChatDaemon`, which clears `no_machine`.
- Classify it in `serverSafeTools` (`machine.go`, enforced by `machine_test.go`).
- Add it to the no-machine narrowing so it is preloaded only when
  `chat.NoMachine`. The natural place is `withoutMachineTools` in `no_machine.go`.
  Never offer it on a machine.
- Update `noMachineSystemNote` to name the tool.

## 4. Stream B: reading code with no checkout

1. **GitHub manifest** (`internal/integrations/catalog/github/manifest.yaml`).
   Today it has user.get, issue.*, pr.get, pr.list_files, pr.review.create,
   repo.list_for_user and workflow.dispatch. Add read-only, server-placed,
   tool-exposed actions:
   - `repo.get_content`: GET /repos/{owner}/{repo}/contents/{path}?ref=. Decode
     base64 file content into text and return directory listings as entries.
   - `code.search`: GET /search/code. Note that GitHub requires a repo/org
     qualifier for useful results.
   - Optionally `repo.get_tree` for listing (git/trees?recursive=1) and
     `repo.get` for repo metadata and the default branch.

   Follow the existing actions' shape and tests (`github_test.go`). The catalog
   drift tests must pass.
2. **Offer connected integrations to the run's owner.**
   - Replace the owner-blind `ConnectionAvailable` withholding with an
     owner-aware check at tool-resolution time. In `call_llm`, the chat's user is
     known: offer an integration's tools when that user has a usable connection,
     or a delegated authority, for that manifest.
   - GitHub tokens come via the control-plane GitHub App (#449). Find how
     `integrationCredentials` / the `httpaction.CredentialSource` resolves the
     run owner's GitHub credential, and use the same source to answer "has one".
   - This applies to machine and no-machine runs alike. Keep `tag:integration`
     filter semantics.
3. **Tell the model what to read.** When a no-machine chat's project has a
   GitHub remote, append it to the no-machine system note ("this project's
   repository is github.com/<owner>/<repo>; read it with
   github__repo_get_content / github__code_search"), so the model doesn't ask
   for files.

## 5. File ownership across the three streams

| Area | A (UI/upgrade) | B (GitHub code) | C (capabilities) |
|---|---|---|---|
| `proto/reliant/v1/chat.proto` (BranchChat no_machine) | owns | — | — |
| `internal/grpc/services/chat_crud.go` + branch handler | owns | — | — |
| `web/src/**` chat UI, picker, mobile | owns | — | — |
| `request_machine` tool (new file, registry, machine.go entry) | owns | — | — |
| `handlers/no_machine.go` | request_machine preload | system note repo hint | leave the API intact |
| `integration_tools.go`, `integration_discovery.go`, the github catalog | — | owns | — |
| `call_llm.go` tool resolution (~2015–2140) | minimal, if any | one localized hook for owner-aware integrations | owns the refactor |
| `execute_tools.go`, `loaded_tools_store.go`, `load_tool.go`, runtime loop | — | — | owns |

When two streams touch the same file, the second to merge rebases onto main
first and re-runs the gates. Merge order: B, then A, then C.

## 6. As built: stream A (UI, upgrade path, request_machine)

What shipped, and where it differs from §2–3. Deviations are marked **Deviation**
with the reason.

### 6.1 Backend

- **Connect a machine = `SetChatDaemon`.** The `UpdateChatActiveDaemon` query
  (`internal/db/postgres/queries/chats.sql`) sets
  `no_machine = no_machine AND daemon IS NULL` in the same write, so ANY pin ends
  no-machine and clearing a pin never sets it. Its only callers are
  `SetChatDaemon` and the launcher's branch-first-send pin, both of which mean
  "this chat is on a machine". Migration `20261006002752` heals any row that
  already disagrees (the daemon, the later choice, wins) and adds
  `chats_no_machine_has_no_daemon_check CHECK (NOT no_machine OR
  active_daemon_id IS NULL)`, the chats twin of the triggers constraint.
- `SetChatDaemon` now **validates the daemon** with
  `validateOwnedProjectDaemon` (owned by the caller, installed when the project
  tracks installs), as `StartChat` does. Before, it accepted any string. It also
  emits `chat_config_changed` with `{active_daemon_id, no_machine}` in the same
  transaction; `globalUpdatesStore.handleChatConfigChanged` patches both.
- A running workflow picks the change up on its next turn: `call_llm` re-reads
  the chat row every activity (`call_llm.go`, "Load chat configuration"), and
  `execute_tools` reads it per call. Pinned by
  `TestCallLLM_ConnectingAMachineGivesTheNextTurnTheFullToolSet`.
- **`BranchChatRequest.no_machine` (field 8).** The branch carries the
  conversation (same fork as any branch), pins no daemon, wakes nothing, and
  leaves the source untouched. `no_machine` with `worktree_id` or
  `workspace_context` is InvalidArgument. A workflow that hard-requires a
  machine is refused at branch time (`Launcher.ValidateNoMachine`), not at the
  branch's first send.
  - **Deviation: the branch binds to the project's MAIN worktree, not "no
    worktree".** Every no-machine chat already does (`launchNew` resolves an
    omitted worktree to main), and the chat list groups by worktree, so a
    worktree-less chat would vanish from it. The main checkout carries no
    `worktree.DaemonID`, so nothing routes through it; the concern in §2.3 was
    a branch worktree's daemon pin, which this never copies.
  - **Addition: inheritance.** A branch of a chat that has no machine stays
    without one unless it names a worktree (a machine's checkout).
  - **Addition: `launchNew` refuses a no-machine chat in a non-main worktree
    with a `DaemonID`**, for the same reason, so the CLI cannot create the
    state the branch path avoids.
- **The branch's first send** (`StartChat` with `chat_id`): `launchPending`
  derives no-machine from the row. It accepts `no_machine=true` for a
  no-machine branch (it used to refuse every pending chat with it), runs
  `validateNoMachine`, and still refuses `no_machine` for a machine branch.
  Naming a `daemon_id` at that first send pins it, which ends no-machine.

### 6.2 `request_machine`

- Registry tool, `PlacementServer`, no tags, classified in `serverSafeTools`
  (`internal/llm/tools/request_machine.go`, `machine.go`). Input
  `{reason: string}`; it returns at once telling the model the user was offered
  the choice and to stop and wait. On a machine (`!nomachine.Is(ctx)`) the call
  is refused.
- **Offered only without a machine** via a new classifier,
  `tools.OnlyWithoutMachine` (`noMachineOnlyTools` in `machine.go`):
  - `withoutMachineTools` adds it to `Preloaded` for any node that was given
    tools (a node with no tools at all, such as a title call, stays with none),
    and `noMachineMenu` adds it to the turn's menu. The one `call_llm` edit is
    `withoutMachineToolNames(...)` → `noMachineMenu(..., access)`.
  - **Never on a machine**, whatever the workflow names: `ExpandToolFilter`
    drops it from every filter (name, glob, `*`); `load_tool` refuses it;
    `DeferredToolNames` and `SearchTools` skip it. Because it is never loaded,
    no grant can outlive the run's no-machine state after Connect.
  - **For stream C:** those four guards are the whole rule. The capability
    resolver should take `OnlyWithoutMachine` as an exclusion input (offered iff
    `chat.no_machine`) and the guards can then go.
- `noMachineSystemNote` names the tool and tells the model to stop after it.
  The notes compose with stream B's repository note through `noMachineNotes`
  (`handlers/no_machine.go`): both point at `request_machine` only when the turn
  was offered it, so they never disagree about what to do when the task needs
  the user's computer. When GitHub cannot be read from here, B's note says to
  call `request_machine` (a machine has the checkout) instead of "say so"; a
  turn without the tool (a node given no tools) gets the original wording and
  never hears the tool's name.
- `go generate ./internal/llm/tools` regenerated the tool catalog.

### 6.3 Web

- **Default rule** (`web/src/lib/chatMachine.ts`, pure, tested):
  `isUsableMachineForChat` = ACTIVE, IDLE or SUSPENDED.
  - **Deviation: a new predicate rather than onboarding's
    `hasUsableDaemonForOnboarding`.** That one also counts PENDING, because it
    answers "has the user already chosen where their machine lives". §2.1 says a
    machine still provisioning falls back to no machine, so the chat rule needs
    the narrower set. The comment on the new predicate says so.
  - `defaultChatMachine` returns the user's machine (send no daemon, server
    resolution, exactly as before) when any machine is usable, No machine when
    none is, and nothing while the list loads or the desktop app's bundled
    daemon is still registering (`useBundledDaemonPending`), so an empty list in
    that window is not read as "no machine".
- **New chat (desktop, `NewChatView`)**: a "Runs on" `MachinePicker` (each
  machine with its status, then No machine). With No machine the workspace
  controls hide (a workspace is a machine's checkout), the chat starts in the
  main worktree with `no_machine`, the composer is enabled without a connected
  machine, and a one-line hint shows. While waiting for a machine, the wait
  state offers **Continue without machine**, which switches the picker.
- **In a chat with no machine**: header pill "No machine · web & integrations"
  (tooltip: "No machine: can't read or change files in this project"), and a
  composer hint; both open `ConnectMachineDialog`, which lists the user's
  machines (suspended ones say they wake on send), says the change is one-way,
  and calls `chatStore.connectChatToMachine` → `SetChatDaemon`. With no machine
  at all it offers the existing set-up flow. The resume nudge, OOM banner and
  wake line are hidden.
- **Continue without machine (existing chat)** lives on the composer's status
  line (`ComposerWakeStatus`), offered when the pinned machine is offline,
  failed or gone, when the run is `WAITING_FOR_DAEMON`, or while a send wakes an
  asleep machine. It branches with `no_machine` at the latest stored main-thread
  message and navigates there.
- **request_machine card**: rendered by `ChatMessage` as its own card OUTSIDE
  the tool rows (a collapsed tool row is where a question goes unseen), and also
  routed in `tool-renderers/index.tsx`. Once the chat has a machine the card
  says "Machine connected" and drops the button.
- **Mobile** (`/m/new`). **Deviation: mobile had no machine picker to keep.**
  `MobileNewChat` gains a "Runs on" row, and `chatDaemonSelection` flips to
  true for mobile in `surface.ts`. No machine is preselected unless a machine
  is awake (ACTIVE/IDLE); picking an asleep machine sends its `daemon_id`, and
  `StartChat` wakes it.

### 6.4 Open

- An UNPINNED machine chat whose default machine is offline gets no "Continue
  without machine" until the run reports `WAITING_FOR_DAEMON`: the web cannot
  see which machine default resolution would pick.
- After Connect, the model does not resume by itself; the user sends the next
  message (the card and toast say so). An automatic "a machine is connected"
  nudge would be a turn the user did not ask for.
- The project-less space (§2.2) is still deferred.

### 6.5 Revision (2026-10-10): No machine only when there is none

**What happened (prod, 2026-10-09 22:17:30 UTC).** The user's only machine was
restarting under a release (registry phase provisioning, web status PENDING)
for the ~20 s its pod took to come back (daemon re-registered 22:17:50). A new
chat created in that window took §6.3's default, No machine, because
`isUsableMachineForChat` excluded PENDING, and StartChat persisted
`chats.no_machine = true` (chat `b222f6e9`, the only no-machine chat in prod).
From then on the chat showed "No machine — this chat can use the web and your
integrations, not the files in this project." while the Files tab and the
terminal worked: those resolve the machine per request on the server
(`chatDaemonID` / default resolution), and the machine was back. One path
had frozen a momentary status into a permanent, one-way fact about the chat;
the other read the live one.

**Rule now.** No machine is a property of the CHAT, persisted and one-way, so it
is never inferred from a machine's momentary state:

- Desktop: `defaultChatMachine` is No machine only when the user has no machine
  at all (`hasMachine`). Any machine, in any state, is the default; the
  composer waits for it (`lib/daemon-wait.ts`: starting / connecting / failed),
  and "Continue without machine" stays the explicit way out.
- Mobile (§2.5): No machine unless a machine is awake OR starting
  (`isStartingMachine`): a starting machine needs no wake.
- `isUsableMachineForChat` now counts PENDING; it only ranks machines (the
  Connect a machine preselection) and no longer decides No machine.
- `defaultMachineDaemon` mirrors the server's default resolution (connected,
  self-hosted first; then starting or asleep; then any), so the picker names
  the machine an unpinned chat actually lands on.

**Lost bindings self-repair (server).** Removing a daemon from the registry
(`RemoveDaemon`, or a registry snapshot) now releases, in the same
transaction, everything that named it: `chats.active_daemon_id` (with a
`chat_config_changed` update so open clients drop the pin),
`worktrees.daemon_id` and `project_daemons`. Each is re-learned from live
evidence: default resolution for the chat, the sweep's adoption for the
worktree, connect-time reconcile for the install. Migration
`20261010020219` released the rows earlier removals left dangling.

**An unpinned chat is not tool-less.** A chat with no `active_daemon_id` (and
`no_machine = false`) routes each tool call by default resolution, the same
resolution the Files tab uses for its main checkout; nothing binds it, and
nothing needs to.

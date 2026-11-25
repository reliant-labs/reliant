# Projects ↔ machines: research (2026-11-06)

Research only, no code changes. Checked against `main` at a07ef079c. `origin/main` is d059db2a3, and the same greps return the same results there. All paths are relative to `reliant/`.

## Summary

- **The data model already has a path per project per machine.** `project_daemons` has the primary key `(project_id, daemon_id)` and a `path` column. A project can already live on several machines, each at its own directory.
- `projects.path` is only the path on the machine that created the project. It is used to recognise old paths, never as the answer.
- **The backend can already attach an existing folder to a project.** `MarkProjectInstalled(project_id, daemon_id, path)` upserts a row for any machine and any path. The UI never calls it, so complaint 1 needs UI work but no new RPC.
- **Tool paths and the "Your working directory is …" line follow the chat's machine.** The server resolves them each turn from that machine's `project_daemons.path`, and rebases paths that pointed at another machine's checkout. So the "directory prefix" is already handled per machine.
- **What blocks complaint 1** is the guard on the chat's machine (`daemonchoice.Validate`) together with UI that only offers to clone, which works only for git remotes and only through the control plane.
- **The folder browser and the machine:** `ListDirectory` takes no machine. It always lists the default machine, so complaint 2 needs a backend change too.
- **"All machines" filter:** I couldn't find it on main or origin/main (details in C3). It needs clarification.
- **Projects page:** Settings → Projects already exists, but it shows no machines.

---

## A. Data model

**`projects`** (`db/migrations/20261014050404_baseline.up.sql:993-1007`)
- Columns: `id, name, path, user_id, …, remote_url, is_forge`.
- `UNIQUE (user_id, path)` (`:2236`). `path` is one value for the whole project.

**`project_daemons`** (`baseline.up.sql:976-986`)
- Columns: `project_id, daemon_id, path NOT NULL, default_branch, cloned_at, install_state ∈ {installing, installed, failed}, install_error, install_request_id`.
- Primary key `(project_id, daemon_id)` (`:2221`). FK to projects with `ON DELETE CASCADE` (`:3498`).
- Later migrations add:
  - `verified_at`: `20261031013101_project_daemon_verified_at.up.sql`. Marks when the checkout was last seen on disk. Pool placement uses it.
  - `clone_started_at`: `20261102054345_project_daemons_clone_started_at.up.sql`. The stuck-clone timeout counts from it.
  - `20261102004554_project_daemons_drop_orphans`.

**So the checkout path is stored per (project, machine), and a project can have many machines.**
- `internal/handlers/workflow/runtime/activities/handlers/chat_checkout.go:17-33` states the rule: `projects.path` is the creating machine's path, and each machine's own checkout is its row.
- `projectcheckout.Target.Rebase` (`internal/projectcheckout/machine.go:72-120`) treats `projects.path` as "recognition only".

**`project_machine_catalogs`** (`20261029064544_…`, `20261029203203_…`, `20261030043359_…`)
- One row per (project, daemon). It holds that machine's synced `.reliant` config: workflows, presets, skills, memories, MCP servers, scenarios and repo state.
- A run reads its own machine's row. `project_configs` keeps the union across machines.
- A newly attached machine fills its row on its next connect.

**Machine pools** (`20261030222234_machine_pools.up.sql`, `dev-docs/design/machine-pools.md:39-72`)
- Tables: `machine_pools` and `daemons.labels`. Chats and triggers get `machine_pool_id`.
- A pool member is placeable only when `verified_at >= daemon_attachment.attached_at` (`project_daemons.verified_at` migration comment).
- Default machine: `daemons.is_default`, at most one per user (`20261030193936_daemon_default_machine.up.sql`).

**How a project gets a `project_daemons` row**

1. **`CreateProject(name, path)`** — the "open folder" path (`internal/handlers/project/rpc_create_project.go`).
   - It takes no `daemon_id`.
   - It resolves the user's *default* machine (`resolveDaemonID`, `:81`; `project.go:37` → `Router.ResolveDaemonID`, which is default resolution, `toolexec/daemon_router_nats.go:193`).
   - It sends `fs.mkdir` to that machine.
   - It writes the row only if the mkdir succeeded synchronously (`:253-254`).
   - If a project already exists at that path, it returns `AlreadyExists` (`:50-57`).
2. **`CreateProjectFromRepo(clone_url, daemon_id, name?, branch?, path?)`** (`proto/reliant/v1/project.proto:180`, `596-610`; `internal/handlers/project/project_from_repo.go`).
   - The control plane queues the clone durably (`:109`).
   - It requires `deps.ControlPlane` (`:71-72`, "requires a Reliant Cloud account").
   - It reuses an existing project with the same remote URL or path (`existingProjectForRepo`, `:185-190`), so calling it again adds another machine to the same project.
3. **`MarkProjectInstalled(project_id, daemon_id, path, default_branch?)`** (`project.proto:159`; `rpc_project_daemons.go:383-427`).
   - Upserts the row with no clone and no check that the directory exists.
   - The only check is that the project belongs to the caller.
   - It learns the forge name.
   - **No frontend code calls it** (only the generated hook and its test).
4. The connect-time reconcile, and clones that were queued for a sleeping machine, settle rows later.

**Other per-machine RPCs** (`project.proto`)

| RPC | Line | What it does | Called from the UI? |
|---|---|---|---|
| `ListProjectDaemons` | :150 | Every row the user has | Yes |
| `ListProjectDaemonsForDaemon` | :142 | Rows on one machine | — |
| `ListRepositoriesForDaemon` | :176 | Rows on one machine, with project name and remote URL | No |
| `PullProjectOnDaemon` | :203 | `git pull` on one machine | No |
| `RemoveProjectFromDaemon` | :220 | Deletes the clone on disk and the row; the project stays | No |
| `RecloneProjectOnDaemon` | :237 | Deletes and re-clones on one machine | No |

I found no web call sites outside `gen/`, `*_gen.ts` and tests for `pull…`, `remove…`, `reclone…`, `listRepositoriesForDaemon` or `markProjectInstalled`. They may be used by the CLI, Electron or the control plane; I did not check.

There is **no RPC to move a chat to another project** (no `SetChatProject` or equivalent in the protos).

## B. Chat → machine binding and the "directory prefix"

- **The chat's machine** is `chats.active_daemon_id`, which the user can choose.
  - It is set at `StartChat.daemon_id` (`chat.proto:801-806`) or by `SetChatDaemon` (`chat.proto:524`; handler `internal/handlers/chat/rpc_chat_adopt.go:20-80`).
  - If it is unset, default resolution applies (`daemons.is_default`, then the machine-state rule).
  - A chat in a branch worktree is pinned to the worktree's owner machine (`worktrees.daemon_id`; `checkWorkspaceMachine`, `rpc_chat_adopt.go:45-49`; `toolDaemonSelector` in `chat_machine.go`).
- **The guard on the chat's machine** is `daemonchoice.Validate` (`internal/daemonchoice/*.go:33-53`).
  - The machine must belong to the user.
  - If the project has any `project_daemons` rows, the machine must have one in state `installed`. Otherwise it fails with `FailedPrecondition "project is not installed on that daemon"`.
  - A project with no rows accepts any machine.
  - **This is the server-side reason a local machine cannot be picked for "properties"** until it has a row.
- **Where the absolute path comes from, each turn:**
  - `resolveCallCheckout` (`internal/workflow/runtime/activities/handlers/chat_checkout.go:61-85`) calls `projectcheckout.ForMachine` / `On` (`internal/projectcheckout/machine.go:23-44`). That returns the machine's own `project_daemons.path`, or `NotOnError` if it has none.
  - The system prompt line: `call_llm.go:1160-1167` computes `workingDir` from that checkout (or the worktree path). `call_llm.go:2729-2737` writes "IMPORTANT: Your working directory is: <path>".
  - Tool calls: `execute_tools.go:190-212` sets `ProjectPath: tec.checkout.main.Path`. The path a run computed earlier is rebased onto the current machine's checkout (`callCheckout.path` → `Target.Rebase`, `chat_checkout.go:99-113`, `machine.go:88-120`).
  - Terminal: `terminal_ws.go:312-327`. Files tab: `file_system/fs_proxy.go:66-99`. Both work the same way.
- **When the chat's machine changes:**
  - On the next turn, the working directory and tool paths re-resolve to the new machine's row, and paths under any known checkout root are rebased.
  - Branch worktree paths are never rebased (`machine.go:84-87`, `inWorktreesDir`).
  - If the new machine has no checkout, each call is refused with `missingRefusal` (`chat_checkout.go:115-123`). The refusal tells the model to stop and ask the user to switch machine or clone. The conversation keeps going and is unblocked on the next turn.
  - **Limits:**
    - Absolute paths already written in conversation history (earlier tool output, user text) are not rewritten. Only the per-call working directory and paths under a known checkout root are.
    - A path outside every known root is passed through as it is (`Rebase`, `machine.go:109-111`).
    - That is the remaining "prefix" friction. It is a model-context problem, not a routing one.

## C. Frontend

### C1. New-chat landing pickers

The landing is `frontends/web/src/components/Chat/NewChatView.tsx`.

- **Project picker:** `NewChatProjectChip` (`NewChatView.tsx:537`; `components/Chat/NewChatProjectChip.tsx:43-110`).
  - When the user has picked a machine, it lists "On <machine>" projects first. The rest appear as "Set up <project> on <machine>", which clones via `createProjectFromRepo`, but only for projects with a `remote_url`.
- **Machine picker:** `MachinePicker` (`NewChatView.tsx:551-570`; `components/Chat/MachinePicker.tsx`).
  - It shows *all* of the user's machines, split into ones that have the project and ones that don't (`lib/projectPlacement.ts:53-71`, `sectionMachines` at `:107`).
  - Placement comes from `useProjectDaemonInstalls` (`NewChatView.tsx:171`).
  - A machine without the project gets a "Set up <project> on <machine>" action, `setUpProjectOn` (`NewChatView.tsx:252-262`). It calls `createProjectFromRepo` with `path: currentProject.path`, and only when `canSetUpElsewhere`, which requires a `remote_url` (`projectPlacement.ts:92-94`).
  - Choosing a machine that doesn't have the project blocks the composer (`startBlockedReason`, `projectPlacement.ts:130-148`; `canChoose`, `:85-89`).
  - **For a plain folder project, or a local machine where the clone is not wanted, nothing lets the user say "it's already at /Users/me/properties on this machine".**
- **Branch picker and "New workspace"** (`NewChatView.tsx:572-700`): `CreateWorktreeModal`. The workspace list is narrowed to the chosen machine (`workspacesOnMachine`, `:275-278`).

### C2. "Add project" flow

Files: `components/Projects/AddProjectDialog.tsx`, `useAddProject.ts`, `DirectoryPicker.tsx`. The app-global mount is in `routes.tsx`, with state in `store/addProjectDialogStore.ts`.

- **Clone:** the user picks a cloud machine with `CloneTargetPicker` (`AddProjectDialog.tsx:104-110`). Only cloud machines are offered (`useAddProject.ts:79-87`, `isCloudDaemon`). The default comes from `pickCloneTarget` (`:104-113`).
- **Open folder:**
  - Electron uses the native dialog on the *local* computer (`useAddProject.ts:196-209`).
  - The browser uses `DirectoryPicker` (`AddProjectDialog.tsx:118-125`). It calls `useListDirectory({ path })` (`DirectoryPicker.tsx:60-63`).
  - `ListDirectoryRequest` has only `path` (`proto/reliant/v1/filesystem.proto:393-395`).
  - The server sends `fs.list_dir` with an empty `machinewake.Target{}`, commented "No workspace to follow: the project picker browses the default machine" (`internal/handlers/file_system/fs_proxy.go:1106-1130`).
  - Then `createProject` (`useAddProject.ts:173`) → `CreateProject`, which also targets the *default* machine (A, item 1).
- **Why the machine can't be changed:** neither `ListDirectory` nor `CreateProject` carries a `daemon_id`. The UI has nothing to pass, and the chat's chosen machine is ignored. The Electron native picker also assumes the default machine is this computer, which may not be true.
- **Existing project:** if the folder's path already belongs to a project, `CreateProject` returns `AlreadyExists` and the UI just opens the existing project (`useAddProject.ts:180-188`).

### C3. Sidebar "All machines" filter — not found

- There is no "All machines" string on main or origin/main. `rg -i 'all machines'` matches only a billing sentence in `daemonUsage.ts:272`.
- No `machineFilter` / `daemonFilter` state exists.
- The sidebar's only filter is `components/Layout/ProjectFilterMenu.tsx` ("All projects" or one project), with state `projectFilterId` in `workspaceStateStore`.
- **Unknown:** the owner may mean the project filter, a filter in an unmerged branch, or an Electron or mobile surface I didn't search (`components/Mobile/*` has no such filter either). Ask the owner which control they mean.
- Chats do carry `activeDaemonId` on the client (`store/chatStore.ts:2907`, `globalUpdatesStore.ts:1019`), so a client-side machine filter is feasible.

### C4. Projects management page

- **It already exists:** Settings → "Machines & projects" → **Projects** (`components/Settings/SettingsNavigation.tsx:78-84`; `components/Settings/ProjectsSection.tsx:24-31`). It lists, filters, opens, renames and removes projects, and has an Add project button. Route: `/settings/$section` (`routes.tsx:637-653`).
- **What it lacks:**
  - Any per-project machine information: which machines, at what path, in what install state.
  - Attach, remove, pull or reclone actions per machine.
  - A link from the sidebar (it is reachable only through Settings).
- The mobile app has `/m/projects` (`routes.tsx:869`).

## D. Gaps per complaint, and the smallest change that closes each

**1. Start a chat on "properties" with the LOCAL machine (the project is only on a remote machine).**
- *Gap:* the backend already models it (a row per machine and path) and has the write RPC (`MarkProjectInstalled`). Two things are missing:
  1. UI to "add a machine at path P" (or "use existing folder").
  2. `MarkProjectInstalled` doesn't check that the directory exists, so a typo creates a row that looks installed but points at nothing.
- *Smallest fix:*
  - In `MachinePicker` and `NewChatProjectChip`, add "Use a folder on <machine>…" beside "Set up (clone)". It opens `DirectoryPicker` against that machine (needs the change in item 2), then calls `MarkProjectInstalled`.
  - Optionally harden `MarkProjectInstalled` to stat the path on the daemon (`fs.list_dir` or a stat) before writing. A machine that is asleep could get a row in state `installing` until it confirms.
  - After that, `daemonchoice.Validate` passes and every path (prompt, tools, terminal, files) resolves to the local path automatically. No changes to the path logic are needed.
- *Chats moving across projects:* there is no RPC for it, and it would be a new feature. It is an architectural decision for the owner. Moving machines within a project already works through `SetChatDaemon` plus `Rebase`.

**2. "Add project" picks the machine itself.**
- *Gap:* `ListDirectoryRequest` and `CreateProjectRequest` have no `daemon_id`. The server uses default resolution (`fs_proxy.go:1127-1128`; `rpc_create_project.go:81-103`).
- *Smallest fix:*
  - Add `optional string daemon_id` to both requests. This is additive and backward compatible.
  - The handlers pass `machinewake.Target{DaemonID}` or `sendToDaemon`, and `CreateProject` writes the row for that machine.
  - The UI gets a machine picker in the open-folder step, defaulting to the chat's selected machine.
  - In Electron, the native dialog should be offered only when the chosen machine is this computer's daemon.
  - When `CreateProject` hits `AlreadyExists` for a path on a new machine, it should attach instead (or the UI calls `MarkProjectInstalled`).

**3. Choosing which machines the sidebar shows.**
- *Gap:* the filter as described isn't on main (C3). Confirm which control the owner means.
- *Smallest fix:* a client-side multi-select over `activeDaemonId` beside `ProjectFilterMenu`, stored in `workspaceStateStore`. Chats with no `active_daemon_id` need a "Default machine" bucket. If server-side paging is needed, add an optional `daemon_ids` filter to ListChats.

**4. A projects page.**
- *Gap:* the page exists (`ProjectsSection`), but it has no machine information and no link from the sidebar.
- *Smallest fix:*
  - Add a per-project "Machines" sub-list to `ProjectsSection`, using `ListProjectDaemons`. Each machine shows its path, state, `verified_at` and `install_error`.
  - Wire the existing unused RPCs as actions: `MarkProjectInstalled` (Add machine at path), `RemoveProjectFromDaemon`, `PullProjectOnDaemon`, `RecloneProjectOnDaemon`.
  - Add a "Manage projects" entry to `ProjectFilterMenu` that links to `/settings/projects`.
  - **Caution:** `RemoveProjectFromDaemon` deletes the clone on disk. A "detach only" option (delete just the row) would be needed for a folder the user owns, such as a local `properties` checkout. That would be a new flag or RPC.

## Open questions and unknowns

- Where the "All machines" sidebar filter lives (C3).
- Whether the CLI or Electron call `MarkProjectInstalled`, `RemoveProjectFromDaemon`, `PullProjectOnDaemon` or `RecloneProjectOnDaemon` (only the web client was checked).
- Cross-project chat moves are a product and architecture decision for the owner.
- `projects_user_id_path_key UNIQUE(user_id, path)` means two projects cannot share a creation path. That is harmless for attaching, since the attach writes `project_daemons`, not `projects`.

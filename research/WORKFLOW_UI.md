# Workflow UI: definitions, runs, automations and the inbox

**Status:** design only. Nothing here is implemented. It is written against
`main` at `7fd8edf4` (#406). Read it with `TRIGGERS.md`, `TOOL_PLACEMENT.md`
and `INTEGRATIONS.md`.

**Why now.** A run used to need a human to type. Now a schedule can start one
(#398, #402), so can an agent (`start_run`, #400), and webhooks and
integrations are next (#401). The UI still assumes that every run is a chat
someone is having. This document redesigns the workflow surfaces around four
nouns, and says what to build first.

---

## Outline

0. Model and vocabulary
1. Information architecture, and the rework of the workflow hub
2. Workflow library and workflow detail
3. The builder: triggers on the canvas, and the integration seam
4. Watching live runs
5. Past runs and history
6. Keeping automated runs out of the chat list (what #398 left)
7. The active-triggers view (Automations, grown up)
8. The inbox: what needs a human
9. Daemon pending, as a cross-cutting state
10. Lessons from n8n
11. Styling contract, applied
12. Phased delivery, with files
13. Backend gaps
14. Open questions for the user
15. Build next, ranked

---

## 0. Model and vocabulary

There are four user-facing nouns. Most of the confusion in today's UI comes
from two of them sharing one surface (runs and chats), and one of them having
no surface at all (runs that nobody started by typing).

| Noun | What it is | Stored as | Started by |
|---|---|---|---|
| **Workflow** | A definition: nodes, edges, typed inputs, presets. | YAML (builtin / project / user drafts) via `WorkflowService` | n/a |
| **Run** | One execution of a workflow. | the `workflows` row (root has `parent_id IS NULL`), surfaced as `Chat` + `RunService.Run` | a launch event |
| **Chat** | A run a human is conversing with. | `chats` row, where `launch_kind = 'chat.start'`, or a run a human has *adopted* (§6) | a human |
| **Automation** | A standing instruction to start runs: a trigger. | `triggers` row; firings in `trigger_events` | n/a |

Every run is backed by a chat row today (chat id = root workflow id = root
thread id, `TRIGGERS.md`). This document does **not** propose changing that.
A run that nobody is talking to is still a chat row underneath. The change is
in **what the UI calls it and where it lists it**:

- A **chat** is a run whose transcript is the point. It lives in the sidebar.
- A **run** is any execution. It lives in the Runs surface, whatever started it.
- So every chat is a run, but not every run is a chat.

The discriminator already exists and shipped in #392: `Chat.launch_kind`
(`chat.start` | `schedule` | `agent.start_run`, later `webhook` and
`integration`) and `Chat.trigger_id`. Lifecycle comes from
`Chat.workflow_state` + `workflow_stop_reason` (#391) and the derived
`Chat.activity` (`IDLE` / `RUNNING` / `AWAITING_INPUT` / `ERROR` / `PAUSED`,
from the `chats_with_activity` view).

### Display vocabulary (use these words everywhere)

One mapping from the wire pair to what a user reads. Today it is
re-derived in at least four places (`WorkflowViewerTab.tsx`'s
`WorkflowStatusIcon`, `MobileWorkflowScreen.tsx`'s adapter,
`Automations/OutcomeBadge.tsx`, `Sidebar.tsx`'s `ActivityDot`). Put it in one
module, `web/src/lib/runStatus.ts`, and have all of them read it.

| Wire | Label | forge-ui `StatusDot` variant | Badge variant |
|---|---|---|---|
| `PENDING` | Queued | `pending` | `info` |
| `ACTIVE` | Running | `active` (pulse) | `info` |
| `ACTIVE` + activity `AWAITING_INPUT` | Needs you | `warning` | `warning` |
| `STOPPED` / `PAUSED` | Paused | `paused` | `warning` |
| `STOPPED` / `COMPLETED` | Completed (or the workflow's declared `outcome`: Succeeded / Failed) | `neutral` | `success` |
| `STOPPED` / `FAILED` | Failed | `error` | `error` |
| `STOPPED` / `CANCELLED` | Cancelled | `neutral` | `neutral` |
| any + daemon pending (§9) | Waiting for machine | `pending` | `warning` |

Trigger-event outcomes keep their own words, because they describe a firing
rather than a run: **Launched**, **Skipped** (with reason), **Failed to
launch** (with reason). A *launched* event points at a run, and that run then
has a run status. Never show "Launched" as if it were the run's result. This
conflation is already visible on `/automations`, where the "last run" column
shows the *event* outcome (`AutomationsListPage.tsx`, `OutcomeBadge`), so a
run that launched and then failed reads as green.

### Launch-kind vocabulary

| `launch_kind` | Short label | "Started by" line |
|---|---|---|
| `chat.start` | Chat | "Started by you" |
| `schedule` | Schedule | "Started by schedule **Nightly triage** for Tue 09:00 (Europe/London)" |
| `schedule` + payload `manual: true` | Run now | "Started by **Run now** on Nightly triage" |
| `agent.start_run` | Agent | "Started by an agent in **Refactor auth** (chat link)" |
| `webhook` (later) | Webhook | "Started by webhook **deploy-hook** at 14:02" |
| `integration` (later) | GitHub, Linear… (provider name and icon) | "Started by GitHub: issue #412 opened by @alice" |
| null (predates #392) | Chat | treat as `chat.start` |

---

## 1. Information architecture, and the rework of the workflow hub

### 1.1 Current state

- **Sidebar** (`web/src/components/Layout/Sidebar.tsx`, nav at ~:1417-1457):
  New chat, Deployments (forge, gated), Projects, **Workflows**,
  **Automations** (added by #398), Search. Below that is the chat list, grouped
  by worktree.
- **Workflows** opens `/workflow` (`routes.tsx` `workflowHubRoute`) →
  `WorkflowPage.tsx` → `WorkflowBuilderPage.tsx`, which renders `WorkflowHub`
  when no workflow is selected. `WorkflowHub.tsx` (2228 lines) is two tabs:
  **Workflows** (a grid of `WorkflowCard`s in sections Custom / Built-in /
  Invalid) and **Presets** (cards plus three modals). Its subtitle is "Manage
  your automation workflows". It has no notion of runs or triggers.
- **Automations** opens `/automations` (`components/Automations/*`, #398): a
  list of triggers across projects, and `/automations/$triggerId` with the
  definition, Run now / Edit / Delete, an enable toggle, and event history (the
  newest 50). Both pages render inside `AutomationsShell`, which is outside the
  app shell. It is the same pattern as `/settings` and `/forge`.
- **Runs** have no surface of their own. A run is visible only as a chat in the
  sidebar, or as a history row on an automation. `exclude_automations` (sent
  unconditionally by `web/src/api/chat-grpc.ts:297`; the SQL is in
  `internal/db/postgres/queries/chats.sql`) hides every chat whose
  `launch_kind` is not `chat.start`, unless its activity is `AWAITING_INPUT`.
  That includes agent-started runs (`agent.start_run`, #400). A schedule run
  can be reached through its automation's history. **An agent-started run has
  no UI path at all**, apart from the id the agent prints in its tool result.
- **Watching** a run is the chat view: `ChatContainer` plus the workflow viewer
  tab (`TabbedViewerPanel.tsx:471` → `WorkflowViewerTab.tsx` →
  `WorkflowViewerPanel.tsx`), fed by `useWorkflowExecutions(chatId)`. The
  `ExecutionSidebar` component itself was removed; only its types and
  `transformWorkflowExecution` remain (`components/Chat/ExecutionSidebar/index.ts`).

### 1.2 Proposal: one "Workflows" area with three tabs, plus an Inbox

Top-level sidebar nav becomes:

```
New chat
Inbox            (badge: count needing you)          NEW
Workflows        → /workflows   (tabs: Library · Runs · Automations)
Projects
Search
[Deployments]    (gated, unchanged)
──────────────
Chats            (human conversations only, §6)
```

- **Workflows** absorbs today's hub, the new Runs surface, and #398's
  Automations. It is one area with one shell (`WorkflowsShell`, generalising
  `AutomationsShell`) and three tabs:
  - **Library**: definitions (the hub, reworked, §2).
  - **Runs**: every execution, whatever started it (§4, §5).
  - **Automations**: standing triggers, grouped and with health (§7).
- **Inbox** is its own top-level entry. It is the one place a human is
  *needed*, and a badge on it is the only interruption an automation is allowed
  to make. It does not belong under Workflows: an interactive chat awaiting
  approval belongs in it too (§8).
- **Presets** stops being a sibling tab of Workflows. Presets are parameter
  bundles for a workflow's inputs. They move to (a) the workflow detail page's
  "Presets" section for that workflow's tags, and (b) Settings → Presets for
  the global list. The three preset modals in `WorkflowHub.tsx`
  (`PresetConfigModal`, `PresetEditModal`, `PresetViewModal`, ~:610-1560)
  are extracted unchanged into `components/workflow/presets/`.

Why tabs under one area rather than three sidebar entries:

- The three tabs are one mental object seen three ways: *what can run*, *what
  ran*, and *what will run on its own*. Every cross-link stays inside one shell
  and keeps its back button. Examples are workflow → its runs, run → its
  automation, and automation → its workflow.
- The sidebar is already long, and the chat list is the thing that needs room.
- n8n reached the same split (Workflows / Executions per project,
  `ProjectTabs.vue`). See §10.

Why **Runs** is its own surface, not a filter on the chat list:

- A run list wants columns (workflow, started by, state, duration, when) and
  multi-axis filters. The sidebar is a single text column that has to stay
  fast and calm.
- Hourly runs produce 24 rows a day. The chat list must never pay for that
  (§6).
- Runs span projects. An automation in project A and a chat in project B are
  both "what ran today". The sidebar is per project.

### 1.3 Routes

| Route | Screen | Replaces |
|---|---|---|
| `/workflows` | redirect to `/workflows/library` | `/workflow` (kept as a redirect) |
| `/workflows/library` | Library (§2.2) | `WorkflowHub` workflows tab |
| `/workflows/library/$workflowRef` | Workflow detail (§2.3) | nothing (net-new) |
| `/workflow/$workflowName` | Builder (§3), unchanged path | unchanged |
| `/workflow/new` | Builder, new | unchanged |
| `/workflows/runs` | Runs list (§5); search params carry the filters | net-new |
| `/workflows/runs/$runId` | Run detail (§4), standalone | net-new (the chat view inside the shell) |
| `/workflows/automations` | Automations (§7) | `/automations` (redirect) |
| `/workflows/automations/$triggerId` | Automation detail | `/automations/$triggerId` (redirect) |
| `/inbox` | Inbox (§8) | net-new |

All `/workflows/*` routes sit under `authenticatedLayoutRoute`, like
`/automations` does today. They resolve the project from a `project` search
param, the way `ForgeLayout.tsx` does, so a hard refresh works. They are
**cross-project by default** and take an optional `project` filter.

The builder keeps its own full-screen chrome (`WorkflowHeader.tsx`). Opening a
workflow from the Library goes to **detail** first, not the builder. Editing
is one click from detail. This matches the fact that most visits are "what
does this do / when did it last run / run it", and only a few are "change it".

### 1.4 Information hierarchy across the area

Highest first, everywhere in the area:

1. **Needs you**: awaiting input, failed automations, daemon pending on a
   live run. These are always pinned to the top of whatever list contains
   them, and counted in the Inbox badge.
2. **Live**: running, queued, paused.
3. **Recent**: finished in the last 24 hours.
4. **Everything else**: paginated, filtered, searched.

A list never mixes the order. "Needs you" rows do not re-sort by recency
among live rows.

### 1.5 States for the shell

- **Loading:** the tab bar renders immediately (it is static). The content
  shows `forge-ui/skeleton_loader` rows shaped like the target list. Never a
  centred spinner on a blank page.
- **Error, at shell level** (project list failed): one `Card` with
  `role="alert"`, the message from the error mapper, and "Try again". This
  copies `AutomationsListPage.tsx`'s pattern.
- **No projects:** "Workflows run inside a project" plus a link to Projects.
- **Escape** leaves the area, as in `AutomationsShell` / `SettingsPage`, and
  steps aside for text entry and open dialogs.

---

## 2. Workflow library and workflow detail

### 2.1 Current state

`WorkflowHub.tsx` has these pieces:

- `WorkflowCard` (~:158-353): an icon tinted by source with hardcoded
  `bg-blue-500/10` / `emerald` / `violet`, a name, two lines of description,
  a source badge, a preset badge, and a default star with a hardcoded
  `amber-500` border. It has six hover-only icon actions: configure presets,
  set default, hide, copy, export, delete.
- Sections: Custom, Built-in, Invalid (`InvalidItemCard`). There is no search,
  no sort, and no "recently used".
- A builder-activity chip (`useBuilderChatActivity` in `chatStoreHooks.ts:257`).
- Clicking a card opens the builder directly.
- Mobile has its own catalog (`Mobile/MobileWorkflowCatalog.tsx`), sectioned by
  origin with per-workflow icons. The reasoning in its header comment is good
  and carries over.

What is missing for the new world: whether a workflow **has automations**,
**when it last ran and how that went**, and **a way to run it with inputs**
that is not "start a chat and pick it in the composer".

### 2.2 Library (`/workflows/library`)

**Purpose:** find a workflow and see whether it is in use.

**Layout:** a `PageHeader` with the title "Workflows" and the subtitle "What
your agents can run." Actions: **New workflow** (primary), **Import**.
Below it is one row of controls: a search box (name plus description), a
**Source** segmented control (All / Mine / Project / Built-in), and **Sort**
(Recently run, the default; Name; Recently edited). Then the list.

**A list, not a card grid.** The current grid gives each workflow roughly
300×120 px for six facts. A row reads faster, and it fits the two new
columns.

Each row is a `Card padding="none"` containing a `ul` with
`divide-y divide-border/60`. That is the same shape as
`AutomationsListPage`.

| Column | Content |
|---|---|
| Name | display name (`getWorkflowDisplayName`), a source badge (forge-ui `Badge`, neutral; "Built-in" / "Project" / "Mine"), a "Draft · N errors" badge (`DraftStatusBadge`) when present, and "Default" as a `Badge variant="info"`. This replaces the amber star. |
| Description | one line, truncated, `text-muted-foreground` |
| Automations | "2 schedules" plus a `StatusDot` (active if any is enabled; error if the newest event of any failed). Empty if none. |
| Last run | relative time plus the run-status dot (§0). Links to that run. |
| Actions | **Run…** (opens the run form, §2.4) and an overflow menu: Edit, Duplicate, Set as default, Hide from composer, Export, Delete. |

Hover-only actions go away. Every action is reachable from the keyboard
through the row's overflow `DropdownMenu`, and the row itself is a link to
detail.

**Sections:** "Needs attention" appears only if any row qualifies: an invalid
definition, or an automation that failed last time. It is followed by one
flat, sorted list. Origin becomes a filter, not a section boundary. Origin
sections made sense when the hub was a catalog. Once the list sorts by
"recently run", sections would split the most relevant rows across headings.

**Data:**

- The list is `WorkflowService.ListWorkflows` (per project, `include_hidden`).
- Automations per workflow is `TriggerService.ListTriggers(project_id)`,
  grouped client-side by `trigger.workflow`.
- Last run per workflow needs a backend addition (§13 G1). Until then, omit
  the column. Do not fake it from the chat list, which hides automations.

**States:**

- *Loading:* five skeleton rows.
- *Empty (no custom workflows):* do not show an empty state. Built-ins always
  exist, so the list is never empty. Show a one-line `CardInset` above the
  list: "Start from a built-in, or **create your own**."
- *Empty search:* "No workflows match '…'", with a "Clear search" button.
- *Error:* the shell-level alert card (§1.5), scoped to the list.
- *Invalid workflows:* listed under "Needs attention" with the first
  validation error inline. The row opens the builder at the error. Today's
  `InvalidItemCard` does this, so keep its behaviour.

### 2.3 Workflow detail (`/workflows/library/$workflowRef`)

**Purpose:** "what does this do, how is it used, run it." This is net-new. It
is the page every other surface links to when it names a workflow.

**Header** (`PageHeader`):

- Title: display name, source badge, Default badge.
- Subtitle: description.
- Actions: **Run…** (primary), **Edit** (opens the builder), and an overflow
  menu (Duplicate, Export, Set default, Hide, Delete).

**Body.** A two-column layout of at least 1024 px. The main column is left;
the side column is a 320 px rail on the right.

Main column, in this order:

1. **Recent runs** (a `Card`, `CardHeader title="Recent runs"`, with a
   "View all" link that goes to `/workflows/runs?workflow=<ref>`).
   - The last 10 runs of this workflow, across launch kinds, in the same row
     component as the Runs list (§5). One component, so the two cannot drift.
   - Empty: "Not run yet. **Run it now** or **add a schedule**." Both are
     buttons.
2. **Automations** (a `Card`, with an "Add schedule" action in the header).
   - Each trigger that names this workflow, as a compact automation row
     (§7.3): name, schedule in words, next fire, enabled toggle, and health.
   - Empty: "Nothing runs this workflow on its own." plus "Add schedule".
   - This is the "its triggers" ask. Triggers stay their own entities, because
     a trigger carries a project, a daemon, a prompt and params. They are
     listed here and created *from* here with the workflow prefilled.
3. **Definition** (a `Card`).
   - A read-only diagram: `WorkflowViewerPanel` with no `chatId`,
     `compact`. It already renders a definition without an execution.
   - Under the diagram, a `CardInset` lists the typed inputs: name, type,
     default, and description.
   - An "Open in builder" link.

Side rail:

- **About**: a `KeyValueList variant="plain"` with Source; File or draft id;
  Last edited; Requires a machine (yes/no, from preflight `RequiresDaemon`,
  §13 G4); Default for (project/user).
- **Presets**: the presets that match this workflow's tags. "Configure"
  opens the extracted `PresetConfigModal`.
- **Used by**: other workflows that `ref` this one (from the definition
  graph). Later, agents that called `start_run` with it.

**States:**

- *Loading:* the header skeleton, plus three card skeletons.
- *Not found* (deleted, or a bad ref): a `Card` alert reading "This workflow
  no longer exists", with "Back to library". If runs of it exist, add "Its
  past runs are still in **Runs**", linking with the filter applied.
- *Invalid definition:* the Definition card shows `WorkflowParseErrorView`
  inline. Run… is disabled, with the reason given as text beside the button,
  not only in a tooltip.

### 2.4 "Run…": the run-with-inputs form

**Purpose:** start a run of this workflow with typed inputs, without opening a
chat composer. This is the manual counterpart to a schedule.

It is a `Modal` with a form generated from the workflow's `inputs`
(`workflow_v2.proto` `Input`: string / number / integer / boolean / enum /
model / message / attachments / tools / array / object / preset). It reuses:

- `useWorkflowInputs` (`components/workflow/useWorkflowInputs.ts`), which
  already parses the definition, builds `inputGroups`, and resolves presets
  per group.
- `WorkflowInputGroup.tsx`, which already renders a group from
  `values`/`onChange` and is store-independent. Its props are plain values.
  (The composer's `WorkflowSelector` binds to chat params. Do not reuse that
  one.)

Fields, in order:

1. **Project** and **Workspace** (worktree). Default to the current ones.
2. **Prompt** (the seed user message). It is required when the workflow's
   entry consumes a message, and hidden otherwise.
3. **Inputs**: the generated groups. Inputs with `ui: hidden` are skipped.
   The `toolbar` / `model` UI hints render as in the composer.
4. **Presets**: the per-group preset picker (from `useWorkflowInputs`).
5. **Machine**: the daemon picker, shown only if the workflow requires a
   daemon. It reuses `Automations/daemonChoices.ts`.
6. **"Run without me"**: a checkbox that sets `unattended`. It is off by
   default for a manual run, because the person is right here. When on, show
   the consequence inline: "Questions and approvals will be answered
   automatically."

Footer: **Run** (primary) and **Save as automation…** (secondary). The second
button turns the same form into the automation dialog, carrying every value
over. This closes one of #398's gaps: today's `AutomationFormDialog` cannot
edit inputs at all.

Submit calls `ChatService.StartChat` with `workflow`, `workflow_params`,
`selected_presets`, `messages` and `worktree_id`. On success, a toast says
"Run started", with **Watch** linking to `/workflows/runs/$runId`. The form
stays on the page you were on. It does not drop you into the chat list.

Errors: field-level errors come from the server's validation
(`launch.ValidationError` → `InvalidArgument` with a field) and render under
the field. Model unavailable and workflow invalid render as a form-level
alert.

**One form component, three hosts.** `RunWorkflowForm`
(`components/workflow/run/RunWorkflowForm.tsx`) is used by:

- the Run… modal
- the automation dialog (as its "What" section, replacing the
  workflow/message fields in `AutomationFormDialog.tsx`)
- the builder's "Test run" panel (§3)

---

## 3. The builder: triggers on the canvas, and the integration seam

### 3.1 Current state

- `WorkflowBuilderPage.tsx` (1047 lines) owns loading, drafts, and the
  hub/builder switch. `WorkflowBuilder.tsx` (2046 lines) owns the canvas.
- On the canvas:
  - `FloatingWorkflowSidebar` (~:1332): a node palette from
    `CatalogService.ListNodes`, grouped by `NodeInfo.category`
    (control_flow / agentic / utility / git).
  - React Flow (~:1633).
  - `FloatingToolbar` (~:1693): pan and select.
  - `WorkflowSettingsEditor` (~:1789).
  - `WorkflowBuilderChat` (~:1828): the AI builder assistant.
  - `ScenarioPanel` (~:2027): scenario tests.
  - The `config/` panels per node kind.
- The entry is drawn as `EventNode` (`nodes/EventNode.tsx`), with event types
  `started`, `message_created`, `pre_tool_use`, `post_tool_use`,
  `chat_complete` and `manual`. It is synthesised by `lib/workflow-flow.ts`
  (`eventType: 'started'`, ~:352, :432), not stored. **A workflow's YAML
  does not know about triggers, and should not**: a trigger is a separate
  stored row with its own project, daemon and prompt (`TRIGGERS.md`).
- There is no "run this now" in the builder except scenarios, which are
  scripted test runs on the real runtime (`ScenarioService.RunScenario`).

### 3.2 Triggers on the canvas, as a projection

The start node becomes a **trigger rail**. It shows how this workflow gets
started, without moving triggers into the definition.

```
┌──────────────────────────────┐
│  Starts when                 │
│  ○ Someone starts a chat     │   always present (chat.start)
│  ◷ Nightly triage  09:00 M-F │   one line per trigger naming this workflow
│  ◷ Hourly sweep    every 1h  │   (dimmed when disabled; red dot if last failed)
│  + Add trigger               │
└──────────────┬───────────────┘
               ▼
          first node …
```

- It renders from `ListTriggers` filtered by `workflow == this ref`, in the
  current project. It is **not saved in the YAML**. Editing a line opens the
  automation dialog. "+ Add trigger" opens it with the workflow prefilled.
- Selecting the start node opens a side panel. It has a tab for "Trigger
  payload", which shows what `trigger.*` exposes to CEL (`kind`, `name`,
  `scheduled_for`, `trigger_id`, `event_id`, `occurred_at`, `payload.*`, from
  #392). Clicking a field inserts the CEL path into the focused expression
  input, through `CELCompletionContext`.
- **Why a projection and not a node.** n8n stores trigger nodes in the graph
  (§10). That couples "what the workflow does" to "who may start it, where,
  and as whom". Here those are different owners: a builtin workflow is
  read-only but is scheduled per user, and two users schedule the same
  workflow differently. Drawing the triggers gives n8n's legibility without
  the coupling.
- For a builtin or read-only workflow, the rail still shows *your* triggers
  for it. Adding one is allowed, because it does not edit the definition.

### 3.3 "Test run" next to Scenarios

- Add a **Run** tab beside Scenarios in the bottom panel. It hosts
  `RunWorkflowForm` (§2.4) with the current *draft* definition.
- The run streams into the canvas: node statuses arrive through
  `useNodeExecutionStatus`, which is already wired for the viewer. A
  "Watch full run" link opens run detail.
- Test runs are `launch_kind = chat.start` today. Tag them so they do not
  pollute the Runs list's "real" view: §13 G6 proposes a `launch_kind` of
  `builder.test`, or a payload flag. Until then, they appear as ordinary runs.

### 3.4 The integration seam (for `INTEGRATIONS.md`)

`INTEGRATIONS.md` §3.1 and §9 settle the model: one generic `action` node
(today's `invoke_tool` grown), usable both as a node and as an agent tool,
plus `integration` and `webhook` trigger kinds. The builder needs three
seams. The integration work fills them in; this document only reserves the
space:

1. **Palette grouping is data-driven, not hardcoded.**
   `FloatingWorkflowSidebar` already groups by `NodeInfo.category`. Extend
   `NodeInfo` (`catalog.proto`) with an optional
   `integration { id, display_name, icon_url, connected }`. When set, the
   palette groups by integration *above* category: "GitHub ▸ Create issue,
   Comment, …", with a **Connect** badge when not connected (`INTEGRATIONS.md`
   §9). Core and Built-in groups stay as they are. Changing one function
   (`sortCategories` in `lib/node-metadata.ts`) and the sidebar's group
   header gives the seam. No palette rewrite is needed.
2. **One node component, schema-driven config.** The action node renders
   with the provider's icon, through the `getNodeIcon` resolution order that
   already exists. Its config panel is generated from the action's `params`
   JSON Schema, reusing `ObjectSchemaEditor` / `ProtoFieldRenderer`, plus a
   **connection picker** at the top of the panel. Add `ActionStepConfig`
   (`config/ActionStepConfig.tsx`, which already exists for actions) as the
   host. Do not add one component per provider.
3. **Integration triggers join the trigger rail.** "GitHub: issue opened in
   org/repo" renders as one more line in §3.2, with the provider icon. The
   Trigger payload tab reads the manifest's `payload_schema`, so
   `trigger.payload.issue.number` autocompletes.

In the **run view**, integration calls render as tool cards with the
provider icon, the connection label, and a link out (`INTEGRATIONS.md` §9).
Untrusted trigger content is visually fenced: a `CardInset` with a "From
GitHub · untrusted" label. It is never rendered as markdown that could carry
links styled as UI.

### 3.5 Builder states

- *Daemon pending.* It does not apply to editing. It applies to Test run:
  see §9. The Run tab shows the pending banner if the selected daemon is
  suspended and the workflow requires one.
- *Trigger rail error* (ListTriggers failed): the rail shows "Couldn't load
  triggers · Retry" in its own line. The canvas is unaffected.
- *Read-only workflow:* the rail is interactive, the canvas is not. The header
  says "Built-in · read-only · **Duplicate to edit**".

---

## 4. Watching live runs

### 4.1 Current state

- A run is watched in the chat view.
  - `ChatInterface` → `ChatContainer` (`components/Chat/ChatContainer.tsx`;
    `tabId` is the chat id) renders messages, tool cards (`ToolExecution*`,
    `RunStepExecution`), thread tabs (`thread-views`), approvals, and the
    composer.
  - The execution tree is `useWorkflowExecutions(chatId)`, refetched on
    `workflow_executions` refetch events.
  - The diagram is the workflow viewer tab, opened from the chat header's
    overflow menu (`ChatHeader.tsx`, `onToggleWorkflowViewer`;
    `viewerStore.openWorkflowViewer`).
- `ChatContainer` reads the current project and worktree from stores, not
  from props (~:55-60). So "open this run" from outside the project view must
  first select the project, switch the worktree, select the chat, and
  navigate. `Automations/openAutomationChat.ts` does exactly that dance. It
  works, but it drops the user out of the Automations area into the project
  view, and the back button returns to the wrong place.
- Mobile proves the alternative: `/m/chats/$chatId` renders the **same**
  `ChatContainer` standalone (`Mobile/MobileChatScreen.tsx`), loading the
  worktrees it needs. `/m/chats/$chatId/workflow` is a run-scoped viewer
  route (`MobileWorkflowDetailRoute.tsx`).
- Nothing in the run view says *who started it*. An automation run looks
  like a chat whose first user message nobody typed. The hidden system seed
  message (`internal/triggers/fire.go` `buildSpec`) says it, but only to the
  model.

### 4.2 Run detail (`/workflows/runs/$runId`)

**Purpose:** watch or review one run without leaving the Workflows area.

**Reuse, don't fork.** Mount `ChatContainer` inside the Workflows shell, the
way mobile does. Load the chat's project and worktree first, in a
`RunRouteLoader`, which is the non-navigating part of `openAutomationChat`
factored out. This is the single biggest reuse in this document: every
message, tool card, approval and stream semantic comes for free.

Layout, top to bottom:

1. **Run header** (net-new, `components/runs/RunHeader.tsx`). It replaces
   `ChatHeader` in this host only. Contents:
   - The title.
   - A run-status badge (§0).
   - **Started by** (the launch-kind line, §0), with links: to the
     automation for `schedule`, and to the parent chat for `agent.start_run`
     (`trigger_events.payload.parent_chat_id`).
   - Workflow name, linked to workflow detail.
   - Project · workspace · machine (daemon name).
   - Started at, and duration (live-ticking while running).
   - Actions, by state:

     | State | Primary | Secondary |
     |---|---|---|
     | Running | **Pause** | Stop, Open diagram |
     | Paused | **Resume** | Stop |
     | Needs you | **Jump to request** (scrolls to the approval or question) | Stop |
     | Failed | **Retry** (resume at position, via SendMessage) | Re-run fresh, Open as chat |
     | Completed / Cancelled | **Re-run** (same inputs) | Re-run with changes…, Open as chat |

     These map to `RunService.PauseRun` / `ResumeRun` / `CancelRun`, and to
     StartChat (re-run).
2. **Trigger card**, only when `launch_kind != chat.start`. It is a
   `CardInset` pinned above the transcript, collapsed to one line by default:
   - Schedule: "Scheduled for **Tue 09:00 Europe/London** by **Nightly
     triage** · fired 09:00:04". If late, "fired 4 min late (catch-up)".
   - Manual fire: "**Run now** by you at 14:12 on Nightly triage".
   - Agent: "Started by an agent in **<parent chat title>** · tool call
     `start_run`", linking to that message.
   - Webhook or integration (later): the payload, as a fenced, collapsible
     JSON tree, labelled untrusted (§3.4).
   - Expanded, it shows the trigger's prompt as sent, the inputs and
     presets used, and "Unattended: questions and approvals were answered
     automatically". The data comes from `trigger_events` (by chat id; §13 G2).
3. **Transcript**: `ChatContainer`, unchanged, with two run-mode
   adjustments:
   - **Unattended auto-resolutions are visible.** Tool results and node
     records carry the `[UNATTENDED]` marker (`runtime/unattended.go`), and
     approvals now auto-resolve under unattended (#404). Render those as a
     neutral "Answered automatically: no one was watching" chip on the
     question or approval card, not as a normal answer. A reviewer has to be
     able to see every decision the agent made alone.
   - **The composer** is collapsed to a bar on an automation run: "This run
     was started by a schedule. **Reply** to take it over." Clicking it
     expands the normal composer. Sending is an adoption (§6.3).
4. **Diagram**: a right-hand panel (`WorkflowViewerPanel` with `chatId`), open
   by default on a run whose workflow has more than one node, and closed for
   `builtin://agent`. Resizable, and remembered per user. On narrow widths it
   becomes a tab.

**Live behaviour:** the run status and duration tick from the
`chat_state_change` and `chat_activity_changed` user updates
(`store/globalUpdatesStore.ts`). The transcript streams through
`ChatContainer`'s own per-chat subscription. Node highlighting comes from
`useNodeExecutionStatus`. Nothing new to subscribe.

### 4.3 The "live now" strip

Watching starts before you open a run. Two places show live runs compactly:

- **Runs tab, top:** a "Live" section (§5.3) with one row per running,
  queued or paused run, and the newest node shown inline ("Implementing ·
  step 3 of 7"). It comes from the root run plus the newest active child in
  `useWorkflowExecutions`.
- **Sidebar footer pill:** "2 automations running", shown only when runs are
  live that are *not* in the chat list. It is an `inline-flex` pill in the
  sidebar footer above Settings, styled like `BackgroundWorkPill.tsx`, and
  links to `/workflows/runs?live=1`. It is the chat list's only nod to
  automations: present while something is happening, gone when nothing is.

### 4.4 States

- *Loading:* the run header skeleton, and the transcript skeleton
  `ChatContainer` already has. The trigger card does not render until its
  event loads. It never shows placeholder text that later changes meaning.
- *Not found / no access:* "This run doesn't exist or isn't yours." plus a
  link to Runs. A deleted automation does not break its runs: `trigger_id` is
  cleared and the header says "Started by a schedule that has since been
  deleted".
- *Queued* (`PENDING`, launched but Temporal not yet started): "Queued. It
  starts as soon as a worker picks it up." If still pending after 60 s, add
  "This is taking longer than usual" and a **Retry start** action. A launch is
  resumable (`TRIGGERS.md`), so re-launching is safe.
- *Failed:* the `WorkflowErrorMessage` card already in the transcript, plus
  the header's Retry. If the failure is "daemon pending" (§9), the header shows
  the machine state instead of a generic failure.
- *Stream disconnected:* `ChatContainer`'s existing reconnect handling.
  Nothing new.
- *Daemon pending:* §9.

---

## 5. Past runs and history

### 5.1 Current state

- Past *chats* are the sidebar (active, grouped by worktree), the archived
  list, and Search. Past *automation runs* are the automation detail's event
  history: `AutomationDetailPage.tsx` `EventHistory`, which shows the newest
  50 (`ListTriggerEvents` `limit`), with no paging or filters, and opens each
  through `openAutomationChat`.
- A chat's own past runs (one chat can hold several root runs:
  continuation after completed/cancelled, `TRIGGERS.md`) are listed inside
  `WorkflowViewerTab.tsx` (`allWorkflows`).
- **There is no cross-cutting run list.** `RunService.ListRuns` filters only
  by `session_id`, `parent_id` and `state`. The agent tool `list_runs` (#400)
  reads chats directly (`internal/llm/tools/list_runs.go`). The web has no
  equivalent.

### 5.2 Runs list (`/workflows/runs`)

**Purpose:** "what ran, and how did it go?", across everything.

**Row** (`components/runs/RunRow.tsx`). It is shared with workflow detail
(§2.3) and automation detail (§7.4):

| Column | Content |
|---|---|
| Status | `StatusDot` plus label (§0). Live rows pulse. |
| Run | title, then a second line in `text-xs text-muted-foreground` with the workflow display name and the project |
| Started by | launch-kind icon plus short label; for schedule/webhook the automation name (linked); for agent, "Agent in <chat>" (linked) |
| Machine | daemon name; omitted when the run needs no daemon |
| Started | relative time with an absolute tooltip (`<time>`), using `lib/relativeTime.ts` |
| Duration | `completed_at - created_at`, or live-ticking |
| Actions | overflow: Open, Open as chat (adopt, §6.3), Re-run, Stop (if live), Archive |

**Filters**, all in the URL search params so links can be shared and the back
button works:

- `workflow` (multi), `trigger` (one automation), `kind` (Chat / Schedule /
  Agent / Webhook / …), `state` (Live / Needs you / Failed / Completed /
  Cancelled), `project`, `machine`.
- Time: Last 24 h (the default), 7 d, 30 d, or custom.
- Text search over the title.
- Show them as a filter row of `DropdownMenu` chips, each showing its active
  value ("Workflow: Nightly triage ✕"). Add one **Clear** link. The n8n filter
  popover hides the active filters behind a badge (§10). Chips do not.

**Default view:** all kinds, the last 24 hours, the current project if one is
selected (with an "All projects" toggle). Defaulting to "all kinds" is
deliberate. This is the one place an hourly automation is *meant* to be seen,
so it must not be filtered out here.

**Grouping of repeats** (the "runs by category" ask). When one automation
produced more than 3 runs in the visible window, and all succeeded, collapse
them into one group row:

```
◷ Hourly sweep · 23 runs · all completed · last 12 min ago     [expand]
```

- A failure in the group breaks it out. Failed rows always render
  individually, above the group.
- Toggle: "Group repeated runs" (on by default).
- This keeps a 24-hour view usable with three hourly automations: three rows,
  not seventy-two.

### 5.3 Sections in the list

1. **Needs you**: awaiting input, or daemon pending on a live run. Only
   present when non-empty.
2. **Live**: running, queued, paused. Newest first.
3. **Finished**: everything else, newest first, paginated (50 per page,
   "Load more" with a keyset cursor; §13 G1).

### 5.4 Automation and workflow history reuse the same list

- Automation detail's history becomes `RunList` filtered `trigger=<id>`,
  *plus* the non-launched events (Skipped / Failed to launch), interleaved by
  time. Those are trigger-event rows rendered with the event vocabulary (§0):
  "Skipped: the previous run was still going", or "Failed to launch: workflow
  `foo` no longer exists". They have no run to open, so they are not links.
- That needs one merged, paginated feed (§13 G3). Until then, show the
  current 50-event list with a "View all runs" link to the Runs tab filtered
  by trigger.

### 5.5 States

- *Loading:* 8 skeleton rows in the Finished shape. The Live section does not
  render until the first page lands, so a section never pops in above
  content the user is reading.
- *Empty, no filters:* `forge-ui/empty_state` titled "No runs yet", with the
  description "Runs appear here when you start a chat, run a workflow, or an
  automation fires." Actions: **Run a workflow**, **Create an automation**.
- *Empty, with filters:* "No runs match these filters", plus **Clear
  filters**, plus "Showing the last 24 hours · **Widen to 30 days**".
- *Error:* an alert card in place of the list. The filters remain usable.
- *Partial* (a page after the first fails): an inline row, "Couldn't load
  more · Retry". Already-loaded rows stay.
- *Daemon pending:* a row-level `Waiting for machine` status (§9).

---

## 6. Keeping automated runs out of the chat list

### 6.1 What #398 shipped

- `ListChatsRequest.exclude_automations` is sent on every sidebar list
  (`web/src/api/chat-grpc.ts:297`, test
  `hooks/__tests__/automationChats.excludedFromList.test.tsx`).
- Server-side, a chat is hidden when its `launch_kind` is neither null nor
  `chat.start`, unless `activity = AWAITING_INPUT`
  (`internal/db/postgres/queries/chats.sql`).
- Automation chats are reopened by id from automation history
  (`openAutomationChat.ts`).

That is the right core rule. What is left:

| # | Gap | Effect today |
|---|---|---|
| L1 | `agent.start_run` is treated as an automation. | A run an agent started *on the user's behalf in a conversation they are having* vanishes. The agent says "I started run X" and there is nowhere to click. |
| L2 | The awaiting-input exception is enforced only at list time. | `handleChatCreated` / `handleChatActivityChanged` in `globalUpdatesStore.ts` invalidate the list, so an automation chat *does* appear when it starts awaiting input. But it then disappears **while the user is in it**, once they answer, because the next refetch excludes it again. The sidebar row under the cursor vanishes. |
| L3 | A failed automation is hidden. | `activity = ERROR` is not an exception. A failure is invisible unless the user opens Automations. (Correct for the sidebar; wrong for the product, because nothing else surfaces it. The Inbox fixes this, §8.) |
| L4 | No adoption. | Replying in an automation chat (via `openAutomationChat`) works, but the chat stays an automation and stays hidden from the sidebar. The user loses it as soon as they navigate away. |
| L5 | Notifications. | `workflow_completed` marks a root completion unread and fires an OS notification (`workflow_status.go:105`, `globalUpdatesStore.ts:607`). An hourly automation therefore notifies every hour. |
| L6 | Search. | Chat search covers only listed chats. Automation runs cannot be found by text. |
| L7 | Opening a run drops you out of the area. | `openAutomationChat` navigates into the project view. Run detail (§4.2) replaces it. |

### 6.2 The policy, exactly

A chat row appears in the sidebar chat list iff **any** of these holds:

1. `launch_kind` is null or `chat.start`, and it is not archived. (Unchanged.)
2. It was **adopted** (§6.3).
3. It is an `agent.start_run` run whose **parent chat is itself listed**, and
   the run started less than 24 hours ago, or is live. It renders *nested
   under its parent chat* as a child row (indented, with a `CornerDownRight`
   icon), not as a top-level chat. After 24 hours finished, it drops out and
   remains reachable from the parent's "Runs started here" chip and from
   Runs.
4. It is **currently open** in the chat view (sticky for the session). This
   fixes L2: a row never vanishes under the user.

It does **not** appear because it needs input or failed. That is the Inbox's
job (§8). Rationale:

- An automation that hits a question at 3am should not jump into the middle
  of the user's conversations, reordering them. The Inbox badge is the one
  interruption.
- The current "unless AWAITING_INPUT" exception (`chats.sql`) is therefore
  replaced by rules 2-4. This tightens #398's behaviour. The awaiting state is
  not lost; it moves to the Inbox.

Implement it **server-side** as a view column, so the list query, search and
`list_runs` agree:

```
chats_with_activity.list_in_sidebar boolean :=
     launch_kind IS NULL OR launch_kind = 'chat.start'
  OR adopted_at IS NOT NULL
  OR (launch_kind = 'agent.start_run' AND parent chat listed AND recent-or-live)
```

`ListChatsRequest.exclude_automations` becomes `sidebar_only` (pre-launch, so
there is no alias; rename it). Rule 4 is client-side: the open chat is
pinned in `chatStore` regardless of list membership.

### 6.3 Adopting a run into a chat

**Adopt** = "this run is now a conversation of mine."

- **Explicit:** "Open as chat" in the run header or a row's overflow menu.
- **Implicit:** sending a message in an automation run's composer adopts it.
  The user is now conversing, and §4.2's composer bar says so before they
  type.
- **What adoption does:** sets `chats.adopted_at = now()` (§13 G5). It does
  **not** change `launch_kind` or `trigger_id`; origin is history and never
  rewritten. It also:
  - Clears `unattended` for *subsequent* runs in this chat. The continuation
    `SendMessage` starts a new root run without the unattended flag, because a
    human is now present. The current run, if live, keeps its flag:
    unattended is monotone within a run (`unattended.go`).
  - Moves the chat to the top of the sidebar, in its worktree group, with an
    origin glyph: a small `CalendarClock` or `Bot` icon before the title, and
    the tooltip "Started by schedule Nightly triage".
- **Un-adopt:** "Remove from chats" in the sidebar context menu. It clears
  `adopted_at`. It is not archive; the run stays in Runs.
- **Overlap:** an adopted run that is still live does not block the next
  schedule fire any differently. Overlap `SKIP` keys off the trigger's latest
  launched chat, and adoption does not change that. The automation detail
  shows "Previous run is still going (adopted by you)" when a fire was
  skipped because of it.

### 6.4 Notifications policy (L5)

| Event | Interactive chat | Automation run |
|---|---|---|
| Completed | unread + OS notification (unchanged) | **none**. Recorded in Runs. Opt-in per automation: "Notify me when it finishes". |
| Failed | (unchanged) | Inbox item + OS notification, **deduplicated per automation**: the first failure notifies; consecutive failures bump the count on one Inbox item ("Failed 3 times in a row"). |
| Needs input (pre-#404 questions, approvals with a notify policy) | (unchanged) | Inbox item + OS notification. |
| Daemon pending at fire time | n/a | Inbox item, once per automation per machine episode. |

Implementation: `WorkflowStatusActivity` (`workflow_status.go:105`) marks
unread on root completion. It skips that when the chat's launch kind is not
interactive and not adopted. The client notification path
(`globalUpdatesStore.ts:607`) needs no change once the server stops emitting.

### 6.5 Search (L6)

Chat search gains a scope toggle: **Chats** (default) / **All runs**. "All
runs" searches the same columns without the sidebar predicate. Results show
the Started-by label. On the Runs tab, the search box is that same query,
pre-scoped.

### 6.6 States in the sidebar

- *A child run row* (rule 3) shows a status dot only; there is no unread
  badge. Its parent row gets a small "1 run" count chip while it is live.
- *An adopted run that is live* shows the normal activity dot.
- *Sidebar footer pill* (§4.3) reads "N automations running", and shows a
  `warning` variant "1 needs you" that links to the Inbox. It is hidden when
  zero.

---

## 7. The active-triggers view (Automations, grown up)

### 7.1 What #398 shipped

`/automations` (`AutomationsListPage.tsx`) lists one row per trigger across
projects. Each row has the name, project name (resolved client-side from
`projectStore`), the schedule in words (`lib/cronText.ts` `describeSchedule`),
the last event's outcome and time, or else the next fire time, and an
enabled toggle. There are loading, empty and error states. Detail
(`AutomationDetailPage.tsx`) has the header (project, "on <daemon>", schedule),
Run now (polling events for 30 s), Edit, Delete, a Definition card
(`DefinitionList`, with the prompt in a `CardInset`), and History.
`AutomationFormDialog.tsx` has the name, project, workflow, prompt, schedule
presets, cron, timezone, overlap, and a required "Runs on" daemon
(`daemonChoices.ts`).

Gaps it left:

- workflow inputs and presets are not editable (they are carried through)
- worktree and catch-up window are not editable
- history is not paginated (newest 50)
- triggers do not carry project or daemon names, so a cold load shows ids
  until the stores fill
- the list is flat: no grouping, no health, no "what's coming up"
- the last-outcome column conflates *launched* with *succeeded* (§0)

### 7.2 Automations tab (`/workflows/automations`)

**Purpose:** "what will run on its own, is it healthy, and what fires next?"

**Top: the "Coming up" timeline** (net-new). It is a horizontal 24-hour strip
in a `Card`, with one lane per enabled automation and ticks at each future fire
time (computed client-side from `ScheduleSource`, using the same cron parser
that powers `cronText.ts`, anchored on the server's `next_fire_at`). Hovering a
tick shows "Nightly triage · 09:00 Europe/London". At a glance it answers
"what happens overnight", which is the question a list cannot answer.

- If there are more than 8 lanes, collapse to the 8 with the soonest fires and
  add a "+N more" line.
- It is hidden when there are no enabled schedule automations. Non-schedule
  kinds (webhook, integration) have no future ticks, so they never get a lane.

**Then: the list, grouped.** The "cron categories" ask. Group by **Workflow**
(the default) or by **Project**, using a segmented control. Each group:

```
Nightly triage workflow                            3 automations · 2 on
  ◷ Triage inbox      Weekdays 09:00 · London   ● Healthy    next in 3h    [on]
  ◷ Triage bugs       Every 4 hours             ▲ 2 failed   next in 40m   [on]
  ◷ Weekend sweep     Sat 10:00                 ○ Paused     —             [off]
```

Row columns:

| Column | Content |
|---|---|
| Kind icon | `CalendarClock` for schedule; later `Webhook`, or the provider icon |
| Name | name, and on a second line: project · machine (daemon name) |
| When | the schedule in words, plus timezone when it differs from the user's |
| Health | see below |
| Next | relative time to `next_fire_at`; "—" when disabled |
| Toggle | enabled. Flipping it is optimistic, with rollback and a toast on error (today's behaviour) |

**Health** is computed from the last N=10 events plus the runs they launched.
That needs run outcomes joined to events (§13 G3):

| Health | Rule | Variant |
|---|---|---|
| Healthy | the last launched run completed, and no failed events in the window | `active` |
| Failing | the last 2+ firings ended Failed (launch failure OR run failure) | `error`, with "N failed" |
| Skipping | the last 3+ events were Skipped (overlap, or disabled at fire time) | `warning`, with "Skipping: previous run still going" |
| Waiting for machine | the last firing hit daemon pending | `warning` |
| New | it has never fired | `neutral` |
| Paused | disabled | `paused` |

Within a group, sort by health severity, then by next fire. A **"Needs
attention"** pseudo-group is pinned on top, holding Failing / Skipping /
Waiting rows across groups. It appears only if non-empty.

**Header actions:** **New automation** (primary), and a Group by control.

### 7.3 The compact automation row

`components/automations/AutomationRow.tsx` is used in three places: this tab,
workflow detail (§2.3), and the builder trigger rail popover (§3.2). It is
built from today's `AutomationRow` inside `AutomationsListPage.tsx`, extracted
and given the health column.

### 7.4 Automation detail (`/workflows/automations/$triggerId`)

Keep #398's structure (header actions, Definition, History). Change these:

1. **Header** adds the health badge and "Next: Tue 09:00 (in 3 h)". The
   subtitle reads "Runs **Nightly triage** in **reliant** on **MacBook**".
   The workflow name links to workflow detail.
2. **Definition** gains:
   - Inputs and presets: rendered read-only from `params`/`presets`, using the
     workflow's input schema for labels (`useWorkflowInputs`).
   - Workspace, catch-up window, and overlap.
   - **Unattended behaviour:** a one-line `CardInset` reading "No one is
     watching these runs. Questions and approvals are answered
     automatically." It links to docs. This is the place the consequence of
     #404 belongs.
3. **History** becomes the merged feed (§5.4), paginated, with an outcome
   filter (All / Failed / Skipped).
4. **Edit** opens a dialog whose "What" section is `RunWorkflowForm` (§2.4).
   That closes the inputs, presets and worktree gaps. Catch-up window goes into
   an "Advanced" disclosure with overlap.
5. **Run now** feedback: keep the 30 s poll (`FIRE_POLL_MS`), but when the
   event lands as launched, show a toast with **Watch** linking to run detail,
   instead of making the user find the new history row.

### 7.5 States

- *Loading:* the timeline skeleton (a fixed height, so nothing shifts), and
  4 skeleton rows.
- *Empty:* `empty_state` titled "Nothing runs on its own yet", with the
  description "An automation runs a workflow on a schedule, with no one
  typing. Each run is recorded under Runs." Actions: **New automation**, and
  **Browse workflows**. Below it, a `CardInset` with three suggested starters
  that prefill the dialog. Examples: "Every weekday at 9: triage new issues",
  "Every hour: check CI on main", "Every Monday: summarise last week's
  merged PRs". This turns the empty page into an onboarding step.
- *Error:* as #398 (alert card, Try again). Per-row toggle errors are toasts.
- *Disabled because of the machine:* if the trigger's daemon was deleted, the
  row shows "Machine removed: choose another" as an `error` health. Firings
  would fail anyway, so make that visible before they do. Opening it goes
  straight to Edit with the machine field focused.
- *Daemon pending:* §9. A schedule run hitting a suspended machine is the
  common case. #402 and #406 mean only the run's start wakes a daemon, so a
  schedule fire wakes its daemon at preflight. Daemon pending at fire time
  means the wake *failed*.

---

## 8. The inbox: what needs a human

### 8.1 Current state

There is no aggregate. Needs-attention signals are scattered:

- The sidebar's notification badge per chat: `awaiting_approval` or `unread`
  (`Sidebar.tsx`, `chatNeedsAttention` ~:174 and `ChatItem` ~:226-227). It
  only covers listed chats, so it excludes automations after #398, except
  while awaiting input.
- OS notifications for `approval_required` / `workflow_completed`
  (`globalUpdatesStore.ts:607`, `:759`).
- `ApprovalService.ListApprovalsByChat` and
  `QuestionService.GetPendingQuestion` are per chat. **Nothing lists pending
  approvals or questions across chats.**
- Failed automations are visible only on `/automations`.

### 8.2 Inbox (`/inbox`)

**Purpose:** one list of everything waiting on *this* user, across every
project and launch kind.

Items, in this priority order:

| Kind | Source | Inline action | Clears when |
|---|---|---|---|
| **Approval** | pending `approvals` row | **Approve** / **Deny**, plus the tool name and its arguments summarised in a `CardInset` (reusing `ApprovalActions.tsx`) | resolved |
| **Question** | pending `questions` row | the answer field inline, reusing `QuestionPrompt.tsx` | answered |
| **Automation failing** | a trigger with Failing health (§7.2) | **Open last run** · **Pause automation** | next launched run completes, or dismissed |
| **Automation launch failed** | `trigger_events.outcome = failed` | the reason, plus **Edit automation** | the next firing launches, or dismissed |
| **Waiting for machine** | a live run blocked on `ErrDaemonPending` (§9) | **Wake <machine>** | the daemon attaches |
| **Run finished** (opt-in per automation, §6.4) | a root completion of an opted-in automation | **Open** | opened, or dismissed |

Interactive chats awaiting approval appear here too, so the Inbox is the
complete answer to "what's waiting on me". They still show their sidebar
badge; the two do not compete. The sidebar shows *where*, the Inbox shows
*everything*.

**Row anatomy:** a kind icon; a one-line title ("Approve `git push` in
**Nightly triage · Tue 09:00**"); a context line (project · workflow · how long
it has been waiting); then the inline action. Clicking the row opens the run
detail scrolled to the request.

**Grouping:** by run when one run has several pending items ("3 approvals
waiting"), and by automation for repeated failures (§6.4 dedupe).

**Badge:** the Inbox nav item shows the count of Approval + Question +
Waiting-for-machine items (the ones that block a live run), as a solid
number. Failures and finished runs add a dot, not a count. A blocked run
is urgent; a failed one is informational.

**Live updates:** approvals and questions already produce
`chat_activity_changed` (`AWAITING_INPUT`) user updates. The Inbox query
invalidates on those, and on `chat_state_change` for failures. It needs no
polling.

### 8.3 States

- *Loading:* 3 skeleton rows.
- *Empty:* "Nothing needs you." with the description "Approvals, questions
  and automation problems show up here." This is the success state, so make it
  calm: no illustration, no call to action.
- *Error:* an alert card. The sidebar badge falls back to the client-known
  count from `activityStore` (listed chats only) and shows a `warning` dot to
  signal it may be incomplete.
- *Item resolved elsewhere* (approved in the chat view, or by another device):
  the row animates out on the next update. If the inline action loses the race
  and the server reports it was already resolved, show "Already handled" in
  place of the action for 2 s, then remove it. It is not an error toast.

---

## 9. Daemon pending, as a cross-cutting state

### 9.1 What changed (#402, #403, #406)

- A trigger names the daemon it runs on (`Trigger.daemon_id`, required).
- Only two things wake a suspended daemon:
  - **a run's start**: run preflight
    (`activities/handlers/preflight_daemon.go`, through `DaemonWaker.EnsureAwake`)
  - **a signed-in user's send**: `wakeDaemonForAttendedTurn`
    (`internal/grpc/services/chat_wake.go`), best effort with a 30 s bound
- Every tool-time path, built-in and MCP, resolves without resuming. A
  suspended daemon returns `ErrDaemonPending`
  (`internal/toolexec/daemon_router.go:28`), whose messages read "the machine
  for this request is suspended and will wake when you next message it" or
  "…is still starting" (`daemon_router_nats.go:283-397`).

So a run can be **live and blocked on a machine**. Examples:

- An automation run whose machine idled out mid-run.
- A chat whose daemon suspended while the agent was thinking.
- A fire whose preflight wake failed.

Today that surfaces only as a tool error string inside a tool card. The
web's `isDaemonConnectingError` (`lib/daemon-errors.ts`) recognises the RPC
form ("no daemon connected") for chat sends, and `ResumeDaemonPill.tsx`
offers a resume. Neither is driven by *a run's* state.

### 9.2 One state, one component

Add **Waiting for machine** to the run display vocabulary (§0). It is a
display state, not a new `WorkflowState`. The run is still `ACTIVE`, or it
paused itself (unattended runs self-resume on a backoff,
`pause_coordinator.go`).

**Detection** (§13 G7). The most recent tool result in the run failed with
`ErrDaemonPending`, and the run has not made progress since. The server
should expose it as a run field, so every surface agrees:

- `Chat.blocked_on_daemon_id` (optional), or
- `ChatActivity.WAITING_FOR_DAEMON`, set by the tool executor when it records
  a pending error and cleared on the next successful tool call.

Do not have the web parse tool-result text.

**`<MachineStatus daemonId runId />`**
(`components/runs/MachineStatus.tsx`, net-new) renders, by daemon state from
`DaemonRegistry`:

| Daemon state | Copy | Action |
|---|---|---|
| suspended | "**MacBook (cloud)** is asleep. This run is waiting for it." | **Wake it** (calls the same resume `ResumeDaemonPill` uses). For an automation run, add: "Schedules wake it when they start; it went to sleep during this run." |
| starting | "**MacBook (cloud)** is starting…" plus a pulsing `pending` dot | none. Auto-clears when it attaches. |
| offline (local daemon, app closed) | "**Sean's laptop** is offline. Open Reliant on that machine to continue." | none; or **Move to another machine** (§14 Q6) |
| deleted | "The machine this run used was removed." | **Re-run on…** (picker) |
| quota exhausted | reuse `ResumeDaemonPill`'s `formatResumeError` copy (plan / coupon) | **Billing** |

**Where it renders:**

- *Run detail:* as a banner directly under the run header. It uses
  `bg-card border-border` with a left `border-l-2 border-l-warning` accent.
  It is not a toast, and not inside the transcript, because it is the run's
  state, not a message.
- *Runs list and the Live strip:* the status column reads "Waiting for
  machine" (`warning`), and the row sorts into **Needs you**.
- *Inbox:* the "Waiting for machine" item (§8.2).
- *Automations:* the "Waiting for machine" health (§7.2). On automation
  detail, the Definition card's machine line shows the daemon's current state
  and its last-seen time.
- *Run… form and automation dialog:* the machine picker shows each daemon's
  state inline ("asleep: will wake when the run starts"). Asleep is not an
  error at pick time. Only a deleted daemon or exhausted quota blocks submit.
- *Builder Test run:* as in the Run… form.
- *Chat composer (interactive chats):* sending already wakes the daemon
  (#406). While the wake is in flight (up to 30 s), show "Waking **MacBook**…"
  on the composer's status line instead of the generic thinking indicator. If
  the wake fails, the tool card shows the pending error, and the banner above
  appears with **Wake it**.

**Never** show the raw `ErrDaemonPending` text as the primary message.
Map it once, in `lib/daemon-errors.ts`, by extending
`isDaemonConnectingError` with an `isDaemonPendingError` that reads the
structured state from G7. Keep the text match only as a fallback.

---

## 10. Lessons from n8n

Read from `~/src/n8n/packages/frontend/editor-ui/src`.

### Take

1. **Executions are a first-class sibling of Workflows** (`ProjectTabs.vue`:
   Workflows / Credentials / Executions / Variables). Global executions
   (`GlobalExecutionsList.vue`: Workflow, Status, Started at, Run time, ID) and
   per-workflow executions (`/workflow/:name/executions`) are the same list,
   filtered. → our Runs tab plus the workflow-detail "Recent runs" using one
   `RunRow` (§5).
2. **A per-workflow executions sidebar beside a preview**
   (`WorkflowExecutionsSidebar.vue` + `WorkflowExecutionsPreview.vue`): pick a
   run on the left and see it rendered on the canvas on the right, with
   auto-refresh. → our run detail's diagram panel, and the builder's Run tab
   streaming into the canvas (§3.3, §4.2).
3. **The status vocabulary is small and fixed** (`execution-status.ts`:
   canceled, crashed, error, new, running, success, unknown, waiting).
   "Waiting" is a distinct, visible state with its resume time
   (`executionsList.statusWaiting`). → our §0 table, including Waiting for
   machine.
4. **Retry is explicit about which definition it uses**
   (`retryWithCurrentlySavedWorkflow` vs `retryWithOriginalWorkflow`). → Re-run
   should say which. Today we cannot honestly offer "original", because runs do
   not snapshot their definition (§14 Q3).
5. **Trigger-first canvas.** An empty canvas asks "What triggers this
   workflow?" (`nodeCreator.triggerHelperPanel.*`: manual, schedule, webhook,
   form, other workflow, chat). → our trigger rail (§3.2) makes "how does
   this start" the first thing on the canvas, without storing triggers in the
   graph.
6. **An activation toggle on the workflow, with a guard.**
   `workflowActivator.thisWorkflowHasNoTriggerNodes` refuses to activate a
   workflow with nothing to start it. → our equivalent is per-trigger
   enablement, plus refusing to enable a trigger whose machine was deleted
   (§7.5).
7. **Insights** (`features/execution/insights`: total, failed, failure rate,
   time saved, average run time). → automation health (§7.2) is the small,
   per-automation version. A dashboard can come later.

### Avoid

1. **Filters hidden in a popover with a count badge** (`ExecutionsFilter.vue`).
   Users cannot see why a list is short. → visible filter chips (§5.2).
2. **Triggers stored as graph nodes.** That couples who-may-start-it to
   what-it-does, and makes a shared or builtin workflow uneditable for
   scheduling. → our projection (§3.2).
3. **One global "active" switch per workflow** that starts every trigger in
   it at once. → our triggers enable independently.
4. **Execution IDs as a primary column.** Integers mean nothing to a user. →
   we show the started-by line instead. The id lives in the overflow menu
   ("Copy run id"), for agents and the CLI.
5. **"Save execution data" settings** (`saveDataSuccessExecution` /
   `saveDataErrorExecution`). Making history optional per workflow is a
   storage tax pushed onto the user. → retention is a server policy, not a
   per-workflow toggle.
6. **Hover-only actions on cards.** This is our own hub's habit as well as
   n8n's. → row overflow menus, reachable by keyboard (§2.2).
7. **An auto-refresh checkbox** (`executionsList.autoRefresh`). → we have a
   push stream, so the list is always live and offers no toggle.
8. **Status colour alone.** n8n's cards lean on a coloured left border. →
   every status in our UI is a dot *plus* a word.

---

## 11. Styling contract, applied

These rules are from reliant's repo memory and
`web/src/components/forge-ui/card.tsx`. They are restated for this surface so
an implementer does not have to infer them.

- **Elevation:**
  - The page is `bg-background`.
  - Every list or panel is a forge-ui `Card` (`bg-card border-border`).
  - Wells inside a card, such as the trigger card in run detail, the prompt in
    automation definition, an approval's arguments, or a fenced payload, are
    `CardInset` (`bg-background border-border/60`).
  - **Never use `bg-muted` for structure.** It is for hover, selected and
    disabled states only. The current `WorkflowCard` hover buttons use
    `bg-background/80 hover:bg-muted`, which is the acceptable form (an
    interaction state).
  - Never nest a `Card` in a `Card`.
- **Lists** are one `Card padding="none"` with `ul.divide-y divide-border/60`,
  matching `AutomationsListPage.tsx`. Section labels between groups use the
  section-label rung (`text-xs font-semibold uppercase tracking-wide
  text-muted-foreground`).
- **Heading ladder:**
  - Page heading: `PageHeader`, once per screen.
  - Panel heading: `CardHeader title`.
  - Section label: as above.
  - Stat caption: for the health numbers, the lighter rung from `card.tsx`.
- **Status:** use forge-ui `StatusDot` and `Badge`, with the variants in §0.
  Drop the hardcoded `blue-500` / `emerald-500` / `violet-500` / `amber-500`
  source tints in `WorkflowCard` and `WorkflowViewerTab`'s `text-sky-500` /
  `text-emerald-500` / `text-red-500`. Source becomes a neutral `Badge`, and
  status uses semantic tokens (`success` / `warning` / `danger` / `accent`).
- **Conditional classes** go through `cn()`. Interaction states use Tailwind
  variants, never DOM style mutation.
- **Accessibility:**
  - Every icon-only control has `aria-label`.
  - Status has text, not only colour.
  - Lists are `ul` with `aria-label`.
  - The Inbox badge has an `aria-live="polite"` count.
  - Filter chips are buttons with `aria-pressed`.
  - The timeline (§7.2) has a text alternative: a visually hidden `ol` of
    upcoming fires.
- **Motion:** the Live dot pulse and row enter/leave transitions respect
  `prefers-reduced-motion`.
- Run `npm run lint:css` if any stylesheet is touched. Nothing here needs new
  CSS; everything is semantic Tailwind classes.

---

## 12. Phased delivery, with files

Each phase ships on its own and leaves the app coherent. "Reuse" marks
existing components carried over; "new" marks net-new ones. Every phase ships
tests that fail before and pass after: vitest for components and hooks, and
Go table tests for the backend gaps.

### Phase 1: Runs exist (the biggest gap)

*Goal: every run, including hidden automation and agent runs, is findable and
watchable inside the Workflows area.*

- **Backend G1:** `RunService.ListRuns` gains filters and a cursor (§13).
- **New:**
  - `web/src/lib/runStatus.ts`: the display vocabulary (§0). Migrate
    `OutcomeBadge.tsx`, `WorkflowViewerTab.tsx` (`WorkflowStatusIcon`) and
    `MobileWorkflowScreen.tsx`'s adapter onto it.
  - `web/src/api/run-grpc.ts`, `web/src/hooks/run-queries.ts`: keys and
    queries, invalidated from `globalUpdatesStore` on `chat_state_change` /
    `chat_activity_changed` / `chat_created`.
  - `web/src/components/runs/RunRow.tsx`, `RunList.tsx` (sections, grouping
    of repeats), `RunFilters.tsx` (chips, bound to search params).
  - `web/src/components/runs/RunHeader.tsx`, `TriggerCard.tsx`,
    `RunDetailPage.tsx`, and `RunRouteLoader.tsx` (factored out of
    `Automations/openAutomationChat.ts`).
  - Routes `/workflows/runs` and `/workflows/runs/$runId` in `routes.tsx`;
    a schema in `routeSchemas.ts` (`runsSearchSchema`).
- **Reuse:** `ChatContainer` (mounted standalone, as in
  `Mobile/MobileChatScreen.tsx`), `WorkflowViewerPanel`,
  `useWorkflowExecutions`, `useNodeExecutionStatus`, `relativeTime.ts`.
- **Change:** `AutomationDetailPage`'s history "open" goes to
  `/workflows/runs/$runId` instead of `openAutomationChat`. A `start_run` /
  `get_run` tool renderer (new, in `components/Chat/tool-renderers/`; none
  exists today) links the run id to run detail. That closes L1's
  dead end even before the sidebar rule changes.
- **Interim shell:** Runs can live at `/runs` inside a copy of
  `AutomationsShell` until Phase 3 merges the area. Prefer doing Phase 3's
  shell first if it fits; it is small.

### Phase 2: Sidebar policy, adoption, notification hygiene

*Goal: the chat list holds conversations only, and nothing a user is in
disappears.*

- **Backend:**
  - G5: `chats.adopted_at`, plus the `list_in_sidebar` view column.
  - Rename `exclude_automations` → `sidebar_only`.
  - The `agent.start_run` nesting rule.
  - `WorkflowStatusActivity` (`workflow_status.go:105`) skips unread for
    non-interactive, non-adopted chats.
  - An `AdoptChat` / `UnadoptChat` RPC on `ChatService`.
  - Continuation after adoption drops `unattended`.
- **Web:**
  - `api/chat-grpc.ts` (`sidebarOnly`).
  - `Sidebar.tsx`: child-run rows, the origin glyph on adopted rows, the
    footer pill (new `components/Layout/AutomationActivityPill.tsx`, modelled
    on `BackgroundWorkPill.tsx`), and pinning of the open chat.
  - `RunHeader` composer bar plus "Open as chat".
  - Chat search scope toggle.
- **Tests:**
  - An hourly automation completion does not mark the chat unread.
  - An open automation chat stays in the list after its question is answered.
  - An adopted chat lists.
  - An agent-started run nests under a listed parent.
  - Adopting clears `unattended` for the next root run only.

### Phase 3: Workflows area, library and detail, Run…

*Goal: the hub is replaced; definitions show their use; manual runs with typed
inputs.*

- **New:**
  - `components/workflows/WorkflowsShell.tsx` (generalised from
    `AutomationsShell.tsx`, with a tab bar and project resolution as in
    `ForgeLayout.tsx`).
  - `components/workflows/library/LibraryPage.tsx`, `WorkflowRow.tsx`.
  - `components/workflows/detail/WorkflowDetailPage.tsx`.
  - `components/workflow/run/RunWorkflowForm.tsx` + `RunWorkflowDialog.tsx`.
  - Routes `/workflows`, `/workflows/library[/$workflowRef]`, and redirects
    from `/workflow` (the hub) and `/automations*`.
- **Reuse:**
  - `useWorkflowInputs`, `WorkflowInputGroup` (store-free), `DraftStatusBadge`,
    `WorkflowParseErrorView`, `WorkflowViewerPanel` (definition-only mode),
    `Automations/daemonChoices.ts`.
  - The three preset modals, extracted from `WorkflowHub.tsx` into
    `components/workflow/presets/` without behaviour change.
- **Delete:** `WorkflowHub.tsx`'s workflows tab and `WorkflowCard`, and the
  hub branch in `WorkflowBuilderPage.tsx` (it renders `WorkflowHub` when no
  workflow is selected). Presets get a Settings section.
- **Sidebar:** "Workflows" goes to `/workflows`; remove the separate
  "Automations" entry. Update `CommandPalette.tsx:130` and
  `NavigationOverlay.tsx:72`.
- **Mobile:** `MobileWorkflowCatalog` is unchanged in this phase. Add
  `/m/runs` later (§14 Q8).

### Phase 4: Automations, grown up

*Goal: grouped, healthy, upcoming, fully editable.*

- **Backend:**
  - G3: merged events-plus-runs feed, with pagination and health.
  - G8: `Trigger` carries `project_name` and `daemon_name`.
- **New:**
  - `components/automations/ComingUpTimeline.tsx`, `AutomationRow.tsx`
    (extracted from `AutomationsListPage.tsx`, with health),
    `AutomationGroups.tsx`, `automationHealth.ts`.
  - Starter templates in the empty state.
- **Change:**
  - `AutomationFormDialog.tsx`: the "What" section becomes
    `RunWorkflowForm`, with worktree and an Advanced section for catch-up and
    overlap.
  - `AutomationDetailPage.tsx`: header health and next fire, read-only inputs
    in Definition, the unattended note, the merged history via `RunList`, and
    Run-now **Watch** toast.

### Phase 5: Inbox, and daemon pending everywhere

*Goal: one place for everything waiting on the user; machine state is never a
mystery.*

- **Backend:**
  - G9: `InboxService.ListInbox` (or `ApprovalService.ListPending` +
    `QuestionService.ListPending` across chats), plus failed-automation
    items from G3 health.
  - G7: a structured daemon-pending run state.
- **New:**
  - `components/inbox/InboxPage.tsx`, `InboxItem.tsx`, and
    `hooks/inbox-queries.ts`.
  - The nav item with a badge in `Sidebar.tsx`.
  - `components/runs/MachineStatus.tsx`.
  - `isDaemonPendingError` in `lib/daemon-errors.ts`.
- **Reuse:** `ApprovalActions.tsx`, `QuestionPrompt.tsx`, and
  `ResumeDaemonPill`'s resume call and `formatResumeError`.
- **Change:**
  - The Runs list and Automations health read G7.
  - The composer's "Waking <machine>…" status line during an attended wake.

### Phase 6: Builder, triggers rail, test run, integration seams

*Goal: the builder shows how a workflow starts, can test-run with inputs, and
is ready for integration nodes.*

- **New:**
  - `components/workflow/nodes/TriggerRailNode.tsx`, which replaces the
    synthesised `EventNode` for the entry. It keeps `EventNode` for in-graph
    event types.
  - `config/TriggerPayloadPanel.tsx`, feeding `trigger.*` into
    `CELCompletionContext`.
  - A Run tab beside `ScenarioPanel`.
- **Change:**
  - `lib/workflow-flow.ts` (the entry node becomes the rail).
  - `FloatingWorkflowSidebar.tsx` / `lib/node-metadata.ts` (`sortCategories`)
    groups by `NodeInfo.integration` when present.
  - `ActionStepConfig.tsx` hosts the schema-driven form and the connection
    picker when the integration work lands.
- **Backend:**
  - G6: `builder.test` launch kind.
  - The `catalog.proto` `NodeInfo.integration` field, owned by the
    integrations work (`INTEGRATIONS.md` phase 5).

---

## 13. Backend gaps

| # | Gap | Needed by | Shape |
|---|---|---|---|
| G1 | A cross-cutting run list. `RunService.ListRuns` filters only by session, parent and state (`run.proto:193`); the agent `list_runs` reads chats directly. | Runs list, Library "last run", workflow detail | `ListRunsRequest` gains `project_id?`, `repeated workflow`, `trigger_id?`, `repeated launch_kind`, `repeated display_state` (incl. needs-input), `started_after/before`, `query`, `page_token`. `Run` gains `title`, `project_id`, `launch_kind`, `trigger_id`, `trigger_name`, `daemon_id`, `activity`. Root runs only by default. Backed by `chats_with_activity`, which already has every column. Plus a `LastRunPerWorkflow` aggregate (or `group_by=workflow` with `limit 1`). |
| G2 | Launch event by chat id. `GetTriggerEventByChatID` exists in the repo (#392), not on the wire. | Run detail trigger card | `TriggerService.GetLaunchEvent(chat_id)`, or `Chat.launch_event` (a `TriggerEvent`), set on `GetChat` only. |
| G3 | Events joined to run outcomes, paginated; and health. | Automation history and health, Inbox failures | `ListTriggerEvents` gains `page_token`, an `outcome` filter, and, per launched event, the run's `(state, stop_reason, outcome)`. `Trigger` gains a read-only `health { status, consecutive_failures, consecutive_skips, last_failure_detail }`, computed on read. |
| G4 | Preflight verdict on the wire. | Workflow detail "Requires a machine", Run… form | `WorkflowListItem.requires_daemon`, from the same preflight analysis `TOOL_PLACEMENT.md` §3.4 tightens. |
| G5 | Adoption. | Sidebar policy | `chats.adopted_at timestamptz NULL` (migration, no down); `list_in_sidebar` in the view; `AdoptChat` / `UnadoptChat`; continuation drops `unattended` when adopted. |
| G6 | Builder test runs are indistinguishable from chats. | Runs list hygiene | `launch_kind = 'builder.test'` (widen the `trigger_events.kind` CHECK the way #400 did), excluded from the sidebar, shown in Runs under a "Tests" kind. |
| G7 | Daemon pending is only a tool-result string. | §9 everywhere | `ChatActivity.WAITING_FOR_DAEMON` (or `Chat.blocked_on_daemon_id`), set when a tool call fails with `ErrDaemonPending`, cleared on the next success or attach. It is emitted as `chat_activity_changed`. |
| G8 | Triggers lack display names. | Automations list on cold load | `Trigger.project_name`, `Trigger.daemon_name` (read-only). |
| G9 | No cross-chat pending approvals and questions. | Inbox | `InboxService.ListInbox` returning typed items, or `ListPending` on both services. Scope: the caller, all projects. |
| G10 | No completion-notify opt-in per automation. | §6.4 | `Trigger.notify_on { completed, failed }`, defaulting to `failed`. |

---

## 14. Open questions for the user

1. **Inbox placement.** A top-level nav item (proposed), or a bell in the
   header? The proposal makes it the only automation interruption, so it
   needs to be visible.
2. **Interactive chats awaiting approval in the Inbox.** Should they appear
   there too (proposed: yes, the Inbox is "everything waiting on me"), or
   should the Inbox be automations-only, leaving chats to the sidebar badge?
3. **Re-run semantics.** Runs do not snapshot their workflow definition, so
   "re-run" always uses the *current* definition. Is that acceptable, or
   should a launch record the definition hash and YAML so we can offer
   "re-run with the original" the way n8n does?
4. **Agent-started runs in the sidebar.** Nest them under the parent chat for
   24 hours (proposed), always nest them, or never list them (Runs only)?
5. **Remove the "awaiting input" exception from `exclude_automations`?** The
   proposal moves that signal to the Inbox, so an automation that asks a
   question never enters the chat list unless adopted. This changes #398's
   shipped behaviour.
6. **Moving a blocked run to another machine.** When a local daemon is
   offline, should the UI offer "continue on another machine"? The run's
   worktree lives on the original daemon, so this is not free. Proposal: not
   in v1; "Re-run on…" only.
7. **Presets' new home.** Should presets move out of the Workflows hub into
   workflow detail plus Settings (proposed), or keep a fourth tab?
8. **Mobile.** Is a `/m/runs` list plus the Inbox in scope for this round, or
   desktop first? Mobile already has a run-scoped viewer
   (`/m/chats/$chatId/workflow`).
9. **Notifications default.** For automation completions: off, with failures
   on (proposed), or should the trigger dialog ask?
10. **Cross-project default.** Should Runs and Automations default to all
    projects (proposed for Automations, since there are few) or to the
    current project (proposed for Runs, since there are many)?
11. **"Run without me" on a manual Run….** Offer it at all, or always run
    attended when a human pressed the button?

---

## 15. Build next, ranked

1. **Runs list and run detail inside the app area** (Phase 1, with G1). It
   is the largest functional hole: agent-started runs are unreachable today,
   and automation runs are reachable only through one automation's history.
   It also reuses the most code (`ChatContainer` mounted standalone, the
   viewer, the execution hooks).
2. **Notification hygiene** (§6.4: one guard in `workflow_status.go`). It is
   tiny, and without it an hourly automation sends 24 OS notifications a day.
   It can ship ahead of Phase 2.
3. **Display vocabulary module** (`lib/runStatus.ts`), plus fixing
   "Launched ≠ succeeded" on `/automations`. This is small, prevents drift in
   every later phase, and corrects a misleading signal already in production.
4. **Sidebar policy and adoption** (Phase 2, with G5). This fixes the
   vanishing-row bug (L2) and gives users a way to keep an automation run.
5. **Daemon-pending state** (G7 + `MachineStatus`). #406 made pending a
   normal outcome for unattended runs. Until it is visible, it reads as "the
   agent got stuck".
6. **Run… form with typed inputs** (`RunWorkflowForm`), reused straight away
   in `AutomationFormDialog`. This closes #398's biggest editing gap (inputs,
   presets, worktree).
7. **Workflows area merge and library/detail** (Phase 3). It is the visible
   "re-envisioning", but it depends on 1, 3 and 6 for its content.
8. **Automation health, grouping and the Coming-up timeline** (Phase 4, with
   G3 and G8).
9. **Inbox** (Phase 5, with G9). It is high value, but it needs G3 and G7 to
   be complete. Ship the approvals-and-questions half first if G9 lands early.
10. **Builder trigger rail, Test run, and integration seams** (Phase 6). The
    seams should be reserved in code review now (data-driven palette
    grouping, `ActionStepConfig` as the host), so `INTEGRATIONS.md` phase 5
    drops in without a palette rewrite.

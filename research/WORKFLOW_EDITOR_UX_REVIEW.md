# Workflow editor: UX review

**Stream R (review only; no product code changed).** Written 2026-10-06 against
the dev stack (reliant-web on 127.0.0.1:3000, served from
`~/src/reliant-labs/reliant` at `main` `eada660f`). Read it with
`WORKFLOW_EDITOR_FEEDBACK.md` (the user's feedback and the E/G fix plan),
`WORKFLOW_UI.md`, `INTEGRATIONS_V1_BRIEF.md` §3a and `TOOL_CAPABILITIES.md`.

Streams E and G already own the palette's Integrations group, Execute/Invoke
naming, brand logos, the `trigger.kind` text, field placeholders, examples and
descriptions, the Twilio and HTTP params, and the connect dialog's env-var
message. None of those is re-reported below. Where a finding builds on one of
them, it says so.

The screenshots this review was written from are kept locally and were not
committed (they are binaries, which this repo does not take). Each finding
below keeps its file:line evidence and a description of what was observed.

### How this was tested (read this before trusting the evidence)

- I signed in the way a brand-new user would, with **Skip for now** (an
  anonymous Supabase user). Onboarding then requires a machine, and creating a
  project needs one too. To get past that I created one scratch project,
  `ux-review-project`, through `ProjectService.CreateProject`. Everything else
  went through the UI.
- That user has no API key and no machine. **Test runs therefore stop at
  "no API keys configured"**. Findings
  about watching and debugging runs come from the code, not from a live run.
  They are marked as such.
- The browser was Playwright Chromium at 1512×900, dark theme, plus a light
  theme pass.
- Cleanup: both test drafts were deleted through the Library's Delete action,
  and the scratch project through `DeleteProject`. The DB shows 0 drafts, 0
  triggers and 0 chats for that user. Two anonymous Supabase users are left
  over: one from this harness and one from an abandoned chrome-devtools
  attempt. Both are empty.

---

## 1. Top 10 issues, ranked by user impact

### 1. "New workflow" silently clones the Agent workflow, keeps its name and description, and creates two drafts

**Evidence**
- `WorkflowBuilderPage.tsx:251-311` creates the draft server-side inside a
  `useEffect`. The `cancelled` flag only guards state updates, not the RPC. One
  click on **New workflow** produced two `workflow_drafts` rows 50 ms apart:
  `high-maple-5a6c` and `smart-seal-7aed`. The double-mount is React StrictMode
  in dev, but any re-run of the effect (for example a `projectId` change) does
  the same in prod.
- `internal/grpc/services/workflow.go:815-835` seeds the draft with the
  embedded `agent.yaml` and replaces only `name:`. `title: Agent` and the
  Agent's full description survive.

**What the user sees**
- They click New workflow and land on a 2-node agent loop named
  `smart-seal-7aed`. Nothing asks what they want to build.
- The Library then shows **three rows called "Agent"** with the same
  description: two "Mine · Draft" and the built-in. Their action menus are all
  labelled "More actions for Agent".
- Deleting one asks `Delete "Agent"? This cannot be undone.` in a native
  `confirm()`, which does not say which Agent.
- The builder header shows the slug (`ux-review-scratch`). The Library, detail
  page, automation dialog ("Runs Agent when…") and activation name
  ("Agent · schedule") all show the title "Agent". The title can only be edited
  in YAML. Workflow info edits only the description.

**Why it hurts.** This is the first thing a new author does, and it produces an
object they cannot identify afterwards. It also teaches the wrong model: that
a workflow is "the agent loop plus stuff".

**Fix**
1. Make creation idempotent. Create the draft on first save, or pass a
   client-generated request id that `CreateWorkflowDraft` dedupes on.
2. Ask for a name in the first step, and write it to both `name` and `title`.
   Clear the description.
3. Make the start a choice, not a clone (see redesign A): **Blank** (start rail
   plus one empty Agent step), **From a template**, or **Describe it to the
   agent**.
4. Add a Title field to Workflow info. Show the title everywhere, with the slug
   as secondary text.
5. Replace the native confirm with an in-app dialog that names the title, slug
   and source.

### 2. Renaming a workflow and then saving throws you out of the editor with an error

**Evidence**
- Repro: rename `ux-review-scratch` → `ux-review-scratch-2`, then **Save
  draft**. The result is the toast `Failed to load workflow "ux-review-scratch"`
  and a redirect to `/workflows/library`. The console shows
  `GetWorkflow … [not_found] workflow not found: ux-review-scratch`.
- The save succeeds under the new slug. The URL keeps the old one, the list
  refresh reloads by the route name, gets a 404, and falls back to the Library
  (`WorkflowBuilderPage.tsx` ~470-530 saves with no navigate; the 404 fallbacks
  are at :385-397).

**Why it hurts**
- It looks like data loss: an error toast, then the user is gone from the
  canvas.
- It happens on the most natural second action after issue 1 ("let me give
  this a real name").

**Fix**
- After a save that changes the slug, `navigate({ to: '/workflow/$workflowName',
  params: { workflowName: response.slug }, replace: true })` before refreshing
  the list.
- Add a test that renames, saves, and asserts the URL and that the canvas is
  still mounted.

### 3. A new step lands disconnected in the middle of the canvas, and connecting it takes a precise mouse drag

**Evidence**
- `WorkflowBuilder.tsx:1409-1454` (`insertStep`) places the node at the
  viewport centre and adds no edge.
- The node ids are `call_llm_1791289869746`, `invoke_tool_1791290154615` and
  `message_post`. These become the CEL path (`nodes.call_llm_1791289869746.…`)
  shown in the Outputs tab.
- Handles measure 11×11 px and are not focusable. Nodes are unlabelled
  `role=group` elements.
- There is no "+" on a node's output, and none on an edge to insert between two
  steps. `CustomEdge.tsx` and the node components have no insert affordance.
- An unconnected node is saved as-is, never runs (the entry is explicit), and
  is only reported after a save (issue 4).

**Why it hurts**
- Every step a user adds needs a second, fiddly gesture before it does
  anything.
- With keyboard or switch access it is impossible: nothing can be connected
  without a pointer drag.
- The timestamp ids leak into every expression that references the step.

**Fix**
- Insert after the selected node, or after the last node on the main path,
  and draw the edge.
- Add n8n's plus-handle on a node's output (`CanvasHandlePlus.vue`) and an
  insert button at the edge midpoint. Both open the step palette, which then
  inserts and wires the step.
- Generate readable ids (`call_llm`, `call_llm_2`, `post_slack_message`), and
  offer a rename that rewrites references.
- Give each node an `aria-label` ("Call LLM, call_llm, not connected"). Add a
  "Connect to…" command on a selected node (in the node menu and as a palette
  action) so connecting has a keyboard path.

### 4. Validation is stale until you save, then arrives as raw text that is not on the canvas

**Evidence**
- With an unconnected Call LLM that has no model, the
  header badge reads **Valid**.
- After saving and reopening it reads **2 errors**.
  The popover lists:
  - `ux-review-scratch.nodes.[1](call_llm_1791289869746).model: call_llm node requires a model …`
  - `… is unreachable (not connected from entry via edges) (add an edge to this node or include it in 'entry') add an edge to this node or include it in 'entry'`

  The remedy is printed twice.
- After the bad nodes were deleted, the badge stayed at "2 errors" until the
  next save.
- Test run dumps all three errors as one paragraph.
- `ValidationStatusBadge.tsx:133-137` makes only *edge* errors clickable.
  Node errors are inert `div`s.
- No step node renders a validation marker. Only the trigger rail does
  (`TriggerRailNode.tsx:195`).
- The only field-level hint seen was a trailing "Required: channel" line at the
  bottom of the Slack panel.

**Why it hurts.** The badge's main job is to tell you the state of what is on
screen, and it says "Valid" about a broken graph. When the errors do arrive,
the user has to map `nodes.[1](id).model` back to a box and a field by hand.

**Fix**
- While dirty, show "Unsaved changes", never a stale verdict.
- Run validation live: debounce the server `ValidateWorkflow` on the in-memory
  YAML at about 800 ms, or validate structure (reachability, required args)
  client-side.
- Map each finding to `{nodeId, field}`, then:
  - put a red dot and count on the node
  - show an inline error under the field (with a red outline)
  - list every finding in a Problems list, where clicking one selects the node,
    opens its panel and focuses the field
- Humanize the text, e.g. "Call LLM (call_llm): choose a model", and print the
  remedy once.

### 5. Referencing data from earlier steps is undiscoverable, and the Outputs tab hides the fields people need

**Evidence**
- In `{}` mode, typing `inputs.`,
  `trigger.` or `nodes.` gives no suggestions. Suggestions appear only after
  typing `{{` (`lib/monaco-cel-completions.ts:613-680`: template mode only
  completes inside `{{ }}`). So `{}` means "template string", not "expression",
  and nothing on screen says so.
- The only click-to-insert picker is for `trigger.*`, in the Workflow start
  panel (`config/TriggerPayloadPanel.tsx`). Nothing lists upstream steps'
  outputs inside a field.
- The Call LLM Outputs tab lists
  `upstream_proxyman_id` (a debug correlation id), `last_stream_seq`,
  `compaction_threshold` and `pending_inbox`. It omits `tool_calls`,
  `response_data` and `message` (`proto/reliant/v1/workflow_v2.proto:1379-1394`;
  `NodeOutputsPanel.tsx` `catalogToOutputFields` drops message and repeated
  types). `tool_calls` is exactly what Execute Tools needs, and
  `response_data` is the structured output.
- The Aa/{} toggle:
  - two icon buttons of 25×19 px, under the 24 px minimum target
  - labelled only by `title`
  - no `aria-pressed`; state is shown only by an `.active` class
  - the word "CEL" in the tooltip

**Why it hurts.** Mapping data between steps is the core skill of any workflow
builder. Here it needs knowledge of the template syntax, the node id, and the
output field names, and the Outputs tab offers the wrong field names.

**Fix**
1. In every field, add an **Insert data** button (and a `{{` shortcut) that
   opens one picker grouped as: this run's inputs; the trigger payload, typed
   from the selected trigger's `payload_schema`; and each *upstream* step's
   outputs, from the graph. Picking an entry inserts
   `{{ nodes.x.response_text }}` and flips the field to expression mode.
2. Fix the outputs list: include message and repeated fields with children,
   and hide internal fields with a `(reliant) = {internal: true}` option.
3. Label the toggle **Fixed / Expression**, with `aria-pressed` and a 24 px
   target. In expression mode, show a one-line hint: "Use `{{ }}` to insert
   values, or pick from Insert data".
4. Later: show the sample value from the last run beside each picker entry, as
   n8n's input panel and `MappingPill.vue` do.

### 6. Three save states and two "valid" states: what can run, and when?

**Evidence**
- The header carries **Valid**, **Draft · 2**, **Save draft** (disabled when
  clean) and **Mark complete**, side by side.
- The detail page: "Drafts cannot run until they are marked complete", with
  **Run…** disabled.
- Builder Test run: "Saves the draft, then runs it".
- A declared trigger: "Save the workflow first: an activation uses the saved
  trigger".
- There is no autosave. A `beforeunload` guard and an exit-confirm modal exist.

**Why it hurts.** A user has to learn four overlapping concepts: unsaved local
edits, a saved draft, a complete (runnable) workflow, and a validation verdict.
Each surface allows a different subset of actions, so "why can't I run this"
has three different answers depending on where you ask.

**Fix**
- Autosave the draft continuously, with a "Saved · just now" indicator.
  `SaveStatusIndicator.tsx` already exists.
- Rename **Mark complete** to **Publish**. It is the one deliberate act, and it
  shows the validation problems (issue 4) that block it.
- Collapse the badges into one status chip: `Draft — 2 problems` /
  `Draft — ready to publish` / `Published` / `Published · unpublished changes`.
- Allow Test run and activation previews against the draft everywhere.
- Allow activation only for a published workflow, and say exactly that.

### 7. One "Add trigger" button, two different trigger models

**Evidence**
- An editable workflow's **Add trigger** opens the trigger palette: Schedule,
  Webhook, When a workflow finishes, and integration events
  It *declares* a trigger in the YAML.
- A built-in (read-only) workflow's **Add trigger** opens **New automation**
  That is an ad hoc, schedule-only automation
  row with its own prompt.
- The two dialogs disagree about machines:
  - New automation: "You have no daemon yet … connect one … then come back"
  - The declared-trigger Activate dialog offers **No machine (server tools
    only)**.
- The workflow detail page's Automations card says "Nothing runs this workflow
  on its own" while the workflow declares a schedule. Declared-but-inactive
  triggers are not shown there, nor on `/workflows/automations`. The brief
  §3a says both should show them with "Activate".
- The Automations page copy is schedule-only: "Runs that start on a schedule…"
  (`AutomationsListPage.tsx:41,93`).
- The run's seed **prompt** exists only on the activation. A declared trigger
  (`proto/reliant/v1/trigger.proto:327-350`) has a filter and input mapping but
  no prompt template. So the workflow author cannot ship "triage
  `{{trigger.payload.issue.title}}`"; every activator must write it.
- "Someone starts a chat" is always listed as a start, with no way to say "this
  workflow is automation-only".

**Why it hurts.** The user asked "could adding triggers be easier?" The answer
today is that it depends on whether the workflow is yours, and two dialogs
with different capabilities and different machine rules look like a bug.

**Fix.** See design question 1: one trigger model on the canvas, with
"declare" and "activate" as two tabs of the same object.

### 8. The "build it with the agent" path is the generic new-chat screen, and it is blocked on a machine it doesn't need

**Evidence**
- The
  editor's chat panel shows the onboarding starters: **Build something new with
  Forge**, **Create a landing page**, **Create a pitch deck**, **Migrate from
  Claude Code**, **Just chat**. Each launches a *different* workflow.
- The composer is disabled with the status **"Starting your machine — This
  usually takes about a minute."** This user has no machine, so nothing is
  starting.
- The workflow tools (`create_workflow`, `edit_workflow`, `write_workflow`,
  `search_integrations`, `get_integration_schema`) are server-side, and
  `NO_MACHINE_CHATS.md` / `DAEMONLESS_RUNS.md` exist for exactly this case.
- Per `WORKFLOW_BUILDER_NORMAL_CHAT.md` the editor chat is deliberately a
  normal chat. That decision is fine. The empty state just has to be about the
  thing on screen.

**Why it hurts**
- For a new user, "describe what you want" is the highest-leverage way in, and
  here it is unavailable.
- What is shown instead is a set of cards that would take them away from the
  workflow they are editing.
- The status message is false.

**Fix**
- When the chat is opened from the builder, show a workflow-scoped empty state.
  Examples:
  - "Describe the automation you want"
  - "Add a Slack message when this finishes"
  - "Explain this workflow"
  - "Write test scenarios"
- Prefill the workflow reference, as the composer already does with
  "Workflow `x`:".
- Start the chat as a no-machine chat when no daemon is attached.
- Never show "Starting your machine" unless a machine is actually starting.

### 9. Config forms aren't shaped for the task: field order, pickers and defaults

These go beyond the placeholder and description work G owns. They are about
field *types*, *order* and *defaults*. G's descriptions make each field
understandable; these make the form usable.

**Evidence**
- Slack Post message: params are alphabetical, giving
  Blocks, Channel, Reply broadcast, Text, Thread ts, Unfurl links. The one
  required field is second, and the field most people want (Text) is fourth,
  behind Blocks (raw JSON).
- Channel is a free-text ID box, though the connection could list channels.
- The panel heading is the raw ref `slack/message.post@1`.
- Invoke Tool's **Tool** is a free-text box, with
  no picker over the tool registry. **Parameters** is an untyped textbox.
- Call LLM:
  - **Model** shows "e.g. flagship…" and is left unset, so the new node is
    immediately invalid.
  - **Thinking level** offers both `None` and `none`.
  - The node has no visible place for the user message.
- The Run form and Activate dialog:
  - Model reads "Select model..." although the input defaults to `flagship`.
  - Tools reads "0 token(s) selected (0/64 concrete tools)" although the
    default is `tag:coding:default`.

  A user cannot tell whether blank means "use the default" or "nothing".
- The declared trigger's "Inputs from the event" lists all 8 inputs, hidden
  ones included (`planning_prompt`, `spawn_presets`), as bare code editors with
  no types or defaults.

**Fix**
- Order fields as: required, then common, then an "Optional" disclosure
  (manifest `x-order` / `x-group`, or required-first as the default).
- Show the integration's display name as the panel title (the node already
  computes it, `ActionNode.tsx` `useIntegrationHeader`).
- Use pickers for reference-shaped fields:
  - tool name: from the registry
  - model: default preselected
  - workflow ref
  - resource ids: dynamic options from the connection, like n8n's
    `ResourceLocator.vue` or Zapier's dynamic dropdowns
- Render an unset input as "Default: flagship", not as empty.
- Hide `ui: hidden` inputs from the mapping list unless "Show advanced" is on.
- Dedupe the enum.

### 10. Keyboard, focus and contrast basics

**Evidence**
- Pressing **Escape** to close the
  validation popover navigated to the Library.
  `hooks/useWorkflowKeyboardShortcuts.ts:96-141` treats "nothing selected" as
  "leave the builder", and it does not know about popovers, menus or the
  palette.
- The `Cmd+I` palette shortcut did not open the palette in headless Chromium;
  the **Add step…** button did. This is minor and may be harness-specific.
- Canvas: handles are not focusable and nodes are unlabelled (issue 3).
- Light theme:
  - `text-muted-foreground` measured 4.42:1 at 12.2 px, just under AA.
  - The `text-2xs` labels ("STARTS WHEN", section headers) are 10.7 px
    uppercase at 4.43:1.
- Library rows have identical accessible names ("More actions for Agent" ×3).
  Each library row repeats the workflow's full description; it is clipped
  visually but read out in full.

**Fix**
- Escape should close the innermost overlay and never navigate. Leaving is the
  Back button, with the existing unsaved-changes guard.
- Bump muted text in light schemes to at least 4.5:1, and use 12 px as the
  floor for labels.
- Give actions unique names: "More actions for Agent (draft, ux-review-scratch)".
- Truncate the description's accessible text, or move it to
  `aria-description`.

---

## 2. The two design questions

### Q1. Should triggers be boxes on the left of the canvas, feeding the start node?

**Recommendation: yes, as a trigger lane of separate cards, one per trigger,
each feeding the entry.** Keep the declare-vs-activate split, and show it
*inside* each card instead of using two different dialogs. Do not make
triggers ordinary graph nodes.

What exists today is a single "Starts when" box (`nodes/TriggerRailNode.tsx`)
listing "Someone starts a chat", the declared triggers, and the ad hoc
automations as lines. It is the right idea, but crammed:

- A trigger cannot be selected as an object.
- There is one generic payload panel for all of them.
- On read-only workflows the same button does something else (issue 7).

**Shape**

```
 ┌ TRIGGERS ───────────────────┐
 │ ┌─────────────────────────┐ │
 │ │ 💬 Chat                 │─┼──┐
 │ │ Anyone can start it     │ │  │
 │ └─────────────────────────┘ │  │     ┌──────────────┐
 │ ┌─────────────────────────┐ │  ├────▶│ first step … │
 │ │ ⟳ GitHub: issue opened  │─┼──┤     └──────────────┘
 │ │ reliant-labs/reliant    │ │  │
 │ │ ● Active · you          │ │  │
 │ └─────────────────────────┘ │  │
 │ ┌─────────────────────────┐ │  │
 │ │ ◷ Weekdays 09:00 ET     │─┼──┘
 │ │ ○ Not active  [Activate]│ │
 │ └─────────────────────────┘ │
 │ ┌ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ┐ │
 │   + Add trigger             │
 │ └ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ┘ │
 └─────────────────────────────┘
```

**The lane**
- Each card is one trigger. It shows:
  - the provider icon (E's logos)
  - the source in words (`describeSchedule`, `describeDeclaredSource`, which
    already exist)
  - **your** activation state as a status dot plus a word: Not active /
    Active / Paused / Failing, as `TriggerRailNode` already computes
  - an inline **Activate** button when inactive
- Each card has its own edge into the entry, so "three ways in, one flow" is
  visible at a glance.
- The lane collapses to today's compact rail when there are more than 4
  triggers, or when zoomed out.

**Selecting a card** opens one panel with two tabs, matching the two owners:

**Definition (the WHEN; part of the workflow)**
- Contents: source, CEL filter, input mapping, and **a prompt template**
  (new; issue 7).
- The prompt template is typed against that trigger's `payload_schema`, and
  the payload picker (issue 5) is scoped to *this* trigger's payload, not a
  generic `trigger.*` list.
- It is editable only by someone who can edit the workflow, and read-only on
  built-ins.

**Activations (the AS WHOM / WHERE; per user)**
- Contents: your rows for this trigger, with project, machine (including **No
  machine**), connection, enabled and notify, plus recent firings with their
  outcomes ("Skipped: filter false", "Launched → run").
- It is always editable, including on built-ins, because it does not change the
  definition.
- The activation's prompt becomes an *override*, prefilled from the template.

**The Chat card is a real, configurable trigger**
- "Anyone can start it from chat" vs "Hidden from chat" maps onto today's
  "Hide from composer".
- That gives automation-only workflows a way to say so. It also answers part of
  the user's second question (see Q2's other reading).

**Built-ins / read-only**
- **Add trigger** opens the *same* palette.
- Because the definition cannot change, the result is an activation with an
  inline source: today's ad hoc row, shown as a card with a "Personal" tag.
- One UI hides the model difference. The user never chooses between "declare"
  and "automation".
- The New-automation dialog becomes this palette plus the Activations tab, so
  it gains webhooks and integrations, and the "no daemon" contradiction goes
  away.

**Empty canvas**
- The lane starts with the Chat card and a dashed "+ Add trigger" card. This is
  n8n's "What triggers this workflow?" without forcing the question first.

**Elsewhere**
- The workflow detail page's Automations card and `/workflows/automations` list
  declared-but-inactive triggers as "Activate" rows (brief §3a).

**Why not real graph nodes (n8n style)**
- n8n's trigger nodes carry credentials and activation inside the graph. That
  couples AS WHOM to WHAT. A built-in or shared workflow then becomes
  unschedulable, and two users cannot activate one definition differently.
  This is `WORKFLOW_UI.md` §10, "Avoid" #2, and `INTEGRATIONS_V1_BRIEF.md` §3a.
- Cards in a lane give n8n's legibility while keeping that split, because the
  card is a *projection* of two sources: the YAML and the per-user rows.

**Trade-offs**
- **Width.** The lane costs about 260 px of horizontal space. Mitigate with
  collapse-to-rail and fit-view awareness (`useFitViewWithPanels` already
  reserves panel widths).
- **One entry for all.** Every trigger feeds the same `entry`. Per-trigger
  entry nodes ("on webhook, skip triage") are a later schema change. When they
  come, the card's edge simply targets a different node, so this layout is
  forward-compatible.
- **A divergent prompt.** Moving the prompt into the declaration means an
  activator's override can drift from it. Show "Overrides the workflow's
  prompt", with a reset.
- **Two colours of state.** A card shows *your* activation state on a
  definition that others may also activate. Label it "you"; never imply global
  state.

### Q2. "What if I don't want some chats to be able to create a workflow?"

**Interpretation I designed for.** "Some agents running in chats should not be
able to create, change, publish or activate workflows and automations."

**The other readings**
- "Triggers shouldn't create chats", i.e. automation runs cluttering the chat
  list. That is already handled by `exclude_automations` and the Runs surface
  (`WORKFLOW_UI.md` §6).
- "Not every workflow should be startable from a chat". That is the Chat card
  toggle in Q1.

**Today**
- `create_workflow`, `edit_workflow` and `write_workflow` are base tier
  (`internal/llm/tools/permissions.go:121-160`: everything not listed is
  `PermissionMutating`).
- The built-in agent has `loadable_tools: ["*"]` and `permission: mutating`
  (`internal/workflow/builtin/agent.yaml:213-231`).
- So **any chat can load them**. `edit_workflow` can rewrite any of the user's
  *complete* workflows, including one with live activations. Activations
  resolve the workflow by name at fire time, so the next unattended run
  silently does something new.
- `activate_trigger` is orchestrator tier, so the default agent cannot activate
  triggers.
- A trigger-launched run can carry untrusted payload text (a GitHub issue body,
  an email). If its workflow can load `edit_workflow`, a prompt injection can
  persist itself into another workflow.
- Per `TOOL_CAPABILITIES.md` §2, declared tool lists are currently steering,
  not enforcement. Whatever is decided here needs the resolver enforcing at
  `execute_tools` to be real.

**Recommendation: a capability with three levels, resolved by the tool
capability resolver from durable inputs, with safe defaults.** Do not use a
per-chat toggle alone.

| Capability | Tools | Interactive chat (`chat.start`) | Unattended / trigger-launched runs |
|---|---|---|---|
| **Read** | `list_workflows`, `get_workflow`, `search_integrations`, `get_integration_schema`, `list_triggers` | Allowed | Allowed |
| **Author drafts** | `create_workflow`, `edit_workflow` / `write_workflow` on *drafts*, scenario tools | Allowed | **Withheld** |
| **Publish** | anything with `complete: true`, and *any* edit to a complete workflow | **Ask** (approval card showing a YAML diff) | **Withheld** |
| **Activate** | `activate_trigger`, enable/disable on activations | **Ask** (approval card naming the project, machine, connection and schedule) | **Withheld** |

Why this split:
- Drafts are inert: they cannot run and cannot be activated. Letting any chat
  author them keeps "the editor chat is a normal chat" true, and keeps it
  useful.
- Publishing changes what runs. Activating creates standing work that acts
  later with nobody watching. Those are the two moments a human should see.

**Where the control lives.** Most restrictive wins:

1. **Launch context (automatic).** `launch_kind != chat.start`, or
   `unattended`, withholds Author, Publish and Activate. This is the security
   default. It needs no setting, and it closes the injection-persistence path.
2. **Project policy (Settings → Project → Agents).** "Agents in this project can
   edit workflows: Never / Drafts only / Drafts, and publish with approval
   (default) / Freely". The same applies to automations. This answers "some
   chats" for the common case: some *projects* (a client repo, a shared team
   project) should not have agents writing automations at all. It is stored
   with the project config, so it is durable.
3. **Workflow / preset declaration.** A workflow author can narrow it with the
   existing `tools:` / `loadable_tools:` (e.g. a "reviewer" preset that never
   gets `workflow:author`). Expose this as a `workflow:*` tool tag, so it is one
   line in YAML and a checkbox in ToolsSelector.
4. **Per chat (composer ⚙ → "This chat can edit workflows").** Choices: Ask /
   Drafts only / Off. It can only **narrow** the project policy, and is stored
   on the chat row next to `no_machine`, so the resolver reads it durably.

**Default (out of the box):** interactive chats author drafts freely, Publish
and Activate need approval, and unattended runs get none of the three.

**Trade-offs**
- **Approval friction.** Approval for Publish adds a click to the
  build-with-the-agent flow. It is the right click: it is the moment the
  workflow becomes live. Allow "Approve for this chat" to batch it.
- **Lock-out.** "Withheld in unattended runs" rules out a deliberate
  self-improving automation. Allow it only via project policy "Freely" plus an
  explicit `workflow:publish` grant in that workflow's declaration. Both are
  visible in review.
- **Implementation dependency.** The per-chat setting adds a column and a
  composer control. Its value comes only after the resolver lands (the
  `TOOL_CAPABILITIES.md` direction), so sequence it after that work.

---

## 3. Quick wins (a day or less each)

1. **Idempotent New workflow.** Dedupe the draft-create RPC, and set `title` to
   the chosen name with an empty description (issue 1).
2. **Rename + save stays on the canvas.** Navigate to the new slug after the
   save (issue 2).
3. **Escape never navigates.** It closes the innermost overlay only (issue 10).
4. **Auto-connect a new step** after the selected node, and use readable ids
   (`call_llm_2`) (issue 3).
5. **The validation badge says "Unsaved changes" while dirty**, prints each
   remedy once, and makes node errors clickable (`ValidationStatusBadge.tsx:133`)
   (issue 4).
6. **Fix the Outputs tab.** Include `tool_calls`, `response_data` and `message`,
   and hide `upstream_proxyman_id`, `last_stream_seq`, `compaction_threshold`
   and `pending_inbox` (issue 5).
7. **The Aa/{} toggle** gets text labels Fixed / Expression, `aria-pressed`, and
   a 24 px target (issue 5).
8. **Show input defaults** in the Run, Activate and declared-trigger forms
   ("Default: flagship", "Default: coding tools") (issue 9).
9. **A workflow-scoped empty state for the builder chat.** Drop the
   Forge / landing-page / pitch-deck starters there, and stop showing
   "Starting your machine" when none is starting (issue 8).
10. **Order params required-first** and title the panel with the integration's
    display name instead of `slack/message.post@1` (issue 9).
11. **Detail page and Automations list show declared-but-inactive triggers**
    with an Activate action. Change the copy to "on a schedule, a webhook or an
    app event" (issue 7).
12. **In-app delete confirmation** naming the title, slug and source, and
    unique `aria-label`s for row action menus (issues 1 and 10).
13. **Dedupe the Thinking level enum** (`None` / `none`). Link "no API keys
    configured" to Settings → Providers from the Run form error.
14. **The palette shows Control Flow twice** (`FloatingWorkflowSidebar.tsx`
    hardcodes Join/Loop/Switch/Router and then `groupPaletteNodes` emits the
    `control_flow` category again, below Git and below the fold at 900 px; the
    DOM has two "CONTROL FLOW" headers).
    This is E's file, so it is flagged for E rather than counted here.
15. **One vocabulary for inputs.** The header button says **Parameters**, the
    panel title says **Workflow Settings**, its tab says **Params (8)**, and the
    detail page, Run form and CEL all say **inputs**. Pick **Inputs**
    everywhere. Also pick **Tests**, not Test Scenarios / Scenarios.

**The vocabulary as it stands.** Settle these once and apply them everywhere:

| Concept | Words seen today | Use |
|---|---|---|
| Definition | workflow, "Agent" (title), slug, name | **Workflow**; show the title, with the slug as secondary |
| Unit on canvas | step ("Add step"), node ("Lock Nodes", "Organize Nodes", "Node reference docs") | **Step** in UI copy; "node" only in YAML/CEL docs |
| Typed inputs | Parameters, Params, Workflow Settings, Inputs | **Inputs** |
| What starts it | trigger, automation, activation, "Declared trigger", "Starts when" | **Trigger** (definition) and **Activation** (yours); "Automations" = the list of your activations |
| Ready to run | Valid, Draft, Mark complete, complete | **Draft** / **Published** |
| An execution | run, test run, chat | **Run**; a **Test run** is a run tagged builder.test |

---

## 4. Bigger redesigns

**A. A creation flow that starts from intent**

```
New workflow
 ┌───────────────────────────────────────────────────────────┐
 │ Name  [ Triage new GitHub issues        ]                 │
 │                                                           │
 │ How do you want to start?                                 │
 │  ( ) Describe it, and the agent drafts it   [textarea]    │
 │  ( ) Start from a template   [Agent ▾] [Best of three …]  │
 │  ( ) Blank: just a trigger and an empty Agent step        │
 └───────────────────────────────────────────────────────────┘
```

- "Describe it" opens the builder with the chat panel already working
  (no-machine) and the first message sent.
- Templates are the built-ins plus a few small automation templates: "Schedule
  → Agent → Slack message" and "GitHub issue opened → Agent triages".
- This replaces the silent Agent clone (issue 1).

**B. The insert/connect model**
- Plus-handles on node outputs.
- Insert-on-edge.
- "Add step" inserts after the selection.
- Auto-layout the inserted branch, with the existing `autoLayoutWorkflow`
  scoped to the new subtree.
- Drag-to-connect stays for power users.
- A keyboard path: select a node, press Enter for the palette, and the choice
  is inserted and wired (issue 3).

**C. Live problems**
- Validation runs on the in-memory definition.
- Findings carry `{nodeId, field}` and render in three places: a node badge,
  the field's inline error, and a Problems drawer in the bottom bar with the
  count in the header chip.
- **Publish** opens the drawer if anything blocks it (issues 4 and 6).

**D. Data mapping with sample data** (n8n's NDV is the model; see
`features/ndv/panel/components/InputPanel.vue`)
- The config panel gets a left **Input** column with the data available at
  this step:
  - the trigger payload
  - inputs
  - upstream outputs, with *values from the last run* (test or real) when there
    is one, and the schema otherwise
- Drag a value into a field, or use the Insert data picker (issue 5).
- **Pin** a step's output so downstream steps can be iterated on without
  re-running upstream LLM calls (`RunDataPinButton.vue`).
- This matters more here than in n8n, because upstream steps are slow and
  cost money.

**E. Run and debug in the builder** (from code; not exercised live, see
"How this was tested")
- The Test run panel exists, paints statuses on the canvas (`useBuilderTestRun`)
  and links "Watch full run".
- What is missing is the debugging loop. `NodeDetailsPanel.tsx` shows the
  *configured* templates and the output JSON, not what the expressions
  **resolved to**, and there is no re-run from a step.
- Add:
  1. A builder-side **Executions** list (this workflow's last N runs, test and
     real), where picking one paints it on the canvas. This is n8n's
     `WorkflowExecutionsSidebar`, and `WORKFLOW_UI.md` §10 "Take" #2.
  2. On a failed run, auto-select the failed step and show its error, its
     **resolved inputs**, and "Fix and re-run from here".
  3. "Run just this step" with pinned or sample input.
  4. Errors carry `{nodeId}` so the toast or alert links to the step.

**F. The save model**
- Autosave the draft.
- One **Publish** that shows a diff and the validation result.
- **Versions** (publish history with "restore"). This also lets Re-run say
  which definition it used (`WORKFLOW_UI.md` §14 Q3).

**G. The trigger lane** (Q1), including the prompt template on declared
triggers and a single palette for built-ins and editable workflows.

**H. The workflow capability policy** (Q2): resolver inputs for launch context,
project policy, declaration and per-chat narrowing, plus Publish and Activate
approval cards with a diff.

**I. Dynamic option pickers for integration params**
- Manifests declare `options_from: { action: slack/conversations.list@1,
  value: id, label: name }`.
- The form renders a searchable picker that calls through the selected
  connection, with an "Enter ID" fallback. This is n8n's resource locator.
- It turns the Slack Channel / Twilio Sid / GitHub repo boxes into choices. It
  builds on G's descriptions and examples.

---

## 5. What's already good, and should be kept

- **The step palette.** Search across built-ins and integrations, category
  facets, a "Changes data" badge, per-row **Connect** badges, and a single
  Steps/Triggers switch.
  It scales to hundreds of integrations, as `INTEGRATIONS_V1_BRIEF.md` §3a
  asked.
- **The schedule editor.** Presets (Every weekday, Every N minutes, Advanced
  cron), a time zone, and a plain-English preview, "Every weekday at 9:00 AM ET".
- **Explanatory copy at the decision points.** "Declaring a trigger doesn't
  fire anything. Activate it to choose the project, machine and connection its
  runs use." and, in the Activate dialog, "Nobody will be watching, so say
  everything the agent needs." Keep this voice.
- **Trigger findings render on the trigger's own line** in the rail. That is
  the pattern issue 4 asks for on steps.
- **Test runs are tagged `builder.test`** and kept out of the chat list. The
  Test run lives beside the canvas and paints statuses onto it.
- **The Outputs tab's copy-path buttons** and typed field list. The mechanism
  is right; only the field selection is wrong (issue 5).
- **CEL completions inside `{{ }}`.** Namespaced, typed, and including
  functions.
- **The insert-target concept** in the Workflow start panel ("Inserts into
  <field>", which survives blur). Generalize it into the Insert data picker
  rather than replacing it.
- **Library as a table** (Name, Source, Automations, Last run, Actions), and a
  detail page with Recent runs, Automations, Definition and Inputs. **Runs**
  with visible filter chips, "Group repeats", and empty states with two clear
  actions. These follow `WORKFLOW_UI.md` closely.
- **Read-only built-ins** with "Create a Copy" and "View YAML", plus an
  unsaved-changes guard on both in-app exit and `beforeunload`.
- **Surface gating.** `web/src/lib/surface.ts` keeps authoring off mobile
  (`workflowAuthoring: false`) while viewing stays. That is the right call for
  a Monaco and drag canvas, so nothing in this review proposes mobile
  authoring.

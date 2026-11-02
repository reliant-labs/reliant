> Copied on 2026-11-01 from `research/app-ux-review-2026-11/05-workflows-automations-integrations.md` (outside any repository) so it is versioned with the synthesis.

# 05 · Workflows, automations, triggers, packs and integrations: UX review

Reviewer area 05 · 2026-11-01 · reliant `origin/main` 0ec4b8119 (`frontends/web`, which is also the Electron renderer).
Review only: no product code was changed.

**Preview:** https://5320-2aab1465-f76b-4d95-b61b-480d9ba070e6.workspaces.reliantapi.com/workflows/library
(port 5320 runs a `vite build` from my own worktree, `ux-review-workflows-cc031b4c`, through harness v2. It uses the shared fixtures plus this area's
`research/app-ux-review-2026-11/05-fixtures/10-workflows.js`, which adds 6 triggers in every health state, runs, unlaunched trigger events, an Inbox
with an activation approval card, user/project workflows, a draft, and a workflow that fails to load. Scenarios: `?fx=wf-empty`,
`?fx=wf-errors`, `?fx=wf-slow`.)

**How this was checked.** Screenshots return no pixels in this harness (bug 3f6a8b8e). Every visual claim below was **measured, not seen**:
from DOM text, `getBoundingClientRect`, computed styles and contrast ratios computed in-page, and from reading the code. Measurements were
taken at 1280×800, 1024×800 and 800×900 (desktop) and at 390×844 (mobile, touch). Brand tokens and the global shell are another reviewer's
area, so I flag only style issues specific to these surfaces.

**Rechecked after fixture fix (2026-11-01).** The harness validator flagged two schema errors in `05-fixtures/`. Both are fixed, and `await __fxValidate()` now returns `[]`:
- `ValidationError.severity` was not a real field. It is replaced by the real `type` (`validation_error`, plus one `warning:style` finding), together with `suggestion`, `edgeIndex`, `edgeFrom` and `caseTo`.
- `APPROVAL_TYPE_TOOL` was not a real enum value. It is now `APPROVAL_TYPE_WORKFLOW_STEP`, the only non-zero value.

No finding changed:
- **F4 still stands.** The Inbox approval row still shows the activation card as one truncated line (384px visible of 8080px), with an unlabelled-subject "Approve" beside it and no expansion. The Inbox does not branch on `approvalType`.
- **F5, F6 and B2 still stand.** The card text, including `(id )`, comes from the server-built title, which the fix does not touch.
- **F7 still stands.** `ApprovalActions` styling and accessible names do not depend on the approval type.
- **The draft row still reads "DRAFT · 2".** Only the two error findings are counted; the new `warning:` finding is correctly excluded by `splitFindings`. No other finding uses validation errors.

Harness limits that affect the findings:
- `CatalogService/SearchCatalog` is not faked, so event names fall back to raw ids such as `github: pull_request.opened`. Real catalog data
  would show friendly names. The fallback is still what users see whenever the catalog search fails (see F18).
- The run-detail transcript (`/workflows/runs/$id`) needs the chat stream stack. With my fixtures it rendered "This run doesn't exist", so the
  chat `PermissionsPanel` was reviewed from code only.
- In this DevTools MCP, the first `evaluate_script` after a `navigate_page` sometimes reads the *previous* document. That is a tool quirk, not
  an app redirect, and I re-checked every such reading.

---

## 1. Inventory

| Surface | Route / entry | Main components |
|---|---|---|
| Workflows area shell (sidebar: Workflows · Runs, project scope, Esc exits) | `/workflows/*` | `components/workflows/WorkflowsShell.tsx`, `workflowsAreaRoutes.tsx` |
| Workflow list ("library"): one table that includes trigger state, "Runs by itself" summary, Coming up timeline, filter chips, Building blocks, Failed to load | `/workflows/library` | `workflows/list/WorkflowsListPage.tsx`, `WorkflowRow.tsx`, `workflowsView.ts`, `ComingUpTimeline.tsx`, `FilterMenu.tsx`, `RowMenu.tsx` |
| Built-ins (22 total): chat-launched (Agent, Get It Right, Best of Three, Build an App, Landing Page, Pitch Deck, Move to Forge, Import…, Create a Workflow), the **Autopilot pack** (PR review, security review, review responder, issue worker, on-call), and hidden building blocks (lease acquire/release, labels, restricted agent loop, failure note) | rows in the list | `internal/workflow/builtin/*.yaml` |
| Workflow detail: Triggers card (grouped by project, per-trigger switch, Fire now, Settings/Edit/Remove, history, configuration history with approval provenance), Recent runs, Definition (read-only diagram and inputs), About, Presets, Used by | `/workflows/library/$ref` | `workflows/detail/WorkflowDetailPage.tsx`, `TriggersCard.tsx`, `WorkflowPresetsSection.tsx`, `workflowUsedBy.ts` |
| Automation dialog: declared triggers (Turn on / Finish setup / Settings) or an inline trigger (schedule, app event, webhook, workflow-run-ended), "Describe the automation → Draft it" | `openAutomationDialog()` from the list, detail, runs, inbox and builder | `Automations/dialog/AutomationDialog.tsx`, `DeclaredAutomationForm.tsx`, `InlineAutomationForm.tsx`, `WhenField.tsx`, `ScheduleSentence.tsx`, `WhereSummary.tsx`, `WebhookCredentialPanel.tsx`, `TestEventPreview.tsx`, `DescribeIt.tsx`, `ActingIdentityField.tsx`, `ErrorSummary.tsx`, `setup/SetupSuccess.tsx` |
| Integration connection | inside the dialog and builder only | `workflow/connections/ConnectionPicker.tsx`, `ConnectIntegrationDialog.tsx` |
| Health vocabulary and CTAs (Needs setup, Broken, N failed in a row, Couldn't start, Off) | everywhere a trigger appears | `lib/automationHealth.ts`, `Automations/AutomationCta.tsx`, `AutomationStateNotice.tsx`, `workflows/automationStatus.tsx` |
| Runs: Needs you / Live / Finished, plus trigger events that started no run; filters; run detail | `/workflows/runs`, `/workflows/runs/$id` | `runs/RunsPage.tsx`, `RunList.tsx`, `RunRow.tsx`, `RunFilters.tsx`, `RunDetailPage.tsx`, `RunHeader.tsx`, `TriggerCard.tsx`, `lib/runStatus.ts` |
| Inbox: Approvals, Questions, Waiting for machine, Failing automations, Needs setup, Broken definitions, Storage, Recovery | `/inbox`, `/m/inbox` | `inbox/InboxPage.tsx`, `InboxItem.tsx`, `Chat/ApprovalActions.tsx` |
| Chat approval bar (approve every pending approval of the chat) | inside a chat | `Chat/PermissionsPanel.tsx` |
| Agent-made trigger activations: approval card text and refusals | `create_trigger_activation` / `update_trigger_activation` tools | `internal/handlers/trigger/activation_agent.go`, `activation_snapshot.go`, `activation_disclosure.go`, `internal/llm/tools/activation_tools.go` |
| Settings → Automations (when an agent's change needs a card) | `/settings/automations` | `Settings/AutomationSettings.tsx` |
| Builder: canvas, trigger rail, YAML modal, builder chat, New workflow dialog | `/workflow/$name`, `/workflow/new` | `workflow/WorkflowBuilderPage.tsx`, `WorkflowBuilder.tsx`, `nodes/TriggerRailNode.tsx`, `YamlEditorModal.tsx`, `WorkflowEditorChatPanel.tsx`, `BuilderChatEmptyState.tsx`, `NewWorkflowDialog.tsx` |
| Mobile | `/m/workflows`, `/m/runs`, `/m/inbox` | `Mobile/MobileWorkflowCatalog.tsx`, `MobileRunsScreen.tsx`, `MobileInboxScreen.tsx` |

What is already good, and should be kept:
- One list instead of a separate Automations list.
- Rows that need you are pinned on top with a reason in words.
- Health is shown as a dot plus words, with the server's reason in a tooltip and in screen-reader text.
- Accessible names carry a row qualifier ("Run Agent (built-in, agent)").
- Enabling is all-or-nothing, and a refusal says which trigger caused it (`enableWorkflow.ts`).
- The error summary takes focus and links to the field at fault.
- Configuration history records how each change was approved (`approvalLabel`).
- The empty Runs page offers "Run a workflow" and "Make one run by itself".
- `?fx=wf-errors` gives a clear error card with Try again.
- Dark-mode contrast is good: muted text measured 7.9:1, warning 9.4:1, danger 7.5:1 on `#121212`.

---

## 2. Findings

### Discoverability: finding and turning on a useful automation

**F1. The Autopilot pack is buried and described for its authors, not for users. (H)**
`/workflows/library` with the real built-ins is one alphabetical table of 18 rows. The five Autopilot automations sit between "Agent" and
"Create a Workflow". Nothing says they form a pack, what they need (a GitHub connection, a repository) or what you get from them.

The descriptions are developer documentation. The page subtitle for PR review is a 120-word paragraph that mentions "node `decide`". The
mobile catalog shows on-call's description, which runs to several hundred words about Alertmanager payload v4. Agent's description is
"Standard interactive agent. Loops while LLM returns tool calls…".

The only entry points are a one-line "Off · github: issues.labeled" cell and a small outline "Set up…" button at the end of the row. A new
user who has never opened the builder has no cue that "Set up…" is the recommended action.
(`workflows/list/workflowsView.ts` sections; `WorkflowsListPage.tsx`; `WorkflowRow.tsx:StartsOnCell/ActionsCell`; the builtin YAML `description:` fields.)

**F2. Three ways to create, none of which mentions the others. (M)**
- "New workflow" (`/workflow/new`) offers only Blank or "Agent loop template".
- "Create a Workflow" is a built-in you *run* from a chat.
- The builder has "Start from chat", and the automation dialog has "Describe the automation → Draft it".

The New workflow dialog never offers "Describe it and let an agent draft it". (`workflow/NewWorkflowDialog.tsx`.)

**F3. The automation dialog's "Do → Workflow" picker lists internal building blocks. (M)**
Starting from Runs → "Make one run by itself", the select lists "Autopilot · take a work-item lease", "release a work-item lease",
"ensure labels exist", "add and remove labels", "restricted agent loop", "failure note", "Scope Conversation" and "Structured Agent". These
are hidden steps that the list page deliberately folds into "Building blocks". (`Automations/dialog/InlineAutomationForm.tsx` workflow options.)

### Approvals and permission disclosure

**F4. The Inbox shows an activation approval as one truncated line, then approves in one click. (H)**
The approval's whole disclosure is `InboxApproval.title`. For an agent-made trigger activation that is a 13-line card. The row renders it in a
single `truncate` span: measured 384px visible of 8080px of text. Only "Approval: widens power: new connection github (acme), new integr…" is
readable, and the rest is a native `title` tooltip, which keyboard and touch users cannot open. `hasExpansion()` returns true only for
questions, so an approval has no way to open, and green **Approve** sits beside it.
This is the main surface where a user grants an automation new power, and it hides what is being granted.
(`inbox/InboxItem.tsx:rowDetail` "approval" case, `hasExpansion`, `ApprovalRowAction`.)

**F5. The activation approval card is a raw key/value dump. (H)**
`activation_agent.go:328` builds the card with `fmt.Sprintf("Approval: %s\nMachine: %s (id %s)\nOperation: %s\nRun as user: %s; …\nActivation: %s\n%s\n%s", …)`.
A user reads:
- `Operation: create_trigger_activation`
- `Run as user: 3f0c1a52-…` (their own UUID)
- `Activation: {"name":…,"no_machine":true,"acting_identity":"app","params":{"repos":"<1 value>"}}` (raw JSON)
- `Executable node kind call_llm: 2`
- `Workflow dependency: 9 nodes; complete definition and its literal privileges fingerprinted (literals withheld)`
- `2 workflow definitions; 1 resolved presets; 4 effective inputs…`

For a no-machine activation it prints `Machine: No machine (server-only) (id )`: the id is empty (see Broken, B2).

The chat renders this card as `<pre>` (`Chat/PermissionsPanel.tsx:64`) and the Inbox truncates it (F4). The facts a person needs are present
but buried:
- which repositories;
- that it writes to GitHub as Reliant bot;
- which tools it can use (read PR, post review);
- that targets are event-chosen;
- that later workflow edits stay live.

**F6. The chat's approval bar is batch-only, "Approve All", and mixes power-widening activations with routine tool calls. (H)**
`PermissionsPanel.handleApprove` calls `batchApprove` with every pending approval id in the chat. An activation that adds a GitHub connection
is approved by the same click as "Run command: gh auth status". The shortcut hint `⌘+↵` makes a reflexive approve one keystroke.
(`Chat/PermissionsPanel.tsx`, `Chat/ApprovalActions.tsx`.)

**F7. Approve and Deny are both saturated fills, and Approve is green. (M, area-specific style)**
`ApprovalActions` paints Approve `bg-success` and Deny `bg-destructive`. Green is reserved for success. A filled red Deny beside a filled green
Approve on every Inbox row is the loudest pattern on the page, for an action that should prompt reading rather than reflex.

Accessibility problems on the same buttons:
- Every Inbox row's buttons are named just "Approve" / "Deny", with no subject, unlike `WorkflowRow`'s qualified names.
- The tooltip repeats the label.

(`Chat/ApprovalActions.tsx`, `inbox/InboxItem.tsx:ApprovalRowAction`.)

**F8. The human setup form discloses less than the agent card does. (M)**
"Turn on Autopilot · security review" asks for a connection, an identity, repositories and a prompt. It never says what the workflow will
*do* with them: read-only or not, which tools, whether it can push. An agent-made activation, by contrast, lists its tool privileges.
(`Automations/dialog/DeclaredAutomationForm.tsx` "When" section.)

**F9. "GitHub identity" is a required-looking numbered section on every declared automation, even one with no GitHub trigger. (M)**
The section renders unconditionally ("Once, whatever starts it", line 690), and it is placed before "What it watches" (the repositories). The
order puts "whose account writes" ahead of "what it watches", which is the question people actually decide first.
(`DeclaredAutomationForm.tsx:693`.)

### Refusals (the trigger-activation fix #1996)

**F10. Refusal reasons read like assertions, keep an internal error code, and the post-approval failure drops the reason. (M)**
Before approval, the agent and the user see messages such as:
- "workflow dependencies could not be resolved for approval (failed_precondition): Child workflow/preset dependencies must resolve before activation approval"
- "Tool privileges depend on runtime/event values; activation approval requires fixed privilege choices"
- "Router workflow candidates must enumerate preset choices for activation approval"

None of them says which node or input caused the refusal, or what to change.

After approval, any `ManageActivation` error becomes the generic "Activation change refused or could not be completed. Inspect its current
state and setup in the app…" (`activation_tools.go:235`), so the specific reason is lost exactly when the user has already said yes.

#1996 removed one false refusal (child args from `nodes.*`). The wording of the remaining ones is unchanged.
(`internal/handlers/trigger/activation_snapshot.go` `refusedPrecondition(...)` calls; `activation_agent.go:235,245,268`.)

**F11. The form's refusal path is good, with one gap. (L)**
`refusalHeadline` ("Nothing was turned on, because "pr-opened" could not be.") and `refusalItems` map the server's sentence to a field by
regex. When nothing matches, the item has no field link and the raw server sentence stands alone. (`DeclaredAutomationForm.tsx:refusalItems`.)

### Run status clarity

**F12. A trigger whose workflow failed to parse is shown as "Workflow not found", and the same workflow appears twice. (M)**
A broken `.reliant/workflows/triage.yaml` appears once under "Failed to load" ("Triage", with the parse errors). Its trigger appears as a
separate pinned row titled "Project://triage" (the scheme capitalised) with a "Workflow not found" badge.

The cause: `workflowsView` builds orphan rows from triggers no *valid* workflow claimed, and never checks the invalid list. The user is told
the file is gone when it is actually there with a typo. (`workflows/list/workflowsView.ts:211-221`; `lib/workflowDisplayName.ts` on a `project://` ref.)

**F13. Health is repeated, and "On" disagrees with health. (L)**
- On the detail page, "Healthy" appears on the project row and again on every trigger row.
- On the list, the "On" switch shows *checked* for an on-call trigger whose health is "Needs setup", which is not running.
- "Off" appears both in Health and as the switch state.

(`TriggersCard.tsx:ProjectGroupRow/TriggerRow`; `WorkflowRow.tsx:OnCell`.)

**F14. Two ages for one item in the Inbox. (L)**
An item waiting 41 hours shows "1d" in the row and "waiting since 2 days ago" as its accessible name and tooltip: `compactAge` floors and
`formatRelativeTime` rounds. (`inbox/InboxItem.tsx:142-143`.)

### Density, vocabulary and layout

**F15. The Workflows table loses the Name column completely below about 1060px viewport (desktop, sidebar open). (H, broken)**
The `layout="fixed"` table gives six columns fixed widths totalling 767px (Runs by itself 12rem, Health 8.5rem, Last run 9.5rem, Next run
6.5rem, On 4rem, Actions 10.5rem). Name takes "the remainder".

Measured Name column width:

| Viewport | Name column |
|---|---|
| 1280 | 228px |
| 1024 | **0px** |
| 800 | **0px** (the wrapper scrolls horizontally, and every workflow name has `clientWidth` 0) |

DataTable's `stacked` mode only applies below `md`, and the shell only switches to the mobile drawer at its narrow breakpoint, so this whole
band is broken. That includes a 13" laptop with the Electron window at half-screen.
(`workflows/list/WorkflowRow.tsx:workflowsColumns`; `forge-ui/data_table.tsx`.)

**F16. Vocabulary drifts between surfaces. (M)**

| Surface | Words used |
|---|---|
| Builder trigger rail | "Active · you" / "Not active" / **Activate** |
| List, dialog, toast | On / Off / **Set up…** / **Turn on** / "is on in shopfront" |
| Code and docs | Enable / activation |
| Badges and filters | badge "Mine" vs chip "Yours" vs About "Yours" vs section "Your workflows" |

The heading "Workflows" appears four times on one screen: sidebar brand, nav item, breadcrumb "Workflows › Workflows", and h1.
(`workflow/nodes/TriggerRailNode.tsx`, `WorkflowSourceBadge.tsx`, `WorkflowsShell.tsx:WorkflowsHeader`.)

**F17. The builder canvas is an author's view. (M)**
- Node headers show raw types: `GITHUB/PR.REVIEW.CREATE@1`, `SAVE MESSAGE`.
- Agent nodes print CEL inline, for example `model: {{ inputs.review_model }}` and a 200-character `labels: {{ [{'name': inputs.skip_label, 'color': 'bfd4f2', …}] }}`.
- Every router renders an extra "SWITCH · Switch" node.
- The header is "WORKFLOW BUILDER · STEPS 20 · EDGES 19 · View Only".

The detail page's read-only diagram reuses this. That is fine for authors, but it is the only "what does this do" view a non-author gets.
(`workflow/nodes/*`, `WorkflowViewerPanel.tsx`.)

**F18. The raw event-id fallback is user-visible. (L)**
When `SearchCatalog` has not answered or fails, `RAW_EVENT_NAMING` renders "github: pull_request.opened, pull_request.ready_for_review +2".
This shows in the list, the detail page, the dialog's "Set by the workflow: …" and the builder rail.
(`hooks/catalog-queries.ts:useIntegrationEventNaming`.)

**F19. Setup-issue capitalisation. (L)**
`sentenceCase()` turns the server message "default_repo is now required" into "Default_repo is now required". (`inbox/InboxItem.tsx:105,416`.)

### Integrations and credentials

**F20. There is no place to see, rename, test, set default for, or revoke integration connections. (H)**
`connection.proto` has `ListConnections`, `RenameConnection`, `SetDefaultConnection`, `TestConnection`, `DeleteConnection` and
`ListConnectionEvents`. The UI uses them only inside `ConnectionPicker` (choose, or "Connect…"). Settings has "GitHub" (git connections)
and "Access tokens", but no Slack/Gmail/Twilio/integration section. No component calls `DeleteConnection`.

A user who connected Slack through a builder dialog cannot find that connection again, see which automations use it, or revoke it.
(`Settings/SettingsNavigation.tsx`; `workflow/connections/*`; `hooks/connection-queries.ts`.)

**F21. The connection fallback copy is lowercase and ambiguous. (L)**
"Couldn't look up how github connects. Your default github connection is used; if you have none, turning it on will say so." It uses the raw
id and pushes discovery to submit time. (`DeclaredAutomationForm.tsx:IntegrationConnection`.)

### YAML editor, builder chat and empty states

**F22. YAML modal accessibility and structure. (L)**
- The Copy button is icon-only with no `aria-label` or `title` (measured: accessible name empty).
- The View/Edit toggle buttons have no `aria-pressed` or `type`.
- A read-only workflow shows a one-option "View" segmented control.
- Edit mode is an unlabelled plain `<textarea>`, with no highlighting and no inline validation until save.
- The toggle uses `bg-muted` structurally, which the `web-styling` rule forbids.

(`workflow/YamlEditorModal.tsx:107-184`.)

**F23. Builder chat empty state: good, but small. (L)**
The four suggestion chips fill the composer, which is good. The heading is "Build this workflow with the agent", which is fine. The chips are
`text-xs` at 12.2px on a 15px root, which is under the 14px body floor for primary actions. (`workflow/BuilderChatEmptyState.tsx`.)

**F24. Loading, empty and error states are consistent and real. (L, positive)**
- Skeletons respect `motion-reduce`.
- Errors show the server message plus Try again.
- The detail page's empty Triggers card states "Off. X runs only when someone starts it. Add a trigger…".

The one gap: when nothing runs by itself, the list has no "what you could turn on" prompt (see F1).

### Mobile

**F25. The mobile catalog has no automation state and an unlabelled badge. (L)**
- `/m/workflows` lists workflows with a trailing blue pill holding a bare number. That number is the step count, with no label for sight or
  screen reader, and it uses brand blue as decoration.
- It shows no On/Off or "needs you" state.
- Descriptions truncate to one line, which hides the over-long built-in descriptions; that is good on mobile.

Automation editing is desktop-only by design, and the Inbox says "Fix on desktop", which is honest.
(`Mobile/MobileWorkflowCatalog.tsx:186-190`.)

---

## 3. Ranked recommendations

| # | Change | Impact | Effort | Files | Concrete change |
|---|---|---|---|---|---|
| 1 | Fix the vanishing Name column (F15) | H | S | `workflows/list/WorkflowRow.tsx`, `WorkflowsListPage.tsx`, `forge-ui/data_table.tsx` (forge first if a primitive change is needed) | Give Name a floor (`minWidth: "16rem"` in the column model, or a `min-w-64` cell class honoured by `table-fixed` via a `<colgroup>` width). Below a container width of ~1100px, drop the Next run column into the Last run cell's second line and Health into the Name subline, using a container query (`@container`) on the table wrapper. Add a test that asserts Name width > 0 at 1024px. |
| 2 | Show what an approval grants before Approve is possible (F4) | H | M | `inbox/InboxItem.tsx`, `proto/reliant/v1/inbox.proto`, `approval.proto` | Make approvals expandable (`hasExpansion` true for `approval` when the title has more than one line). For activation approvals (`ApprovalType` activation, or a new `InboxApproval.kind`), render the expansion open by default and put Approve inside it, under the disclosure. Never offer Approve beside a truncated title. |
| 3 | Rewrite the activation card into plain language with structure (F5) | H | M | `internal/handlers/trigger/activation_agent.go` (title), `activation_disclosure.go`, `approval.proto` (add `repeated DisclosureLine disclosure {label, value, widens}` alongside the hashed title), new `Chat/ActivationApprovalCard.tsx` used by `PermissionsPanel` and the Inbox | Lead with one sentence: "Turn on **Autopilot · PR review** in **shopfront**. It reviews pull requests in acme/shopfront and posts reviews as **Reliant bot**." Then use labelled rows: Why approval is needed (the widening list); Connection (GitHub, account acme); Writes as; Can use (tools); Runs on ("No machine"); Targets ("chosen by each event"). Mark widening rows. Put UUIDs, node counts and the JSON behind "Technical details". Keep the hash over the full text. Fix `(id )`. |
| 4 | No batch approve for power-widening approvals (F6) | H | S | `Chat/PermissionsPanel.tsx`, `Chat/ApprovalActions.tsx` | When any pending approval is an activation (or any approval marked `widens`), list approvals individually, each with its own Approve/Deny, and keep "Approve all" only for routine tool calls. Drop the `⌘+↵` hint on activation cards. |
| 5 | Add Settings → Integrations (connections) (F20) | H | M | `Settings/SettingsNavigation.tsx`, new `Settings/IntegrationsSettings.tsx`, `hooks/connection-queries.ts` | List connections by integration: name, account, status, default star, last used (`ListConnectionEvents`), and "Used by N automations" (from `ListTriggers` `connectionId`). Actions: Rename, Make default, Test, Remove (confirm, naming the automations that will lose it). Link "Change" in `ConnectionPicker` here. |
| 6 | Surface the Autopilot pack as ready-made automations, with user copy (F1, F24) | H | M | `workflows/list/workflowsView.ts` (new `automations` section), `WorkflowsListPage.tsx`, `internal/workflow/builtin/autopilot-*.yaml` + `WorkflowListItem` (new `summary` field), `WorkflowDetailPage.tsx` subtitle | When fewer than all Autopilot automations are on in scope, show a compact "Ready-made automations" strip above the table: one card per Autopilot workflow with a one-line `summary` ("Reviews every non-draft PR and posts one review. Never approves or merges."), chips for "Needs GitHub" and "No machine", and a primary "Turn on…" (the existing `openAutomationDialog({mode:"setup"})`). Use `summary` everywhere a one-liner is shown (list subline, mobile, picker). Collapse `description` into an "About" disclosure on the detail page. |
| 7 | Show refusals as what, why and how to fix (F10, F11) | M | S | `internal/handlers/trigger/activation_snapshot.go`, `activation_agent.go`, `internal/llm/tools/activation_tools.go:235` | Rewrite each `refusedPrecondition` reason to name the node or input and the fix, e.g. "Step `review` picks its preset from `nodes.lane.model`, which only exists during a run. Set that preset to a fixed value, or move the choice into an input." Drop the `(failed_precondition)` code from user text, since the code stays on the error. In `Execute`, pass through a classified `ActivationRefusal.Reason` instead of the generic sentence. |
| 8 | Recognise orphans of workflows that failed to load (F12) | M | S | `workflows/list/workflowsView.ts`, `lib/workflowDisplayName.ts`, `WorkflowDetailPage.tsx` | Before making an orphan row, match the trigger's ref against `invalid` (by slug and source). If it matches, merge the trigger into the Failed to load entry ("1 trigger is refusing events") with Remove/Pause, rather than a "Workflow not found" row. Never capitalise a ref scheme: display `triage` and not `Project://triage`. |
| 9 | Use one switch vocabulary (F16) | M | S | `workflow/nodes/TriggerRailNode.tsx`, `workflow/TriggerRailContext.tsx`, `workflows/WorkflowSourceBadge.tsx`, `WorkflowsShell.tsx` | Use On / Off / "Turn on…" on the builder rail, replacing Active / Not active / Activate. Use "Yours" on the badge to match the chip. Hide the breadcrumb's second crumb when it equals the area name. |
| 10 | Show GitHub identity only when it matters, after scope (F9) | M | S | `Automations/dialog/DeclaredAutomationForm.tsx` | Render the identity section when `plan.integrations` includes `github` or the workflow's tools write to GitHub (`listing.needsMachine` / catalog tool tags). Place it after "What it watches". |
| 11 | Say what a human-enabled automation can do (F8) | M | M | `DeclaredAutomationForm.tsx`, `TriggersCard.tsx:TriggerDefinition` | Reuse the structured disclosure from #3 as a "What it can do" section in Turn on and Settings (read-only or not, tools, where it writes), and on the trigger's expanded row. |
| 12 | Hide building blocks from the automation picker (F3) | M | S | `Automations/dialog/InlineAutomationForm.tsx` | Filter the workflow options with the same rule as `workflowsView` building blocks (`builtin && isHidden`), unless the user already has a trigger on that workflow. |
| 13 | Calm the approval buttons and name them (F7) | M | S | `Chat/ApprovalActions.tsx`, `inbox/InboxItem.tsx` | Approve uses `primary` (the brand fill), Deny uses `outline`/`ghost` with danger text. Add `aria-label={`Approve $\{subjectName(item)\}`}` and the matching Deny label. Remove the label-echo tooltip. |
| 14 | Make the canvas readable for non-authors (F17) | M | M | `workflow/nodes/*`, `WorkflowViewerPanel.tsx` | Use catalog display names for action nodes ("Post PR review", not `GITHUB/PR.REVIEW.CREATE@1`). Collapse CEL input previews to "5 inputs" in read-only/compact mode. Draw router branches as edge labels instead of separate Switch nodes. |
| 15 | YAML modal accessibility (F22) | L | S | `workflow/YamlEditorModal.tsx` | Copy button: `aria-label="Copy YAML"` plus a live "Copied" announcement. Toggle: `role="radiogroup"` or `aria-pressed`, and hide it when read-only. Replace `bg-muted` with `bg-background border border-border/60`. Label the textarea and validate on blur. |
| 16 | Prettify the raw event fallback (F18, F21) | L | S | `hooks/catalog-queries.ts:RAW_EVENT_NAMING`, `DeclaredAutomationForm.tsx` | Turn `pull_request.opened` into "Pull request opened" and `github` into "GitHub" (via `integrationDisplayName`) even without the catalog. |
| 17 | Inbox details (F14, F19) | L | S | `inbox/InboxItem.tsx` | Derive the visible age and the accessible age from the same rounding. In `sentenceCase`, leave the first token alone when it contains `_` or is a code identifier. |
| 18 | One status per trigger row (F13) | L | S | `TriggersCard.tsx`, `WorkflowRow.tsx:OnCell` | Omit trigger-row health when it equals the group's. For Needs setup or Broken, show the switch as "On · not running" (or disabled with the reason). |
| 19 | Mobile catalog badge (F25) | L | S | `Mobile/MobileWorkflowCatalog.tsx` | Drop the step-count pill, or label it ("20 steps", `text-muted-foreground`). Add an "On" or "Needs you" chip from `useTriggers`. |
| 20 | Offer "Describe it" in New workflow (F2) | L | S | `workflow/NewWorkflowDialog.tsx` | Add a third start, "Describe it", which opens the builder with the chat panel focused on the user's text (the existing `BuilderChatEmptyState` path). |

---

## 4. Broken

| # | What | Where | Evidence |
|---|---|---|---|
| B1 | Workflow names invisible: the Name column is 0px wide at ≤~1060px desktop viewports | `workflows/list/WorkflowRow.tsx:workflowsColumns` | Measured: Name column header width 0 at 1024×800 and 800×900; each name link `clientWidth` 0 (`scrollWidth` 39–168). 228px at 1280. |
| B2 | Activation approval card prints `Machine: No machine (server-only) (id )` | `internal/handlers/trigger/activation_agent.go:328` | The format string always appends `(id %s)` with `row.DaemonID`, which is empty when `NoMachine`. |
| B3 | A trigger of a workflow that fails to parse is labelled "Workflow not found", titled `Project://triage`, and listed twice | `workflows/list/workflowsView.ts:211-221` | Orphans ignore `invalid`. Reproduced with fixture `triage` (invalid) plus trigger `trg-triage-06`. |
| B4 | The automation dialog's workflow picker offers hidden building blocks (lease acquire/release, labels, restricted agent loop…) | `Automations/dialog/InlineAutomationForm.tsx` | Runs → "Make one run by itself" → Do → Workflow lists all 22 built-ins. |
| B5 | Inbox age mismatch: "1d" shown vs "2 days ago" announced, for the same 41h item | `inbox/InboxItem.tsx:142-143` | Measured in the Inbox fixture `ib-4`. |
| B6 | "Default_repo is now required" (identifier capitalised) | `inbox/InboxItem.tsx:sentenceCase` | Inbox "Automations that need setup" row. |
| B7 | The YAML modal's Copy button has no accessible name | `workflow/YamlEditorModal.tsx:138-148` | Accessible-name check returned "" for the button. |
| B8 | After approval, every activation `Execute` failure returns the same generic sentence, so the reason is lost | `internal/llm/tools/activation_tools.go:235` | Code path: any `ManageActivation` error other than the two special-cased ones. |

None of these was seen as pixels. They were measured or read from code as stated.

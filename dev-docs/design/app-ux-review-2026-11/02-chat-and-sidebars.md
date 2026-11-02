> Copied on 2026-11-01 from `research/app-ux-review-2026-11/02-chat-and-sidebars.md` (outside any repository) so it is versioned with the synthesis.

# 02 — Chat, agent runs, threads and sidebars

Reviewer 02 of the 2026-11 app UX review. Review only: no product code changed.

- **Build reviewed:** reliant `origin/main` 0ec4b8119 (the harness build), `frontends/web`.
  Code references are to that commit (my worktree
  `~/.reliant/worktrees/reliant-labs/ux-review-chat-sidebars-cd87add1/reliant`, reset to 0ec4b8119).
- **Preview (left running):** https://5317-2aab1465-f76b-4d95-b61b-480d9ba070e6.workspaces.reliantapi.com/
  (local `http://127.0.0.1:5317/`). Scenarios: `?fx=ask` (a blocking ask_user on the open chat),
  `?fx=nochats`, `?fx=errors`, `?fx=clear`. Open the chat **"Refactor cart totals and fix
  multi-quantity discounts"** for the long agent run.
- **Fixtures:** `research/app-ux-review-2026-11/fixtures-chat-sidebars/20-chat-sidebars.js`: two
  projects, three workspaces (one with a long mixed-case branch), running / needs-input / failed /
  unread / scheduled / untitled / archived chats, and a 6-message agent run with grouped reads, edit
  diffs, a failed shell call, a 120-line shell output, a markdown table and code block, a running
  spawn plus a running shell, and (with `?fx=ask`) a blocking ask_user.
- **How it was judged:** `take_screenshot` returns no pixels in this harness (bug 3f6a8b8e), and
  saving with `filePath` into `research/.../shots/` is refused by the DevTools MCP ("not within any
  of the configured workspace roots"), so **there are no screenshots**. Every finding below is
  **measured, not seen**: DOM, accessibility tree, computed styles, WCAG contrast computed in-page
  (alpha and opacity composited onto the real background), element geometry at 1905x2053,
  1440x900 and 390x844, and the code. Mobile was checked by resizing a desktop browser; the
  `touch:` variants (44px targets) key on a coarse pointer, which resizing does not emulate, so the
  mobile target sizes I measured (23–26px) are what a desktop-width mouse user sees, not a phone.

## Contents

1. Inventory of surfaces
2. Findings by surface
3. Broken (fix regardless of design direction)
4. Ranked recommendations
5. Brand-alignment notes specific to this area

---

## 1. Inventory of surfaces

| Surface | Main files | Notes |
|---|---|---|
| Left sidebar: nav block (New chat, Apps, Inbox, Workflows, Search) | `components/Layout/Sidebar.tsx` (`SidebarNavButton`, `SidebarComponent`) | `New chat` shows its shortcut; the rest do not |
| Project filter, sort, view menus | `Sidebar.tsx` (`ProjectFilterMenu`, two `DropdownMenu`s) | Active/Archived switch lives **inside the Sort menu** |
| Chat list: All projects view | `Sidebar.tsx` `renderProjectSection` → `WorktreeGroupComponent` (nested) → `ChatItem` | Project header sticky; main-only projects drop the workspace header |
| Chat list: one project, grouped by workspace | `renderWorktreeGroup` → `WorktreeGroupComponent` → `ChatItem` | Branch name is the group title |
| Chat list: flat | `ChatItem` + `ChatWhere` (project · branch line) | |
| Archived list | `ArchivedChatItem`, `renderArchivedGroup` | Restore / permanent delete on hover |
| Row context menu | `getChatContextMenuItems`, `getWorktreeContextMenuItems` | Rename, Copy ID, Copy path, Mark unread, Move back to Runs, Delete |
| Footer: automation pill, org switcher, Settings | `AutomationActivityPill.tsx`, `OrgSwitcher`, `SidebarNavButton` | |
| Chat header | `Chat/ChatHeader.tsx`, `ContextUsageIndicator.tsx` | Title, options, context %, age |
| Transcript | `Chat/thread-views/InterleavedTimeline.tsx` (virtualized), `ChatMessage.tsx`, `pinnedHeader.ts` | Sticky pinned user message, "Start of conversation", load-older |
| User message | `ChatMessage.tsx` (user branch) | 3rem collapsed with fade mask, click to expand |
| Assistant message / markdown / code | `ChatMessage.tsx`, `MarkdownRenderer.tsx`, `CodeBlock.tsx` | Interleaved text and tool runs |
| Tool rows | `ToolExecution.tsx`, `ToolExecutionGroup.tsx`, `ToolExecutionCollapsibleGroup.tsx`, `tool-renderers/*` | Read-only tools fold into "Explored N files M searches" |
| Diffs | `LightweightDiffViewer.tsx`, `tool-renderers/FileToolRenderer.tsx`, `changes/*` | |
| Spawned agents | `tool-renderers/SpawnToolRenderer.tsx`, `ToolExecution.tsx` (Maximize "Open full thread view"), `BackgroundWorkPill.tsx` | |
| Thread tabs | `thread-views/ThreadTabs.tsx`, `threadUtils.ts` (`getThreadColor`) | Only workflow threads; spawned threads are filtered out |
| Approvals | `ToolExecution.tsx` approval block, `ApprovalActions.tsx`, `PermissionsPanel*.tsx` | `Cmd+Enter` approves all |
| ask_user | `AskUserCard.tsx` (inline card), `QuestionPrompt.tsx` (replaces composer) | Blocking vs non-blocking modes |
| Run status / thinking footer | `ChatPresenter.tsx` (`thinkingFooter`), `ChatThinkingIndicator.tsx`, `UsageLimitWaitNote`, `ChatMachineNotice.tsx`, `ComposerWakeStatus.tsx` | |
| Composer | `ChatInput.tsx`, `ChatTextArea.tsx`, `ChatActionButtons.tsx`/`useChatButtons.tsx`, `ChatButton.tsx`, `WorkflowSelector.tsx`, `InlinePresetPicker`, `ComposerModelName.tsx`, `AccountUsageChip.tsx`, `settings/ChatSettingsPopover` | |
| Slash commands, prompt picker | `SlashCommandMenu.tsx`, `PromptPicker.tsx` | `/` or `Cmd+/` |
| Attachments, drag-drop | `AttachmentPreview.tsx`, `DragDropOverlay.tsx`, `MessageAttachments.tsx` | |
| Queued messages, failed send | `QueuedMessages.tsx`, `FailedSendStatus.tsx` | |
| Right sidebar | `components/FileBrowser/RightSidebar.tsx` (+ `FileTree`, `RecentChanges`, `TasksPanel`, `CommandsViewerTab`, `BrowserSidebarContent`) | Files, Changes, Processes, Tasks, Browser (Electron), Terminal toggle |
| Terminal panel | `components/Terminal/*` | |
| Keyboard | `config/shortcuts.yaml` (77 bindings), `lib/keyboard/*`, `components/Shortcuts/ShortcutHost.tsx`, `Layout/CommandPalette.tsx`, `Settings/KeyboardShortcutsSettings.tsx` | |
| Mobile | `components/Mobile/MobileChatList.tsx`, `MobileChatScreen.tsx`, `MessageActionsSheet.tsx` | Under 768px the app routes to `/m/...` |

---

## 2. Findings by surface

Severity: **H** breaks a task, loses data or blocks a user group; **M** slows or misleads; **L** polish.

### 2.1 Left sidebar and chat list

**F1 (H) The chat list cannot be used from the keyboard.** Measured: 0 of 7 chat rows are
focusable. `ChatItem` and `ArchivedChatItem` are a `<div onClick>` with no `role`, `tabIndex` or
key handler (`Sidebar.tsx` `ChatItem` root, ~line 315; `ArchivedChatItem` ~line 515). The only
keyboard paths are global chords (`Cmd+Shift+Up/Down`, `Cmd+E`, `Cmd+K D`). A screen-reader user
hears row titles as plain text with no way to activate them; Tab from the project header goes
straight to the hover-only buttons inside the rows.

**F2 (H) 21 of the sidebar's 42 tab stops are invisible, and 21 have no accessible name.**
Measured in the accessibility tree: every row's Archive button, every workspace header's "New chat
in this workspace" and "Archive this workspace" buttons, and every group's chevron toggle are
`opacity-0` until hover (`group-hover:opacity-100`, no `focus:`/`focus-visible:` reveal) and are
named only by a tooltip (`aria-describedby`), so they read as "button". Tab walks through invisible
controls. Files: `Sidebar.tsx` `ChatItem` (More options wrapper ~line 410, Archive ~line 427),
`WorktreeGroupComponent` (~lines 689–735). (The project header's "New chat in &lt;project>" does it
right: `aria-label` plus `focus:opacity-100`.)

**F3 (H) A failed chat is painted as a running chat, in green.** Measured: the row "Retry Stripe
webhooks…" (activity ERROR, status label "Failed") has the same `bg-success/5` fill and
`border-success/30` stripe as the running chat. `ChatItem` picks the row treatment with
`runStatus ? "border-success/30 bg-success/5 …"`, and `runStatus` is non-null for *every* lifecycle
status (running, failed, paused…), not only running (`Sidebar.tsx` ~line 323;
`lib/runStatus.ts` `chatListIndicators`). Green is for success only; here it marks a failure, and
the only thing that distinguishes failed from running is a 2xs dot whose meaning is in a hover
tooltip.

**F4 (H) "Delete" archives, and the archived-row trash deletes permanently with no confirm and
no undo.** The active row's context menu item is labelled **Delete** (danger), but
`useDeleteChat` → `DeleteChat` *archives* a chat that is not archived
(`internal/handlers/chat/chat_crud.go` ~line 586) — the same as the row's Archive button, with no
toast or undo. On an archived row, the hover trash icon calls the same mutation, which now
**permanently deletes** (`chat_crud.go` ~line 568) on a single click, with no confirmation and no
undo (`Sidebar.tsx` `ArchivedChatItem.handleDelete`). The hover Archive in the active list, by
contrast, has an Undo toast. Same verb, opposite consequences.

**F5 (M) The list has no loading or error state.** `useChatListWithWorkspaces` is read for `data`
only (`Sidebar.tsx` ~line 783); there is no `isLoading`/`isError` branch. While the first fetch is
in flight, or if it fails, the sidebar shows the empty state "No active chats — Create your first
chat to get started". For a user with 200 chats on a slow or failing connection, that reads as
data loss.

**F6 (M) Scannability: state is carried by tint and a tiny glyph, and the row carries no
"what is it doing" text.** Rows are 32px, title 12.2px/400, time 7.9:1. Running, needs-input and
failed are a 5%-alpha row tint plus a 2px left stripe at 30–40% alpha plus a dot/glyph; labels are
tooltip-only (`side="left"`, `delay=300`). The mobile list already renders the label as text
("Working", "Needs your input", "Needs you") — the desktop list should too, at least for
needs-input and failed. The active row uses `border-primary` + `bg-primary/10` + semibold; with the
running/attention tints in the same hue family as brand blue on some schemes the active row is not
the strongest thing in the list.

**F7 (M) The workspace hierarchy is an all-caps-free branch name with no workspace name.** In the
grouped view the group title is the **branch** (`title={group.worktreeBranch}`), so
`feat/Checkout-RateLimit-per-IP-and-per-API-key` (47 chars) truncates at ~180px, and the
worktree's human name (`rate-limit-checkout`) appears nowhere. The chevron is a separate unnamed
button at the far right, duplicating the title button's toggle. Archived groups key on
`worktreeName` and title on it, so the same workspace is named differently in Active and Archived.

**F8 (M) Active vs Archived is hidden inside the Sort menu.** The only way to see archived chats is
Sort icon → "List: Archived chats" (`Sidebar.tsx` ~line 1768). Nothing in the list says you are
looking at archived chats except the rows' different hover buttons; the header still says the
project name. A visible segmented control (Active · Archived) above the list, or an "Archived (n)"
row at the foot of the list, fixes discoverability and orientation.

**F9 (L) Untitled chats are "New chat" on desktop and "Untitled chat" on mobile.** Desktop
`chat.title || "New chat"` (`Sidebar.tsx` ~line 310) collides with the **New chat** nav button
directly above; mobile says "Untitled chat". Use one label, and not the name of a command.

**F10 (L) Rename failure uses `window.alert`.** `handleSaveRename` → `alert("Failed to rename
chat…")` (`Sidebar.tsx` ~line 1149). Every other failure here is a toast.

**F11 (L) Relative time is unlabelled and coarse.** `getRelativeTime` returns `now`, `40m`, `3h`,
`3d` with no `<time dateTime>` and no full timestamp on hover. "3d" vs "26d" both look like days.

### 2.2 Transcript and message rendering

**F12 (H) Running work is collapsed by default, so the transcript hides what the agent is doing
right now.** Measured: the last message's two executing tools (a spawned reviewer and
`gh pr create`) render as a single collapsed row, "spawn and 1 other tool · 2". Their "Executing…"
badges only appear after expanding. `ChatMessage.renderToolRun` passes `defaultCollapsed={true}`
to `ToolExecutionGroup` for any run of 2+ non-read-only tools, regardless of status
(`ChatMessage.tsx` ~line 653). Meanwhile *completed* edit diffs in earlier messages are expanded
(each `edit` row measured `aria-expanded="true"`). The density budget is spent on finished work
and withheld from live work: inverted.

**F13 (H) Tool-group headers are mouse-only.** Both the "Explored 2 files 2 searches" header and
the "spawn and 1 other tool" header are a `<div onClick>` with no role, tabIndex, key handler or
`aria-expanded` (`ToolExecutionCollapsibleGroup.tsx` ~line 101, `ToolExecutionGroup.tsx` ~line
191; measured: `role=null tabindex=null`). Individual `ToolExecution` rows do this correctly
(`role="button"`, `tabIndex=0`, Enter/Space, `aria-expanded`), so the fix is to copy that.

**F14 (M) A failed tool reads as "Warning".** Measured: the failing `npm test` row shows a
**Warning** badge (8.9:1) and an amber stripe. `getStatusText` returns "Warning" for any
`is_error` result that does not contain "blocked", and for `hasFailed` (`ToolExecution.tsx`
~lines 712–716). A test run that failed is a failure. Reserve Warning for degraded-but-succeeded
results; say "Failed" (or the exit code for shell) otherwise.

**F15 (M) Every tool row says "Completed", which is noise in a long run.** Measured: 4 of 5 rows
carry a "Completed" pill (12.2px, 4.5:1). In a 200-call run this is 200 identical pills. The
success stripe was already toned down for this reason (comment at `ToolExecution.tsx` ~line 742);
the pill should follow: show a status word only for not-completed states, and let the check icon
carry success.

**F16 (M) The tool row's primary text is the tool's function name, not the action.**
`edit ( totals.ts )`, `shell (Run cart tests)`, `spawn ()`. For a manager-of-agents reader the
useful line is the action and object ("Edited src/cart/totals.ts +2 −1", "Ran npm test — 1
failed", "Spawned reviewer: Review discount fix"). `spawn` with a `title` but no `preset` renders
as bare `spawn()` (`lib/toolFormatters.ts` `formatSpawnParams`), so a sub-agent's name is lost
exactly where the user decides whether to open it.

**F17 (M) Message chrome takes a 28px band above every older message.** Measured
`padding-top: 26.25px` on every message column except the newest (`ChatMessage.tsx`
`reservesToolbarBand && "pt-7"`). On a 900px viewport that is ~3 fewer messages per screen in a
long run. The comment explains why (avoid overlapping right-pinned tool controls), but the cost is
paid by every message to make room for a hover-only copy/branch toolbar. A left-gutter toolbar, or
putting copy/branch in the message's own overflow menu (as mobile does with a sheet), recovers it.

**F18 (M) Timestamps are invisible at rest and relative.** Measured: per-message `<time>`
contrast 1.0 at rest (opacity 0 until hover), "17 minutes ago". In a run read hours later the
reader cannot see when steps happened, or how long a step took, without hovering each message.
Show elapsed per tool call (the data is there: `startedAt`/`completedAt`) and a quiet absolute time
on message groups.

**F19 (L) The thinking footer contradicts visible state.** With two tools executing, the footer
read "Still working — no response yet · 1m 04s", then "Processing", then "Synthesizing"
(`ChatPresenter.tsx` `thinkingFooter`, keyed on `progressKey`). "No response yet" while tools are
visibly running reads as stuck. Name what is running ("Running 2 tools · 1m 04s") instead.

**F20 (L) User message is collapsed to 3rem (two lines) with a fade.** A 7-line spec prompt
collapses to two lines (`COLLAPSED_MESSAGE_MAX_HEIGHT = "3rem"`, `ChatMessage.tsx` line 73), and
on mobile the pinned header shows it again above the transcript. The user's own instructions are
the thing they re-read most when checking the agent's work; 5–6 lines collapsed is a better
default.

**F21 (L) Header context indicator is a bare number.** The chat header reads
"Refactor cart totals… | 77 | about 1 hour". "77" is percent of context used; it is labelled only in
a tooltip/aria-label (`ContextUsageIndicator.tsx`). Visually it reads as a count. Add "%" or a ring.

### 2.3 Threads and spawned agents

**F22 (M) Spawned sub-agents have no tab; they live behind a 14px icon on a collapsed row.**
`ThreadTabs` filters `!thread.isSpawn` (`ThreadTabs.tsx` line ~135), so a spawned agent is reached
only via the tool row's Maximize icon ("Open full thread view", `ToolExecution.tsx` ~line 918) or
the background-work pill. Combined with F12, a running sub-agent is two clicks deep and invisible at
rest. Show live sub-agents as a strip under the header (name, status, needs-you) — the data exists in
`useThreads`.

**F23 (L) Thread colours are generated hues, unrelated to the brand.** `getThreadColor` hashes the
id into `hsl(30–330, 65%, 55%)` (`threadUtils.ts`), and the selected tab fills with
`${color}10` (hex alpha appended to an `hsl()` string, which is invalid CSS: the background never
applies — `ThreadTabs.tsx` ~line 206). The composer border and thread label use the same hue
(`ChatInput.tsx` `threadBorderColor`). Arbitrary hues collide with semantic colours (a thread can
come out green or red). Use a fixed 4–6 step palette of neutral-chroma hues, and fix the
background.

**F24 (L) The thread tab's context ring label is 6px.** `MiniContextIndicator` renders the percent
at `text-[6px]` (`ThreadTabs.tsx` ~line 116). Unreadable; drop the number, keep the ring and the
tooltip.

### 2.4 Approvals and ask_user

**F25 (M) A blocking question removes the composer.** With `?fx=ask`, the composer is replaced by
`QuestionPrompt` (measured: no chat textarea present; `showAskOnly` hides it, `ChatInput.tsx`
~line 1760). The user cannot tell the agent anything other than an answer to this question (e.g.
"stop, you're on the wrong branch"); the free-text "Or type your own answer…" is an answer, not a
message. The inline card says "Answer it below", which is correct but leaves the composer gone.
Keep the composer one click away ("Send a message instead").

**F26 (M) The question is shown twice.** The inline `AskUserCard` and the composer `QuestionPrompt`
both render "Merge PR #482 into main now?" (measured). That is right for scroll-back, but the inline
card should collapse to a one-line "Waiting for your answer ↓" while the composer holds the form.

**F27 (M) Answer options are radios without a radiogroup.** Options have `role="radio"` and
`aria-checked` but their parent has no `role="radiogroup"` or label (measured `group: null`), and
they also carry `aria-pressed` (`QuestionPrompt.tsx` ~line 198) — two conflicting state models.
Arrow keys do not move between them.

**F28 (L) Hard-coded colours on the decision surfaces.** Pending-question composer border
`border-yellow-500/70` (`ChatInput.tsx` ~line 1728), unset-params dot `bg-red-500`
(~line 1978), provider dots `bg-orange-400 / bg-green-500 / bg-blue-400`
(~line 1918). These bypass the `warning`/`destructive` tokens and, for OpenAI, put green (success)
on a model name. Approve is a solid `bg-success` button (`ToolExecution.tsx` ~line 996): fine on
semantics, but it is the single brightest thing on screen; brand guidance says primary actions are
blue.

### 2.5 Composer

**F29 (M) Controls are below the 24px target minimum and have no visible focus.** Measured at
desktop: workflow, Ask, Mode, Unattended, model, settings and attach controls are 23px tall
(`h-6`), the send/stop button 26px; none has a focus outline (`outline-style: none`; only
send/stop have a `focus-visible:ring`). WCAG 2.2 SC 2.5.8 asks 24x24. `ChatButton.tsx`,
`ChatInput.tsx` model pill and settings button.

**F30 (M) The textarea has no accessible name.** Measured: no `aria-label`/`aria-labelledby`; its
placeholder ("Ask Reliant, e.g. …", a random example) is the only name, and it changes per mount
(`ChatTextArea.tsx` `defaultPlaceholder`).

**F31 (M) On mobile width the composer takes a quarter of the screen.** Measured at 390x844: the
composer is 206px tall (the toolbar's 6 controls wrap to 2–3 rows), leaving a 533px transcript.
`ChatInput.tsx` toolbar `flex-wrap`. Collapse Mode/Unattended/settings into one "Run options" chip
on narrow widths.

**F32 (L) The model pill can read "auto".** Measured: the model control showed the label "auto"
(63px), which says nothing about which model will answer. Show the resolved model, with "Auto" as a
qualifier.

**F33 (L) Streaming tints the whole composer.** `backgroundColor: hsl(var(--primary)/0.05)` while
streaming (`ChatInput.tsx` ~line 1736), inline style, on top of `--chat-input-bg` hex tokens
(`index.css` ~lines 415–439, a parallel `--chat-*` palette with literal hexes). Brand-token work
should fold these into the main tokens; flagged for the brand agent, not duplicated here.

### 2.6 Right-side panels

**F34 (H) Right-sidebar tabs are not keyboard-reachable and are not tabs.** Measured: Files,
Changes, Processes, Tasks and the terminal toggle all have `tabIndex=-1`, no `role="tab"`, no
`aria-selected` (`FileBrowser/RightSidebar.tsx` lines ~503–655). Clicking the active tab
**closes the sidebar** (`handleTabClick`), a surprising toggle for a tab. Only chords
(`Cmd+K F/G/R/K`) reach them.

**F35 (M) Tab status uses raw colours and is unlabeled.** Processes running: `bg-green-500
animate-pulse`; tasks pending: `bg-yellow-500`; tasks in progress: green pulse; all done: green
check; browser tabs open: `bg-blue-500` (`RightSidebar.tsx` ~lines 569–622). "In progress" in green
conflicts with green = success, and none of the dots is in the accessible name.

**F36 (L) Icon-only tabs at 1440px.** Five icon tabs plus a divider in a 40px bar; names only in
tooltips. There is room for short labels at the sidebar's default width.

### 2.7 Empty, loading, error and recovery

- **F5** (list shows "No active chats" while loading/failing) is the biggest gap.
- No-project and no-chat empty states exist and are plain; the project-level "No chats yet · start
  one" is good copy. The sidebar empty state's icon well uses `bg-muted/30` (structure built from
  `bg-muted`, against the `web-styling` elevation rule).
- Recovery paths are well covered in code (`ComposerWakeStatus` "Can't reach Reliant —
  reconnecting…", `ChatMachineNotice`, `UsageLimitWaitNote` with Retry, `ResumeDaemonPill`,
  `FailedSendStatus`, `TimelineCatchUpOverlay`). I could not exercise a mid-stream disconnect in the
  harness (the streams cannot be dropped and resumed there), so these are reviewed in code only.

### 2.8 Keyboard shortcuts

- 77 bindings in `config/shortcuts.yaml`, chords (`Cmd+K X`) for most pane moves. Coverage is
  strong; **discoverability is weak**: there is no in-app cheat sheet (no `?`/`Cmd+/` help overlay —
  `Cmd+/` opens the slash menu), only Settings → Shortcuts and the command palette. Tooltips do show
  chords ("Files (Ctrl+K then F)"), which is good.
- `Escape` stops the run with `allow_in_input: true` (`stopStreaming`). Pressing Escape to dismiss
  something in the composer (an autocomplete, a draft) stops a 20-minute agent run with no
  confirmation and no undo. Recommend Escape-twice or a confirm when the run has been going > N
  seconds.
- `Cmd+Enter` approves **all** pending tool requests (`approveToolRequests`), outside the composer.
  A bulk approval on a chord with no confirmation, no count and no undo is risky; show a toast
  "Approved 3 tool calls · Undo" or require the approval card to have focus.
- `Cmd+Shift+Up/Down` (next/previous chat) collide with native selection in the composer and
  editor; the yaml resolves it with context shadowing, which means the chord does nothing while
  the composer is focused, i.e. most of the time.

### 2.9 Mobile width

- Under 768px the app routes to `/m/chats`; no horizontal overflow at 390px (measured
  `scrollWidth = 390`). The mobile chat list carries text status labels ("Working", "Needs your
  input", "Needs you") — better than desktop (F6). It labels the failed chat "Needs you" while
  desktop says "Failed": pick one vocabulary.
- Composer height (F31) is the main problem. Message actions move to a long-press sheet
  (`MessageActionsSheet`), which is right, but nothing teaches the long-press.

---

## 3. Broken

Fix these regardless of design direction:

1. **Failed chats rendered with the success (green) row treatment** — `Sidebar.tsx` `ChatItem`
   row class keyed on `runStatus` truthiness (F3).
2. **One-click permanent delete of an archived chat, no confirm, no undo**; and the active-row
   menu's **"Delete" actually archives** (F4). `Sidebar.tsx` `ArchivedChatItem.handleDelete`,
   `getChatContextMenuItems`; `internal/handlers/chat/chat_crud.go` `DeleteChat`.
3. **Chat rows not focusable / not operable by keyboard** (F1) and **tool-group headers mouse-only**
   (F13): keyboard and screen-reader users cannot open a chat from the list or expand a group.
4. **Right-sidebar tabs `tabIndex=-1`** (F34).
5. **Selected thread tab background is invalid CSS** (`${hsl(...)}10`), so it never renders
   (F23, `ThreadTabs.tsx` ~line 206).
6. **Sidebar shows "No active chats — Create your first chat" while the list is loading or has
   failed** (F5).
7. Harness note (not product): `take_screenshot` with a `filePath` under `research/` is refused by
   the DevTools MCP workspace-root check, so reviewers cannot save the screenshots HARNESS.md asks
   for.

---

## 4. Ranked recommendations

| # | Change | Impact | Effort | Files | Concrete change |
|---|---|---|---|---|---|
| 1 | Make live work visible: expand running tool groups, collapse finished ones | H | S | `Chat/ChatMessage.tsx` (`renderToolRun`), `Chat/ToolExecutionGroup.tsx` | Pass `defaultCollapsed={!runHasLiveCall}` (any call `executing`/`preparing`/`needs approval`); auto-collapse when the last call finishes unless the user toggled. Default completed `edit` rows collapsed with a `+2 −1` summary; keep failed rows expanded. |
| 2 | Fix destructive actions in the list | H | S | `Layout/Sidebar.tsx`, `hooks/chat-queries.ts` | Rename the active-row menu item to **Archive** and call `useArchiveChat` with the same Undo toast as the hover button. For archived rows, label **Delete permanently…**, open the forge `confirmation_dialog`, and use a distinct `usePermanentlyDeleteChat` so the verb cannot flip with server state. |
| 3 | Make the chat list keyboard- and screen-reader-operable | H | M | `Layout/Sidebar.tsx` (`ChatItem`, `ArchivedChatItem`, `WorktreeGroupComponent`) | Render each row's primary action as a `<button>` (or `role="link"` with `tabIndex=0`, Enter/Space), roving tabindex with Up/Down inside the list (`role="list"`/`listitem`, `aria-current` on the active chat). Give every icon button an `aria-label`; add `focus-visible:opacity-100` / `group-focus-within:opacity-100` to every hover-revealed control; merge the duplicate chevron into the title button with `aria-expanded`. |
| 4 | One status vocabulary and colour per state, as text | H | S | `Layout/Sidebar.tsx` (`ChatItem`), `lib/runStatus.ts`, `Chat/ToolExecution.tsx` | Row treatment: running = neutral row + animated dot; needs-you = warning tint **plus a "Needs you" text chip**; failed = destructive dot **plus "Failed" chip**; no green rows at all. In tool rows, "Failed" instead of "Warning" for errors (keep Warning for degraded success), and drop the "Completed" pill (icon only). Use the same words on mobile and desktop. |
| 5 | Keyboard parity for tool groups and right-sidebar tabs | H | S | `Chat/ToolExecutionGroup.tsx`, `Chat/ToolExecutionCollapsibleGroup.tsx`, `FileBrowser/RightSidebar.tsx` | Group headers: copy `ToolExecution`'s `role="button" tabIndex=0` + Enter/Space + `aria-expanded`. Right sidebar: `role="tablist"`, each tab `role="tab"` `aria-selected` `tabIndex={selected?0:-1}` with arrow-key movement; do not close the sidebar when the active tab is clicked (the header's Cmd+B toggle does that). Replace `bg-green-500/yellow-500/blue-500` dots with tokens and include the state in `aria-label` ("Processes, 2 running"). |
| 6 | Loading and error states for the chat list | M | S | `Layout/Sidebar.tsx` | `isPending` → 6 skeleton rows (forge `skeleton_loader`); `isError` → inline "Couldn't load chats · Retry" (forge `alert_banner`, `refetch`). Show the empty state only on `isSuccess && total === 0`. |
| 7 | Tool rows say what happened, with timing | M | M | `lib/toolFormatters.ts`, `Chat/ToolExecution.tsx` | Primary text = action + object ("Edited totals.ts +2 −1", "Ran `npm test` · failed · 4.1s", "Spawned **reviewer**: Review discount fix"); fall back to `title` when `preset` is absent; right-align elapsed from `startedAt/completedAt`. Keep the function name in the expanded inspector. |
| 8 | Live sub-agent strip | M | M | `Chat/ChatHeader.tsx` or `thread-views/ThreadTabs.tsx`, `thread-views/useThreads.ts` | Under the header, one chip per live or needs-you sub-agent (name, status, "needs you"), click to open its thread. Replaces hunting for the Maximize icon. Fixed thread palette (4–6 tokens), fix the `${color}10` background, drop the 6px number. |
| 9 | Recover the 28px band per message | M | M | `Chat/ChatMessage.tsx` | Move copy/branch into a left-gutter hover toolbar (or a "…" on the message's first line) so `pt-7` goes; per-message time shown quietly (not opacity 0) on the first message of each minute group. |
| 10 | Blocking question keeps the composer reachable | M | S | `Chat/ChatInput.tsx`, `Chat/QuestionPrompt.tsx`, `Chat/AskUserCard.tsx` | Under the question form add "Send a message instead", which restores the textarea; collapse the inline card to one line while the composer holds the form; wrap options in `role="radiogroup"` with `aria-labelledby` the question and arrow-key movement; drop `aria-pressed`. |
| 11 | Composer targets, focus and name | M | S | `Chat/ChatButton.tsx`, `Chat/ChatInput.tsx`, `Chat/ChatTextArea.tsx` | `h-7 min-w-7` (28px) for every control, `focus-visible:ring-2 ring-ring` on all, `aria-label="Message the agent"` on the textarea, a stable placeholder. On widths < 640px fold Mode/Unattended/settings into one "Options" chip so the toolbar is one row. Model pill shows the resolved model. |
| 12 | Safer chords | M | S | `config/shortcuts.yaml`, `Chat/ChatContainer.tsx` | Escape-to-stop only when the composer is empty, or a second Escape within 1.5s; Cmd+Enter approve-all shows "Approved N · Undo". Add a `?` shortcut-help overlay (reuse `KeyboardShortcutsSettings` data). |
| 13 | Make Archived and workspaces legible | M | S | `Layout/Sidebar.tsx` | Visible "Active · Archived (n)" segmented control above the list; workspace group title = worktree name with the branch as a muted second line (truncate the middle of long branches); one untitled label ("Untitled chat") everywhere; rename failure → toast; `<time>` with full date on hover. |
| 14 | User message collapse at 5–6 lines | L | S | `Chat/ChatMessage.tsx` | `COLLAPSED_MESSAGE_MAX_HEIGHT = "7.5rem"`; header context indicator shows "77%". |

Top 5 by impact-to-effort: **#1, #2, #4, #5, #3**.

---

## 5. Brand-alignment notes specific to this area

(The global token and shell work is owned by another agent; these are area-specific.)

- This area is where the brand promise lives — "You set the direction. Your agents do the work."
  The UI should make **direction** (the user's prompt, questions waiting on them) and **work** (what
  each agent is doing now) the two strongest layers. Today the strongest elements are completed
  diffs, a solid green Approve button and per-row "Completed" pills (F12, F15, F28).
- Colour use: blue `#0d8cff` for the active chat, primary actions (Approve, Submit, Send), and the
  live-run accent; violet `#6e39ff` is a natural, sparing marker for **sub-agents / threads**
  instead of hashed hues (F23); green only for a finished-successfully state, never for running
  (F3, F35) or a provider (F28).
- Geist Mono belongs on tool rows' object text (paths, commands) and the transcript's code; the tool
  row's verb in Geist sans. Today rows mix `font-mono` fragments inconsistently (`task()` is mono,
  `edit ( totals.ts )` is not).
- 1px white-alpha borders: tool rows already use `border-border/30` and white-alpha hairlines on
  the floating toolbar; the composer uses a 2px border (`border-2`, `ChatInput.tsx`), which will
  look heavy against the landing's 1px language.

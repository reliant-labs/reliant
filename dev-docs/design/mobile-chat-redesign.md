# Mobile and tablet chat redesign (2026-11)

A design for the chat thread on phones and tablets: less chrome, more
conversation, and every current feature still reachable. The model is the
ChatGPT and Claude mobile apps, adapted to what Reliant does: long-running agent
runs with workflows, workspaces, approvals and background work.

- **Scope:** the `/m/*` chat screen (`MobileChatScreen` and the shared
  `ChatContainer` → `ChatPresenter` → `ChatHeader` / `InterleavedTimeline` /
  `ChatInput` tree under it), the mobile new-chat screen, and the composer at
  tablet widths inside the desktop shell. The chat list and drawer get a light
  pass (§9).
- **Not in scope:** visual identity (tokens, type, color). This doc uses the
  existing tokens and the `web-styling` contract as they are.
- **Status:** design accepted. Owner decisions D1–D4 are recorded in §14. No
  product code has changed.
- **Binding constraints:** `dev-docs/responsive-ui-standards.md` (feature parity,
  44px targets, 16px inputs, safe areas, no hidden approvals) and
  `src/lib/surface.ts` (capability map). Where this doc and those disagree, those
  win. This doc does not loosen either of them.

---

## 1. What's wrong today

### 1.1 The vertical budget with the keyboard open

On an iPhone 15/16 (393×852) in Safari with the keyboard up, the visual viewport
is about **400 CSS px** tall. The chat screen stacks this chrome inside it.

Measured by `e2e/mobile-chat-geometry.spec.ts` on the pre-redesign tree
(commit `39efe9fc6`), Chromium with touch emulation, `/m/chats/chat_a` with the
`busyChat` fixture: a running workflow with two threads and a long last turn
that the timeline pins, a one-line draft in the composer (blurred). CSS px at
the default Appearance step (the root font size resolves to 17px, so rem
paddings are a little larger than their Tailwind names suggest).

| Band | Source | 390×844 | 390×480 |
|---|---|---|---|
| Screen header (back, title, workspace) | `MobileChrome.tsx` `MobileScreenHeader`, `min-h-[56px]` + border | 56 | 56 |
| Workflow pill, as its own full-width row | `MobileWorkflowExecutionEntry`, `min-h-[44px]`. Renders for **any** execution, not just a running one, whatever its comment says | 44 | 44 |
| `ChatHeader`: the `⋯` menu and context ring row, plus thread tabs | `ChatHeader.tsx`, `py-1`, a 44px `touch-target` row, then `ThreadTabs` (45 of the 100) | 100 | 100 |
| Composer, one line | `ChatInput.tsx`: `mt-1 mb-1.5` (11), 2px border, 4px padding, an attachment wrapper whose `pt-3` renders **even with no attachments** (13), the text line, then a toolbar row (`pt-2 mt-2 border-t` + 44px controls, 62) | 163 | 163 |
| **In-flow chrome** | | **363** | **363** |
| Transcript scroller | `InterleavedTimeline.tsx` `[data-context=transcript]` | 481 (57%) | 117 (24%) |
| Pinned user-message overlay, drawn over the scroller | `InterleavedTimeline.tsx` `pinnedUserMsg`, `pt-1 pb-1.5` + a clamped bubble | 56 | 56 |
| **Readable conversation** | scroller minus overlay | **425 (50%)** | **61 (13%)** |

Two things the class names did not show. The empty composer is not one line:
its placeholder is a random example prompt that wraps to two lines at 390px
often enough to make it 191px, which leaves **33px (7%)** at 390×480. And no
banner rendered for this fixture, so these are the floor: each banner in §1.2
item 5 comes off the 61.

390×480 is a short window, not a keyboard: browser emulation cannot open one
(`responsive-ui-standards.md`, "Native acceptance"). With the keyboard up a
phone has ~400px, less than this, so the real number is worse. Keyboard-open
evidence stays a Device Hub task.

### 1.2 Structural causes

1. **Three header bands do one header's job.** `MobileScreenHeader` holds the
   title. `MobileChatScreen` passes `hideChatTitle`, which leaves `ChatHeader` as
   a whole 52px row for one `⋯` button and a ring. The workflow pill takes a
   third row.
2. **Two overflow affordances.** The screen header's workspace button and the
   `ChatHeader` `⋯` menu split chat-level actions between them with no rule.
3. **The pinned prompt is desktop chrome.** On a 1200px desktop column, a
   sticky copy of the last prompt is cheap context. On a phone it's a full
   bubble, shadow and border, plus a 44px jump button, sitting on top of the
   two lines that are left.
4. **The composer is a desktop toolbar folded onto a phone.** It keeps the
   card → text → divider → toolbar structure and only swaps the contents. The
   divider row costs ~60px whether or not the user touches it.
5. **Banners stack.** Queued messages, background work, permissions, OOM, disk,
   wake status, the recovery banner, the no-machine hint and the take-over bar
   are independent `flex-shrink-0` siblings above the composer
   (`ChatPresenter.tsx`). Each is reasonable alone. They have no shared budget.
6. **Tablets get the desktop composer, crammed.** `MOBILE_MAX_WIDTH = 768`
   (`lib/mobileRedirect.ts`) sends an iPad in portrait (820/834px) to the
   desktop ADE. That's correct under the standards ("touch capability alone must
   not redirect every tablet"). But there the composer's single wrapping row
   tries to fit the workflow selector, preset pickers, toolbar params, model
   pill, usage chip, settings, attach and send. The `isCompact` flag
   (container < 500px) only shrinks icons. It never re-prioritizes anything.
7. **Layout keys on surface, not space.** `mobile = useSurface() === "mobile"`
   drives the composer layout. `surface.ts` says this is wrong: "Use Tailwind
   breakpoints for layout that adapts; use this for functionality that is
   absent." So the compact composer never shows up where it's needed most: a
   narrow desktop split pane or a tablet.

---

## 2. What the reference apps do (2025–2026)

| Pattern | ChatGPT (iOS/Android) | Claude (iOS/Android) | Takeaway for us |
|---|---|---|---|
| Thread header | One bar: menu, centered title/model, new-chat. No second row | One bar: menu, title with chevron (opens chat actions), new-chat | **One bar, one row.** The title doubles as the chat-actions entry point |
| Composer at rest | A calm pill: `+`, text, mic/voice. No tool chips | A calm card: `+` left, model and effort on the right rail, send | **Calm by default.** Configuration is one tap away, not always visible |
| Tools and modes | Removed the row of tool icons in May 2025. Everything goes behind `+` / a sliders icon, which opens a **bottom sheet** | `+` menu with flyout submenus. Model picker kept separate on the right | **One `+` sheet** for attach, tools and configuration, the model included (D1) |
| Armed state | A removable chip in the composer shows active scope (e.g. "Deep research ×") | Model/effort on the rail. Skills don't show armed state (a known weakness) | **Chips only for non-default scope**, so the user sees what this send will do |
| Attachments | Thumbnail with dismiss, inside the composer | Inline preview inside the card | Previews live **inside** the composer card and take space only when present |
| Navigation | Sidebar simplified in March 2026: destinations as a horizontal strip above history | Drawer with recents | Drawer for destinations. The chat list stays the home (§9) |
| Long content | Assistant text full width with no bubble. User messages in a compact right-aligned bubble | Same | Already true here. Keep it |
| Back to live end | A small circular ↓ button | Same | A circle, no expanding label on touch |
| Message actions | Long-press opens a sheet | Long-press opens a sheet | Already true here (`MessageActionsSheet`) |

Platform guidance that applies:

- **iOS 26 (Liquid Glass):** bars and toolbars float over content, and a
  *scroll-edge effect* replaces hairline dividers. Chrome recedes until content
  scrolls under it. Honor *Reduce Transparency*. On the web that's
  `@media (prefers-reduced-transparency: reduce)` with an opaque fallback.
- **Material 3 window size classes:** compact < 600, medium 600–839,
  expanded ≥ 840 (dp ≈ CSS px). List–detail goes two-pane at expanded. A
  compact *height* class (< 480) matters for landscape phones.
- **Viewport and keyboard:** `interactive-widget=resizes-content` on the viewport
  meta resizes the layout viewport for the keyboard in Chrome 108+ and
  Firefox 132+. WebKit doesn't support it, so iOS still needs the
  `visualViewport` path (`useVisualViewport`, already wired in
  `MobileLayout`). The iOS installed PWA has its own keyboard viewport bug, so
  test it separately.
- **Targets and type:** 44×44 minimum (our standard, stricter than WCAG 2.2 AA's
  24px). Editable text at ≥ 16px so iOS doesn't zoom on focus. Body prose at
  16–17px on phones (ChatGPT and Claude both use the platform body size).

---

## 3. Principles

1. **The conversation owns the screen.** Chrome is a budget, not a stack. Target
   with the keyboard open on a phone: the transcript gets **≥ 70%** of the
   visual viewport, and **≥ 60%** with any single status line showing.
2. **One bar on top, one composer at the bottom.** Everything else is either in
   the transcript, in a sheet, or in the single status slot (§6).
3. **Calm by default, and armed state stays visible.** Hide configuration until
   asked. Show a chip whenever this send differs from the default (other
   thread, preset, attachments).
4. **Interruptions are never collapsed.** Approvals, `ask_user` questions,
   errors and the final answer stay fully visible (standards doc). Only ambient
   status collapses.
5. **Layout follows available space, capability follows surface.** Composer and
   header density key off container width (`@container`). `useSurface()` keeps
   gating functionality only.
6. **Same content, adaptive shells.** Every sheet renders the same settings
   components the desktop popovers use (`ChatSettingsPopover`'s pages). Only the
   shell changes: bottom sheet on compact, anchored popover or form sheet at
   medium and up.
7. **State survives everything.** Rotation, keyboard, sheet dismissal and
   resizing keep drafts, uploads, parameters, question answers and chat identity
   (standards doc). The layout changes here move controls. They never remount
   the composer's state owner.

---

## 4. Layout system

### 4.1 Width classes

| Class | Width | Where it occurs | Chat layout |
|---|---|---|---|
| Compact | < 600 | Phones portrait, foldable outer screens, narrow split panes | Single pane. Top bar plus the inline composer |
| Medium | 600–839 | Phone landscape, iPad mini portrait (744), foldables open, iPad split view | Single pane, transcript column ≤ 720px centered, sheets become anchored popovers or form sheets |
| Expanded | ≥ 840 | iPad portrait and landscape, desktop | `/m/*`: list–detail (chat list as a 320px pane). Desktop shell: unchanged, plus the adaptive composer (§5.4) |

**Compact height** (< 480, landscape phones, or any viewport with the keyboard
open and < 480 visible) is a modifier, not a class: the top bar drops to 44px
and the pinned prompt is suppressed (§7.2).

These are **layout** breakpoints. The surface redirect (`MOBILE_MAX_WIDTH = 768`
plus a coarse pointer) doesn't change. Tablets keep the desktop shell, as the
standards require, and get the adaptive composer there.

### 4.2 Container queries, not viewport media queries

The composer and the top bar size themselves with Tailwind v4 `@container`
queries against their own column. Viewport media queries don't fit here. The
same `ChatInput` renders in a phone viewport, a 400px desktop split pane, a
tablet column and the new-chat screen. Only its container knows how much room
it has. This replaces the `isCompact` `ResizeObserver` in `ChatInput.tsx`
(container < 500 → smaller icons) with one mechanism.

### 4.3 Floating chrome and the scroll edge

The top bar and the composer float over the transcript instead of taking space
in the flex column:

- The transcript scroller runs full height, with `scroll-padding` and content
  padding equal to the measured bar and composer heights (custom properties
  `--chat-top-inset` / `--chat-bottom-inset`, written by a `ResizeObserver`,
  the same way `--chat-pinned-header-h` works today).
- Bars use `bg-background/85` + `backdrop-blur-xl` with **no border at rest**.
  A hairline (`border-border/60`) and a soft fade appear only when content is
  scrolled under the bar. That's the scroll-edge effect.
- `@media (prefers-reduced-transparency: reduce)` → opaque `bg-background` with
  the hairline always shown.
- No `bg-muted` for structure (web-styling elevation rule). The composer card is
  `bg-card` + `border-border`, the same in both modes.

This doesn't free pixels on its own. It removes the hard edges that make the
chrome read as heavy, and it lets the last messages scroll fully into view
above the composer.

---

## 5. The chat screen, compact

```
┌───────────────────────────────────────┐  safe-area top (MobileLayout)
│ ‹   Fix flaky login test  ⌄    ◔  ⧉  │  48px top bar
│     ● Running · Get It Right          │  (subtitle only when there's status)
├╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌┤  hairline only when scrolled under
│                                       │
│                    ┌───────────────┐  │
│                    │ the user turn │  │  user bubble ≤ 85%
│                    └───────────────┘  │
│ Assistant prose, full width, 16px.    │
│ ▸ Read 3 files                        │  tool rows collapsed (existing)
│ ▸ Edited login.spec.ts  +12 −4        │
│                                       │
│                  (↓)                  │  44px circle, overlay
├───────────────────────────────────────┤
│ ⚠ Machine waking…                 ›   │  status slot: 0 or 1 line (§6)
│ ╭───────────────────────────────────╮ │
│ │ ⊕  Message…                    ⬆ │ │  inline composer, 52px
│ ╰───────────────────────────────────╯ │
└───────────────────────────────────────┘  --mobile-safe-bottom
```

### 5.1 Top bar (48px)

One row. It replaces `MobileScreenHeader`, the `ChatHeader` row, the thread-tab
row and the workflow-pill row.

| Slot | Content | Notes |
|---|---|---|
| Leading | `‹` back to `/m/chats`. Viewing a sub-thread, it goes back to the parent thread first | Absorbs `ChatHeader`'s "Back to parent thread". 44px target |
| Title | Chat title, 15px semibold, one line, ellipsis, with a `⌄` chevron. **Tapping it opens the Chat sheet** (§5.2) | Claude's pattern. The chevron is the discoverability cue |
| Subtitle (optional) | One 12px line, only when there's status: `● Running · <workflow>` (spinner), `Thread: <name>`, `No machine` | Replaces the 44px workflow row and the `NoMachinePill` |
| Trailing 1 | Context ring, **only at ≥ 70% of the compaction threshold** | The "it matters now" rule `AccountUsageChip` already uses at 80%. Tapping it opens the Chat sheet's context section |
| Trailing 2 | Workspace (`FolderGit2`) when the chat has a worktree | Opens `MobileWorkspaceSheet`, unchanged |

**Why no new-chat button in the bar (unlike ChatGPT and Claude; decision D4):** a Reliant
chat is a long-running agent run. Starting a new one from inside a thread is
rare. Checking what the agent changed (the workspace) is common. The bar gets
two trailing icons at most, and the workspace earns the slot. New chat stays one
tap away on the chat list and in the drawer.

### 5.2 The Chat sheet (tap the title)

A bottom sheet (medium detent, can expand to large) that collects every
chat-level action now spread across the `ChatHeader` dropdown, the thread tabs,
the context ring and the workflow pill:

```
 ───                                    drag handle
 Fix flaky login test            ✎     inline rename
 Get It Right · started 2h ago
 ┌──────────────────────────────────┐
 │ ● Workflow: running step 3 of 5 ›│   → /m/chats/$id/workflow
 │ ⇄ Thread: Main                  ›│   thread picker (when > 1 thread)
 │ ◔ Context: 142k / 200k  · Compact│   ContextUsageIndicator + /compact
 │ ⧉ Files, Git & Plan             ›│   MobileWorkspaceSheet
 └──────────────────────────────────┘
 ┌──────────────────────────────────┐
 │ ↧ Export chat                    │
 │ ⧉ Copy chat ID                   │
 │ ⟲ Workspace recovery…           ›│   WorkspaceRecoveryMenuItems
 └──────────────────────────────────┘
 ┌──────────────────────────────────┐
 │ ▣ Archive chat                   │   with Undo toast (existing)
 └──────────────────────────────────┘
```

It reuses `MobileCardGroup` and `MOBILE_ROW` from `MobileChrome.tsx`, so it
matches the settings screens. "Open Terminal" from the desktop menu does **not**
appear: the mobile surface has no terminal panel, so the item opens nothing on
`/m/*` today. That's the named product reason the standards require. Add a
`terminal` capability to `surface.ts` rather than letting the item silently do
nothing.

### 5.3 Inline composer (compact container, < 560px)

The ChatGPT/iMessage shape: **one row whose height follows the text**.

```
 at rest / single line
 ╭─────────────────────────────────────╮
 │ ⊕  Message…                      ⬆ │   44px controls, 4px inset → 52px
 ╰─────────────────────────────────────╯

 multi-line, attachment, armed chips
 ╭─────────────────────────────────────╮
 │ [▣ screenshot.png ×] [Preset: Fast ×]│  chip row, only when present
 │    Refactor the retry loop so that  │
 │    backoff is jittered and capped   │
 │ ⊕  at 30s, then update the tests  ⬆ │  controls bottom-aligned
 ╰─────────────────────────────────────╯
```

- **Structure:** one `bg-card border-border rounded-3xl` card, no inner divider.
  Leading `⊕` (44px), then the textarea (16px, grows to the existing
  `composerMaxHeight` cap of 20% of the visual viewport), then the action button
  (44px). Controls stay bottom-aligned as the text grows.
- **Chip row** (above the text, inside the card, horizontal scroll): attachments
  with upload progress, retry and cancel (today these are separate full-width
  rows); the target thread when it isn't main (today a separate colored line);
  a non-default preset. Each chip has a × and opens its sheet on tap. **No
  chips means no row.** That removes the always-rendered `pt-3` attachment
  wrapper.
- **Action button**, one slot and four states:

  | State | Button | Enter key |
  |---|---|---|
  | Idle, empty | Send, disabled | n/a |
  | Idle, text or attachment | **Send** ⬆ (filled `bg-primary`) | send |
  | Agent running, empty | **Stop** ■ | n/a |
  | Agent running, text | **Queue** ⬆ (filled), with a ghost ■ Stop to its left | queue (existing `onQueue`) |

  The running-with-text state is the only one with two controls, and only
  while it applies. Queueing is a Reliant feature ChatGPT doesn't have, so it
  gets its own visible state instead of being hidden.
- **No microphone button.** The OS keyboard has dictation, and Reliant has no
  voice mode today. Adding one is a feature, not part of this redesign.
- **Return inserts a newline on touch (decided).** When the primary pointer is
  coarse (`(pointer: coarse)`, the same signal the mobile redirect uses),
  plain Enter falls through to the textarea's newline. Only the action button
  sends or queues. `enterKeyHint` stays at its default (`enter`) so the key
  label doesn't promise a send. Cmd/Ctrl+Enter sends (or queues while the agent
  runs) everywhere, so a hardware keyboard on an iPad keeps a send shortcut.
  The desktop shell with a fine pointer keeps today's "Enter sends". This
  changes `ChatTextArea`'s `handleKeyDown`, which today always sends, queues or
  stops on Enter. It matches ChatGPT and Claude and avoids sending half-typed
  prompts.

### 5.4 The `⊕` sheet: Chat options

The one place for everything the composer configures. Same content as the
desktop popovers, in a sheet shell. Pages push inside **one** sheet, not
sheet-on-sheet. Today `ChatInput` closes `ComposerOptions` and then opens a
separate `ChatSettingsPopover` sheet, which drops the user's place.

```
 ───
 ┌────────┐ ┌────────┐ ┌────────┐
 │ Photos │ │ Camera │ │ Files  │        attach tiles (capability chatAttachments)
 └────────┘ └────────┘ └────────┘
 ┌──────────────────────────────────┐
 │ Model            Sonnet 4.6     ›│   → model page (ChatSettingsPopover 'model')
 │ Preset           Fast           ›│   InlinePresetPicker content, one row per target
 │ Workflow         Get It Right   ›│   locked once started: "Set when the chat began"
 │ All settings  ● 1 required      ›│   → 'main' page; red dot = hasUnsetParams
 └──────────────────────────────────┘
 ┌──────────────────────────────────┐
 │ Prompts                         ›│   PromptPicker (/prompt)
 │ Commands                        ›│   the slash list, for touch users
 └──────────────────────────────────┘
 Account: 82% of 5h · alice@     ›     AccountUsageChip, only when it shows today
```

- **The model is never on the composer (decided).** Its only home on touch
  layouts is this sheet's first configuration row, with the current value
  shown. It gets no armed chip and no inline pill at any width, and no chip on
  the new-chat screen. This is a deliberate departure from Claude's
  always-visible model. On a 390px phone with the keyboard up, a model chip
  costs either a second row (~40px) or a third of the single row. The value is
  still one tap away, and the row shows it before the user drills in.
- **Required inputs:** when `hasUnsetParams`, the `⊕` button carries the red
  dot that `Settings2` carries on desktop today, and sending opens this sheet on
  "All settings" (today's mobile behavior at `ChatInput.tsx:1251`).
- **Loading and error states** keep today's explicit copy ("Loading workflow
  settings…", "Could not load workflow settings." + Retry, "This workflow has no
  configurable settings."). They become rows in the sheet's style instead of
  bare `<p>` elements.

### 5.5 Adaptive composer at medium and expanded widths

The same component at wider containers. Controls fill a **priority+ row**: in
priority order, whatever fits shows inline and the rest folds into `⊕`.

```
 ≥ 560px container (tablet, wide pane)
 ╭───────────────────────────────────────────────────────────╮
 │ Refactor the retry loop…                                   │
 │                                                            │
 │ ⊕  [Get It Right ⌄] [Preset: Fast ⌄]                    ⬆ │
 ╰───────────────────────────────────────────────────────────╯
```

Priority (first stays visible longest): send/stop/queue → `⊕` → workflow (only
before the first send) → preset(s) → toolbar params → account chip → settings
(`Settings2`). Anything that doesn't fit is a row in the `⊕` sheet or popover.
**The model is never in this row.** It stays in `⊕` on every touch layout,
tablets included. Only the desktop shell with a fine pointer keeps today's model
pill, and that's outside this doc's scope. This fixes the crammed iPad desktop
composer without removing a single control. It also means a narrow desktop
split pane gets the inline composer for free.

---

## 6. The status slot

Today up to nine bands stack above the composer. Compact gets **one slot, one
line, highest priority wins**, with a `+N` count when more are active. Tapping
it opens a sheet listing every active item in full, each with its existing
actions.

| Priority | Item (existing component) | Collapsible? |
|---|---|---|
| — | Pending approval (`PermissionsPanel`) | **Never.** Renders in full above the composer and takes its place, like `QuestionPrompt` does today |
| — | `ask_user` question (`QuestionPrompt`) | **Never.** Replaces the composer (existing), keeping "Send a message instead" / "Answer it" |
| — | Take over a run (`TakeOverRunBar`) | Replaces the composer (existing) |
| 1 | Session interrupted + Restart (recovery banner) | Slot |
| 2 | No machine (`NoMachineComposerHint`) | Slot |
| 3 | Waking / Continue without machine (`ComposerWakeStatus`) | Slot |
| 4 | OOM kill (`OomKillBanner`) | Slot |
| 5 | Disk pressure (`DiskPressureBanner`) | Slot |
| 6 | Resume suspended machine (`ResumeDaemonPill`, today under the header) | Slot |
| 7 | Queued messages (`QueuedMessages`) | Slot: `2 queued ›`, sheet keeps take-back |
| 8 | Background work (`BackgroundWorkPill`) | Slot: `3 running ›` |

Ambient items (7–8) can share the line as two compact chips when nothing above
them is active. At medium and up the slot shows up to three lines before
collapsing. The desktop shell is unchanged.

In-transcript notices stay where they are: usage-limit wait, machine notice,
the thinking indicator and the queued-for-machine note all live in the
timeline footer and cost no fixed chrome.

---

## 7. Transcript

### 7.1 Content

- **Prose 16px on compact** (today `text-sm`, 14px, in user bubbles and
  markdown), line-height 1.55, gutters 16px. Through the root font-size scale
  (`applyRootFontSize`'s mobile map), not per-component sizes, so the
  Appearance setting keeps working.
- Transcript column ≤ 720px centered at medium and up on `/m/*` (about 75
  characters a line). The desktop's 900/1200px columns are unchanged.
- Kept as is: user bubble right-aligned ≤ 85%, assistant full width, tool calls
  collapsed on mobile (`SurfaceProvider`), date separators, long-press actions
  sheet, local horizontal scroll in code blocks, "Load earlier messages",
  virtualizer.

### 7.2 Pinned prompt ("jump to last user message")

On compact it becomes a **one-line strip**, not a floating bubble:

```
 ↑  Refactor the retry loop so that backoff is jit…
```

- 36px visual, 44px hit area (the whole strip is the jump target, no separate
  button), `text-xs text-muted-foreground`, same floating treatment as the top
  bar. It sits directly under the bar and shares its scroll-edge.
- **Hidden while the keyboard is open, and in compact height.** You're writing
  the next turn, the previous prompt is the least useful thing on screen, and
  space is scarcest then.
- Medium and up: today's overlay, unchanged.

### 7.3 Scroll to bottom

A 44px circle on touch, no expanding "Scroll to bottom" label (on a phone the
label animation covers text). Same anchoring as today: inside the transcript
frame, above the composer's floating inset.

---

## 8. New chat screen

Today it stacks the header, a "Start with" card of rows, a bordered footer, and
`ChatInput`'s own bordered card inside that footer: two layers of chrome
around one input.

The canvas holds the **workspace options**, not suggested prompts (decided:
no starters). Where a chat runs is the decision that matters before the first
send, and it can't change afterward.

```
┌───────────────────────────────────────┐
│ ‹   New chat                          │  48px, subtitle = project
│     reliant                           │
│                                       │
│  Workspace                            │
│  ┌─────────────────────────────────┐  │
│  │ ◉ main                  default │  │  main pinned first
│  │ ○ fix-login-flake       2h ago  │  │  recent workspaces, ranked
│  │ ○ retry-backoff         1d ago  │  │  as the chat list ranks them
│  │ ⊕ New workspace                 │  │  MobileCreateWorkspaceSheet
│  │   Show all (12)               › │  │  full list + paging
│  └─────────────────────────────────┘  │
│  ┌─────────────────────────────────┐  │
│  │ Runs on          MacBook      › │  │  chatDaemonSelection only
│  │ Workflow         Get It Right › │  │
│  └─────────────────────────────────┘  │
├───────────────────────────────────────┤
│ ╭───────────────────────────────────╮ │
│ │ ⊕  What do you want to do?     ⬆ │ │  same inline composer, no chips
│ ╰───────────────────────────────────╯ │
└───────────────────────────────────────┘
```

- **Workspace list inline.** The first few workspaces (main first, then the
  most recent, the same order `MobileChatList` already uses) are selectable
  right on the canvas, with "New workspace" and "Show all" below. Today the
  workspace is a single row that opens a sheet. One tap picks a workspace now,
  and the list fills the empty space instead of a starter grid. The visibility
  rules stay: no workspace group with no machine; when main is the only
  workspace and none can be created, the group collapses to a single
  read-only "main" line.
- **Runs on and Workflow** stay `MobileSelectRow`s (existing sheets and rules:
  machine only with `chatDaemonSelection`; the "Runs on <machine>, where this
  workspace lives" lock note; the placement block with "Set up here").
- **No model here.** The model is set from `⊕` like everywhere else (§5.4).
- **Keyboard open:** the canvas scrolls under the composer. The selected
  workspace stays visible as a one-line summary under the top bar
  (`main · MacBook · Get It Right`), and tapping it scrolls the options back
  into view. Workspace choice stays visible while typing without chips on the
  composer.
- The placement block, errors and "Open created chat" keep their current copy.
  They render as status-slot lines (§6), not loose paragraphs.
- Drop the outer bordered footer. The composer card is the only frame.

---

## 9. Chat list and drawer (light pass)

- **Chat list top bar:** `☰  Chats  🔍  ✎`. Search becomes an icon instead of a
  drawer-only destination, and new chat moves into the bar. A large title that
  collapses on scroll (the iOS idiom) is optional polish.
- **Expanded `/m/*`:** list–detail. The chat list is a persistent 320px left
  pane. Opening a chat replaces the right pane, not the route stack's whole
  screen, and the `‹` in the chat bar disappears.
- **Drawer:** keep it (it scales past five destinations, see its module comment).
  Following ChatGPT's March 2026 simplification, put the destinations in a
  compact grid at the top and the project switcher below. No other change.
- Don't hijack the left edge swipe. Safari uses it for history-back, and that
  already does the right thing on `/m/chats/$id`.

---

## 10. Keyboard and viewport mechanics

- Add `interactive-widget=resizes-content` to the viewport meta in
  `frontends/web/index.html`, keeping `viewport-fit=cover`. Chromium and Firefox
  then resize the layout viewport. Keep `MobileLayout`'s `visualViewport`
  pinning for WebKit. It must stay a no-op when the layout viewport already
  matches, so Android doesn't get double compensation.
- **Compose mode** = `useVisualViewport().keyboardOpen` on a compact container.
  In compose mode: pinned prompt hidden, status slot limited to one line
  (interruptions excepted), top bar subtitle kept (status matters), and
  `--mobile-safe-bottom` at 0 (existing).
- The composer height cap stays at `MOBILE_COMPOSER_VIEWPORT_SHARE` (20%).
  The inline layout gives more of that cap to the text because the toolbar row
  is gone.
- **iOS installed PWA:** test separately (known WebKit standalone keyboard bug).
  Native evidence is a Device Hub task (standards doc, "Native acceptance").

**Target budget** (393×852, keyboard open, ~400px visible):

| Band | Today (est.) | Proposed |
|---|---|---|
| Header bands | 153–185 | 48 |
| Pinned prompt | 50–75 | 0 (compose mode) |
| Status | 0–N stacked | 0 or 36 |
| Composer, one line | ~135 | 52 |
| **Transcript** | **~30–60 (8–15%)** | **~264–300 (66–75%)** |

**After** (measured on the redesigned tree by `e2e/mobile-chat-geometry.spec.ts`,
same fixture and method as §1.1: Chromium with touch emulation, `busyChat`, a
one-line blurred draft, default Appearance step). The chrome floats (§4.3), so
the transcript scroller is the full height and readable = composer band top −
top bar bottom − pinned strip; the spec checks its structural walk against those
edges. No status line renders for this fixture.

| Band | 390×844 | 390×480 | 1024×768 (list–detail) |
|---|---|---|---|
| Top bar | 48 | 48 | 48 |
| Pinned prompt strip | 37 | 37 | 56 (wide column: the pinned bubble, not the one-line strip) |
| Composer band (card + 8px + safe area) | 60 (card 52) | 60 (card 52) | 114 (wide composer: text row + priority+ row) |
| **Readable transcript** | **699 (83%)** | **335 (70%)** | **550 (72%)** |
| §1.1 baseline | 425 (50%) | 61 (13%) | — |

The empty composer is the same 60px band: its example-prompt placeholder is
clipped to one ellipsized line and never sizes the textarea. A multi-line draft
grows to the 20% cap (168px of text at 844px, a 184px band). Compose mode hides
the pinned strip, so with the keyboard up the strip's 37px also goes to the
transcript; that state still needs native evidence (§12 Acceptance).

---

## 11. Feature parity map

Every control on the chat screen today, and where it lives after the change.
**Nothing is removed.** "Open Terminal" gets a named capability reason.

| Today | Where | After |
|---|---|---|
| Back to chats | Screen header | Top bar leading |
| Chat title | Screen header | Top bar title |
| Rename | `ChatHeader` menu / title click | Chat sheet, inline ✎ |
| Workspace (Files/Git/Plan/Packages) | Screen header trailing | Top bar trailing + Chat sheet row |
| Workflow pill → workflow view | Own row | Top bar subtitle status + Chat sheet row |
| Show/hide workflow (desktop) | `ChatHeader` menu | Unchanged on desktop |
| Open Terminal | `ChatHeader` menu | Desktop only, via a new `terminal` capability (no panel on `/m/*`) |
| Workspace recovery items | `ChatHeader` menu | Chat sheet |
| Export chat | `ChatHeader` menu | Chat sheet |
| Copy chat ID | `ChatHeader` menu | Chat sheet |
| Archive (+Undo) | `ChatHeader` menu | Chat sheet |
| No-machine pill | `ChatHeader` row | Top bar subtitle + status slot |
| Context usage ring | `ChatHeader` row | Top bar at ≥ 70% + Chat sheet (always), with Compact action |
| Thread tabs / All threads | `ChatHeader` row 2 | Chat sheet thread picker + subtitle + composer thread chip |
| Back to parent thread | `ChatHeader` | Top bar `‹` (parent first) |
| Child runs strip | `ChatHeader` row 3 | Unchanged (`runsArea` off on mobile) |
| Pinned prompt + jump | Overlay | One-line strip, hidden in compose mode |
| Scroll to bottom | Overlay | 44px circle |
| Load earlier / start marker | Transcript | Unchanged |
| Message actions | Long-press sheet | Unchanged |
| Attach files, upload retry/cancel | `⊕` sheet / rows | `⊕` tiles + composer chips |
| Workflow picker | `⊕` sheet | `⊕` sheet (locked after start, with reason) |
| Preset pickers | `⊕` sheet | `⊕` sheet rows + armed chip |
| Toolbar params | Desktop inline only | Priority+ inline when they fit, else `⊕` |
| All settings + required badge | `⊕` sheet | `⊕` sheet + dot on `⊕` |
| Model pill | Composer row | `⊕` first row only, at every touch width (never on the composer) |
| Enter to send | Textarea | Touch: Return = newline, button or Cmd/Ctrl+Enter sends. Fine pointer: unchanged |
| New-chat workspace, machine, workflow | "Start with" card rows | Workspace list inline on the canvas + Runs on / Workflow rows (§8) |
| Account usage chip | `⊕` sheet | `⊕` sheet (shown when it shows today) |
| Send / Stop / Queue | Composer row | One action slot, four states (§5.3) |
| Question prompt, send-instead, answer-it | Composer | Unchanged |
| Slash commands | Typing `/` | Unchanged + Commands row in `⊕` |
| `/prompt` picker | Slash command | Unchanged + Prompts row in `⊕` |
| Queued messages | Band | Status slot + sheet |
| Background work | Band | Status slot + sheet |
| Approvals | Band | Never collapsed, in place of the composer |
| OOM / disk / wake / resume / recovery | Bands | Status slot by priority + sheet |
| Take-over bar | In place of the composer | Unchanged |

---

## 12. Rollout

Each phase ships on its own and leaves the app consistent.

0. **Measure.** Add a geometry assertion to `e2e/mobile-responsive.spec.ts`: on
   `/m/chats/chat_a` (fixtures from `mobileMocks.ts`, which need a workflow
   execution, a multi-thread chat and a pinned prompt), record each band's
   height at 390×844 and at 390×480. Commit today's numbers in this doc. Gate
   later phases on the transcript share. Per the standards, the 480px run is a
   short-window check, not keyboard evidence.
1. **One top bar + Chat sheet.** Fold `ChatHeader`'s row, the thread tabs and
   the workflow pill into the mobile top bar. Extract `ChatHeader`'s menu
   actions into a hook (`useChatActions(chatId)`) so the desktop dropdown and
   the mobile sheet share one implementation. Add the `terminal` capability.
2. **Inline composer + `⊕` sheet with paged navigation.** Replace the `mobile`
   branch in `ChatInput` with an `@container` layout. Fix the always-rendered
   attachment padding. Chip row. Four-state action slot. Model moves into `⊕`
   only. Return-is-newline on coarse pointers, with Cmd/Ctrl+Enter sending
   (`ChatTextArea.handleKeyDown`).
3. **Status slot.** A `ComposerStatusSlot` in `ChatPresenter` takes the banner
   siblings as prioritized entries on compact, renders them as today at medium
   and up, and keeps approvals and questions out of it.
4. **Transcript polish.** Pinned strip and compose mode, 44px circle,
   compact type scale, floating chrome and scroll-edge.
5. **Tablet.** Priority+ composer row inside the desktop shell. List–detail on
   expanded `/m/*`. Medium sheets as anchored popovers. Viewport meta
   `interactive-widget`.
6. **New chat + list.** Inline workspace list on the canvas, keyboard-open
   summary line, single composer frame, list top bar.

**Primitives:** bottom-sheet detents, drag-to-dismiss and in-sheet page push
come from forge-ui's `Modal` (`sheetOnMobile`). If forge lacks them, build them
in forge first (web-styling: "a primitive forge lacks is built in forge
first"), then install. Don't fork `forge-ui/modal.tsx`, which stays verbatim.

### Acceptance (per phase, as applicable)

- 390×844 touch, compose-mode geometry: transcript ≥ 66% of the visible height
  with no status line, ≥ 60% with one.
- Every parity row in §11 has a rendered e2e assertion that changes a value and
  checks the submitted request (standards: "a passing 'sheet opens' test does
  not establish parity").
- 44×44 hit-testing on every new control, including chips' × and sheet rows.
- Draft, uploads, params and question answers survive rotation, the 540px pane,
  320px width and sheet dismissal (existing journeys extended).
- Reduced-transparency and both color modes: bars stay legible. The
  color-scheme screenshot pass (`e2e/color-schemes.spec.ts`) gets reviewed.
- Native: Device Hub evidence on iPhone (Safari and installed PWA, keyboard
  open), iPad (portrait, split view) and one Android phone. A missing
  capability is recorded as a blocker, not a pass.

### Status (2026-11, branch `feat/mobile-update`)

All seven phases are implemented. What landed, and what did not:

| Phase | Status | Notes |
|---|---|---|
| 0. Measure | Done | `e2e/mobile-chat-geometry.spec.ts` + `busyChat` fixture; baseline in §1.1, after-numbers in §10. The spec lives beside `mobile-responsive.spec.ts`, not in it |
| 1. Top bar + Chat sheet | Done | `useChatActions` shared by the desktop dropdown and the Chat sheet; `terminal` capability in `surface.ts` |
| 2. Inline composer + `⊕` | Done | `@container` grid (one textarea, never remounted across widths), chip row only when present, four-state action, model only in `⊕` (D1), touch Return = newline (D2). One-line card measured at 52px, band 60px; the placeholder is one ellipsized line |
| 3. Status slot | Done | `ComposerStatusSlot`; approvals and questions stay out of it |
| 4. Transcript polish | Done | One-line pinned strip hidden in compose mode, 44px scroll circle, floating chrome + scroll-edge hairlines |
| 5. Tablet | Partly | Priority+ composer row, list–detail at ≥ 840px and `interactive-widget=resizes-content` are in. **Deferred:** medium-width sheets as anchored popovers |
| 6. New chat + list | Done | Workspace options inline on the canvas (D3), list top bar with search/new |

Acceptance:

- **Geometry gate: met** at 390×844 (83%) and 390×480 (70%) with no status
  line, and `mobile-chat-geometry.spec.ts` now runs it as a real `test`. The
  ≥ 60%-with-one-status-line case has no fixture yet.
- **Draft survival: met** for resize (wide, 540px pane, short window) and `⊕`
  dismissal. The e2e test also requires the same textarea element throughout.
- **Deferred, needs forge `Modal` support:** bottom-sheet detents and
  drag-to-dismiss. Medium-width anchored popovers wait on the same primitive
  work. The sheets ship as today's `sheetOnMobile` sheets with in-sheet page
  push.
- **Not done:** Device Hub native evidence (iPhone Safari + installed PWA with
  the keyboard open, iPad, Android). Keyboard-open geometry is still inferred
  from the 480px short window. The webkit e2e projects
  (`responsive-webkit-phone`, `responsive-webkit-tablet`) were not run locally;
  CI runs them. The color-scheme screenshot pass was not reviewed.

---

## 13. Alternatives considered

| Option | Why not (or not yet) |
|---|---|
| **Keep the model chip on the bar** (Claude) | Rejected (decision D1). Costs a second row or a third of the single row on a 390px phone with the keyboard up |
| **Suggested prompts on new chat** (ChatGPT, Claude) | Rejected (decision D3). The canvas holds workspace options instead |
| **Model picker in the header** (ChatGPT) | Our header title is the chat title, which users need for orientation across many parallel runs. ChatGPT's single-assistant model doesn't map |
| **Auto-hide the top bar on scroll** | Hides the back button and run status, which are the things a monitoring user looks for. Used only in compact height |
| **Send tablets to `/m/*`** | Contradicts the standards: wide touch keeps desktop capability. The adaptive composer fixes the actual tablet complaint without a second navigation model |
| **A bottom tab bar** | Already tried and replaced by the drawer (`MobileNavDrawer` module comment). It also costs ~50px on every screen, including the chat |
| **Separate mobile composer component** | Forks the hardest state in the app (drafts, presets, params, uploads, queueing). One `ChatInput` with container-driven layout keeps one state owner |

---

## 14. Decisions (owner)

- **D1. No model on the composer.** The model lives only in the `⊕` sheet on
  every touch layout. No inline pill, no armed chip, no new-chat chip (§5.4,
  §5.5).
- **D2. Return inserts a newline on touch.** The button sends. Cmd/Ctrl+Enter
  sends for hardware keyboards. The fine-pointer desktop keeps Enter-to-send
  (§5.3).
- **D3. No suggested prompts.** The new-chat canvas shows the workspace options
  (§8).
- **D4. Workspace takes the thread bar's trailing slot,** not new-chat (§5.1).

## Sources

- ChatGPT composer teardown: aiuxplayground.com/teardowns/chatgpt/composer
- Claude composer teardown: aiuxplayground.com/teardowns/claude/composer
- OpenAI release notes, 2025-05-06 (tool icons replaced by a bottom-sheet
  menu); 2026-03-26 (simplified mobile sidebar)
- Apple, *Adopting Liquid Glass* (scroll-edge effect, custom bar backgrounds)
- Material 3 window size classes (compact / medium / expanded)
- Bramus, "Control the viewport resize behavior on mobile with
  interactive-widget" (HTMHell, 2024-12-04); WebKit bug 259770
- WCAG 2.2 SC 2.5.8 Target Size (Minimum)
- In repo: `dev-docs/responsive-ui-standards.md`, `src/lib/surface.ts`,
  `web-styling` skill

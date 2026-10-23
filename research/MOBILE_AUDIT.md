# Mobile audit — the web app at 390×844

What a phone can reach, how it looks, and what is missing, measured against
the desktop app's top-level surfaces. Written 2026-10-22 on
`feat/mobile-parity-pass` (base `a84e40ac`).

## How this was measured

No `forge env up` stack was running on this machine (no
`../control-plane/.forge/logs/dev/`, nothing listening on a Vite or API
port), so every number here comes from **the real web app in a `vite` dev
server, driven by Playwright against mocked RPCs**:

- `frontends/web/e2e/mobileMocks.ts` — a signed-in, onboarded user (API-key login, the
  path `onboarding.spec.ts` uses), one forge-enabled project, three chats, a
  chat timeline with a shell call and a file read, and an inbox holding an
  approval, an async `ask_user` question, a machine-asleep item and a failing
  automation. Every other Connect RPC answers `{}`.
- `frontends/web/e2e/mobile-audit.spec.ts` — opens each surface COLD (`page.goto`, as a
  notification or another app would) at **390×844, touch, coarse pointer**
  (the two signals `lib/mobileRedirect.ts` keys on), waits 4s, records where
  the page ended up, and measures it: document `scrollWidth`, elements whose
  right edge passes the viewport, interactive elements under 44px on either
  axis, `<table>` count, and the width of any sidebar/nav. Runs only when
  `MOBILE_SHOTS=<dir>` is set; it writes a screenshot per surface there.

```
MOBILE_SHOTS=/tmp/mobile-before BASE_URL=http://127.0.0.1:<vite port> \
  npx playwright test e2e/mobile-audit.spec.ts --project=chromium
```

`?desktop=1` rows load the DESKTOP component at phone width (the redirect's
documented opt-out), to measure what reusing it on mobile starts from.

## The one mechanism behind most of the gaps

`RootShell` (`frontends/web/src/routes.tsx`) sends a phone on ANY path outside `/m/*`
(and a short preserved list: `/auth`, `/oauth`, `/onboarding`, …) to
**`/m/chats`** — `shouldRedirectToMobile` in `lib/mobileRedirect.ts`. The
target is always the chat list, whatever was asked for. So on a phone:

- a desktop surface with no `/m/*` twin does not exist — there is no URL that
  reaches it, and nothing in `MobileNavDrawer` links to it;
- a deep link into one (an inbox notification, a run link, a forge alert
  email, a teammate's pasted URL) silently becomes the chat list. A run IS a
  chat (`/workflows/runs/$runId` ≡ `/m/chats/$chatId`), and even that mapping
  is lost.

## Surface inventory

Desktop entry points: `Layout/Sidebar.tsx` (New chat, **Apps**,
**Observability**, **Inbox**, Workflows, Projects, Search, the chat list, the
automation activity pill), the right-panel `NavigationBar` (Files, Changes,
Projects, Workspaces), Settings (`/settings/$section`), and the routes in
`routes.tsx`, `workflowsAreaRoutes.tsx`, `forgeConsoleRoutes.tsx`.

Mobile entry points: `Mobile/MobileNavDrawer.tsx` (New chat, Search,
Workflows, Machines, GitHub, Settings, Account, plus the project card), each
screen's `MobileMenuButton`, and the `/m/*` routes under `MobileShell`.

| Desktop surface | Route | Reachable on a phone? | How / what happens | How it looks at 390px |
|---|---|---|---|---|
| Chat list | `/` | Yes | `/m/chats` (`MobileChatList`) — the mobile home | Good: 8 targets, all ≥44px, no overflow |
| Chat timeline | `/project/$id?chat=` | Yes | `/m/chats/$chatId` (`MobileChatScreen` → the desktop `ChatContainer`) | Fits (no sideways scroll), but **11 of 13 tap targets are under 44px**: message actions (More options, Copy, Branch), "Toggle tool details", "Open file" on tool cards. 2 elements overflow the viewport edge inside the timeline |
| Tool cards (shell, view, …) | in the timeline | Yes | Same renderers as desktop; long output wraps (`whitespace-pre-wrap break-all`) | Readable; header toggles are ~24px tall (see above) |
| New chat | sidebar | Yes | `/m/new` | Good |
| **Inbox** | `/inbox` | **No** | Redirected to `/m/chats`. No drawer entry, no badge anywhere on mobile, so pending approvals and async questions are invisible from a phone | Desktop `InboxView` at 390px (`?desktop=1`): fits without overflow, but **all 17 targets are under 44px** (Approve/Deny 24px, dismiss 21px, the scope toggle 24px) |
| **Apps** (forge console) | `/forge`, `/forge/p/$project/…` | **No** | Redirected to `/m/chats`; no drawer entry | Desktop console at 390px (`?desktop=1`, fixture harness): the **sidebar is a fixed 225px** (`forge-ui/sidebar_layout.tsx`, `w-60`), leaving ~165px for content; **378 elements overflow** the viewport on the Overview, 3 `<table>`s with 240px cells, 55 of 60 targets under 44px |
| **Observability** | `/forge/p/$project/observability/…` | **No** | Same as Apps | Same chrome; 97 overflowing elements and a `<table>` on the overview |
| Workflows → Library | `/workflows/library` | Partly | `/m/workflows` (`MobileWorkflowCatalog`): a read-only catalog, by design (`workflowAuthoring: false`) | Good |
| Workflows → Runs | `/workflows/runs` | No (by decision) | `runsArea: false` — "decision 8" in `lib/surface.ts`. A run is reachable as a chat from the chat list, but a run LINK lands on the chat list, not the run | Desktop at 390px: collapsed 53px rail, fits, 13/13 targets small |
| Workflows → Automations | `/workflows/automations` | No | Redirected; no mobile surface | — |
| Projects | sidebar | Yes | `/m/projects` | Good |
| Search | sidebar / shortcut | Yes | `/m/search` | Good |
| Machines | `/settings/environments` | Yes | `/m/daemons` | Good |
| GitHub | settings | Yes | `/m/github` | Good |
| Settings | `/settings/$section` | Partly | `/m/settings`: Account, Billing, GitHub, AI, Appearance, Notifications, Workspace, Privacy, About. MCP, prompts, shortcuts, developer, connectors are desktop-only by design. A `/settings/…` link lands on the chat list, not `/m/settings` | Good. Desktop settings at 390px: 28 overflowing elements |
| Files / Changes / Workspaces panels | right panel | Partly | Read-only file browse + git status sheets from a chat (`MobileWorkspaceSheet`) | Good |
| Terminal | right panel | No (by design) | `terminal: false` | — |
| **Diagnostics** | — | **Does not exist on any surface** | Browser console → `.forge/logs/<env>/frontend_*.log` only in a DEV build (`lib/browser-log-forward.ts`, `import.meta.env.DEV`). Prod has Sentry and HyperDX/ClickStack browser telemetry (`lib/telemetry.ts`), both consent-gated and with `consoleCapture: false`, and neither visible to the user. On a phone there is no devtools, so a user who sees "something went wrong" has nothing to report | — |

The mobile-native screens themselves are in good shape: every `/m/*` screen
measured 0 overflowing elements and 0 sub-44px targets, the drawer included.
The problems are **reachability** and the desktop components that mobile would
need to reuse.

## Gaps, ranked by severity

1. **Inbox unreachable** (blocker). Approvals and async questions are the one
   thing a phone is best placed to answer, and the phone cannot see them: no
   route, no drawer entry, no badge, and every inbox deep link becomes the chat
   list.
2. **Apps and Observability unreachable** (blocker). Checking a deploy or a
   firing alert from a phone is impossible; a forge alert link lands on the
   chat list.
3. **Deep links collapse to the chat list** (high). `/inbox`, `/workflows/runs/$id`,
   `/settings`, `/workflows` all become `/m/chats`, discarding what the link
   was about, even where a mobile equivalent exists.
4. **The forge console's chrome is desktop-only** (high, blocks fixing 2):
   a fixed 225px sidebar with no off-canvas mode; the top bar and content use a
   32px gutter; 20 hand-written `<table>`s across the console pages overflow at
   phone width.
5. **No way to see what went wrong on a phone** (high for support). No
   diagnostics view, no client error history, no way to copy app/stream state.
6. **Inbox rows are mouse-sized** (medium, blocks 1 being usable): 24px
   Approve/Deny, a 21px dismiss, a 24px scope toggle; row links point at the
   desktop Runs area, which a phone cannot open.
7. **Timeline tap targets** (medium): message actions and tool-card toggles
   are ~24px. Not fixed in this pass — the timeline is being changed by the
   async-question work (`AsyncQuestionCard`/`ChatMessage`), and a parallel edit
   would collide.
8. **Runs and Automations areas** (low, product decision): `runsArea: false`
   is a recorded decision; runs remain reachable as chats.
9. **Popovers wider than a phone** (low): `ChatSettingsPopover` is a fixed
   `w-[360px]` anchored `left-0`.

## What this pass fixed

Re-measured with the same script after the change (`MOBILE_SHOTS=/tmp/mobile-after`):

| Surface | Before | After |
|---|---|---|
| Inbox | unreachable (`/inbox` → `/m/chats`; no entry, no badge) | `/m/inbox` (`Mobile/MobileInboxScreen` = the desktop `InboxView` in the mobile chrome); drawer row with the count; a dot on every screen's menu button; `/inbox` links land there. 16 targets, **0 under 44px**, 0 overflow. Approve/deny, wake, dismiss + Undo, and answering questions (async included) are the desktop code path |
| Apps / Observability | unreachable | drawer rows to `/forge` and `/forge/observability` (same `isForgeUIEnabled` gate as desktop); `/forge*` exempt from the phone redirect |
| Console chrome at 390px | 225px fixed sidebar, 378 overflowing elements (workloads), 97 (observability) | off-canvas nav drawer below `md` (`forge-ui/sidebar_layout.tsx`), 16px gutter, native-select env scope, two-up env strip, single-column page grids; **6** and **3** overflowing elements left, all inside clipped table headers or chart internals; no sideways page scroll |
| Console tables | 20 hand-written `<table>`s overflowing | the list tables (services, jobs, pods, releases, environments, alerts, observability overview + home, dashboards) stack into cards below `md` (`Forge/StackedTable`, `lib/stackedTable.ts`), each line labelled by its column header |
| Deep links | all → `/m/chats` | `/inbox` → `/m/inbox`, `/workflows/runs/$id` → `/m/chats/$id`, `/workflows`, `/workflows/library/*` → `/m/workflows`, `/settings/environments/*` → `/m/daemons`, `/settings/*` → `/m/settings` (`mobilePathFor`) |
| Diagnostics | none | Settings → Diagnostics, also at `/m/diagnostics`: app/server/release version, network, update-stream status, last sequence, subscribed chat, machine heartbeat, the device's last 50 client errors (uncaught, rejections, `console.error`, error boundaries; `lib/clientDiagnostics.ts`, local only), "Copy diagnostics" |
| Safe areas | `env(safe-area-inset-*)` resolved to 0 (no `viewport-fit=cover`) | `viewport-fit=cover`; console drawer, top bar and page pad for the insets |
| Onboarding checklist | floated over the console on a phone | hidden on phone viewports |

## Left, ranked

1. **Timeline tap targets** — message actions and tool-card toggles are ~24px
   (11 of 13 targets on the chat screen). Deferred because the async-question
   work is changing `ChatMessage`/`AsyncQuestionCard`; do it once that lands.
2. **The remaining console tables** — workload detail's events, logs/traces
   virtualized grids, release comparison, DNS records, dashboard panel tables
   still scroll inside their cards at phone width. `StackedTable` is a drop-in
   for each.
3. **Observability views built for a pointer** — the trace waterfall, the
   profile flamegraph and the log row expansion work but are cramped; they
   want a phone layout of their own, not a reflow.
4. **Upstream the responsive `SidebarLayout` to forge**
   (`forge/pkg/components/components/layouts/sidebar_layout.tsx`); reliant's
   copy is now ahead, and a forge-ui re-install must carry it over.
5. **Runs / Automations areas** — `runsArea: false` stands; runs open as
   chats. Automations are desktop-only, and the Inbox says "on desktop" for
   their fixes rather than linking away.
6. **`ChatSettingsPopover`** is a fixed 360px popover.

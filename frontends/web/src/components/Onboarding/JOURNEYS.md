# Onboarding journeys: writing content for the engine

A **journey** is a named, versioned, ordered list of steps. There are two:
`workflows` ("Use AI workflows") and `forge` ("Explore a forge app"). Content is
data: one file per journey, `journeys/<id>.tsx`, exporting a `Journey`. The
engine draws and walks it. You should not need to touch the engine to write a
journey. If a step needs something the engine can't express, extend the engine
in its own change.

| File | What it is |
|---|---|
| `journeys/workflows.tsx`, `journeys/forge.tsx` | **Content.** Yours to replace. |
| `journeys/signals.ts` | The tour signals (typed events) steps can wait on. Add yours here. |
| `journeys/layoutActions.ts` | Workspace panel actions for `onEnter` / `onComplete`. |
| `journeys/registry.ts` | Validation, ordering, `?tour=` resolution. Builds in the forge journey only. |
| `journeys/catalog.ts` | Registers the workflows journey and Reliant's end-of-tour hooks. The Reliant and forge shells import it; the forge demo's bundle doesn't. |
| `calloutPlacement.ts` | Where a spotlight's card goes so it never covers its target. |
| `TourControls.tsx` | A step's own controls inside its card (`controls: "callout"`). |
| `types.ts` | `Journey`, `JourneyStep`, `AdvanceOn`, … The schema below is defined here. |
| `advanceOn.ts` | Required actions (`advanceOn`) and `phases`. |
| `useTourNavigation.ts` | Moving between steps, finishing, skipping, analytics. |
| `JourneyTour.tsx` | The engine UI (picker and step). Shell-neutral. |
| `OnboardingWizard.tsx` | Reliant's wrapper: auth gates, the version re-offer, the checklist's state. |
| `OnboardingChecklist.tsx`, `checklistItems.ts` | The Setup Guide: a floating "Get started · 3/8" pill on every shell page, and its items with their completion signals. |
| `tourShell.ts` | Which shell (`reliant` / `forge`) and where progress is stored. |

## The URL

The URL is the source of truth for what is open:

```
?tour=workflows:wf-new     journey + step (canonical; what the engine writes)
?tour=wf-new               a bare step id (step ids are unique across journeys)
?tour=workflows            the journey's first step
?tour=pick / pick:menu     the journey picker (after onboarding / from a menu)
?tour=chat-and-sidebars    a retired single-tour id: opens its successor journey
```

The picker's entries in Reliant: the nav footer's **Help › Take a tour**
(`Layout/sidebar/HelpMenu.tsx`), the command palette, Settings › About, the
Setup Guide's "Take a tour" and the chat home's "Prefer a quick tour first?".

The root route **retains `?tour=` across navigations** that don't name it
(`routes.tsx`, `retainTourParam`). So a journey can span pages that the user
moves between with ordinary links. A navigation clears it only by setting
`tour: undefined` explicitly, which every tour exit does.

## A journey

```ts
export const workflowsJourney: Journey = {
  id: "workflows",
  version: 2,                       // bump when the steps change meaningfully
  title: "Use AI workflows",        // picker card
  description: "…",                 // picker card
  shells: ["reliant"],              // default: both shells
  steps: [ /* JourneyStep[] */ ],
  outro: [ /* JourneyStep[], walked after steps; see "Outro" */ ],
  finish: (shell) => shell === "reliant" ? { to: "/forge" } : null, // where Finish lands; default: Reliant's chat
  returnToStart: (shell) => true,   // Finish, Exit tour and Back off step 1 return to where the journey was started; see "Leaving"
  onLiveChange: (live) => {},       // told when the journey goes live and when it stops, however it stops; see "Leaving"
  elsewhere: { label: "Try AI workflows", href: () => workflowLibraryPath() }, // where it runs: an app path opens in place, a URL in a new tab
  isAvailable: () => true,          // e.g. a feature flag
  reshowAfterVersionBump: true,     // default true
  phone: MyPhoneDeck,               // optional: shown on Reliant phones instead of "Open this on a computer"
  controls: "bar",                  // or "callout"; see "Controls"
};
```

**Registering content.** `registry.ts` imports the forge journey itself. The
workflows journey's content pulls in Reliant's workflow hooks and stores, so
it is registered by `journeys/catalog.ts` (`registerJourney`), which the
Reliant and forge shells import for its effects. Until then (in the forge
demo's bundle, always) `workflows` is `WORKFLOWS_ELSEWHERE`: no steps, never
available, only its `elsewhere` link for the end card. A test keeps the
placeholder's version, title and link equal to the real journey's. The
Reliant-only end of a tour (land on the chat, ask for an API key) is
installed the same way, through `setReliantTourHooks` (tourShell.ts).

**Controls.** Every step carries its own controls in its card (the default,
`controls: "callout"`, in every shell): **Step X of Y**, **Back** (not on the
first step; ← works too), **Next** / **Finish**, and **Exit tour** (not on the
last step). There is no bottom nav bar and no generic Skip step.
- A required action (`advanceOn`) keeps Next **disabled** until it's done
  (the step's `actionHint` is the button's title and one line in the card);
  then Next shows a **check**. With `autoAdvance` the tour moves on by itself
  instead (default false).
- A step's own `skip` with a label ("Create it for me") is a button in the
  card while the action is pending.
- The card is placed beside its target first (right, left, then below,
  above), flipped to whichever side has room, and never over the target
  unless no side can hold it (`calloutPlacement.ts`, tested by a sweep). A
  multi-spotlight keeps its labels and adds a centred card for the copy and
  controls.
- On a phone (below 640px) the card is a bottom sheet (at most 45% of the
  screen) and the target is scrolled into the space above it. Reliant's
  mobile surface defers instead ("Open this on a computer"); the forge shell
  and the demo use the sheet. Every control is at least 44px tall there.
  A journey with its own phone presentation, `Journey.phone` (a component
  taking `{ onFinish, onExit }`), shows that there instead, on the mobile
  surface too: the workflows journey's lessons as a swipeable card deck with
  still diagrams (`journeys/workflowsPhone.tsx`).
- `controls: "bar"` keeps the retired bottom nav bar, for a journey that
  still needs it. Nothing ships with it.

**Versioning.** Each journey records its outcome per version, under
`onboarding.tour.<id>.v<version>.completed`, with the value `completed`,
`skipped` or `shown`.
- When you bump `version`, users who **completed** an older version are offered
  the new one once. The wizard opens it on their next app load and records
  `shown` first, so it can never loop.
- The retired single tour counts as workflows **v0**.
- Placeholders ship `reshowAfterVersionBump: false`. When real content lands,
  bump the version and remove that line.

**End card.** The last walked step shows two buttons:
- **Finish** goes to `finish(shell)`, or to the default landing.
- **Try the other journey** starts the other journey. If the other journey
  doesn't run in this shell, it becomes the other journey's `elsewhere` link.

**Leaving.** A journey that walks pages which are not the person's own (the
forge journey's sample app) sets `returnToStart`. Starting it (the picker, a
menu, Try the other journey) records the page it was started from, less
`?tour=`, in the journey context (`startedFrom`), unless that page is one of
the journey's own. Finish, Exit tour and Back on the first step go back there
and clear it. With nothing recorded (a `?tour=` link opened cold), `finish`
applies.

State that exists only while a journey runs (the forge journey's sample app in
the switcher) hangs on `onLiveChange`: `JourneyLiveWatch`, at the app's root,
calls it with true when `?tour=` first names one of the journey's steps and
with false when it stops naming them, whatever stopped it (Finish, Exit tour,
Back, a link, the next journey). It is not called between the journey's own
steps.

## A step (`JourneyStep`)

| Field | Meaning |
|---|---|
| `id` | kebab-case, **unique across all journeys**, prefixed `wf-` / `fg-`. Appears in the URL and in analytics. Don't rename a shipped id. |
| `title`, `description` | Card copy. `description` is a ReactNode. |
| `target` | CSS selector to spotlight. Prefer `[data-tour='…']`; see "Anchors". No target (and no `targets`) means a centred card. |
| `targets` | Multi-spotlight: several labelled targets (`{selector, label, description, labelPosition?}`). A label goes on the first side with room (right, left, above, below); `labelPosition` names the side to try first, for a wide target whose side is over other chrome. |
| `type` | `spotlight` / `multi-spotlight` / `card`. Inferred from `target` / `targets`. |
| `tooltipPosition`, `tooltipPadding`, `spotlightConfig` | Placement. A preferred side; the card flips to a side with room and never covers its target (`calloutPlacement.ts`). |
| `copyByShell` | `{ forge: { description: … } }`: per-shell title / description / target. |
| `shell` | `"reliant"` / `"forge"` / `"both"` (default). A step that doesn't match is left out. |
| `isAvailable` | The step's `when`: `() => boolean`, read at navigation time. False leaves the step out (Next / Back skip it and it isn't counted). |
| `route` | The page the step lives on; see below. |
| `phases` | Sub-targets within one step; see below. |
| `advanceOn` | The step's **required action**; see below. |
| `card` | `{ wide?, modal? }` for a step with no target: `wide` gives the card room (a player), `modal` locks the page's scroll and takes the backdrop's clicks. No spotlight either way. |
| `autoAdvance` | Once the action is done, move on by itself after 600 ms. Default false: Next shows a check and the user clicks it. |
| `afterAction` | Extra copy appended once the action is done ("Copy something to try it."). |
| `actionHint` | What to do, while waiting ("Drag Review to the loop"): one line in the card and disabled Next's title. Announced to screen readers. |
| `showNext` | Keep Next enabled while the action is pending. Default false. |
| `blockOutside` | While pending, block pointer clicks outside the target (spotlight steps). Keyboard and screen readers are unaffected. Off by default; the spotlight is normally click-through. |
| `advanceIfAlreadyDone` | If the state already holds on entry, advance at once. |
| `skip` | `{ label?, run? }`. Overrides **Skip step** for a step that later steps depend on (W1: "Create it for me" seeds the draft). If `run` throws, the user stays on the step; show your own toast. `run` gets `{context, setContext}`. |
| `signal` | Analytics name for the action (`draft_created`). Sent as `signal` on `journey_step_completed` when the action completed the step. |
| `onEnter` | Runs once when the step opens (e.g. `layoutActions.showSidebar`). |
| `onComplete` | Runs once when the user leaves the step forward: Next, the action, the step's skip or Finish. It doesn't run on Back or Exit tour. |

Every card but the last has **Exit tour** (the bar called it End tour).
- Exit tour records the journey as skipped at this version, and remembers the
  step in `onboarding.journey_resume`. The picker then offers "Resume where you
  left off".
- Back goes to the previous step. On the first step it closes the tour.

### `route`

```ts
route: {
  path: "/workflow/",                                     // pathname prefix the step needs
  open: (ctx) => ctx.workflowSlug                         // static RouteTarget, or built from the journey context
    ? { to: "/workflow/$workflowName", params: { workflowName: String(ctx.workflowSlug) } }
    : null,
  label: "Open the builder",                              // the "Open <page>" card's button
  message: "Open your workflow to continue this step.",   // optional
}
```

- **Moving to the step** (Next, Back, a skip) navigates to `open`.
  - If the user is already on that page, `open.search` is merged into the
    current search, so `{ view: "traces" }` switches the view and keeps the
    rest.
  - With no `open`, the step navigates to `path`, and only when the user is
    off it.
- **Arriving off the page any other way** (a reload, or the user wandering
  off) shows an "Open <page>" card instead of an empty spotlight.

### `advanceOn`: required actions

`advanceOn` takes one condition or an array, and the step completes when any
of them is met. Until one is met, Next is disabled, the card shows
`actionHint`, and Enter/arrow-key navigation is off (the user is typing into
the app).

| Condition | Kind | Fires when |
|---|---|---|
| `{ kind: "click", selector? }` | edge | The element (default: the step's `target`) is clicked. Capture phase, deferred one tick, so the click's own navigation runs first. |
| `{ kind: "event", event, where? }` | edge | A tour signal is emitted, optionally narrowed by `where(payload)`. |
| `{ kind: "route", path }` | level | The pathname matches: a prefix string or a RegExp. |
| `{ kind: "checklist", item }` | level | An onboarding-checklist item is complete. Reliant only; the checklist's own detectors (`onboardingChecklistStore.subscribeToStoreChanges`) set it, e.g. `run-workflow` when a user-source workflow exists. |
| `{ kind: "store", test, subscribe }` | level | Any store state. Build it with `whenStore(useSomeStore, (s) => …)`. |
| `{ kind: "element", selector }` | level | An element matching `selector` is in the DOM. |
| `{ kind: "when", test(ctx) }` | level | Anything else. `ctx = { location: {pathname, search}, signals, context }`. It is re-checked on every location change, signal and context write. |

- **Edge** conditions fire on the action itself.
- **Level** conditions are states, and reaching one while the step is open
  completes the step. A state that **already holds when the step opens** shows
  Next at once but never auto-advances, so Back doesn't bounce the user
  forward. To skip ahead instead, use `advanceIfAlreadyDone`.
- `signals` in a `when` holds the **last payload of each signal emitted since
  the step opened**. That lets a step combine conditions:
  ```ts
  advanceOn: { kind: "when", test: ({ signals, location }) =>
    signals["workflow:draftCreated"]?.start === "tour-fix-my-pr" && location.pathname.startsWith("/workflow/") },
  // a signal, then another:
  advanceOn: { kind: "when", test: ({ signals }) =>
    signals["workflow:triggerDeclared"]?.source === "schedule" && !!signals["workflow:saved"] },
  // search params (forge demo):
  advanceOn: { kind: "when", test: ({ location }) => location.search.view === "logs" && location.search.trace === FIXTURE_TRACE_ID },
  ```

### `phases`

Use phases for one step with a changing target, e.g. (a) the New button, then
(b) the dialog it opens.

```ts
phases: [
  { target: "[data-tour='new-workflow']", description: "Click New workflow." },
  { when: { kind: "element", selector: "[data-tour='new-workflow-dialog']" },
    target: "[data-tour='new-workflow-dialog']", description: "Keep the guided start and click Create." },
],
```

- The active phase is the **last one whose `when` has been met**. Phases latch:
  once reached, a phase stays.
- A phase overrides `title` / `description` / `target` / `targets` /
  `tooltipPosition`.
- `when` takes any `AdvanceOn`.
- Phases are presentation only. The step still completes on its `advanceOn`.

### Journey context

Context is per-journey state that a step records and later steps read. For
example, W1 records the draft's slug and W9's route needs it.

```ts
useTourStore.getState().setJourneyContext("workflows", { workflowSlug: slug, draftId });
```

- It is persisted as `onboarding.journey_context`.
- Steps read it in `route.open(ctx)`, in `when` predicates (`ctx.context`)
  and in `skip.run({context, setContext})`.

### Outro

`Journey.outro` lists steps walked after `steps`, once the journey proper is
done. Gate them per step: `shell: "reliant"` and/or `isAvailable`. If no outro
step applies (in the forge shell, say), the journey ends on its last step.
The end card sits on whichever step is last.

The workflows outro tours the chat and the shell around it, then closes only
what it opened:

```ts
outro: [{
  id: "wf-outro-layout",
  shell: "reliant",
  title: "Back to the chat",
  targets: [/* the composer, the menu toggle, the files + terminal toggles */],
  description: "… The menu on the left holds your chats and workflows. The file browser opens on the right and the terminal at the bottom …",
  route: { path: "/project/", open: () => …, label: "Open the chat" },
  onEnter: () => { layoutActions.showSidebar(); layoutActions.showFilesForTour(); },
  onComplete: layoutActions.hidePanelsTourOpened,
}],
```

Never close the left nav when a tour ends: it is the one shell's navigation,
not a panel the tour borrowed.

`layoutActions` acts through each panel's existing owner and persists the
change exactly as the user's own toggle would:

| Action | Owner it uses |
|---|---|
| `showSidebar` / `hideSidebar` | `workspaceStateStore.setLeftSidebarExpandedGlobal` |
| `showTerminal` / `hideTerminal` | `terminalStore.showTerminal` / `hideTerminal` |
| `showFiles` / `hideFiles` | ModernApp's file browser, through its `open-file-browser` / `close-file-browser` window events |
| `showFilesForTour` / `showTerminalForTour` | The same, remembering that the tour opened the panel (nothing when it was already open) |
| `hidePanelsTourOpened` | Closes the files panel and the terminal, but only those the `…ForTour` actions opened |
| `collapseAll` | All three, the left nav included; no shipped journey uses it |

## Anchors (`data-tour="…"`)

Target elements through a stable attribute, never through classes or text.

1. Add `data-tour="<kebab-name>"` to the element in its component, e.g.
   `<Button data-tour="new-workflow" …>`. If the component doesn't forward
   props (some forge-ui components take no data attributes), wrap it in a
   `<span data-tour=… className="contents">`, or anchor on an attribute it
   already renders (`aria-label`, an existing `data-testid`).
2. Target it as `"[data-tour='new-workflow']"`.
3. Existing anchors still work: `data-onboarding='…'` (the old tour's),
   `data-testid`, and React Flow's own `.react-flow__node[data-id="…"]` /
   `.react-flow__handle.source[data-nodeid="…"]`.
4. A target that never appears doesn't trap the user. The spotlight keeps
   retrying for about 3 s, then shows its card anyway, with Skip step and End
   tour available. `blockOutside` blocks nothing while the target is missing.
5. `__tests__/tourAnchors.test.ts` fails when a selector a journey, phase or
   tip names is rendered nowhere, so a rename can't strand a step.

### The nav's anchors: one set, every shell

The left nav is data (`Layout/sidebar/navConfig/`), drawn by one renderer,
`Layout/sidebar/NavRenderer.tsx`. The signed-in app, the forge shell and the
public demo all draw it, inside the one shell (`Layout/shell/AppShell.tsx`), so
a step that rings the nav names the same anchor wherever it runs:

| Anchor | Where |
|---|---|
| `forge-console-nav` | the nav's top level (`nav-top`): the app's rows |
| `nav-drill-in` | an area's header ("‹ Observability") while the nav is drilled into it |
| `nav-overview`, `nav-observability`, `nav-deployments`, `nav-tests`, `nav-security`, `nav-configure` | one row or group each |

A nav item names its anchor with `tour: "<name>"` (`navConfig/types.ts`), and
`NavRenderer` renders it as `data-tour` (on a group, the whole block). Set it
only on an item a journey points at.

On an area's routes (Observability is a drill-in) the nav draws no `nav-top`
and no area row: a step that rings the nav there targets `nav-drill-in`, never
`forge-console-nav` alone. `tourAnchors.test.ts` pins every anchor in this
table, and `e2e/journey-forge.spec.ts` walks the demo's journey in the shell.

There is no anchor for a frame of the console's own: the console is a page of
the shell (`Forge/Console/ConsoleFrame.tsx`), in the app and on the demo host
alike. The old `[data-tour='forge-console'] aside` named the console's second
frame (`ConsoleShell`); no step targets it, and after #3574 nothing renders it.

## Signals (the event bus)

Signals ride the app's existing typed bus, `@/lib/events`. They are declared
in `journeys/signals.ts` as a declaration merge into `EventMap`. Emit them
through the thin wrapper, which initialises the bus on shells that never set
it up and isolates tour listeners from the emitter:

```ts
import { tourEvents } from "@/components/Onboarding/tourEvents";
tourEvents.emit("workflow:nodeAdded", { nodeId, type: "approval", anchor: "splice", spliceTarget: "merge" });
```

- **Emit unconditionally**, from the code that performs the action. Emitting
  is free when no step is listening, and the tour subscribes only while a step
  that uses signals is open.
- **To add a signal:** add it to `TourSignals` **and** `TOUR_SIGNAL_NAMES` (a
  compile-time check fails if they drift). Names follow the bus's
  `<area>:<camelCasePastTense>` pattern.
- Signals advance the tour only. Product analytics stay on `trackEvent`.

Declared today, matching journeys-spec §6: `workflow:draftCreated`,
`workflow:edgeConnected`, `workflow:nodeSelected`, `workflow:loopExpanded`,
`workflow:nodeAdded`, `workflow:triggerDeclared`, `workflow:saved`,
`automation:dialogOpened`, `automation:dialogClosed`,
`automation:machineChosen`, `askAgent:opened`, `askAgent:copied` and
`checklist:itemCompleted`. The last one is emitted by the checklist store; the
emit sites for the rest are the content work.

## Shells (Reliant and forge.reliantlabs.io)

The engine runs in both shells. The forge shell has no daemon, chat, settings
service or signed-in user. To run journeys there, call this once at boot,
before rendering:

```ts
import { setTourShell, setTourStorage, localStorageTourStorage } from "@/components/Onboarding/tourShell";
setTourShell("forge");                                    // the hostname forge.reliantlabs.io implies it too
setTourStorage(localStorageTourStorage("forge.tour.v1")); // instead of settings RPCs
```

Then mount `<JourneyTour />` (not `OnboardingWizard`). The picker lists only
the journeys whose `shells` include the current shell. Reliant-only side
effects are skipped when storage isn't the settings adapter:
- the checklist;
- the API-key prompt;
- the chat landing.

The forge brand does this in `routes.tsx` (`bootForgeTour`,
ForgeShell/forgeTourBoot.ts, at module load on the forge host), mounts
`<JourneyTour />` at its root in place of `OnboardingWizard`, and both brands
mount `/forge/demo` (and `/demo`) as public routes that leave for the demo
host, query kept (`Forge/Demo/demoHandoff.ts`): the demo has one home, so a
journey started there continues on the demo host's cut. A landing `?journey=` is handled
by ForgeShell/landingJourney.ts:
- `forge` starts the forge journey: at once for a signed-out visitor (on the
  public demo, instead of the sign-in redirect), on arrival for a signed-in
  one, and after sign-in for a visitor who came in through `/auth`.
- `workflows` never runs there; a card links to Reliant
  (`<APP_URL>/?journey=workflows`).

The public demo host (`demo.reliantlabs.io`, the `demo` shell,
`demoShellRoutes.tsx`) boots the tour as the forge shell does, but with
`memoryTourStorage()`: nothing is remembered. Its pages render in the app's
one shell with the demo's nav (`Forge/Demo/demoNavConfig.ts`), so its steps ring the
same anchors as the app's ("The nav's anchors", above). A bare entry **opens
on the Home** (`/forge/demo?section=home`), free to explore:
- The Home's hero and the action row under every page's header offer **Take
  the tour** (`TOUR_START`, `Forge/Demo/DemoScreenActions.tsx`). Nothing starts
  the tour by itself. (`DemoTourToast` has rendered nothing since #2334.)
- On the demo host the tour walks the **sample app's own console pages**
  (`/forge/p/shop/…`, the led console a visitor explores): each step's route is
  built per shell in `journeys/forge.tsx` (`stop`). The root route keeps
  `?tour=` across the console's links (`retainTourParam`), and an older
  `/forge/demo?view=…&tour=…` link is redirected to the console page it names
  (`consoleTourHref`), which is also where Reliant's and the forge shell's
  `/forge/demo` hand a tour off to.
- `Forge/Demo/tips` explains each page (below, "Page tours"). Its card opens by
  itself on a section's first visit this session; tips are off while the tour
  is open.

The forge journey runs its own cut there, chosen with `isAvailable`
(`lib/demoTour.ts`, runtime config `DEMO_TOUR`).

**Signed in, on either app host, it walks the sample app inside the shell**
(`/forge/p/sample_shop/…`, `Forge/Sample/sampleApp.ts`): the shell's real nav
and console pages, so the tour teaches the UI a person's own app gets. Its
nav steps spotlight the nav's own anchors (`tour: "nav-<section>"` on the nav
config's items, which NavRenderer renders as `data-tour`), the same set the
demo host's walk uses. The sample's data is its TransportScope's
(`fixtures/scope.tsx`): the shop pack, every id it mints prefixed `sample_`,
which is why the steps' trace, span and issue ids are read through `shown()`.
The name `sample_shop` cannot be a real forge app (names are
`[a-z][a-z0-9-]*`), the shell never remembers it as the last pick
(`shellProject.ts` `remembersPick`), and leaving the tour returns to where it
started (`returnToStart`). While the tour is live (`onLiveChange`, below) the
switcher lists "shop (sample)"; when it stops, the row goes and the sample's
cache is cleared. A signed-in `/forge/demo` or `/demo` link opens the page it
names there (`Forge/Sample/ForgeDemoEntry.ts`). Signed out, those links leave
for the demo host (`Forge/Demo/demoHandoff.ts`), where the demo's cut runs.

| Step | shell, signed in | an app host, signed out | demo, `short` (default) | demo, `full` |
|---|---|---|---|---|
| fg-intro (what forge is, Install forge) | ✓ | ✓ | ✓ | ✓ |
| fg-console (the console's sections in the nav) | | ✓ | | ✓ |
| fg-nav (the shell's nav, the app's sections) | ✓ | | | |
| fg-instrumented | ✓ | ✓ | ✓ | ✓ |
| fg-deployments (Deployments ▾, on Releases) | ✓ | | | |
| fg-trace | ✓ | ✓ | ✓ | ✓ |
| fg-hops (between services) | ✓ | ✓ | ✓ | ✓ |
| fg-inside (inside a service) | | | | ✓ |
| fg-logs | ✓ | ✓ | ✓ | ✓ |
| fg-profile | | ✓ | | ✓ |
| fg-graph | | | | ✓ |
| fg-issue | ✓ | ✓ | ✓ | ✓ |
| fg-oncall | ✓ | ✓ | | ✓ |
| fg-agent (what a coding agent reads) | | | | ✓ |
| fg-ask | ✓ | ✓ | ✓ | ✓ |
| fg-analytics (Analytics ▾, on Product overview) | ✓ | | ✓ | ✓ |
| fg-data (Data, on staging: prod's reads ask a reason) | ✓ | | | ✓ |
| fg-tests (Tests ▾) | ✓ | | | |
| fg-security (Security ▾) | ✓ | | | |
| fg-configure (Configure ▾, on Secrets) | ✓ | | | |
| fg-built (the recorded forge run, a wide modal card, `card: { wide, modal }`) | | | ✓ | ✓ |
| fg-done | ✓ | ✓ | ✓ | ✓ |
| **steps** | 16 | 11 | 10 | 17 |

fg-built is the demo's last step before the end card. It does not replace
fg-instrumented, which runs in every cut; the short cut leaves out fg-agent
instead (fg-built's card says what forge gives an agent, and fg-ask hands the
issue to one), and gives fg-inside's place to fg-analytics, so it stays at ten.

On the demo host every console step names `env=prod`, the story's environment:
the console remembers a page's scope (`Forge/Console/envScope.ts`), so
fg-data's staging, or a visitor's dev, would otherwise carry into the steps
after it.

The short cut takes the best moments only; each view's tip covers the rest.
`forgeJourney.test.tsx` pins it. `finish` returns null on the demo host, so
the visitor stays on the console.

### Page tours (the demo)

The public demo gives each page one compact, non-modal explanation: what it
is, why it helps, and how to use it (2–3 sentences). Done closes it and leaves an
accessible `?` restore button. Secondary agent help is a click/tap/keyboard
disclosure, not hover-only. Deploy yours free is primary (the only button on a phone), Install forge and Sign up free secondary.
There is no global toast or automatic local multi-stop sequence.

A section's first visit this session opens that page's explanation by itself
on a laptop; a reload, coming back, or a sub-page does not (a phone gets a
closed "About this page" disclosure instead). `DemoTips` owns `PageEntryTour`,
with Done (and "How an agent helps", where the page has no agent card of its
own) inside its callout. There is no full-site
sequence or prerequisite action. `?` replays it. Same-page filters, polling and
rerenders do not restart it. Legacy `/forge/demo?view=logs&tour=forge:fg-logs`
explains Logs.
Phone callouts use a bottom sheet after the drawer closes; their targets scroll
into view above it. No auth or modal is required to explore.

Copy and a single optional anchor live in `tips/tipCopy.ts`; page mapping is
`tips/tipKeys.ts`: every page the demo's nav lists has a tip (the pages it does
not serve, `DEMO_HIDDEN_PAGES`, have none). `DemoTipsAndToast.test.tsx` pins automatic entry, Done /
restore, current-page legacy links, real navigation and plain-language copy.

| Event | Props |
|---|---|
| `demo_tip_shown`, `demo_tip_dismissed`, `demo_tips_exited` | `tip` |
| `demo_page_tour_started`, `demo_page_tour_completed` | `tip` |
| `demo_page_tour_exited` | `tip, at` (the stop it was on) |
| `demo_cta_clicked` | `cta: signup\|install, placement: banner\|corner\|end_card\|page_card\|toast\|tour_intro` |

## Analytics (through `trackEvent`)

| Event | Props |
|---|---|
| `journey_selected` | `journey, source: landing\|picker\|menu\|demo, version` (+ `from_journey` from the end card, `resumed`) |
| `journey_step_viewed` | `journey, step, version, step_index, total_steps` |
| `journey_step_completed` | `journey, step, version, via: action\|next\|skip, duration_ms, signal?` |
| `journey_completed` | `journey, version, duration_ms` |
| `journey_skipped` | `journey, step, version` (`journey: "none", step: "picker"` when the picker is dismissed) |
| `journey_offered` | `journey, version, reason: version_bump` |
| `onboarding_completed` | Also carries `journey`. The tour sends it at Finish; the setup flow sends the landing journey. |
| `tour_deferred_mobile` | — |

## Testing your journey

- `__tests__/journeys.registry.test.ts` validates every journey at import.
  Bad ids, duplicates, or a click without a target fail the whole suite.
- Drive the engine in a unit test with a memory router at
  `/?tour=<journey>:<step>`, emit signals with `tourEvents.emit`, and assert the
  `tour` param (see `__tests__/journeyEngine.test.tsx`).

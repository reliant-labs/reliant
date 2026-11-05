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
| `journeys/registry.ts` | Validation, ordering, `?tour=` resolution. |
| `types.ts` | `Journey`, `JourneyStep`, `AdvanceOn`, … The schema below is defined here. |
| `advanceOn.ts` | Required actions (`advanceOn`) and `phases`. |
| `useTourNavigation.ts` | Moving between steps, finishing, skipping, analytics. |
| `JourneyTour.tsx` | The engine UI (picker and step). Shell-neutral. |
| `OnboardingWizard.tsx` | Reliant's wrapper: auth gates, the version re-offer, the checklist. |
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
  elsewhere: { label: "Try AI workflows in Reliant", href: () => `${reliantAppOrigin()}/?journey=workflows` }, // lib/shell.ts: runtime config APP_URL
  isAvailable: () => true,          // e.g. a feature flag
  reshowAfterVersionBump: true,     // default true
};
```

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

## A step (`JourneyStep`)

| Field | Meaning |
|---|---|
| `id` | kebab-case, **unique across all journeys**, prefixed `wf-` / `fg-`. Appears in the URL and in analytics. Don't rename a shipped id. |
| `title`, `description` | Card copy. `description` is a ReactNode. |
| `target` | CSS selector to spotlight. Prefer `[data-tour='…']`; see "Anchors". No target (and no `targets`) means a centred card. |
| `targets` | Multi-spotlight: several labelled targets (`{selector, label, description}`). |
| `type` | `spotlight` / `multi-spotlight` / `card`. Inferred from `target` / `targets`. |
| `tooltipPosition`, `tooltipPadding`, `spotlightConfig` | Placement. Keep the card off its target; `auto` already picks a free side. |
| `copyByShell` | `{ forge: { description: … } }`: per-shell title / description / target. |
| `shell` | `"reliant"` / `"forge"` / `"both"` (default). A step that doesn't match is left out. |
| `isAvailable` | The step's `when`: `() => boolean`, read at navigation time. False leaves the step out (Next / Back skip it and it isn't counted). |
| `route` | The page the step lives on; see below. |
| `phases` | Sub-targets within one step; see below. |
| `advanceOn` | The step's **required action**; see below. |
| `autoAdvance` | Once the action is done, move on by itself after 600 ms. Default false: show a check and Next. |
| `afterAction` | Extra copy appended once the action is done ("Copy something to try it."). |
| `actionHint` | Nav-bar text while waiting ("Drag Review to the loop"). Announced to screen readers. |
| `showNext` | Keep Next visible while the action is pending. Default false. |
| `blockOutside` | While pending, block pointer clicks outside the target (spotlight steps). Keyboard and screen readers are unaffected. Off by default; the spotlight is normally click-through. |
| `advanceIfAlreadyDone` | If the state already holds on entry, advance at once. |
| `skip` | `{ label?, run? }`. Overrides **Skip step** for a step that later steps depend on (W1: "Create it for me" seeds the draft). If `run` throws, the user stays on the step; show your own toast. `run` gets `{context, setContext}`. |
| `signal` | Analytics name for the action (`draft_created`). Sent as `signal` on `journey_step_completed` when the action completed the step. |
| `onEnter` | Runs once when the step opens (e.g. `layoutActions.showSidebar`). |
| `onComplete` | Runs once when the user leaves the step forward: Next, the action, Skip step or Finish. It doesn't run on Back or End tour. |

Every card has **Skip step** (until the action is done) and **End tour**.
- End tour records the journey as skipped at this version, and remembers the
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
of them is met. Until one is met, Next is hidden, the nav bar shows
`actionHint` plus **Skip step**, and Enter/arrow-key navigation is off (the
user is typing into the app).

| Condition | Kind | Fires when |
|---|---|---|
| `{ kind: "click", selector? }` | edge | The element (default: the step's `target`) is clicked. Capture phase, deferred one tick, so the click's own navigation runs first. |
| `{ kind: "event", event, where? }` | edge | A tour signal is emitted, optionally narrowed by `where(payload)`. |
| `{ kind: "route", path }` | level | The pathname matches: a prefix string or a RegExp. |
| `{ kind: "checklist", item }` | level | An onboarding-checklist item is complete. Reliant only; the checklist's own detectors (`onboardingChecklistStore.subscribeToStoreChanges`) set it, e.g. `create-workflow` when a user-source workflow exists. |
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

The workflows outro tours the chat and its panels, then tidies them away:

```ts
outro: [{
  id: "wf-outro-layout",
  shell: "reliant",
  title: "Back to the chat",
  target: "[data-onboarding='chat-input']",
  description: "Conversations on the left, files on the right, the terminal below. Each opens from its button and closes the same way.",
  route: { path: "/", open: { to: "/" }, label: "Open the chat" },
  onComplete: layoutActions.collapseAll,
}],
```

`layoutActions` acts through each panel's existing owner and persists the
change exactly as the user's own toggle would:

| Action | Owner it uses |
|---|---|
| `showSidebar` / `hideSidebar` | `workspaceStateStore.setLeftSidebarExpandedGlobal` |
| `showTerminal` / `hideTerminal` | `terminalStore.showTerminal` / `hideTerminal` |
| `showFiles` / `hideFiles` | ModernApp's file browser, through its `open-file-browser` / `close-file-browser` window events |
| `collapseAll` | All three |

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

The forge shell does this in `forgeShellRoutes.tsx` (`bootForgeTour`,
ForgeShell/forgeTourBoot.ts), mounts `<JourneyTour />` at its root, and mounts
`/forge/demo` (and `/demo`) as public routes. A landing `?journey=` is handled
by ForgeShell/landingJourney.ts:
- `forge` starts the forge journey: at once for a signed-out visitor (on the
  public demo, instead of the sign-in redirect), on arrival for a signed-in
  one, and after sign-in for a visitor who came in through `/auth`.
- `workflows` never runs there; a card links to Reliant
  (`<APP_URL>/?journey=workflows`).

## Analytics (through `trackEvent`)

| Event | Props |
|---|---|
| `journey_selected` | `journey, source: landing\|picker\|menu, version` (+ `from_journey` from the end card, `resumed`) |
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

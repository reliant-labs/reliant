/**
 * Onboarding Types
 *
 * Types for the guided tour + achievement-based checklist onboarding system.
 */

import type { JourneyId } from "./tourParam";
import type { TourEventMap, TourEventName } from "./tourEvents";
import type { TourShell } from "./tourShell";

export type { JourneyId } from "./tourParam";
export type { TourShell } from "./tourShell";

// ─── Journey Types ───────────────────────────────────────────────────────────
// A journey is a named, versioned, ordered list of steps. Content is a data
// declaration per journey (journeys/<id>.tsx); the engine (JourneyTour,
// useTourNavigation, advanceOn.ts) renders and walks it. See JOURNEYS.md.

/**
 * How the step is drawn. Default: "spotlight" when `target` is set,
 * "multi-spotlight" when `targets` is set, otherwise "card".
 */
export type JourneyStepType = "spotlight" | "multi-spotlight" | "card";

/** Free-form per-journey state a step records and later steps read (e.g. `{ workflowSlug }`). */
export type JourneyContext = Record<string, unknown>;

/** A navigation target. */
export interface RouteTarget {
  to: string;
  params?: Record<string, string>;
  search?: Record<string, unknown>;
}

/**
 * The page a step lives on. Moving to the step (Next, Back, a skip) goes
 * there; arriving any other way off that page shows an "Open <page>" card
 * instead of an empty spotlight, and the user clicks it.
 */
export interface JourneyRoute {
  /** Pathname prefix the step needs, e.g. "/workflows/library" or "/workflow/". */
  path: string;
  /**
   * Where to go: static, or built from the journey context (a slug a
   * previous step recorded). Its `search` is merged into the current search
   * when already on `path` — e.g. `{ view: "traces" }`. Default `{ to: path }`.
   */
  open?: RouteTarget | ((context: JourneyContext) => RouteTarget | null);
  /** The Open button's label, e.g. "Open Workflows". */
  label: string;
  /** The card's message. Defaults to "<label> to continue this step." */
  message?: string;
}

/** What a `when` predicate sees. Re-evaluated on every route change, signal and context write. */
export interface AdvanceContext {
  location: { pathname: string; search: Record<string, unknown> };
  /** The last payload of each tour signal emitted since the step opened. */
  signals: Partial<TourEventMap>;
  /** The journey's context (JourneyContext). */
  context: JourneyContext;
}

type EventAdvance = {
  [K in TourEventName]: {
    kind: "event";
    event: K;
    /** Narrow which emits count; omitted = every emit of `event`. */
    where?: (payload: TourEventMap[K]) => boolean;
  };
}[TourEventName];

/**
 * A condition that completes a step's required action.
 *
 * click / event fire on the action itself (edge). route / checklist /
 * store / element / when are states (level): reaching the state while the
 * step is open satisfies it; a state that already holds when the step opens
 * also satisfies it (Next shows at once) but never auto-advances, unless
 * `advanceIfAlreadyDone`.
 */
export type AdvanceOn =
  /** The element matching `selector` (default: the step's target) was clicked. */
  | { kind: "click"; selector?: string }
  /** A tour signal was emitted (journeys/signals.ts). */
  | EventAdvance
  /** The pathname matched: a prefix by default, or a RegExp. */
  | { kind: "route"; path: string | RegExp }
  /** An onboarding-checklist item is complete (onboardingChecklistStore; Reliant only). */
  | { kind: "checklist"; item: ChecklistItemId }
  /** Any store state: build with `whenStore(store, selector)` (advanceOn.ts). */
  | { kind: "store"; test: () => boolean; subscribe: (onChange: () => void) => () => void }
  /** An element matching `selector` is in the DOM. */
  | { kind: "element"; selector: string }
  /**
   * Anything else, combined freely: search params, a signal AND a route, a
   * signal then another (`signals.a && signals.b`), the journey context.
   */
  | { kind: "when"; test: (ctx: AdvanceContext) => boolean };

/** What a step's target/copy is, for drawing. A phase overrides the step's fields. */
export interface StepPresentation {
  title?: string;
  description?: React.ReactNode;
  target?: string;
  targets?: SpotlightTarget[];
  tooltipPosition?: "top" | "bottom" | "left" | "right" | "auto";
}

/**
 * A sub-target inside one step: e.g. (a) the New button, then (b) the dialog
 * it opens. The active phase is the last one whose `when` has been met (it
 * latches; a state already true on entry counts). Phase 0 needs no `when`.
 */
export interface JourneyPhase extends StepPresentation {
  when?: AdvanceOn;
}

/** Helpers a step's skip handler gets. */
export interface StepSkipContext {
  context: JourneyContext;
  setContext: (patch: JourneyContext) => void;
}

export interface JourneyStep extends StepPresentation {
  /**
   * kebab-case, unique across ALL journeys (so a bare `?tour=<id>` resolves),
   * conventionally prefixed `wf-` / `fg-`. Appears in the URL and analytics.
   */
  id: string;
  type?: JourneyStepType;
  title: string;
  description: React.ReactNode;
  tooltipPadding?: number;
  spotlightConfig?: SpotlightConfig;
  /**
   * A card step (no target) drawn as the step's own view: `wide` for content
   * that needs room (a player, a table), `modal` to hold the page still behind
   * it (scroll locked, clicks on the backdrop stopped) so nothing on the page
   * moves out from under the step. Default: a narrow, non-modal card.
   */
  card?: { wide?: boolean; modal?: boolean };
  /** Per-shell overrides of title / description / target. */
  copyByShell?: Partial<Record<TourShell, StepPresentation>>;
  /** Shells the step appears in. Default both. */
  shell?: TourShell | "both";
  /** The page the step needs; see JourneyRoute. Omitted = any page. */
  route?: JourneyRoute;
  /** Sub-targets inside the step; see JourneyPhase. */
  phases?: JourneyPhase[];
  /**
   * The step's required action. An array means any one of them. Until one
   * is met, Next is hidden (see `showNext`); then it shows with a check, or
   * the tour moves on by itself after 600 ms with `autoAdvance`.
   */
  advanceOn?: AdvanceOn | AdvanceOn[];
  /** Move on by itself 600 ms after the action. Default false: the user clicks Next. */
  autoAdvance?: boolean;
  /** Appended to the description once the action is met (e.g. "Copy something to try it."). */
  afterAction?: React.ReactNode;
  /** What the nav bar says while waiting, e.g. "Click New workflow". */
  actionHint?: string;
  /** Keep Next visible while the action is pending. Default false. */
  showNext?: boolean;
  /**
   * Block clicks outside the target while the action is pending, so the
   * only way forward is the action (or Skip step / End tour). Keyboard
   * focus and screen readers are unaffected. Spotlight steps only.
   */
  blockOutside?: boolean;
  /** When the state already holds on entry, advance at once instead of enabling Next. */
  advanceIfAlreadyDone?: boolean;
  /**
   * What "Skip step" does when later steps depend on this one (e.g. "Create
   * it for me" seeds the draft). `run` throwing keeps the user on the step:
   * show your own error (a toast) — the engine just doesn't move on.
   */
  skip?: { label?: string; run?: (ctx: StepSkipContext) => void | Promise<void> };
  /** `signal` prop on journey_step_completed when the action completed it (e.g. "draft_created"). */
  signal?: string;
  /**
   * Runs once when the step opens (e.g. expand the sidebar its target sits
   * in). Layout helpers: journeys/layoutActions.ts.
   */
  onEnter?: () => void;
  /**
   * Runs once when the user leaves the step forward — Next, its action,
   * Skip step or Finish (not Back, not End tour). E.g. collapse the panels
   * the step just explained.
   */
  onComplete?: () => void;
  /**
   * The step's `when`: whether it belongs in this user's journey right now
   * (a flag, the shell, a target that can exist). False leaves it out
   * entirely: skipped by Next/Back and not counted. Read at navigation time,
   * so it must be synchronous.
   */
  isAvailable?: () => boolean;
}

export interface Journey {
  id: JourneyId;
  /**
   * Bump when the steps change meaningfully. Completion is recorded per
   * version (`onboarding.tour.<id>.v<version>.completed`), so a bump re-offers
   * the journey once to users who completed an older version.
   */
  version: number;
  /** Re-offer this version to users who completed an older one. Default true. */
  reshowAfterVersionBump?: boolean;
  /** Picker card title, e.g. "Use AI workflows". */
  title: string;
  /** Picker card description. */
  description: string;
  steps: JourneyStep[];
  /**
   * Steps walked after `steps`, once the journey proper is done — e.g. the
   * workflows journey's tour of the chat layout, which then tidies it away.
   * Usually gated per step (`shell: "reliant"`, `isAvailable`); when none
   * applies the journey simply ends on its last step. The end card (Finish,
   * Try the other journey) sits on whichever step is last.
   */
  outro?: JourneyStep[];
  /** Shells the journey runs in. Default both. */
  shells?: TourShell[];
  /** Whether the journey can run for this user (e.g. a feature flag). Default yes. */
  isAvailable?: () => boolean;
  /** Where Finish lands, per shell. Default: Reliant's chat (and nothing in the forge shell). */
  finish?: (shell: TourShell) => RouteTarget | null;
  /**
   * How to reach this journey from a shell it doesn't run in: the end card's
   * "Try the other journey" becomes this external link (e.g. the forge shell
   * → "Try AI workflows in Reliant").
   */
  elsewhere?: { label: string; href: () => string };
  /**
   * How a step's controls are drawn.
   *  - "bar" (default): the bottom nav bar (progress, Back, Skip step, Next, End tour).
   *  - "callout": no bar. Each card carries its own small controls: the step
   *    count, Exit tour, and Next only on a step with no required action. A
   *    required action moves the tour on as soon as it's done (`autoAdvance`
   *    defaults to true; set it false to hold the step, e.g. to read what
   *    the action opened). No Skip step, no Back button (← still goes back).
   */
  controls?: "bar" | "callout";
}

/** How a step was completed, for `journey_step_completed.via`. */
export type StepCompletionVia = "action" | "next" | "skip";

// ─── Spotlight Types ─────────────────────────────────────────────────────────

export interface SpotlightConfig {
  /** Padding between the element and the spotlight cutout/border (default: 8) */
  padding?: number;
  /** Border radius for the spotlight (default: auto-detect from element) */
  borderRadius?: number | "auto" | "none";
  /** Whether to detect border-radius from the element's computed style (default: true) */
  detectBorderRadius?: boolean;
}

export interface SpotlightTarget {
  /** CSS selector for the target element */
  selector: string;
  /** Label shown in the tooltip for this target */
  label: string;
  /** Short description for this target */
  description?: string;
  /** Icon component to display */
  icon?: React.ReactNode;
  /** Spotlight config for this specific target */
  spotlightConfig?: SpotlightConfig;
}

// ─── Checklist Types ─────────────────────────────────────────────────────────

export type ChecklistItemId =
  | "add-api-key"
  | "start-chat"
  | "use-custom-workflow"
  | "create-workflow"
  | "take-product-tour"
  | "create-workspace"
  | "create-preset"
  | "install-mcp"
  | "read-docs";

export type ChecklistCategory = "required" | "bonus";

export type ChecklistAction =
  | "navigate"
  | "external-link"
  | "open-modal"
  | "focus-input";

export interface ChecklistItem {
  id: ChecklistItemId;
  title: string;
  description: string;
  category: ChecklistCategory;
  /** What happens when the user clicks the action button */
  action: ChecklistAction;
  /** URL for external links, or route/target identifier for navigation */
  actionTarget?: string;
  /** Button label for the action */
  actionLabel: string;
}

// ─── Store Types ─────────────────────────────────────────────────────────────

export type ChecklistPanelState = "collapsed" | "expanded" | "dismissed";
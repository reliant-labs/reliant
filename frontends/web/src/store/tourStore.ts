/**
 * Tour Store
 *
 * Owns the post-onboarding journeys' *persistence* — completion flag, per-step
 * completed/skipped sets, per-journey versioned outcomes, the chosen journey.
 * It has ZERO navigation and ZERO router access. The active journey and step
 * live in the URL as `?tour=<journey>:<step>`; anything that needs to know
 * whether the tour is active reads the URL, not this store.
 *
 * The wizard UI derives the active step from `useSearch({ strict: false })`
 * via `useTourNavigation()`. Step components observe the current pathname and
 * either render their spotlight (right page) or a "navigate here to continue"
 * modal (wrong page). The user owns the URL.
 */

import { create } from "zustand";
import { logger } from "../lib/logger";
import { trackEvent } from "../lib/analytics";
import { getTourStorage } from "../components/Onboarding/tourShell";
import {
  TOUR_SETTINGS_KEYS,
  journeyCompletionKey,
} from "../components/Onboarding/constants";
import { JOURNEY_IDS, isJourneyId, type JourneyId } from "../components/Onboarding/tourParam";
import type { JourneyContext } from "../components/Onboarding/types";

// ─── Journey records ──────────────────────────────────────────────────────────
// Each journey records its outcome per VERSION under
// `onboarding.tour.<journey>.v<version>.completed`, so changing a journey's
// steps (and bumping its version) re-offers it to users who completed an
// older one — once: the re-offer itself records "shown" at the new version.
// The retired single tour's `onboarding.completed` counts as workflows v0.

/** What happened at a journey version. Any value means "don't auto-offer again". */
export type JourneyOutcome = "completed" | "skipped" | "shown";

export interface JourneyRecord {
  /** The journey's current version, as declared by its content. */
  version: number;
  /** The outcome recorded at `version`, if any. */
  current: JourneyOutcome | null;
  /** The user completed an older version (for workflows: or the retired single tour). */
  completedOlder: boolean;
}

const emptyRecords = (): Record<JourneyId, JourneyRecord> =>
  Object.fromEntries(
    JOURNEY_IDS.map((id) => [id, { version: 1, current: null, completedOlder: false }]),
  ) as Record<JourneyId, JourneyRecord>;

function parseOutcome(value: string | undefined): JourneyOutcome | null {
  return value === "completed" || value === "skipped" || value === "shown" ? value : null;
}

// ─── Store Interface ──────────────────────────────────────────────────────────

export interface TourState {
  /** `<journey>:<step>` keys. */
  completedSteps: Set<string>;
  /** `<journey>:<step>` keys. */
  skippedSteps: Set<string>;
  /**
   * Some journey (or the picker) was finished or skipped. The legacy flag
   * the rest of the app reads to mean "the tour is out of the way".
   */
  hasCompletedOnboarding: boolean;
  journeys: Record<JourneyId, JourneyRecord>;
  /** The journey the user last chose. */
  lastJourney: JourneyId | null;
  /** `<journey>:<step>` the user ended the tour on (End tour), for "Resume". */
  resumeStep: string | null;
  /** Per-journey state steps record and later steps read (e.g. the draft's slug). */
  contexts: Partial<Record<JourneyId, JourneyContext>>;
  projectHasCode: boolean | null;
  isInitialized: boolean;
  isLoading: boolean;

  loadState: () => Promise<void>;
  completeStep: (stepKey: string) => Promise<void>;
  skipStep: (stepKey: string) => Promise<void>;
  /**
   * Mark every listed step not yet completed or skipped as skipped, in memory
   * WITHOUT persisting. Callers batch this with a single trailing save — used
   * by "Skip tour" so we don't re-persist the whole tour state once per step.
   */
  markRemainingSkipped: (stepKeys: string[]) => void;
  /**
   * End the tour: sets the flag and persists. With a journey, also records
   * `outcome` at the journey's current version.
   */
  markTourCompleted: (journey?: JourneyId, outcome?: Exclude<JourneyOutcome, "shown">) => Promise<void>;
  /** Record that a journey was auto-offered at its current version, so it is never offered again. */
  markJourneyShown: (journey: JourneyId) => Promise<void>;
  /** Remember the journey the user chose. */
  setLastJourney: (journey: JourneyId) => Promise<void>;
  /** Remember (or, with null, forget) where the user ended the tour. */
  setResumeStep: (stepKey: string | null) => Promise<void>;
  /** Merge into a journey's context, and persist it. */
  setJourneyContext: (journey: JourneyId, patch: JourneyContext) => void;
  /**
   * Queue a coalesced background save. Returns immediately — callers on a UI
   * path (step transitions) must not block on settings RPCs. Terminal
   * transitions still call `saveTourState()` directly so the write is awaited.
   */
  scheduleSave: () => void;
  /** Reset all per-step progress so the tour can be restarted from scratch. */
  resetTourProgress: () => Promise<void>;
  saveTourState: () => Promise<void>;
  /**
   * Forget this user's tour in memory, WITHOUT persisting: cancel the queued
   * save, clear progress and contexts, and make in-flight loads and saves
   * started before it drop their result. Called on an identity change.
   */
  reset: () => void;
  detectProjectCode: () => Promise<void>;
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// ─── Coalesced background persistence ────────────────────────────────────────
// A step transition used to AWAIT the full save before the UI advanced, and a
// save is three settings RPCs (completed flag, completed set, skipped set).
// Measured 2026-08-26 21:45:58: three UpdateSetting calls sat in flight at 1s
// each behind a backend stalled ~19s on mcp.ensure_loaded, so every Next click
// paid that latency before the next step painted — the "crazy slow" the user
// hit stepping between 3 and 4, and it multiplied each time they went back.
//
// The step sets are recomputed whole on every save, so a save in flight is
// worth nothing once another transition has happened: collapse rapid
// transitions into one trailing write instead of N×3 RPCs. In-memory state
// still updates synchronously, which is what the UI actually renders from.
const SAVE_DEBOUNCE_MS = 400;
let _saveTimer: ReturnType<typeof setTimeout> | null = null;

function cancelPendingSave(): void {
  if (_saveTimer !== null) {
    clearTimeout(_saveTimer);
    _saveTimer = null;
  }
}

// Bumped by reset() (an identity change: authStore): a load started for the
// previous user drops its answer instead of installing it, and a save or
// terminal transition started for them writes nothing under the next one.
let generation = 0;

/** Test-only: drop any scheduled save so state can't leak across cases. */
export function __resetTourSaveScheduler(): void {
  cancelPendingSave();
}

async function detectHasCode(): Promise<boolean> {
  try {
    const { getFileTree } = await import("../api/fileSystem");
    const files = await getFileTree("/", false);
    const ignoreDirs = new Set([".git", ".reliant", "node_modules", ".vscode", ".idea"]);
    const codeFiles = files.filter((f) => !ignoreDirs.has(f.name));
    return codeFiles.length > 1;
  } catch {
    return true;
  }
}

// ─── Store ────────────────────────────────────────────────────────────────────

export const useTourStore = create<TourState>((set, get) => ({
  completedSteps: new Set(),
  skippedSteps: new Set(),
  hasCompletedOnboarding: false,
  journeys: emptyRecords(),
  lastJourney: null,
  resumeStep: null,
  contexts: {},
  projectHasCode: null,
  isInitialized: false,
  isLoading: false,

  loadState: async () => {
    if (get().isLoading) return;
    set({ isLoading: true });
    const startedIn = generation;

    // Versions come from the journey content. Imported lazily so this store
    // never pulls journey content (and whatever it imports) into its static
    // graph — content importing a store that imports this one would cycle.
    const { JOURNEYS } = await import("../components/Onboarding/journeys/registry");
    const versionKeys = JOURNEY_IDS.flatMap((id) =>
      Array.from({ length: JOURNEYS[id].version }, (_, i) => ({
        id,
        version: i + 1,
        key: journeyCompletionKey(id, i + 1),
      })),
    );

    const [
      tourCompletedSetting,
      completedStepsSetting,
      skippedStepsSetting,
      lastJourneySetting,
      resumeSetting,
      contextSetting,
      legacyCurrentStepSetting,
      ...versionSettings
    ] = await Promise.all([
      getTourStorage().get(TOUR_SETTINGS_KEYS.COMPLETED),
      getTourStorage().get(TOUR_SETTINGS_KEYS.COMPLETED_STEPS),
      getTourStorage().get(TOUR_SETTINGS_KEYS.SKIPPED_STEPS),
      getTourStorage().get(TOUR_SETTINGS_KEYS.LAST_JOURNEY),
      getTourStorage().get(TOUR_SETTINGS_KEYS.RESUME),
      getTourStorage().get(TOUR_SETTINGS_KEYS.JOURNEY_CONTEXT),
      getTourStorage().get(TOUR_SETTINGS_KEYS.CURRENT_STEP),
      ...versionKeys.map(({ key }) => getTourStorage().get(key)),
    ]);

    const hasCompletedOnboarding = tourCompletedSetting === "true";

    let completedSteps = new Set<string>();
    if (completedStepsSetting) {
      try { completedSteps = new Set(JSON.parse(completedStepsSetting)); } catch { /* ignore */ }
    }

    let skippedSteps = new Set<string>();
    if (skippedStepsSetting) {
      try { skippedSteps = new Set(JSON.parse(skippedStepsSetting)); } catch { /* ignore */ }
    }

    const journeys = emptyRecords();
    for (const id of JOURNEY_IDS) journeys[id].version = JOURNEYS[id].version;
    // The retired single tour is workflows v0.
    if (hasCompletedOnboarding) journeys.workflows.completedOlder = true;
    versionKeys.forEach(({ id, version }, i) => {
      const outcome = parseOutcome(versionSettings[i]);
      if (version === journeys[id].version) journeys[id].current = outcome;
      else if (outcome === "completed") journeys[id].completedOlder = true;
    });

    const lastJourney = isJourneyId(lastJourneySetting) ? lastJourneySetting : null;
    const resumeStep = resumeSetting || null;
    let contexts: Partial<Record<JourneyId, JourneyContext>> = {};
    if (contextSetting) {
      try { contexts = JSON.parse(contextSetting) as typeof contexts; } catch { /* ignore */ }
    }

    // The identity changed while the settings were in flight: they are the
    // previous user's. reset() already cleared isLoading for the next load.
    if (startedIn !== generation) return;

    set({
      hasCompletedOnboarding,
      completedSteps,
      skippedSteps,
      journeys,
      lastJourney,
      resumeStep,
      contexts,
      isInitialized: true,
      isLoading: false,
    });

    // Backwards-compat: a previous version of this store persisted the
    // current step under TOUR_SETTINGS_KEYS.CURRENT_STEP. That value is no
    // longer the source of truth (the URL is). Delete it when it is still
    // there so the row doesn't linger forever — best-effort, swallow
    // failures. Only when it is there: the read above is served from the
    // settings cache, while an unconditional delete was an RPC on every
    // startup that answered NotFound for every user who no longer had it.
    if (legacyCurrentStepSetting !== undefined) {
      void getTourStorage().delete(TOUR_SETTINGS_KEYS.CURRENT_STEP).catch(() => {
        /* ignore */
      });
    }
  },

  // Per-step progress persists optimistically: the in-memory set is the
  // authority the UI renders from, and the write is a background detail. If
  // it loses a race with the tab closing, the user re-sees one tour step —
  // far cheaper than making every Next click wait on three RPCs.
  // Step analytics (journey_step_completed, …) are the navigation layer's
  // (journeyAnalytics.ts): only it knows how a step was completed.
  completeStep: async (stepKey: string) => {
    const newCompleted = new Set(get().completedSteps);
    newCompleted.add(stepKey);
    set({ completedSteps: newCompleted });
    get().scheduleSave();
  },

  skipStep: async (stepKey: string) => {
    const newSkipped = new Set(get().skippedSteps);
    newSkipped.add(stepKey);
    set({ skippedSteps: newSkipped });
    get().scheduleSave();
  },

  scheduleSave: () => {
    cancelPendingSave();
    _saveTimer = setTimeout(() => {
      _saveTimer = null;
      void get().saveTourState();
    }, SAVE_DEBOUNCE_MS);
  },

  markRemainingSkipped: (stepKeys: string[]) => {
    const { completedSteps, skippedSteps } = get();
    const newSkipped = new Set(skippedSteps);
    for (const key of stepKeys) {
      if (!completedSteps.has(key)) newSkipped.add(key);
    }
    set({ skippedSteps: newSkipped });
  },

  markTourCompleted: async (journey, outcome = "completed") => {
    trackEvent('tour_completed', {
      completed_count: get().completedSteps.size,
      skipped_count: get().skippedSteps.size,
    });
    set({ hasCompletedOnboarding: true });
    if (journey) {
      const record = get().journeys[journey];
      set({ journeys: { ...get().journeys, [journey]: { ...record, current: outcome } } });
      void getTourStorage().set(journeyCompletionKey(journey, record.version), outcome).catch(
        (error: unknown) => logger.error("[TourStore] Failed to save journey outcome", error),
      );
    }
    // Coordinate with the achievement checklist — the wizard's onComplete
    // path used to do this; keep it here so any caller that finishes the
    // tour (wizard hook or external) gets consistent side effects.
    // The checklist is Reliant's (settings-backed); a shell with its own
    // storage (the forge shell) has none to update.
    const startedIn = generation;
    if (getTourStorage().kind === "settings") try {
      const { useOnboardingChecklistStore } = await import("./onboardingChecklistStore");
      if (startedIn !== generation) return;
      void useOnboardingChecklistStore.getState().markComplete("take-product-tour");
      void useOnboardingChecklistStore.getState().markWelcomeShown();
    } catch {
      /* ignore */
    }
    // The identity changed while the import was pending: the store now holds
    // nobody's tour, and saving it would overwrite the next user's.
    if (startedIn !== generation) return;
    await get().saveTourState();
  },

  markJourneyShown: async (journey) => {
    const record = get().journeys[journey];
    if (record.current !== null) return;
    set({ journeys: { ...get().journeys, [journey]: { ...record, current: "shown" } } });
    try {
      await getTourStorage().set(journeyCompletionKey(journey, record.version), "shown");
    } catch (error) {
      logger.error("[TourStore] Failed to save journey offer", error);
    }
  },

  setLastJourney: async (journey) => {
    if (get().lastJourney === journey) return;
    set({ lastJourney: journey });
    try {
      await getTourStorage().set(TOUR_SETTINGS_KEYS.LAST_JOURNEY, journey);
    } catch (error) {
      logger.error("[TourStore] Failed to save last journey", error);
    }
  },

  setResumeStep: async (stepKey) => {
    if (get().resumeStep === stepKey) return;
    set({ resumeStep: stepKey });
    try {
      if (stepKey) await getTourStorage().set(TOUR_SETTINGS_KEYS.RESUME, stepKey);
      else await getTourStorage().delete(TOUR_SETTINGS_KEYS.RESUME);
    } catch (error) {
      logger.error("[TourStore] Failed to save resume step", error);
    }
  },

  setJourneyContext: (journey, patch) => {
    const contexts = { ...get().contexts, [journey]: { ...get().contexts[journey], ...patch } };
    set({ contexts });
    void getTourStorage()
      .set(TOUR_SETTINGS_KEYS.JOURNEY_CONTEXT, JSON.stringify(contexts))
      .catch((error: unknown) => logger.error("[TourStore] Failed to save journey context", error));
  },

  resetTourProgress: async () => {
    trackEvent('tour_restarted');
    const startedIn = generation;

    try {
      await getTourStorage().delete(TOUR_SETTINGS_KEYS.SKIPPED_ALL);
    } catch {
      /* ignore */
    }
    if (startedIn !== generation) return;

    set({
      completedSteps: new Set(),
      skippedSteps: new Set(),
      hasCompletedOnboarding: false,
      projectHasCode: null,
    });

    // The rest is Reliant's checklist and project; a shell with its own
    // storage (the forge shell) has neither.
    if (getTourStorage().kind !== "settings") {
      await get().saveTourState();
      return;
    }

    // Clear checklist state without clobbering its isInitialized flag —
    // consumers like ContextualTipsLayer gate on isInitialized.
    //
    // The panel state goes through revive() rather than this setState: a raw
    // in-memory write left the store saying "expanded" while the settings row
    // still said "dismissed", so the guide reappeared now and then vanished on
    // the next reload.
    //
    // Every await is followed by the generation check before anything that
    // touches the checklist or settings: a restart begun for user A must not
    // clear user B's checklist, or persist B's panel, once B is adopted while
    // A's import or revive is still pending.
    let checklist: typeof import("./onboardingChecklistStore").useOnboardingChecklistStore | undefined;
    try {
      ({ useOnboardingChecklistStore: checklist } = await import("./onboardingChecklistStore"));
    } catch {
      /* ignore */
    }
    if (startedIn !== generation) return;
    if (checklist) {
      checklist.setState({
        completedItems: new Set(),
        welcomeShown: false,
      });
      try {
        await checklist.getState().revive();
      } catch {
        /* ignore */
      }
    }
    if (startedIn !== generation) return;
    localStorage.removeItem("reliant.checklist.welcomeShown");
    localStorage.removeItem("reliant.checklist.completedItems");

    await get().saveTourState();
    await get().detectProjectCode();
  },

  saveTourState: async () => {
    // An explicit save supersedes anything queued — otherwise a trailing
    // debounce could fire after a terminal write with the same (or staler)
    // snapshot and spend three more RPCs saying nothing new.
    cancelPendingSave();
    const state = get();
    try {
      // These three keys are independent — persist them concurrently instead of
      // chaining awaits so a full tour save is one round-trip batch, not three.
      await Promise.all([
        getTourStorage().set(
          TOUR_SETTINGS_KEYS.COMPLETED,
          state.hasCompletedOnboarding ? "true" : "false",
        ),
        getTourStorage().set(
          TOUR_SETTINGS_KEYS.COMPLETED_STEPS,
          JSON.stringify(Array.from(state.completedSteps)),
        ),
        getTourStorage().set(
          TOUR_SETTINGS_KEYS.SKIPPED_STEPS,
          JSON.stringify(Array.from(state.skippedSteps)),
        ),
      ]);
    } catch (error) {
      logger.error("[TourStore] Failed to save tour state", error);
    }
  },

  detectProjectCode: async () => {
    const startedIn = generation;
    const hasCode = await detectHasCode();
    if (startedIn !== generation) return;
    set({ projectHasCode: hasCode });
  },

  reset: () => {
    generation++;
    cancelPendingSave();
    set({
      completedSteps: new Set(),
      skippedSteps: new Set(),
      hasCompletedOnboarding: false,
      journeys: emptyRecords(),
      lastJourney: null,
      resumeStep: null,
      contexts: {},
      projectHasCode: null,
      isInitialized: false,
      isLoading: false,
    });
  },
}));
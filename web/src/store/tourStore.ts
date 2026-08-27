/**
 * Tour Store
 *
 * Owns the post-onboarding tour's *persistence* — completion flag, per-step
 * completed/skipped sets. It has ZERO navigation and ZERO router access.
 * The active step lives in the URL as `?tour=<step-id>`; anything that needs
 * to know whether the tour is active reads the URL, not this store.
 *
 * The wizard UI derives the active step from `useSearch({ strict: false })`
 * via `useTourNavigation()`. Step components observe the current pathname and
 * either render their spotlight (right page) or a "navigate here to continue"
 * modal (wrong page). The user owns the URL.
 */

import { create } from "zustand";
import { logger } from "../lib/logger";
import { trackEvent } from "../lib/analytics";
import {
  safeGetSetting,
  upsertStringSetting,
  deleteSettingIfExists,
} from "../lib/settingsPersistence";
import { TOUR_SETTINGS_KEYS, ONBOARDING_STEPS } from "../components/Onboarding/constants";
import type { OnboardingStepId } from "../components/Onboarding/types";

// ─── Store Interface ──────────────────────────────────────────────────────────

interface TourState {
  completedSteps: Set<OnboardingStepId>;
  skippedSteps: Set<OnboardingStepId>;
  hasCompletedOnboarding: boolean;
  projectHasCode: boolean | null;
  isInitialized: boolean;
  isLoading: boolean;

  loadState: () => Promise<void>;
  completeStep: (stepId: OnboardingStepId) => Promise<void>;
  skipStep: (stepId: OnboardingStepId) => Promise<void>;
  /**
   * Mark every not-yet-completed, not-yet-skipped step as skipped in memory
   * (firing per-step analytics) WITHOUT persisting. Callers batch this with a
   * single trailing save — used by "Skip tour" so we don't re-persist the whole
   * tour state once per step.
   */
  markRemainingSkipped: () => void;
  /** Mark the tour as completed (sets flag + analytics + persists). */
  markTourCompleted: () => Promise<void>;
  /**
   * Queue a coalesced background save. Returns immediately — callers on a UI
   * path (step transitions) must not block on settings RPCs. Terminal
   * transitions still call `saveTourState()` directly so the write is awaited.
   */
  scheduleSave: () => void;
  /** Reset all per-step progress so the tour can be restarted from scratch. */
  resetTourProgress: () => Promise<void>;
  saveTourState: () => Promise<void>;
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
  projectHasCode: null,
  isInitialized: false,
  isLoading: false,

  loadState: async () => {
    if (get().isLoading) return;
    set({ isLoading: true });

    const [
      tourCompletedSetting,
      completedStepsSetting,
      skippedStepsSetting,
    ] = await Promise.all([
      safeGetSetting(TOUR_SETTINGS_KEYS.COMPLETED),
      safeGetSetting(TOUR_SETTINGS_KEYS.COMPLETED_STEPS),
      safeGetSetting(TOUR_SETTINGS_KEYS.SKIPPED_STEPS),
    ]);

    const hasCompletedOnboarding = tourCompletedSetting?.value === "true";

    let completedSteps = new Set<OnboardingStepId>();
    if (completedStepsSetting?.value) {
      try { completedSteps = new Set(JSON.parse(completedStepsSetting.value)); } catch { /* ignore */ }
    }

    let skippedSteps = new Set<OnboardingStepId>();
    if (skippedStepsSetting?.value) {
      try { skippedSteps = new Set(JSON.parse(skippedStepsSetting.value)); } catch { /* ignore */ }
    }

    set({
      hasCompletedOnboarding,
      completedSteps,
      skippedSteps,
      isInitialized: true,
      isLoading: false,
    });

    // Backwards-compat: a previous version of this store persisted the
    // current step under TOUR_SETTINGS_KEYS.CURRENT_STEP. That value is no
    // longer the source of truth (the URL is). Delete it on first load so
    // the row doesn't linger forever — best-effort, swallow failures.
    void deleteSettingIfExists(TOUR_SETTINGS_KEYS.CURRENT_STEP).catch(() => {
      /* ignore */
    });
  },

  // Per-step progress persists optimistically: the in-memory set is the
  // authority the UI renders from, and the write is a background detail. If
  // it loses a race with the tab closing, the user re-sees one tour step —
  // far cheaper than making every Next click wait on three RPCs.
  completeStep: async (stepId: OnboardingStepId) => {
    const newCompleted = new Set(get().completedSteps);
    newCompleted.add(stepId);
    set({ completedSteps: newCompleted });
    trackEvent('tour_step_completed', { step_id: stepId });
    get().scheduleSave();
  },

  skipStep: async (stepId: OnboardingStepId) => {
    const newSkipped = new Set(get().skippedSteps);
    newSkipped.add(stepId);
    set({ skippedSteps: newSkipped });
    trackEvent('tour_step_skipped', { step_id: stepId });
    get().scheduleSave();
  },

  scheduleSave: () => {
    cancelPendingSave();
    _saveTimer = setTimeout(() => {
      _saveTimer = null;
      void get().saveTourState();
    }, SAVE_DEBOUNCE_MS);
  },

  markRemainingSkipped: () => {
    const { completedSteps, skippedSteps } = get();
    const newSkipped = new Set(skippedSteps);
    for (const step of ONBOARDING_STEPS) {
      if (!completedSteps.has(step.id) && !newSkipped.has(step.id)) {
        newSkipped.add(step.id);
        trackEvent('tour_step_skipped', { step_id: step.id });
      }
    }
    set({ skippedSteps: newSkipped });
  },

  markTourCompleted: async () => {
    trackEvent('tour_completed', {
      completed_count: get().completedSteps.size,
      skipped_count: get().skippedSteps.size,
    });
    set({ hasCompletedOnboarding: true });
    // Coordinate with the achievement checklist — the wizard's onComplete
    // path used to do this; keep it here so any caller that finishes the
    // tour (wizard hook or external) gets consistent side effects.
    try {
      const { useOnboardingChecklistStore } = await import("./onboardingChecklistStore");
      void useOnboardingChecklistStore.getState().markComplete("take-product-tour");
      void useOnboardingChecklistStore.getState().markWelcomeShown();
    } catch {
      /* ignore */
    }
    await get().saveTourState();
  },

  resetTourProgress: async () => {
    trackEvent('tour_restarted');

    try {
      await deleteSettingIfExists(TOUR_SETTINGS_KEYS.SKIPPED_ALL);
    } catch {
      /* ignore */
    }

    set({
      completedSteps: new Set(),
      skippedSteps: new Set(),
      hasCompletedOnboarding: false,
      projectHasCode: null,
    });

    // Clear checklist state without clobbering its isInitialized flag —
    // consumers like ContextualTipsLayer gate on isInitialized.
    //
    // The panel state goes through revive() rather than this setState: a raw
    // in-memory write left the store saying "expanded" while the settings row
    // still said "dismissed", so the guide reappeared now and then vanished on
    // the next reload.
    try {
      const { useOnboardingChecklistStore } = await import("./onboardingChecklistStore");
      useOnboardingChecklistStore.setState({
        completedItems: new Set(),
        welcomeShown: false,
      });
      await useOnboardingChecklistStore.getState().revive();
    } catch {
      /* ignore */
    }
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
        upsertStringSetting(
          TOUR_SETTINGS_KEYS.COMPLETED,
          state.hasCompletedOnboarding ? "true" : "false",
        ),
        upsertStringSetting(
          TOUR_SETTINGS_KEYS.COMPLETED_STEPS,
          JSON.stringify(Array.from(state.completedSteps)),
        ),
        upsertStringSetting(
          TOUR_SETTINGS_KEYS.SKIPPED_STEPS,
          JSON.stringify(Array.from(state.skippedSteps)),
        ),
      ]);
    } catch (error) {
      logger.error("[TourStore] Failed to save tour state", error);
    }
  },

  detectProjectCode: async () => {
    const hasCode = await detectHasCode();
    set({ projectHasCode: hasCode });
  },
}));
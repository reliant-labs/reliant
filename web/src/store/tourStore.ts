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
  /** Reset all per-step progress so the tour can be restarted from scratch. */
  resetTourProgress: () => Promise<void>;
  saveTourState: () => Promise<void>;
  detectProjectCode: () => Promise<void>;
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

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

  completeStep: async (stepId: OnboardingStepId) => {
    const newCompleted = new Set(get().completedSteps);
    newCompleted.add(stepId);
    set({ completedSteps: newCompleted });
    trackEvent('tour_step_completed', { step_id: stepId });
    await get().saveTourState();
  },

  skipStep: async (stepId: OnboardingStepId) => {
    const newSkipped = new Set(get().skippedSteps);
    newSkipped.add(stepId);
    set({ skippedSteps: newSkipped });
    trackEvent('tour_step_skipped', { step_id: stepId });
    await get().saveTourState();
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
    try {
      const { useOnboardingChecklistStore } = await import("./onboardingChecklistStore");
      useOnboardingChecklistStore.setState({
        completedItems: new Set(),
        welcomeShown: false,
        panelState: "expanded",
      });
    } catch {
      /* ignore */
    }
    localStorage.removeItem("reliant.checklist.welcomeShown");

    await get().saveTourState();
    await get().detectProjectCode();
  },

  saveTourState: async () => {
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
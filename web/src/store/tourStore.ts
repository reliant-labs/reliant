/**
 * Tour Store
 *
 * Owns the guided-tour wizard state — the post-onboarding step-by-step
 * walkthrough. Coordinates with the checklist store for the
 * "take-product-tour" achievement.
 */

import { create } from "zustand";
import { logger } from "../lib/logger";
import { trackEvent } from "../lib/analytics";
import {
  safeGetSetting,
  upsertStringSetting,
  deleteSettingIfExists,
} from "../lib/settingsPersistence";
import {
  TOUR_SETTINGS_KEYS,
  ONBOARDING_STEPS,
  getNextStepId,
  getPreviousStepId,
  stepRequiresWorkflowMode,
  stepRequiresWorkflowBuilder,
  stepRequiresChatMode,
  stepRequiresSettingsMode,
} from "../components/Onboarding/constants";
import type { OnboardingStepId } from "../components/Onboarding/types";
import { useChatStore } from "./chatStore";
import { useViewerStore } from "./viewerStore";
import { useOnboardingChecklistStore } from "./onboardingChecklistStore";

// ─── Store Interface ──────────────────────────────────────────────────────────

interface TourState {
  isWizardActive: boolean;
  currentStepId: OnboardingStepId | null;
  completedSteps: Set<OnboardingStepId>;
  skippedSteps: Set<OnboardingStepId>;
  hasCompletedOnboarding: boolean;
  projectHasCode: boolean | null;
  isInitialized: boolean;
  isLoading: boolean;

  loadState: () => Promise<void>;
  startWizard: () => Promise<void>;
  resumeWizard: () => Promise<void>;
  closeWizard: () => void;
  goToStep: (stepId: OnboardingStepId) => void;
  completeStep: (stepId: OnboardingStepId) => Promise<void>;
  skipStep: (stepId: OnboardingStepId) => Promise<void>;
  skipAll: () => Promise<void>;
  nextStep: () => void;
  previousStep: () => void;
  restartWizard: () => Promise<void>;
  saveTourState: () => Promise<void>;
  detectProjectCode: () => Promise<void>;
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

async function promptApiKeyIfNeededAfterOnboarding(): Promise<void> {
  const { useApiKeySetupStore } = await import("./apiKeySetupStore");
  if (useApiKeySetupStore.getState().hasApiKey === true) return;
  useApiKeySetupStore.setState({ hasChecked: false });
  await useApiKeySetupStore.getState().ensureApiKeyOrShowModal();
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
  isWizardActive: false,
  currentStepId: null,
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
      currentStepSetting,
      completedStepsSetting,
      skippedStepsSetting,
    ] = await Promise.all([
      safeGetSetting(TOUR_SETTINGS_KEYS.COMPLETED),
      safeGetSetting(TOUR_SETTINGS_KEYS.CURRENT_STEP),
      safeGetSetting(TOUR_SETTINGS_KEYS.COMPLETED_STEPS),
      safeGetSetting(TOUR_SETTINGS_KEYS.SKIPPED_STEPS),
    ]);

    const hasCompletedOnboarding = tourCompletedSetting?.value === "true";
    const currentStep = currentStepSetting?.value as OnboardingStepId | undefined;

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
      currentStepId: currentStep || null,
      completedSteps,
      skippedSteps,
      isInitialized: true,
      isLoading: false,
    });
  },

  startWizard: async () => {
    set({
      isWizardActive: true,
      currentStepId: ONBOARDING_STEPS[0].id,
      completedSteps: new Set(),
      skippedSteps: new Set(),
      hasCompletedOnboarding: false,
    });
    trackEvent('tour_started');
    await get().saveTourState();
    await get().detectProjectCode();
  },

  resumeWizard: async () => {
    const state = get();
    if (state.currentStepId) {
      set({ isWizardActive: true });
    } else {
      await get().startWizard();
    }
  },

  closeWizard: () => {
    set({ isWizardActive: false });
    get().saveTourState();
  },

  goToStep: (stepId: OnboardingStepId) => {
    set({ currentStepId: stepId });
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

  skipAll: async () => {
    const viewerStore = useViewerStore.getState();
    if (viewerStore.isWorkflowMode) {
      viewerStore.setWorkflowMode(false);
    }

    trackEvent('tour_skipped_all', { completed_count: get().completedSteps.size });

    set({
      isWizardActive: false,
      currentStepId: null,
    });

    void useOnboardingChecklistStore.getState().markComplete("take-product-tour");
    void useOnboardingChecklistStore.getState().markWelcomeShown();

    await upsertStringSetting(TOUR_SETTINGS_KEYS.SKIPPED_ALL, "true");
    await get().saveTourState();

    useChatStore.getState().clearCurrentChat();
    void promptApiKeyIfNeededAfterOnboarding();
  },

  nextStep: () => {
    const state = get();
    if (!state.currentStepId) return;

    const nextId = getNextStepId(state.currentStepId);
    if (nextId) {
      set({ currentStepId: nextId });
      get().saveTourState();
    } else {
      const viewerStore = useViewerStore.getState();
      viewerStore.setSettingsMode(false);
      viewerStore.setWorkflowMode(false);

      trackEvent('tour_completed', {
        completed_count: get().completedSteps.size,
        skipped_count: get().skippedSteps.size,
      });

      set({
        isWizardActive: false,
        hasCompletedOnboarding: true,
        currentStepId: null,
      });

      void useOnboardingChecklistStore.getState().markComplete("take-product-tour");
      void useOnboardingChecklistStore.getState().markWelcomeShown();

      get().saveTourState();

      useChatStore.getState().clearCurrentChat();
      void promptApiKeyIfNeededAfterOnboarding();
    }
  },

  previousStep: () => {
    const state = get();
    if (!state.currentStepId) return;

    const prevId = getPreviousStepId(state.currentStepId);
    if (prevId) {
      const viewerStore = useViewerStore.getState();

      if (stepRequiresWorkflowMode(prevId)) {
        viewerStore.setSettingsMode(false);
        const workflowName = stepRequiresWorkflowBuilder(prevId) ? "builtin://agent" : undefined;
        viewerStore.setWorkflowMode(true, workflowName);
      } else if (stepRequiresSettingsMode(prevId)) {
        viewerStore.setWorkflowMode(false);
        viewerStore.setSettingsMode(true);
      } else if (stepRequiresChatMode(prevId)) {
        viewerStore.setSettingsMode(false);
        viewerStore.setWorkflowMode(false);
      } else {
        viewerStore.setSettingsMode(false);
        viewerStore.setWorkflowMode(false);
      }

      set({ currentStepId: prevId });
      get().saveTourState();
    }
  },

  restartWizard: async () => {
    trackEvent('tour_restarted');
    const viewerStore = useViewerStore.getState();
    viewerStore.setSettingsMode(false);
    viewerStore.setWorkflowMode(false);

    useChatStore.getState().clearCurrentChat();

    try {
      await deleteSettingIfExists(TOUR_SETTINGS_KEYS.SKIPPED_ALL);
    } catch {
      /* ignore */
    }

    set({
      isWizardActive: true,
      currentStepId: ONBOARDING_STEPS[0].id,
      completedSteps: new Set(),
      skippedSteps: new Set(),
      hasCompletedOnboarding: false,
      projectHasCode: null,
    });

    // Clear checklist state without clobbering its isInitialized flag —
    // consumers like ContextualTipsLayer gate on isInitialized.
    useOnboardingChecklistStore.setState({
      completedItems: new Set(),
      welcomeShown: false,
      panelState: "expanded",
    });
    localStorage.removeItem("reliant.checklist.welcomeShown");

    await get().saveTourState();
    await get().detectProjectCode();
  },

  saveTourState: async () => {
    const state = get();
    try {
      await upsertStringSetting(
        TOUR_SETTINGS_KEYS.COMPLETED,
        state.hasCompletedOnboarding ? "true" : "false",
      );
      if (state.currentStepId) {
        await upsertStringSetting(TOUR_SETTINGS_KEYS.CURRENT_STEP, state.currentStepId);
      }
      await upsertStringSetting(
        TOUR_SETTINGS_KEYS.COMPLETED_STEPS,
        JSON.stringify(Array.from(state.completedSteps)),
      );
      await upsertStringSetting(
        TOUR_SETTINGS_KEYS.SKIPPED_STEPS,
        JSON.stringify(Array.from(state.skippedSteps)),
      );
    } catch (error) {
      logger.error("[TourStore] Failed to save tour state", error);
    }
  },

  detectProjectCode: async () => {
    const hasCode = await detectHasCode();
    set({ projectHasCode: hasCode });
  },
}));

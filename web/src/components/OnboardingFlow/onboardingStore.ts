import { create } from "zustand";
import { persist } from "zustand/middleware";
import { stepRegistry } from "./StepRegistry";
import type { LaunchPlan, OnboardingState, StepConfig } from "./types";

interface OnboardingStore {
  state: OnboardingState;
  plan: Partial<LaunchPlan>;
  currentStepIndex: number;

  updatePlan: (updates: Partial<LaunchPlan>) => void;
  nextStep: () => void;
  prevStep: () => void;
  skipOnboarding: () => void;
  completeOnboarding: () => void;
  reset: () => void;

  // Computed-style getters
  visibleSteps: () => StepConfig[];
  currentStep: () => StepConfig | null;
  currentCategory: () => string;
  progress: () => { current: number; total: number; categoryIndex: number };
}

export const useOnboardingFlowStore = create<OnboardingStore>()(
  persist(
    (set, get) => ({
      state: "not_started" as OnboardingState,
      plan: {},
      currentStepIndex: 0,

      updatePlan: (updates) => {
        const current = get();
        const nextState: Partial<OnboardingStore> = {
          plan: { ...current.plan, ...updates },
        };
        if (current.state === "not_started") {
          nextState.state = "in_progress";
        }
        set(nextState);
      },

      nextStep: () => {
        const { currentStepIndex, plan } = get();
        const visible = stepRegistry.getVisibleSteps(plan);
        if (currentStepIndex < visible.length - 1) {
          set({ currentStepIndex: currentStepIndex + 1 });
        }
      },

      prevStep: () => {
        const { currentStepIndex } = get();
        if (currentStepIndex > 0) {
          set({ currentStepIndex: currentStepIndex - 1 });
        }
      },

      skipOnboarding: () => {
        set({ state: "skipped" });
      },

      completeOnboarding: () => {
        set({ state: "completed" });
      },

      reset: () => {
        set({ state: "not_started", plan: {}, currentStepIndex: 0 });
      },

      visibleSteps: () => {
        return stepRegistry.getVisibleSteps(get().plan);
      },

      currentStep: () => {
        const steps = stepRegistry.getVisibleSteps(get().plan);
        const idx = get().currentStepIndex;
        return steps[idx] ?? null;
      },

      currentCategory: () => {
        const step = get().currentStep();
        return step?.category ?? "goal";
      },

      progress: () => {
        const steps = stepRegistry.getVisibleSteps(get().plan);
        const idx = get().currentStepIndex;
        const current = steps[idx];
        const categories = stepRegistry.getCategories();
        const categoryIndex = current
          ? categories.indexOf(current.category)
          : 0;
        return { current: idx + 1, total: steps.length, categoryIndex };
      },
    }),
    {
      name: "reliant-onboarding",
      partialize: (s) => ({
        state: s.state,
        plan: s.plan,
        currentStepIndex: s.currentStepIndex,
      }),
    }
  )
);

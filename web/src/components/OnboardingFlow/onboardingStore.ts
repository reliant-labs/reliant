import { create } from "zustand";
import { persist } from "zustand/middleware";
import { stepRegistry } from "./StepRegistry";
import { derivePath } from "./steps";
import type { LaunchPlan, OnboardingState, StepConfig } from "./types";

type HydrationFn = () => Promise<boolean>;
let hydrationFn: HydrationFn | null = null;

export function registerHydrationFn(fn: HydrationFn) {
  hydrationFn = fn;
}

interface OnboardingStore {
  state: OnboardingState;
  plan: Partial<LaunchPlan>;
  currentStepIndex: number;
  hydrating: boolean;
  devForceShow: boolean;

  updatePlan: (updates: Partial<LaunchPlan>) => void;
  nextStep: () => void;
  prevStep: () => void;
  completeOnboarding: () => void;
  requireOnboarding: () => void;
  reset: () => void;
  hydrateFromBackend: (options?: { force?: boolean }) => Promise<void>;
  setDevForceShow: (force: boolean) => void;

  visibleSteps: () => StepConfig[];
  currentStep: () => StepConfig | null;
  progress: () => { current: number; total: number };
}

export const useOnboardingFlowStore = create<OnboardingStore>()(
  persist(
    (set, get) => ({
      state: "not_started" as OnboardingState,
      plan: {},
      currentStepIndex: 0,
      hydrating: false,
      devForceShow: false,

      updatePlan: (updates) => {
        const current = get();
        const plan = { ...current.plan, ...updates };
        const nextState: Partial<OnboardingStore> = { plan };
        if (current.state === "not_started") {
          nextState.state = "in_progress";
        }
        set(nextState);
      },

      nextStep: () => {
        const { currentStepIndex, plan } = get();
        const path = derivePath(plan);
        const steps = stepRegistry.getStepsForPath(path);
        if (currentStepIndex < steps.length - 1) {
          set({ currentStepIndex: currentStepIndex + 1 });
        }
      },

      prevStep: () => {
        const { currentStepIndex } = get();
        if (currentStepIndex > 0) {
          set({ currentStepIndex: currentStepIndex - 1 });
        }
      },

      completeOnboarding: () => {
        set({ state: "completed" });
      },

      requireOnboarding: () => {
        const { state } = get();
        if (state === "completed") {
          set({ state: "not_started", currentStepIndex: 0 });
        }
      },

      reset: () => {
        set({ state: "not_started", plan: {}, currentStepIndex: 0, devForceShow: false });
      },

      hydrateFromBackend: async (options) => {
        const { state, devForceShow } = get();
        if (!options?.force && state === "completed") return;
        if (devForceShow) return;
        if (!hydrationFn) return;

        set({ hydrating: true });
        try {
          const completed = await hydrationFn();
          if (completed) {
            set({ state: "completed" });
          }
        } catch {
          // Fail silently — show onboarding if we can't reach backend.
        } finally {
          set({ hydrating: false });
        }
      },

      setDevForceShow: (force: boolean) => {
        set({ devForceShow: force });
      },

      visibleSteps: () => {
        const path = derivePath(get().plan);
        return stepRegistry.getStepsForPath(path);
      },

      currentStep: () => {
        const path = derivePath(get().plan);
        const steps = stepRegistry.getStepsForPath(path);
        const idx = get().currentStepIndex;
        return steps[idx] ?? null;
      },

      progress: () => {
        const path = derivePath(get().plan);
        const steps = stepRegistry.getStepsForPath(path);
        const idx = get().currentStepIndex;
        return { current: idx + 1, total: steps.length };
      },
    }),
    {
      name: "reliant-onboarding",
      partialize: (s) => ({
        state: s.state,
        plan: s.plan,
        currentStepIndex: s.currentStepIndex,
      }),
    },
  ),
);

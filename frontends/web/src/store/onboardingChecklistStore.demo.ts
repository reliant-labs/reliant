// Copyright (c) 2025 Reliant Labs

/**
 * store/onboardingChecklistStore.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts). The checklist is Reliant's onboarding and is
 * kept in a Reliant account's settings; the demo has neither, so nothing is
 * ever checked and nothing is stored. The tour store's Reliant-only calls
 * (finishing the product tour marks an item) do nothing here.
 */

import { create } from "zustand";

import type * as Real from "./onboardingChecklistStore";

type OnboardingChecklistState = ReturnType<typeof Real.useOnboardingChecklistStore.getState>;

export const useOnboardingChecklistStore: typeof Real.useOnboardingChecklistStore = create<OnboardingChecklistState>()(() => ({
  completedItems: new Set(),
  welcomeShown: true,
  panelState: "dismissed",
  isInitialized: true,
  isLoading: false,
  requiredCompleted: () => 0,
  requiredTotal: () => 0,
  totalCompleted: () => 0,
  allRequiredComplete: () => true,
  completionPercentage: () => 100,
  loadState: async () => {},
  markComplete: async () => {},
  markWelcomeShown: async () => {},
  setPanelState: async () => {},
  revive: async () => {},
  detectCompletedItems: async () => {},
  subscribeToStoreChanges: () => () => {},
  reset: () => {},
}));

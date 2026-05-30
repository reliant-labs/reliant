/**
 * Tour Store contract tests.
 *
 * After the URL refactor the tourStore is pure persistence — it tracks which
 * steps have been completed/skipped and whether the tour was ever finished.
 * Navigation, "current step", and "is wizard active" live in the URL now,
 * surfaced via `useTourNavigation`.
 *
 * These tests intentionally do NOT exercise live settings RPCs; we mock the
 * persistence layer so the store's set/get logic can be tested in isolation.
 */
/* eslint-disable @typescript-eslint/no-explicit-any */
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../lib/settingsPersistence", () => ({
  safeGetSetting: vi.fn(async () => null),
  upsertStringSetting: vi.fn(async () => undefined),
  deleteSettingIfExists: vi.fn(async () => undefined),
}));

vi.mock("../../lib/analytics", () => ({
  trackEvent: vi.fn(),
}));

vi.mock("../../api/fileSystem", () => ({
  getFileTree: vi.fn(async () => []),
}));

vi.mock("../chatStore", () => ({
  useChatStore: {
    getState: () => ({ clearCurrentChat: vi.fn() }),
  },
}));

vi.mock("../onboardingChecklistStore", () => ({
  useOnboardingChecklistStore: {
    getState: () => ({
      markComplete: vi.fn(async () => undefined),
      markWelcomeShown: vi.fn(async () => undefined),
    }),
    setState: vi.fn(),
  },
}));

vi.mock("../apiKeySetupStore", () => ({
  useApiKeySetupStore: {
    getState: () => ({
      hasApiKey: true,
      ensureApiKeyOrShowModal: vi.fn(async () => undefined),
    }),
    setState: vi.fn(),
  },
}));

import { useTourStore } from "../tourStore";

describe("tourStore — URL refactor contract", () => {
  beforeEach(() => {
    // Reset the store between tests. We assume the refactored store
    // exposes a resetTourProgress() that clears progress, and we also
    // wipe any persisted-state in-memory by re-setting.
    useTourStore.setState({
      completedSteps: new Set(),
      skippedSteps: new Set(),
      hasCompletedOnboarding: false,
      projectHasCode: null,
      isInitialized: false,
      isLoading: false,
    } as any);
    vi.clearAllMocks();

    // jsdom in this project ships localStorage as an opaque object without
    // method bindings (see vitest setup warning about --localstorage-file).
    // The store calls localStorage.removeItem during resetTourProgress; stub
    // it so we can exercise the reset path without hitting a TypeError.
    if (!globalThis.localStorage || typeof globalThis.localStorage.removeItem !== "function") {
      Object.defineProperty(globalThis, "localStorage", {
        configurable: true,
        value: {
          getItem: vi.fn(() => null),
          setItem: vi.fn(),
          removeItem: vi.fn(),
          clear: vi.fn(),
          key: vi.fn(),
          length: 0,
        },
      });
    }
  });

  // ─── State shape ─────────────────────────────────────────────────────────

  describe("state shape", () => {
    it("does NOT expose currentStepId on the state", () => {
      const state = useTourStore.getState() as Record<string, unknown>;
      expect("currentStepId" in state).toBe(false);
    });

    it("does NOT expose isWizardActive on the state", () => {
      const state = useTourStore.getState() as Record<string, unknown>;
      expect("isWizardActive" in state).toBe(false);
    });

    it("exposes the persistence fields", () => {
      const state = useTourStore.getState() as Record<string, unknown>;
      expect("hasCompletedOnboarding" in state).toBe(true);
      expect("completedSteps" in state).toBe(true);
      expect("skippedSteps" in state).toBe(true);
      expect("projectHasCode" in state).toBe(true);
      expect("isInitialized" in state).toBe(true);
      expect("isLoading" in state).toBe(true);
    });
  });

  // ─── Methods removed ─────────────────────────────────────────────────────

  describe("methods removed", () => {
    it("does NOT expose startWizard", () => {
      const state = useTourStore.getState() as Record<string, unknown>;
      expect("startWizard" in state).toBe(false);
    });

    it("does NOT expose nextStep", () => {
      const state = useTourStore.getState() as Record<string, unknown>;
      expect("nextStep" in state).toBe(false);
    });

    it("does NOT expose previousStep", () => {
      const state = useTourStore.getState() as Record<string, unknown>;
      expect("previousStep" in state).toBe(false);
    });

    it("does NOT expose skipAll (skipAll lives on useTourNavigation now)", () => {
      const state = useTourStore.getState() as Record<string, unknown>;
      expect("skipAll" in state).toBe(false);
    });

    it("does NOT expose goToStep (navigation is URL-driven via useTourNavigation)", () => {
      const state = useTourStore.getState() as Record<string, unknown>;
      expect("goToStep" in state).toBe(false);
    });

    it("does NOT expose closeWizard / resumeWizard / restartWizard", () => {
      const state = useTourStore.getState() as Record<string, unknown>;
      expect("closeWizard" in state).toBe(false);
      expect("resumeWizard" in state).toBe(false);
      expect("restartWizard" in state).toBe(false);
    });
  });

  // ─── Methods retained ────────────────────────────────────────────────────

  describe("methods retained", () => {
    it("exposes loadState, completeStep, skipStep, saveTourState", () => {
      const state = useTourStore.getState() as Record<string, unknown>;
      expect(typeof state.loadState).toBe("function");
      expect(typeof state.completeStep).toBe("function");
      expect(typeof state.skipStep).toBe("function");
      expect(typeof state.saveTourState).toBe("function");
    });

    it("exposes detectProjectCode", () => {
      const state = useTourStore.getState() as Record<string, unknown>;
      expect(typeof state.detectProjectCode).toBe("function");
    });

    it("exposes markTourCompleted", () => {
      const state = useTourStore.getState() as Record<string, unknown>;
      expect(typeof state.markTourCompleted).toBe("function");
    });

    it("exposes resetTourProgress", () => {
      const state = useTourStore.getState() as Record<string, unknown>;
      expect(typeof state.resetTourProgress).toBe("function");
    });
  });

  // ─── Behavior ────────────────────────────────────────────────────────────

  describe("completeStep", () => {
    it("adds the step id to completedSteps", async () => {
      const completeStep = (useTourStore.getState() as any).completeStep;
      if (typeof completeStep !== "function") {
        expect.fail("completeStep not implemented");
        return;
      }
      await completeStep("workflow-intro");
      expect(
        (useTourStore.getState() as any).completedSteps.has("workflow-intro")
      ).toBe(true);
    });
  });

  describe("markTourCompleted", () => {
    it("flips hasCompletedOnboarding to true", async () => {
      const mark = (useTourStore.getState() as any).markTourCompleted;
      if (typeof mark !== "function") {
        expect.fail("markTourCompleted not implemented");
        return;
      }
      await mark();
      expect(
        (useTourStore.getState() as any).hasCompletedOnboarding
      ).toBe(true);
    });
  });

  describe("resetTourProgress", () => {
    it("clears completedSteps + skippedSteps and resets hasCompletedOnboarding", async () => {
      useTourStore.setState({
        completedSteps: new Set(["workflow-intro"]),
        skippedSteps: new Set(["workspaces"]),
        hasCompletedOnboarding: true,
      } as any);
      const reset = (useTourStore.getState() as any).resetTourProgress;
      if (typeof reset !== "function") {
        expect.fail("resetTourProgress not implemented");
        return;
      }
      await reset();
      const state = useTourStore.getState() as any;
      expect(state.completedSteps.size).toBe(0);
      expect(state.skippedSteps.size).toBe(0);
      expect(state.hasCompletedOnboarding).toBe(false);
    });
  });

  describe("skipStep", () => {
    it("adds the step id to skippedSteps", async () => {
      const skip = (useTourStore.getState() as any).skipStep;
      if (typeof skip !== "function") {
        expect.fail("skipStep not implemented");
        return;
      }
      await skip("workspaces");
      expect(
        (useTourStore.getState() as any).skippedSteps.has("workspaces")
      ).toBe(true);
    });
  });
});

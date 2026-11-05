/**
 * Per-journey versioned completion: a changed journey (version bump) is
 * offered again to users who completed an older version — once — and the
 * storage adapter lets the forge shell keep progress without settings RPCs.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const settings = vi.hoisted(() => ({ rows: new Map<string, string>() }));

vi.mock("../../lib/settingsPersistence", () => ({
  safeGetSetting: vi.fn(async (key: string) =>
    settings.rows.has(key) ? { key, value: settings.rows.get(key) } : null,
  ),
  upsertStringSetting: vi.fn(async (key: string, value: string) => {
    settings.rows.set(key, value);
  }),
  deleteSettingIfExists: vi.fn(async (key: string) => {
    settings.rows.delete(key);
  }),
}));
vi.mock("../../lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("../onboardingChecklistStore", () => ({
  useOnboardingChecklistStore: {
    getState: () => ({ markComplete: vi.fn(async () => undefined), markWelcomeShown: vi.fn(async () => undefined) }),
    setState: vi.fn(),
  },
}));

import { useTourStore, __resetTourSaveScheduler } from "../tourStore";
import { journeyToReshow } from "../../components/Onboarding/journeys/reshow";
import { JOURNEYS } from "../../components/Onboarding/journeys/registry";
import { journeyCompletionKey } from "../../components/Onboarding/constants";
import {
  localStorageTourStorage,
  setTourStorage,
  settingsTourStorage,
} from "../../components/Onboarding/tourShell";

const reset = () =>
  useTourStore.setState({
    isInitialized: false,
    isLoading: false,
    hasCompletedOnboarding: false,
    lastJourney: null,
    resumeStep: null,
    contexts: {},
    completedSteps: new Set(),
    skippedSteps: new Set(),
  } as any);

const allowAll = () => true;

describe("tourStore — journey versions", () => {
  beforeEach(() => {
    settings.rows.clear();
    __resetTourSaveScheduler();
    setTourStorage(settingsTourStorage);
    reset();
  });
  afterEach(() => {
    __resetTourSaveScheduler();
    setTourStorage(settingsTourStorage);
    localStorage.clear();
  });

  it("uses one key per journey version", () => {
    expect(journeyCompletionKey("workflows", 2)).toBe("onboarding.tour.workflows.v2.completed");
  });

  it("counts the retired single tour as workflows v0, completed", async () => {
    settings.rows.set("onboarding.completed", "true");
    await useTourStore.getState().loadState();
    const { journeys } = useTourStore.getState();
    expect(journeys.workflows).toMatchObject({ version: JOURNEYS.workflows.version, current: null, completedOlder: true });
    expect(journeys.forge.completedOlder).toBe(false);
    expect(journeyToReshow(useTourStore.getState(), allowAll)).toBe("workflows");
  });

  it("reads the current version's outcome and older completions", async () => {
    settings.rows.set(journeyCompletionKey("forge", JOURNEYS.forge.version), "skipped");
    await useTourStore.getState().loadState();
    expect(useTourStore.getState().journeys.forge.current).toBe("skipped");
    expect(journeyToReshow(useTourStore.getState(), allowAll)).toBeNull();
  });

  it("never re-offers twice: markJourneyShown records the offer at the current version", async () => {
    settings.rows.set("onboarding.completed", "true");
    await useTourStore.getState().loadState();
    await useTourStore.getState().markJourneyShown("workflows");
    expect(settings.rows.get(journeyCompletionKey("workflows", JOURNEYS.workflows.version))).toBe("shown");
    expect(journeyToReshow(useTourStore.getState(), allowAll)).toBeNull();

    reset();
    await useTourStore.getState().loadState();
    expect(journeyToReshow(useTourStore.getState(), allowAll)).toBeNull();
  });

  it("respects the content's opt-out and prefers the last-chosen journey", () => {
    const state = {
      lastJourney: "forge" as const,
      journeys: {
        workflows: { version: 3, current: null, completedOlder: true },
        forge: { version: 2, current: null, completedOlder: true },
      },
    };
    expect(journeyToReshow(state, allowAll)).toBe("forge");
    expect(journeyToReshow(state, (id) => id !== "forge")).toBe("workflows");
    expect(journeyToReshow(state, () => false)).toBeNull();
  });

  it("markTourCompleted records the outcome for the journey's current version", async () => {
    await useTourStore.getState().loadState();
    await useTourStore.getState().markTourCompleted("forge", "skipped");
    await Promise.resolve();
    expect(settings.rows.get(journeyCompletionKey("forge", JOURNEYS.forge.version))).toBe("skipped");
    expect(useTourStore.getState().hasCompletedOnboarding).toBe(true);
    expect(useTourStore.getState().journeys.forge.current).toBe("skipped");
  });

  it("persists the chosen journey, the resume step and the journey context", async () => {
    await useTourStore.getState().loadState();
    await useTourStore.getState().setLastJourney("forge");
    await useTourStore.getState().setResumeStep("forge:fg-trace");
    useTourStore.getState().setJourneyContext("workflows", { workflowSlug: "fix-my-pr" });
    await Promise.resolve();
    reset();
    await useTourStore.getState().loadState();
    const state = useTourStore.getState();
    expect(state.lastJourney).toBe("forge");
    expect(state.resumeStep).toBe("forge:fg-trace");
    expect(state.contexts.workflows).toEqual({ workflowSlug: "fix-my-pr" });
  });

  it("a local storage adapter keeps progress with no settings RPC (the forge shell)", async () => {
    setTourStorage(localStorageTourStorage("forge.tour.v1"));
    await useTourStore.getState().loadState();
    await useTourStore.getState().markTourCompleted("forge", "completed");
    await Promise.resolve();
    expect(settings.rows.size).toBe(0);
    const held = JSON.parse(localStorage.getItem("forge.tour.v1") ?? "{}");
    expect(held[journeyCompletionKey("forge", JOURNEYS.forge.version)]).toBe("completed");
    expect(held["onboarding.completed"]).toBe("true");
  });
});
